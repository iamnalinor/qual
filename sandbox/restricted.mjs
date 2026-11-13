// Trusted parent: this runs inside the mount boundary, never inside the source.
// The official runtime owns the network proxy and nested network namespace.
import { readFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import { stripVTControlCharacters } from 'node:util';
import { SandboxManager } from '@anthropic-ai/sandbox-runtime';

// Backend errors can contain proxy URLs, selected credentials or command text.
// Keep useful OS messages, without emitting stacks, invocation data or controls.
function safeDetail(value) {
  let text = String(value);
  const credentials = Object.entries(process.env)
    .filter(([name, secret]) => /(?:KEY|TOKEN|PASSWORD|SECRET|AUTH)$/i.test(name) && secret)
    .flatMap(([, secret]) => [secret, encodeURIComponent(secret)])
    .sort((a, b) => b.length - a.length);
  for (const secret of credentials) text = text.replaceAll(secret, '[redacted]');
  text = text.replace(/\bBearer\s+[^\s,;]+/gi, 'Bearer [redacted]');
  // Remove the entire URL: credentials can also occur in paths and fragments.
  text = text.replace(/\b[a-z][a-z0-9+.-]*:\/\/[^\s<>"']+/gi, '[redacted URL]');
  return stripVTControlCharacters(text).replace(/[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/gu, ' ').slice(0, 2048);
}

function report(stage, error) {
  const details = [];
  const seen = new Set();
  for (let current = error; current != null && !seen.has(current) && details.length < 5; current = current.cause) {
    seen.add(current);
    details.push(safeDetail(current instanceof Error
      ? `${current.code || current.name}: ${current.message}` : current));
  }
  console.error(`sandbox_backend: restricted ${stage} failed: ${details.join('; caused by: ')}`);
}
const quote = value => "'" + value.replaceAll("'", "'\\''") + "'";
let child;
let stage = 'invocation read';
let initialized = false;
try {
  const input = readFileSync('/invocation.json', 'utf8');
  stage = 'invocation parse';
  let invocation;
  try {
    invocation = JSON.parse(input);
  } catch (error) {
    // JSON.parse messages may quote the input. Keep the syntax location only.
    const location = error.message.match(/(?:at position \d+)? ?\(line \d+ column \d+\)|at position \d+/)?.[0];
    const reason = error.message === 'Unexpected end of JSON input' ? ': unexpected end of input' : '';
    throw new SyntaxError(`invalid invocation JSON${location ? ' ' + location : reason}`);
  }
  if (!invocation || !Array.isArray(invocation.args) || !Array.isArray(invocation.allow)
      || ![...invocation.args, ...invocation.allow].every(value => typeof value === 'string')) {
    throw new Error('invocation args and allow must be arrays of strings');
  }
  stage = 'initialize';
  // Initialization can allocate resources before throwing; reset it once.
  initialized = true;
  await SandboxManager.initialize({
    network: { allowedDomains: invocation.allow, deniedDomains: [], strictAllowlist: true },
    filesystem: { denyRead: [], allowWrite: ['/workspace', '/state', '/output', '/tmp'], denyWrite: [] },
    ripgrep: { command: '/opt/qual-tools/rg' },
  });
  const command = ['/opt/qual-tools/qual', ...invocation.args].map(quote).join(' ');
  stage = 'wrap';
  const wrapped = await SandboxManager.wrapWithSandbox(command, '/bin/sh');
  stage = 'spawn';
  child = spawn('/bin/sh', ['-c', wrapped], { stdio: 'inherit', env: process.env });
  for (const signal of ['SIGINT', 'SIGTERM']) {
    process.on(signal, () => { child.kill(signal); });
  }
  const code = await new Promise((resolve, reject) => {
    child.on('error', reject);
    child.on('exit', (status, signal) => resolve(status ?? (signal === 'SIGINT' ? 130 : 143)));
  });
  process.exitCode = code;
} catch (error) {
  report(stage, error);
  process.exitCode = 125;
} finally {
  if (initialized) {
    try {
      await SandboxManager.reset();
    } catch (error) {
      report('reset', error);
      process.exitCode = 125;
    }
  }
}

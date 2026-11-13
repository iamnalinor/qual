#!/usr/bin/env python3
"""Offline subprocess fault injection for trusted sandbox launch diagnostics."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class RestrictedDiagnosticsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='qual-diagnostics-')
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        shutil.copyfile(ROOT / 'sandbox/restricted.mjs', self.base / 'restricted.mjs')
        package = self.base / 'node_modules/@anthropic-ai/sandbox-runtime'
        package.mkdir(parents=True)
        (package / 'package.json').write_text('{"type":"module","main":"index.mjs"}')
        (package / 'index.mjs').write_text('''
import { appendFileSync } from 'node:fs';
function step(stage) {
  appendFileSync(process.env.TRACE, stage + '\\n');
  if (process.env.FAIL_STAGE === stage || (stage === 'reset' && process.env.FAIL_RESET)) {
    const cause = new Error(process.env.DETAIL || 'namespace permission denied');
    cause.code = 'EPERM';
    const error = new Error(stage + ' unavailable', {cause});
    error.code = 'E_BACKEND';
    throw error;
  }
}
export const SandboxManager = {
  async initialize() { step('initialize'); },
  async wrapWithSandbox() { step('wrap'); return 'exit ' + (process.env.CHILD_CODE || '0'); },
  async reset() { step('reset'); },
};
''')
        # Only replace fixed invocation IO and spawn's executable. Everything else,
        # including the launched shell, process events and exit codes, remains real.
        (self.base / 'preload.mjs').write_text('''
import fs from 'node:fs';
import cp from 'node:child_process';
import { syncBuiltinESMExports } from 'node:module';
const read = fs.readFileSync;
fs.readFileSync = (path, ...args) => {
  if (path !== '/invocation.json') return read(path, ...args);
  if (process.env.READ_ERROR) return read('/nonexistent/qual-invocation.json', ...args);
  return process.env.BAD_JSON || '{"args":[],"allow":[]}';
};
const spawn = cp.spawn;
cp.spawn = (path, ...args) => {
  if (process.env.FAIL_STAGE === 'spawn') path = '/nonexistent/qual-diagnostics-shell';
  return spawn(path, ...args);
};
syncBuiltinESMExports();
''')

    def run_restricted(self, **env):
        return subprocess.run([shutil.which('node'), '--import', str(self.base / 'preload.mjs'),
                               str(self.base / 'restricted.mjs')], text=True, capture_output=True,
                              timeout=10, env=dict(os.environ, TRACE=str(self.base / 'trace'), **env))

    def trace(self):
        path = self.base / 'trace'
        return path.read_text().splitlines() if path.exists() else []

    def test_initialization_and_wrap_failures_identify_stage_and_cause(self):
        for stage, expected in [('initialize', 'initialize'), ('wrap', 'wrap')]:
            with self.subTest(stage=stage):
                result = self.run_restricted(FAIL_STAGE=stage)
                self.assertEqual(result.returncode, 125, result.stderr)
                for detail in ('sandbox_backend', expected, 'E_BACKEND', 'EPERM', 'namespace permission denied'):
                    self.assertIn(detail, result.stderr)
                self.assertEqual(result.stdout, '')

    def test_spawn_failure_identifies_os_error(self):
        result = self.run_restricted(FAIL_STAGE='spawn')
        self.assertEqual(result.returncode, 125, result.stderr)
        self.assertIn('spawn', result.stderr)
        self.assertIn('ENOENT', result.stderr)
        self.assertIn('/nonexistent/qual-diagnostics-shell', result.stderr)

    def test_cleanup_failure_keeps_primary_failure_and_is_not_retried(self):
        result = self.run_restricted(FAIL_STAGE='wrap', FAIL_RESET='1')
        self.assertEqual(result.returncode, 125, result.stderr)
        self.assertIn('wrap unavailable', result.stderr)
        self.assertIn('reset unavailable', result.stderr)
        self.assertLess(result.stderr.index('wrap unavailable'), result.stderr.index('reset unavailable'))
        self.assertEqual(self.trace(), ['initialize', 'wrap', 'reset'])

    def test_cleanup_failure_after_child_exit_fails_closed_once(self):
        result = self.run_restricted(FAIL_STAGE='reset', CHILD_CODE='7')
        self.assertEqual(result.returncode, 125, result.stderr)
        self.assertIn('reset', result.stderr)
        self.assertIn('EPERM', result.stderr)
        self.assertEqual(self.trace(), ['initialize', 'wrap', 'reset'])

    def test_malformed_invocation_is_handled_without_echoing_input(self):
        result = self.run_restricted(BAD_JSON='{"args": "synthetic-invocation-secret')
        self.assertEqual(result.returncode, 125, result.stderr)
        self.assertIn('invocation', result.stderr)
        self.assertIn('position 37', result.stderr)
        self.assertIn('SyntaxError', result.stderr)
        self.assertNotIn('synthetic-invocation-secret', result.stderr)
        self.assertNotIn('file://', result.stderr)
        self.assertEqual(self.trace(), [])

    def test_diagnostics_redact_credentials_auth_urls_and_terminal_controls(self):
        result = self.run_restricted(FAIL_STAGE='initialize', OPENAI_API_KEY='synthetic-selected-key',
            DETAIL='namespace failed synthetic-selected-key Bearer synthetic-bearer '
                   'https://user:synthetic-password@proxy.invalid/path?token=synthetic-query#synthetic-fragment '
                   'https://api.invalid/token/synthetic-path?access_token=synthetic-access '
                   '\x1b[31munsafe\nforged: diagnostic\x1b[0m')
        self.assertEqual(result.returncode, 125, result.stderr)
        self.assertIn('namespace failed', result.stderr)
        self.assertIn('EPERM', result.stderr)
        for secret in ('synthetic-selected-key', 'synthetic-bearer', 'synthetic-password',
                       'synthetic-query', 'synthetic-fragment', 'synthetic-path', 'synthetic-access', '\x1b'):
            self.assertNotIn(secret, result.stderr)
        self.assertEqual(len(result.stderr.splitlines()), 1)

    def test_invocation_read_failure_reports_errno_before_initializing(self):
        result = self.run_restricted(READ_ERROR='1')
        self.assertEqual(result.returncode, 125, result.stderr)
        self.assertIn('invocation read', result.stderr)
        self.assertIn('ENOENT', result.stderr)
        self.assertEqual(self.trace(), [])

    def test_invalid_invocation_shape_is_handled_before_initializing(self):
        result = self.run_restricted(BAD_JSON='{"args":["synthetic-secret"],"allow":null}')
        self.assertEqual(result.returncode, 125, result.stderr)
        self.assertIn('invocation parse', result.stderr)
        self.assertNotIn('synthetic-secret', result.stderr)
        self.assertEqual(self.trace(), [])

    def test_success_preserves_child_exit(self):
        result = self.run_restricted(CHILD_CODE='7')
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertEqual(result.stderr, '')
        self.assertEqual(self.trace(), ['initialize', 'wrap', 'reset'])


class LauncherDiagnosticsTests(unittest.TestCase):
    def test_invalid_repository_metadata_json_has_location_without_input(self):
        with tempfile.TemporaryDirectory(prefix='qual-json-') as directory:
            base = Path(directory)
            runtime = base / 'runtime'
            package = runtime / 'node_modules/@anthropic-ai/sandbox-runtime/package.json'
            package.parent.mkdir(parents=True)
            package.write_text('{"synthetic-secret": invalid}')
            # Runtime preflight resolves these executables but does not run them.
            tools = base / 'tools'
            tools.mkdir()
            for name in ('node', 'rg', 'which'):
                (tools / name).symlink_to('/bin/true')
            result = subprocess.run([str(ROOT / 'scripts/qual-sandbox'), '--output', directory + '/out',
                                     '--qual', '/bin/true', '--network', 'restricted', '--runtime-dir',
                                     str(runtime), '--', 'review', '.'], text=True, capture_output=True,
                                    timeout=10, env=dict(os.environ, PATH=str(tools) + ':/usr/bin:/bin'))
            self.assertEqual(result.returncode, 125, result.stderr)
            self.assertIn('sandbox_setup: invalid JSON', result.stderr)
            self.assertIn('Expecting value', result.stderr)
            self.assertIn('line 1', result.stderr)
            self.assertNotIn('synthetic-secret', result.stderr)
            package.write_bytes(b'\xff')
            result = subprocess.run(result.args, text=True, capture_output=True, timeout=10,
                                    env=dict(os.environ, PATH=str(tools) + ':/usr/bin:/bin'))
            self.assertEqual(result.returncode, 125, result.stderr)
            self.assertIn('UnicodeDecodeError', result.stderr)
            self.assertIn('invalid start byte', result.stderr)

    def test_setup_error_retains_path_context_and_redacts_selected_credential(self):
        with tempfile.TemporaryDirectory(prefix='qual-selected-') as directory:
            base = Path(directory)
            credential = base / 'credential'
            credential.write_text('synthetic-selected-secret')
            credential.chmod(0o600)
            missing = base / 'settings-synthetic-selected-secret-\x1b[31m.json'
            result = subprocess.run([str(ROOT / 'scripts/qual-sandbox'), '--output', directory + '/out',
                                     '--qual', '/bin/true', '--credential', f'OPENAI_API_KEY={credential}',
                                     '--settings', str(missing), '--', 'review', '.'],
                                    text=True, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 125, result.stderr)
            self.assertIn('ENOENT', result.stderr)
            self.assertIn(directory, result.stderr)
            self.assertNotIn('synthetic-selected-secret', result.stderr)
            self.assertNotIn('\x1b', result.stderr)

    def test_missing_input_reports_operation_and_errno_without_invocation(self):
        with tempfile.TemporaryDirectory(prefix='qual-setup-') as directory:
            result = subprocess.run([str(ROOT / 'scripts/qual-sandbox'), '--input', directory + '/missing',
                                     '--output', directory + '/out', '--qual', '/bin/true', '--',
                                     'https://user:synthetic-password@example.invalid/?token=synthetic-token'],
                                    text=True, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 125, result.stderr)
            self.assertIn('sandbox_setup', result.stderr)
            self.assertIn('ENOENT', result.stderr)
            self.assertIn('No such file or directory', result.stderr)
            self.assertIn(directory + '/missing', result.stderr)
            self.assertNotIn('synthetic-', result.stderr)


if __name__ == '__main__':
    unittest.main(verbosity=2)

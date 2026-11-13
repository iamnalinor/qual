#!/usr/bin/env python3
"""Offline, real-process tests of the external launcher security boundary."""
import json
import http.server
import threading
import time
import os
from pathlib import Path
import socket
import signal
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
LAUNCHER = ROOT / 'scripts/qual-sandbox'


class BoundaryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='qual-boundary-')
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.repo = self.base / 'source'
        self.repo.mkdir()
        self.host_home = self.base / 'real-home'
        self.host_home.mkdir()
        (self.host_home / 'private').write_text('synthetic-host-secret')
        (self.host_home / '.qual').mkdir()
        (self.host_home / '.qual/private').write_text('synthetic-main-state-secret')
        self.env = dict(os.environ, HOME=str(self.host_home), QUAL_HOME=str(self.host_home / '.qual/private'),
                        INHERITED_SECRET='synthetic-environment-secret', SSH_AUTH_SOCK=str(self.base / 'agent.sock'))
        self.git('init', '-q')
        (self.repo / 'tracked').write_text('committed\n')
        self.git('add', 'tracked')
        self.git('-c', 'user.name=Sandbox Test', '-c', 'user.email=sandbox@example.invalid', 'commit', '-qm', 'initial')
        (self.repo / 'tracked').write_text('staged\n')
        self.git('add', 'tracked')
        (self.repo / 'tracked').write_text('unstaged\n')
        (self.repo / 'untracked').write_text('untracked\n')
        self.output = self.base / 'result'
        self.qual = self.base / 'qual-helper'
        self.qual.write_text('#!/usr/bin/python3\nimport os,json,pathlib,subprocess,sys\n'
                             'pathlib.Path("/output/result.json").write_text(json.dumps({"cwd":os.getcwd(),"env":dict(os.environ),"args":sys.argv[1:]}))\n')
        self.qual.chmod(0o755)

    def git(self, *args):
        return subprocess.check_output(['/usr/bin/git', '-C', str(self.repo), *args], env=self.env)

    def run_wrapper(self, *flags, mode='none', args=('review', '.', '--staged')):
        return subprocess.run([str(LAUNCHER), '--input', str(self.repo), '--output', str(self.output),
                               '--qual', str(self.qual), '--network', mode, *flags, '--', *args],
                              env=self.env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45)

    def helper(self, body):
        self.qual.write_text('#!/usr/bin/python3\n' + body)

    def test_independent_git_index_work_files_and_environment(self):
        self.helper('''import os,json,pathlib,subprocess
p=pathlib.Path
result={"work":p("tracked").read_text(), "untracked":p("untracked").read_text(),
        "index":subprocess.check_output(["git","show",":tracked"],text=True),
        "head":subprocess.check_output(["git","show","HEAD:tracked"],text=True),
        "env":dict(os.environ)}
p("tracked").write_text("hostile")
subprocess.check_call(["git","update-index","--force-remove","tracked"])
p("/output/result.json").write_text(json.dumps(result))
''')
        index_before = (self.repo / '.git/index').read_bytes()
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        data = json.loads((self.output / 'reports/result.json').read_text())
        self.assertEqual((data['head'], data['index'], data['work'], data['untracked']),
                         ('committed\n', 'staged\n', 'unstaged\n', 'untracked\n'))
        self.assertEqual((self.repo / 'tracked').read_text(), 'unstaged\n')
        self.assertEqual((self.repo / '.git/index').read_bytes(), index_before)
        self.assertNotIn('INHERITED_SECRET', data['env'])
        self.assertNotIn('SSH_AUTH_SOCK', data['env'])
        self.assertEqual(data['env']['QUAL_HOME'], '/state')
        self.assertNotEqual(data['env']['HOME'], str(self.host_home))

    def test_home_source_socket_and_descendant_are_confined(self):
        listener = socket.socket(socket.AF_UNIX)
        self.addCleanup(listener.close)
        listener.bind(str(self.base / 'agent.sock'))
        listener.listen()
        self.helper('''import pathlib,socket,subprocess,json,sys
paths=sys.argv[1:]
code="""import pathlib,socket,sys,json
results=[]
for p in sys.argv[1:3]:
 try: pathlib.Path(p).read_bytes(); results.append(False)
 except OSError: results.append(True)
try:
 pathlib.Path(sys.argv[3]).write_text('attack'); results.append(False)
except OSError: results.append(True)
s=socket.socket(socket.AF_UNIX)
try: s.connect(sys.argv[4]); results.append(False)
except OSError: results.append(True)
print(json.dumps(results))
"""
r=subprocess.check_output([sys.executable,'-c',code,*paths],text=True)
pathlib.Path('/output/result.json').write_text(r)
''')
        result = self.run_wrapper(args=(str(self.host_home / 'private'), str(self.host_home / '.qual/private'),
                                        str(self.repo / 'tracked'), str(self.base / 'agent.sock')))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.output / 'reports/result.json').read_text()), [True] * 4)
        self.assertEqual((self.repo / 'tracked').read_text(), 'unstaged\n')

    def test_selected_settings_and_credentials_only(self):
        settings = self.base / 'settings.yaml'
        settings.write_text('fixture: selected\n')
        credential = self.base / 'credential'
        credential.write_text('synthetic-selected-key')
        credential.chmod(0o600)
        result = self.run_wrapper('--settings', str(settings), '--credential', f'OPENAI_API_KEY={credential}')
        self.assertEqual(result.returncode, 0, result.stderr)
        data = json.loads((self.output / 'reports/result.json').read_text())
        self.assertEqual(data['env']['OPENAI_API_KEY'], 'synthetic-selected-key')
        self.assertEqual((self.output / '.qual-sandbox/state/settings.yaml').read_text(), 'fixture: selected\n')
        self.assertNotIn('synthetic-selected-key', result.stderr + result.stdout)

    def test_restricted_starts_qual_behind_enforced_network_boundary(self):
        result = self.run_wrapper(mode='restricted')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.output / 'reports/result.json').exists())

    def test_network_modes_block_direct_tcp_udp_and_dns_bypass(self):
        tcp = socket.socket()
        self.addCleanup(tcp.close)
        tcp.bind(('0.0.0.0', 0))
        tcp.listen()
        udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.addCleanup(udp.close)
        udp.bind(('0.0.0.0', 0))
        # UDP send success alone proves nothing; a host fixture must reply.
        udp.settimeout(.1)
        stopped = threading.Event()
        self.addCleanup(stopped.set)
        def echo_datagrams():
            while not stopped.is_set():
                try:
                    packet, address = udp.recvfrom(4096)
                    udp.sendto(packet, address)
                except socket.timeout:
                    continue
                except OSError:
                    return
        threading.Thread(target=echo_datagrams, daemon=True).start()
        host_ip = '127.0.0.1'
        self.helper('''import json,pathlib,socket,sys,subprocess
# Run the attacker as a descendant with all proxy variables removed.
code="""import socket,json,sys,os
for key in list(os.environ):
 if 'proxy' in key.lower(): del os.environ[key]
results=[]
try:
 socket.create_connection((sys.argv[1],int(sys.argv[2])),timeout=.5).close();results.append(True)
except OSError: results.append(False)
# Standard DNS query for fixture.invalid, sent only to the local fixture.
dns_query=bytes([18,52,1,0,0,1,0,0,0,0,0,0,7])+b'fixture'+bytes([7])+b'invalid'+bytes([0,0,1,0,1])
for packet in (b'udp-fixture',dns_query):
 port=int(sys.argv[3])
 s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(.3)
 try:
  s.sendto(packet,(sys.argv[1],port));reply,_=s.recvfrom(4096);results.append(reply==packet)
 except OSError: results.append(False)
 s.close()
print(json.dumps(results))
"""
r=subprocess.check_output([sys.executable,'-c',code,*sys.argv[1:]],text=True)
pathlib.Path('/output/result.json').write_text(r)
''')
        for mode in ('none', 'restricted', 'open'):
            with self.subTest(mode=mode):
                self.output = self.base / ('result-' + mode)
                result = self.run_wrapper(mode=mode, args=(host_ip, str(tcp.getsockname()[1]), str(udp.getsockname()[1])))
                self.assertEqual(result.returncode, 0, result.stderr)
                attempts = json.loads((self.output / 'reports/result.json').read_text())
                self.assertEqual(attempts, [mode == 'open'] * 3)

    def test_restricted_proxy_allows_only_selected_host_and_port(self):
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'fixture-response')
            def log_message(self, *_args):
                pass
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.addCleanup(server.server_close)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.shutdown)
        port = server.server_address[1]
        self.helper(r'''import base64,json,os,pathlib,socket,sys,urllib.parse
p=urllib.parse.urlsplit(os.environ['HTTP_PROXY'])
auth=base64.b64encode((urllib.parse.unquote(p.username)+':'+urllib.parse.unquote(p.password)).encode()).decode()
results=[]
for target in [int(sys.argv[1]),int(sys.argv[1])+1]:
 with socket.create_connection((p.hostname,p.port),timeout=3) as s:
  s.sendall(('GET http://127.0.0.1:'+str(target)+'/ HTTP/1.1\r\nHost: 127.0.0.1:'+str(target)+'\r\nProxy-Authorization: Basic '+auth+'\r\nConnection: close\r\n\r\n').encode())
  results.append(s.recv(4096).split(b'\r\n')[0].decode())
pathlib.Path('/output/result.json').write_text(json.dumps(results))
''')
        result = self.run_wrapper('--allow', f'127.0.0.1:{port}', mode='restricted', args=(str(port),))
        self.assertEqual(result.returncode, 0, result.stderr)
        responses = json.loads((self.output / 'reports/result.json').read_text())
        self.assertIn('200', responses[0])
        self.assertIn('403', responses[1])

    def test_linked_worktree_keeps_its_own_index_without_gitdir_reference(self):
        linked = self.base / 'linked'
        self.git('worktree', 'add', '-q', '-b', 'sandbox-test', str(linked), 'HEAD')
        self.repo = linked
        (self.repo / 'tracked').write_text('linked-staged\n')
        self.git('add', 'tracked')
        self.helper('''import pathlib,subprocess
assert subprocess.check_output(['git','show',':tracked'])==b'linked-staged\\n'
assert not pathlib.Path('.git/commondir').exists()
pathlib.Path('/output/result.json').write_text('{}')
''')
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.output / 'reports/result.json').exists())

    def test_bare_linked_worktree_with_readonly_configs_becomes_independent_checkout(self):
        bare = self.base / 'origin.git'
        self.git('clone', '--bare', '-q', str(self.repo), str(bare))
        linked = self.base / 'bare-linked'
        subprocess.check_call(['/usr/bin/git', '--git-dir='+str(bare), 'worktree',
                               'add', '-q', '--detach', str(linked), 'HEAD'], env=self.env)
        self.repo = linked
        (linked / 'tracked').write_text('linked-staged\n')
        self.git('add', 'tracked')
        (linked / 'tracked').write_text('linked-work\n')
        gitdir = Path(self.git('rev-parse', '--absolute-git-dir').decode().strip())
        config_before, index_before = (bare / 'config').read_bytes(), (gitdir / 'index').read_bytes()
        # A worktree-local core.worktree also needs relocation after flattening.
        self.git('config', 'extensions.worktreeConfig', 'true')
        self.git('config', '--worktree', 'core.worktree', str(linked))
        config_before = (bare / 'config').read_bytes()
        private_before = (gitdir / 'config.worktree').read_bytes()
        (bare / 'config').chmod(0o444)
        (gitdir / 'config.worktree').chmod(0o444)
        self.helper(r'''import pathlib,subprocess
assert subprocess.check_output(['git','rev-parse','--show-toplevel']).strip()==b'/workspace'
assert subprocess.check_output(['git','rev-parse','--is-bare-repository']).strip()==b'false'
assert subprocess.check_output(['git','show',':tracked'])==b'linked-staged\n'
assert pathlib.Path('tracked').read_text()=='linked-work\n'
subprocess.check_call(['git','update-index','--force-remove','tracked'])
pathlib.Path('/output/result.json').write_text('{}')
''')
        result = self.run_wrapper(mode='open')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((bare / 'config').read_bytes(), config_before)
        self.assertEqual((gitdir / 'config.worktree').read_bytes(), private_before)
        for config in (bare / 'config', gitdir / 'config.worktree',
                       self.output / '.qual-sandbox/source/.git/config',
                       self.output / '.qual-sandbox/source/.git/config.worktree'):
            self.assertEqual(config.stat().st_mode & 0o777, 0o444)
        self.assertEqual((gitdir / 'index').read_bytes(), index_before)

    def test_linked_worktree_preserves_absent_index_and_private_metadata(self):
        main = self.repo
        head = self.git('rev-parse', 'HEAD').decode().strip()
        self.git('update-ref', '--create-reflog', 'refs/worktree/main-only', head)
        (main / '.git/ORIG_HEAD').write_text(head + '\n')
        linked = self.base / 'linked-empty-index'
        self.git('worktree', 'add', '-q', '-b', 'sandbox-empty-index', str(linked), 'HEAD')
        self.repo = linked
        self.git('update-ref', '--create-reflog', 'refs/worktree/selected-only', head)
        gitdir = Path(self.git('rev-parse', '--absolute-git-dir').decode().strip())
        (gitdir / 'index').unlink()
        (gitdir / 'ORIG_HEAD').unlink(missing_ok=True)
        # A missing index is a valid empty index, not the main worktree's index.
        self.assertEqual(self.git('ls-files'), b'')
        self.assertEqual(self.git('diff', '--cached', '--name-status'), b'D\ttracked\n')
        self.helper(r'''import pathlib,subprocess
p=pathlib.Path
assert not p('.git/index').exists()
assert subprocess.check_output(['git','ls-files'])==b''
assert subprocess.check_output(['git','diff','--cached','--name-status'])==b'D\ttracked\n'
assert not p('.git/ORIG_HEAD').exists()
assert not p('.git/refs/worktree/main-only').exists()
assert not p('.git/logs/refs/worktree/main-only').exists()
assert p('.git/refs/worktree/selected-only').is_file()
assert p('.git/logs/refs/worktree/selected-only').is_file()
p('/output/result.json').write_text('{}')
''')
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.output / 'reports/result.json').exists())
        self.assertFalse((gitdir / 'index').exists())
        self.assertEqual(self.git('diff', '--cached', '--name-status'), b'D\ttracked\n')

    def test_prepare_never_executes_git_hooks_filters_or_fsmonitor(self):
        marker = self.base / 'executed-outside'
        hook = self.repo / '.git/hooks/pre-commit'
        hook.write_text('#!/bin/sh\ntouch '+str(marker)+'\n')
        hook.chmod(0o755)
        self.git('config', 'core.fsmonitor', str(hook))
        self.git('config', 'filter.hostile.clean', str(hook))
        (self.repo / '.gitattributes').write_text('* filter=hostile\n')
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(marker.exists())
        self.assertNotEqual((self.repo / 'tracked').stat().st_ino,
                            (self.output / '.qual-sandbox/source/tracked').stat().st_ino)

    def test_object_alternates_fail_closed_before_qual(self):
        (self.repo / '.git/objects/info/alternates').write_text('/host/objects\n')
        result = self.run_wrapper()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sandbox_repository', result.stderr)
        self.assertFalse((self.output / 'reports/result.json').exists())

    def test_timeout_kills_descendants_and_retains_state(self):
        self.helper('''import pathlib,subprocess,sys,time
subprocess.Popen([sys.executable,'-c',"import pathlib,time;time.sleep(2);pathlib.Path('/output/escaped').write_text('alive')"],start_new_session=True)
pathlib.Path('/state/evidence').write_text('before cancellation')
time.sleep(30)
''')
        result = self.run_wrapper('--timeout', '0.5')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sandbox_timeout', result.stderr)
        time.sleep(2)
        self.assertFalse((self.output / 'reports/escaped').exists())
        self.assertEqual((self.output / '.qual-sandbox/state/evidence').read_text(), 'before cancellation')

    def test_restricted_profile_is_readonly_and_argv_has_no_shell_interpretation(self):
        self.helper('''import json,pathlib,sys
try:
 pathlib.Path('/opt/qual-sandbox/restricted.mjs').write_text('attack'); raise AssertionError('writable profile')
except OSError: pass
pathlib.Path('/output/result.json').write_text(json.dumps(sys.argv[1:]))
''')
        arguments = ('review', "'; touch /output/injected; #", 'line one\nline two', 'space and $HOME')
        result = self.run_wrapper(mode='restricted', args=arguments)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.output / 'reports/result.json').read_text()), list(arguments))
        self.assertFalse((self.output / 'reports/injected').exists())

    def test_python_startup_injection_never_runs_outside_boundary(self):
        injected = self.base / 'injection'
        injected.mkdir()
        marker = self.base / 'python-startup-ran'
        (injected / 'sitecustomize.py').write_text('from pathlib import Path\nPath('+repr(str(marker))+').touch()\n')
        self.env['PYTHONPATH'] = str(injected)
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(marker.exists())

    def test_restricted_missing_backend_has_no_fallback(self):
        runtime = self.base / 'missing-runtime'
        runtime.mkdir()
        result = self.run_wrapper('--runtime-dir', str(runtime), mode='restricted')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sandbox_backend', result.stderr)
        self.assertFalse((self.output / 'reports/result.json').exists())

    def test_rejects_credential_environment_injection(self):
        file = self.base / 'payload'
        file.write_text('malicious-startup-file')
        file.chmod(0o600)
        result = self.run_wrapper('--credential', 'BASH_ENV='+str(file))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sandbox_credential', result.stderr)
        self.assertFalse((self.output / 'reports/result.json').exists())

    def test_inherited_file_descriptors_are_closed(self):
        secret = self.base / 'descriptor-secret'
        secret.write_text('synthetic-descriptor-secret')
        with secret.open() as stream:
            os.set_inheritable(stream.fileno(), True)
            self.helper('''import json,os,pathlib,sys
try: os.read(int(sys.argv[1]),100); result=False
except OSError: result=True
pathlib.Path('/output/result.json').write_text(json.dumps(result))
''')
            result = subprocess.run([str(LAUNCHER), '--input', str(self.repo), '--output', str(self.output),
                                     '--qual', str(self.qual), '--', str(stream.fileno())], env=self.env,
                                    pass_fds=(stream.fileno(),), text=True, capture_output=True, timeout=45)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads((self.output / 'reports/result.json').read_text()))

    def test_url_review_runs_from_empty_workspace_inside_boundary(self):
        result = subprocess.run([str(LAUNCHER), '--output', str(self.output), '--qual', str(self.qual),
                                 '--network', 'none', '--', 'review', 'https://example.invalid/pull/1'],
                                env=self.env, text=True, capture_output=True, timeout=45)
        self.assertEqual(result.returncode, 0, result.stderr)
        data = json.loads((self.output / 'reports/result.json').read_text())
        self.assertEqual(data['cwd'], '/workspace')
        self.assertEqual(data['args'], ['review', 'https://example.invalid/pull/1'])
        self.assertFalse((self.output / '.qual-sandbox/source/.git').exists())

    def test_toolchain_is_selected_readonly_and_cannot_expose_host_socket(self):
        toolchain = self.base / 'tools'
        (toolchain / 'bin').mkdir(parents=True)
        tool = toolchain / 'bin/selected-tool'
        tool.write_text('#!/bin/sh\necho selected\n')
        tool.chmod(0o755)
        self.helper('''import pathlib,subprocess
assert subprocess.check_output(['selected-tool'])==b'selected\\n'
try:
 pathlib.Path('/opt/toolchains/0/bin/selected-tool').write_text('attack');raise AssertionError('writable tool')
except OSError: pass
pathlib.Path('/output/result.json').write_text('{}')
''')
        result = self.run_wrapper('--toolchain', str(toolchain))
        self.assertEqual(result.returncode, 0, result.stderr)
        listener = socket.socket(socket.AF_UNIX)
        self.addCleanup(listener.close)
        listener.bind(str(toolchain / 'management.sock'))
        self.output = self.base / 'result-socket'
        result = self.run_wrapper('--toolchain', str(toolchain))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sandbox_toolchain', result.stderr)
        self.assertFalse((self.output / 'reports/result.json').exists())

    def test_repository_path_cannot_replace_host_boundary_backend(self):
        marker = self.base / 'fake-backend-ran'
        fake = self.base / 'untrusted-bin'
        fake.mkdir()
        backend = fake / 'bwrap'
        backend.write_text('#!/bin/sh\ntouch '+str(marker)+'\nexit 1\n')
        backend.chmod(0o755)
        self.env['PATH'] = str(fake) + ':' + self.env['PATH']
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(marker.exists())

    def test_existing_and_dangling_symlink_output_are_rejected(self):
        target = self.base / 'unexpected-target'
        self.output.symlink_to(target, target_is_directory=True)
        result = self.run_wrapper()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(target.exists())
        self.assertIn('sandbox_output', result.stderr)
        self.output.unlink()
        self.output.mkdir()
        result = self.run_wrapper()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sandbox_output', result.stderr)
        self.assertFalse((self.output / '.qual-sandbox').exists())

    def test_sigint_kills_tree_and_retains_evidence(self):
        self.helper('''import pathlib,subprocess,sys,time
subprocess.Popen([sys.executable,'-c',"import pathlib,time;time.sleep(2);pathlib.Path('/output/escaped').write_text('alive')"],start_new_session=True)
pathlib.Path('/state/evidence').write_text('ready')
time.sleep(30)
''')
        child = subprocess.Popen([str(LAUNCHER), '--input', str(self.repo), '--output', str(self.output),
                                  '--qual', str(self.qual), '--', 'review', '.'], env=self.env,
                                 text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 5
            while not (self.output / '.qual-sandbox/state/evidence').exists():
                if time.monotonic() > deadline or child.poll() is not None:
                    self.fail('sandbox helper never became ready')
                time.sleep(.02)
            child.send_signal(signal.SIGINT)
            _, stderr = child.communicate(timeout=5)
            self.assertEqual(child.returncode, 130, stderr)
            time.sleep(2)
            self.assertFalse((self.output / 'reports/escaped').exists())
            self.assertEqual((self.output / '.qual-sandbox/state/evidence').read_text(), 'ready')
        finally:
            if child.poll() is None:
                child.kill()
                child.communicate(timeout=5)

    def test_backend_failure_does_not_execute_qual(self):
        self.env['PATH'] = '/nonexistent'
        result = self.run_wrapper(mode='restricted')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.output / 'reports/result.json').exists())
        self.assertIn('sandbox_backend', result.stderr)

    def test_rejects_unsafe_mode_without_executing_qual(self):
        result = self.run_wrapper(mode='automatic')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.output / 'reports/result.json').exists())


if __name__ == '__main__':
    unittest.main(verbosity=2)

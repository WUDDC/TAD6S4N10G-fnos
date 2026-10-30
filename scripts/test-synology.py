#!/usr/bin/env python3
"""Isolated packaging/startup regressions; never start a daemon or touch DSM."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent


class SynologyScripts(unittest.TestCase):
    def test_credentials_private_at_creation_and_upstream_refresh(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            source = (ROOT / 'synology/scripts/real-start-stop-status').read_text()
            # Load definitions only: no lifecycle action, module loading or hardware.
            definitions = source.split('\ncase "${1:-}" in\n')[0]
            (work / 'functions').write_text(definitions)
            (work / 'etc').mkdir()
            body = r'''
. "$TEST_DIR/functions"
PKG_ETC="$TEST_DIR/etc"
AUTH="$PKG_ETC/web-auth.json"
PROXY_TOKEN="$PKG_ETC/proxy-token"
NGINX_CONF_DIR="$TEST_DIR/nginx"
NGINX_CONF="$NGINX_CONF_DIR/dsm.tad-module.conf"
# Observe modes BEFORE the production chmod; a post-write chmod cannot hide a leak.
chmod() {
  shift
  for secret in "$@"; do
    test "$(stat -c %a "$secret")" = 600 || exit 71
  done
}
nginx() { test "$1" = -t; }
systemctl() { echo reload >> "$TEST_DIR/reloads"; }
umask 022
ensure_web_auth
ensure_proxy_token
ensure_nginx
test "$(umask)" = 0022
# Unchanged config must avoid a redundant reload.
ensure_nginx
test "$(wc -l < "$TEST_DIR/reloads")" -eq 1
# Both ordinary and IPv6 upstreams must replace an otherwise matching v5/token config.
HTTP_ADDR=127.0.0.1:9000
ensure_nginx
grep -Fqx '    proxy_pass http://127.0.0.1:9000;' "$NGINX_CONF"
HTTP_ADDR='[::1]:9001'
ensure_nginx
grep -Fqx '    proxy_pass http://[::1]:9001;' "$NGINX_CONF"
test "$(wc -l < "$TEST_DIR/reloads")" -eq 3
test "$(umask)" = 0022
'''
            env = dict(os.environ, TEST_DIR=tmp, SYNOPKG_PKGDEST=tmp,
                       SYNOPKG_PKGETC=str(work / 'etc'), SYNOPKG_PKGVAR=tmp)
            subprocess.run(['sh', '-ec', body], env=env, check=True, timeout=15)

    def test_nginx_failures_keep_token_and_retry_reload(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            source = (ROOT / 'synology/scripts/real-start-stop-status').read_text()
            (work / 'functions').write_text(source.split('\ncase "${1:-}" in\n')[0])
            (work / 'etc').mkdir()
            env = dict(os.environ, TEST_DIR=tmp, SYNOPKG_PKGDEST=tmp,
                       SYNOPKG_PKGETC=str(work / 'etc'), SYNOPKG_PKGVAR=tmp)
            body = r'''
. "$TEST_DIR/functions"
NGINX_CONF_DIR="$TEST_DIR/nginx"
NGINX_CONF="$NGINX_CONF_DIR/dsm.tad-module.conf"
mkdir() { return 1; }
if ensure_nginx; then exit 70; fi
test -s "$PROXY_TOKEN"
unset -f mkdir
nginx() { return 0; }
systemctl() { echo reload >> "$TEST_DIR/reloads"; return 1; }
if ensure_nginx; then exit 71; fi
test ! -f "$NGINX_CONF"
systemctl() { echo reload >> "$TEST_DIR/reloads"; return 0; }
ensure_nginx
test -f "$NGINX_CONF"
test "$(wc -l < "$TEST_DIR/reloads")" -eq 2
'''
            subprocess.run(['sh', '-ec', body], env=env, check=True, timeout=15)

    def test_pid_identity_rejects_stale_or_invalid_targets(self):
        for scenario in ['zero', 'negative', 'text', 'legacy', 'reused', 'reboot',
                         'other-exe', 'other-command', 'exit-reuse', 'valid']:
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as tmp:
                work = Path(tmp)
                (work / 'var').mkdir()
                (work / 'bin').mkdir()
                binary = work / 'bin/tad-module'
                binary.write_text('isolated binary fixture')
                proc = work / 'proc/123'
                proc.mkdir(parents=True)
                # stat: comm may contain spaces/parentheses; starttime is field 22.
                (proc / 'stat').write_text('123 (tad (module)) S ' + '0 ' * 18 + '456 0\n')
                (proc / 'cmdline').write_bytes(str(binary).encode() + b'\0' +
                                              (b'init' if scenario == 'other-command' else b'serve') + b'\0')
                (proc / 'exe').symlink_to('/bin/sh' if scenario == 'other-exe' else binary)
                boot = work / 'proc/sys/kernel/random'
                boot.mkdir(parents=True)
                (boot / 'boot_id').write_text('boot-A\n')
                pid_line = {'zero': '0 456 boot-A', 'negative': '-1 456 boot-A',
                            'text': 'oops 456 boot-A', 'legacy': '123',
                            'reused': '123 999 boot-A', 'reboot': '123 456 boot-B'}.get(scenario, '123 456 boot-A')
                (work / 'var/tad-module.pid').write_text(pid_line + '\n')
                source = (ROOT / 'synology/scripts/real-start-stop-status').read_text()
                # Test-only replacement: all process files point into this temporary tree.
                source = source.replace('/proc/', str(work / 'proc') + '/')
                (work / 'service').write_text(source)
                env = dict(os.environ, TEST_DIR=tmp, SCENARIO=scenario, SYNOPKG_PKGDEST=tmp,
                           SYNOPKG_PKGETC=tmp, SYNOPKG_PKGVAR=str(work / 'var'))
                body = r'''
kill() {
  echo "$*" >> "$TEST_DIR/signals"
  if [ "$SCENARIO" = exit-reuse ] && [ "$*" = 123 ]; then
    sed -i 's/456/999/' "$TEST_DIR/proc/123/stat"
  fi
  return 0
}
sleep() { :; }
set -- stop
. "$TEST_DIR/service"
'''
                subprocess.run(['sh', '-ec', body], env=env, check=True, timeout=15)
                signals = (work / 'signals').read_text().splitlines() if (work / 'signals').exists() else []
                if scenario == 'valid':
                    self.assertIn('123', signals)
                    self.assertIn('-9 123', signals)
                elif scenario == 'exit-reuse':
                    self.assertIn('123', signals)
                    self.assertNotIn('-9 123', signals, 'reused PID must not receive KILL')
                else:
                    self.assertFalse(signals, f'invalid PID identity received signal: {signals}')

    def test_docker_target_and_reject_wrong_architecture(self):
        for machine, ident in [(b'\xb7\0', b'\x7fELF\x02\x01'),
                               (b'\x3e\0', b'\x7fELF\x01\x01')]:
            with self.subTest(machine=machine, ident=ident), tempfile.TemporaryDirectory() as tmp:
                work = Path(tmp)
                (work / 'scripts').mkdir()
                (work / 'manifest').write_text('version="1.0.1"\n')
                script = (ROOT / 'scripts/build-spk.sh').read_text()
                # Force only the Docker branch even on a developer machine with Go.
                script = script.replace('if command -v go >/dev/null 2>&1; then', 'if false; then')
                (work / 'scripts/build-spk.sh').write_text(script)
                tools = work / 'tools'
                tools.mkdir()
                # Fake Docker writes an invalid target so validation must stop before packaging.
                stub = tools / 'docker'
                stub.write_text('#!/bin/sh\n' + r'''
for required in GOOS=linux GOARCH=amd64 CGO_ENABLED=0; do
  found=no
  for arg in "$@"; do [ "$arg" != "$required" ] || found=yes; done
  [ "$found" = yes ] || exit 72
done
cp "$FAKE_ELF" app/bin/tad-module
''')
                stub.chmod(0o755)
                data = bytearray(64)
                data[:6] = ident
                data[18:20] = machine
                elf = work / 'wrong-arch'
                elf.write_bytes(data)
                result = subprocess.run(['bash', 'scripts/build-spk.sh'], cwd=work,
                                        env=dict(os.environ, PATH=str(tools) + ':' + os.environ['PATH'], FAKE_ELF=str(elf)),
                                        capture_output=True, text=True, timeout=15)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn('x86_64', result.stderr)
                self.assertFalse(list(work.glob('*.spk')))


if __name__ == '__main__':
    unittest.main()

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

"""Audience denial must use an authenticated route in org and public modes."""
import base64
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch
from urllib.parse import urlsplit


class AudienceIsolationTests(unittest.TestCase):
    def test_org_and_public_keep_cross_backend_replay_denials(self):
        for mode in ('org', 'public'):
            with self.subTest(mode=mode):
                self.run_acceptance(mode)

    def run_acceptance(self, mode):
        spec = importlib.util.spec_from_file_location('audience_isolation', Path(__file__).with_name('audience_isolation.py'))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        now = int(time.time())

        def token(audience):
            payload = {'aud': [audience], 'iat': now, 'exp': now + (3600 if audience == 'platform-api' else 60)}
            return 'header.' + base64.urlsafe_b64encode(json.dumps(payload).encode()).decode().rstrip('=') + '.signature'

        source = token('platform-api')
        observed = []

        def request(url, method='GET', headers=None, body=None):
            parsed = urlsplit(url)
            path, host = parsed.path, parsed.hostname
            authorization = (headers or {}).get('Authorization', '')
            observed.append((host, path, authorization))
            if path == '/api/v1/auth/login':
                return 200, {}, json.dumps({'access_token': source}).encode()
            if path == '/api/v1/auth/me':
                return (200 if authorization == 'Bearer ' + source else 401), {}, b'{}'
            if path.startswith('/internal/auth/exchange/'):
                if host == 'platform' and authorization == 'Bearer ' + source:
                    audience = path.rsplit('/', 1)[1] + '-api'
                    return 200, {'Authorization': 'Bearer ' + token(audience)}, b''
                return 401, {}, b'{}'
            if path == '/auth/login':
                return 200, {'Set-Cookie': 'mcp_ui_session=opaque; Secure'}, b'{}'
            if path == '/auth/logout':
                return 200, {}, b'{}'
            if path.startswith('/api/ui/'):
                return (403 if method == 'POST' else 200), {}, b'{}'
            # The public catalog deliberately allows reads even when the bearer
            # does not authenticate. It cannot prove an audience was accepted.
            if host == 'runtime' and mode == 'public' and path == '/api/v1/runtime/servers':
                return 200, {}, b'{"servers":[]}'
            audience = 'runtime-api' if '/runtime/' in path else 'analytics-api'
            expected = 'platform-api' if host == 'gateway' else host + '-api'
            status = 200 if authorization == 'Bearer ' + token(expected) and (host == 'gateway' or expected == audience) else 401
            return status, {}, b'{}'

        env = dict(PLATFORM_URL='http://gateway', DIRECT_PLATFORM_URL='http://platform',
                   DIRECT_RUNTIME_URL='http://runtime', DIRECT_ANALYTICS_URL='http://analytics',
                   PLATFORM_ADMIN_EMAIL='fixture@example.test', PLATFORM_ADMIN_PASSWORD='fixture', BIN='/fixture/cli')
        with patch.dict(os.environ, env), patch.object(module, 'request', request), \
                patch.object(module.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=b'SLUG')), \
                contextlib.redirect_stdout(io.StringIO()):
            module.main()
        self.assertTrue(any(host == 'runtime' and path == '/api/v1/runtime/workspace/teams'
                            and auth == 'Bearer ' + source for host, path, auth in observed))


if __name__ == '__main__':
    unittest.main()

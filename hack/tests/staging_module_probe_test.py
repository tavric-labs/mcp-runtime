"""Prove that module-network diagnostics do not disclose signed URLs or bodies."""
import importlib.util
import io
import json
from pathlib import Path
import unittest
from urllib.error import HTTPError

SPEC = importlib.util.spec_from_file_location('staging_module_probe', Path(__file__).parents[1] / 'qa/staging_module_probe.py')
probe = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(probe)


class ModuleProbe(unittest.TestCase):
    def test_forbidden_response_reports_only_safe_metadata(self):
        target = 'https://proxy.golang.org/github.com/segmentio/encoding/@v/v0.5.4.zip'
        signed = 'https://storage.googleapis.com/public-bucket/archive?Signature=never-report-this&Expires=1'
        error = HTTPError(signed, 403, 'forbidden', {}, io.BytesIO(b'<Error><Code>AccessDenied</Code><Message>never-report-this</Message></Error>'))
        def request(_request, timeout):
            self.assertEqual(timeout, 20)
            raise error
        result = probe.probe(target, request)
        self.assertEqual(result['http_status'], 403)
        self.assertEqual(result['final_host'], 'storage.googleapis.com')
        self.assertEqual(result['error_category'], 'access_denied')
        self.assertTrue(result['signed_redirect_expired'])
        rendered = json.dumps(result)
        self.assertNotIn('never-report-this', rendered)
        self.assertNotIn('Signature', rendered)
        self.assertNotIn('Message', rendered)

    def test_redirect_refuses_other_hosts_and_plain_http(self):
        for target in ['http://storage.googleapis.com/archive', 'https://unrelated.example/archive', 'https://user:secret@storage.googleapis.com/archive']:
            with self.subTest(target=target), self.assertRaises(ValueError):
                probe.safe_host(target)

    def test_success_does_not_read_or_report_archive_contents(self):
        class Response(io.BytesIO):
            status = 200
            headers = {}
            def geturl(self):
                return 'https://storage.googleapis.com/public-bucket/archive?Signature=do-not-print&Expires=9999999999'
        result = probe.probe('https://proxy.golang.org/example.test/module/@v/v1.0.0.zip', lambda _request, timeout: Response(b'private-response-body'))
        self.assertEqual(result['http_status'], 200)
        self.assertFalse(result['signed_redirect_expired'])
        self.assertNotIn('do-not-print', json.dumps(result))
        self.assertNotIn('private-response-body', json.dumps(result))


if __name__ == '__main__':
    unittest.main()

"""A successful process is not evidence when a required staging stage skipped."""
import importlib.util
from pathlib import Path
import unittest


class ReleaseEvidenceTests(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location('release_evidence', Path(__file__).with_name('release_evidence.py'))
        self.check = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.check)

    def summary(self, auth='disabled'):
        stages = self.check.REQUIRED_STAGING + (('oidc',) if auth == 'enabled' else ())
        return dict(result='passed', sha='a' * 40, platform_mode='public', mcp_auth=auth,
                    stages=[{'stage': stage, 'status': 'passed'} for stage in stages])

    def test_full_matching_cell_passes(self):
        for auth in ('disabled', 'enabled'):
            self.check.validate_staging(self.summary(auth), 'a' * 40, 'public', auth)

    def test_skip_missing_or_duplicate_stage_blocks_release(self):
        good = self.summary()
        for stages in (good['stages'][:-1], good['stages'] + [good['stages'][0]],
                       [{**stage, 'status': 'skipped'} for stage in good['stages']]):
            with self.assertRaises(ValueError):
                self.check.validate_staging({**good, 'stages': stages}, 'a' * 40, 'public', 'disabled')

    def test_wrong_source_mode_or_auth_blocks_release(self):
        for changes in ({'sha': 'b' * 40}, {'platform_mode': 'org'},
                        {'mcp_auth': 'enabled'}, {'result': 'failed'}):
            with self.assertRaises(ValueError):
                self.check.validate_staging({**self.summary(), **changes}, 'a' * 40, 'public', 'disabled')

    def test_enabled_auth_cannot_skip_oidc(self):
        summary = self.summary('enabled')
        summary['stages'][-1]['status'] = 'skipped'
        with self.assertRaises(ValueError):
            self.check.validate_staging(summary, 'a' * 40, 'public', 'enabled')


if __name__ == '__main__':
    unittest.main()

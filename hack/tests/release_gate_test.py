"""Release authorization must use complete evidence for the exact source commit."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


class ReleaseGateTest(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location('release_gate', ROOT / 'hack/release/verify_gate.py')
        self.gate = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.gate)
        self.sha = 'a' * 40

    def run_record(self, **changes):
        record = dict(id=123, head_sha=self.sha, status='completed', conclusion='success',
                      event='workflow_dispatch', path='.github/workflows/pre-release-regression.yaml',
                      head_repository={'full_name': 'tavric-labs/mcp-runtime'})
        record.update(changes)
        return record

    def test_requires_exact_commit_and_completed_success(self):
        self.gate.validate_run(self.run_record(), self.sha, 'tavric-labs/mcp-runtime')
        for changes in ({'head_sha': 'b' * 40}, {'status': 'in_progress'},
                        {'conclusion': 'failure'}, {'conclusion': 'cancelled'},
                        {'event': 'pull_request'}, {'path': '.github/workflows/ci.yaml'},
                        {'head_repository': {'full_name': 'someone/fork'}}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.gate.validate_run(self.run_record(**changes), self.sha, 'tavric-labs/mcp-runtime')

    def test_missing_skipped_failed_or_duplicate_jobs_block_release(self):
        jobs = [{'name': name, 'status': 'completed', 'conclusion': 'success'}
                for name in self.gate.REQUIRED_JOBS]
        self.gate.validate_jobs(jobs)
        for bad in (jobs[:-1], jobs + [jobs[0]],
                    [{**job, 'conclusion': 'skipped'} for job in jobs],
                    [{**job, 'conclusion': 'failure'} for job in jobs]):
            with self.assertRaises(ValueError):
                self.gate.validate_jobs(bad)

    def test_latest_failed_run_cannot_reuse_an_older_success(self):
        with patch.object(self.gate, 'api', return_value={'workflow_runs': [
                self.run_record(id=1), self.run_record(id=2, conclusion='failure')]}):
            with self.assertRaises(ValueError):
                self.gate.verify('tavric-labs/mcp-runtime', self.sha)

    def test_job_pagination_checks_the_entire_run(self):
        jobs = [{'name': name, 'status': 'completed', 'conclusion': 'success'}
                for name in self.gate.REQUIRED_JOBS]
        jobs += [{'name': f'extra-{i}', 'status': 'completed', 'conclusion': 'success'}
                 for i in range(100 - len(jobs))]
        with patch.object(self.gate, 'api', side_effect=[
                {'workflow_runs': [self.run_record()]}, {'jobs': jobs},
                {'jobs': [{'name': 'unexpected failure', 'status': 'completed', 'conclusion': 'failure'}]}]) as api:
            with self.assertRaises(ValueError):
                self.gate.verify('tavric-labs/mcp-runtime', self.sha)
            self.assertIn('page=2', api.call_args.args[0])

    def test_no_run_or_invalid_source_blocks_release(self):
        with patch.object(self.gate, 'api', return_value={'workflow_runs': []}):
            with self.assertRaises(ValueError):
                self.gate.verify('tavric-labs/mcp-runtime', self.sha)
        for repo, commit in (('bad/path?query', self.sha), ('owner/repo', 'main')):
            with self.assertRaises(ValueError):
                self.gate.verify(repo, commit)

    def test_api_queries_remain_read_only(self):
        with patch.object(self.gate.subprocess, 'check_output', return_value='{}') as command:
            self.gate.api('repos/owner/repo/actions/runs/1/jobs')
            self.assertEqual(command.call_args.args[0], ['gh', 'api', 'repos/owner/repo/actions/runs/1/jobs'])


if __name__ == '__main__':
    unittest.main()

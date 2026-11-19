"""Execute the real staging startup/SSH fragments with offline fixtures."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class StagingCacheRegistry(unittest.TestCase):
    def configured(self, owner=None, override=None, enabled=None):
        source = (ROOT / 'test/e2e/staging-vm.sh').read_text()
        start = source.index('export E2E_IMAGE_CACHE=')
        fragment = source[start:source.index('export BIN ', start)]
        env = {'PATH': os.environ['PATH']}
        for key, value in [('GITHUB_REPOSITORY_OWNER', owner), ('E2E_IMAGE_CACHE_REGISTRY', override), ('E2E_IMAGE_CACHE', enabled)]:
            if value is not None:
                env[key] = value
        result = subprocess.run(['bash', '-c', fragment + '\nprintf "%s\\n%s\\n" "$E2E_IMAGE_CACHE_REGISTRY" "$E2E_IMAGE_CACHE"'], env=env, text=True, capture_output=True, check=True)
        return result.stdout.splitlines()

    def test_repository_owner_matches_kind_cache(self):
        self.assertEqual(self.configured(owner='tavric-labs')[0], 'ghcr.io/tavric-labs/mcp-runtime')

    def test_fork_owner_is_forwarded_into_cache_prefix(self):
        self.assertEqual(self.configured(owner='fixture-fork')[0], 'ghcr.io/fixture-fork/mcp-runtime')

    def test_manual_runs_default_to_current_project_owner(self):
        self.assertEqual(self.configured()[0], 'ghcr.io/tavric-labs/mcp-runtime')

    def test_explicit_registry_override_is_preserved(self):
        self.assertEqual(self.configured(owner='tavric-labs', override='ghcr.io/fixture/private-cache')[0], 'ghcr.io/fixture/private-cache')

    def test_cache_can_still_be_disabled(self):
        self.assertEqual(self.configured(enabled='0')[1], '0')

    def test_workflow_passes_owner_to_vm_without_passing_token_in_arguments(self):
        source = (ROOT / '.github/workflows/staging-e2e.yaml').read_text()
        fragment = source.split('      - name: Run staging E2E\n', 1)[1].split('        run: |\n', 1)[1].split('\n      - name:', 1)[0]
        fragment = '\n'.join(line[10:] for line in fragment.splitlines())
        for expression, value in [('github.repository_owner', 'fixture-fork'), ('github.actor', 'fixture-user'), ('secrets.E2E_VM_USER', 'fixture-user'), ('secrets.E2E_VM_HOST', 'fixture-host')]:
            fragment = fragment.replace('${{ ' + expression + ' }}', value)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            log = root / 'arguments.json'
            sshpass = root / 'sshpass'
            sshpass.write_text('#!/usr/bin/env python3\nimport json,sys\nfrom pathlib import Path\nPath(' + repr(str(log)) + ').write_text(json.dumps(sys.argv[1:]))\nsys.stdin.read()\n')
            sshpass.chmod(0o700)
            git = root / 'git'
            git.write_text('#!/bin/sh\nprintf "%s\\n" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n')
            git.chmod(0o700)
            env = {'PATH': str(root) + os.pathsep + os.environ['PATH'], 'HOME': str(root), 'GHCR_TOKEN': 'synthetic-fixture-token', 'E2E_RUN_ID': 'fixture-run', 'RUN_MULTITENANCY': 'true', 'FRESH_CERTIFICATE': 'false', 'WITH_MCP_AUTH': 'disabled', 'PLATFORM_MODE': 'org', 'GITHUB_REF_NAME': 'main', 'GITHUB_SHA': 'a' * 40}
            subprocess.run(['bash', '-c', fragment], env=env, text=True, capture_output=True, check=True, timeout=20)
            arguments = json.loads(log.read_text())
            self.assertIn("GITHUB_REPOSITORY_OWNER='fixture-fork'", arguments[-1])
            self.assertNotIn('synthetic-fixture-token', json.dumps(arguments))


if __name__ == '__main__':
    unittest.main()

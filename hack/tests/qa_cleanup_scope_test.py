"""Reused Kind QA must preserve unrelated namespaces and Pending workloads."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ReusedClusterCleanup(unittest.TestCase):
    def test_cached_cleanup_never_deletes_namespace_or_arbitrary_pending_pods(self):
        script = (ROOT / "test/e2e/qa-e2e.sh").read_text()
        start = script.index('  echo "[cache] removing previous policy/OAuth test grants and sessions"')
        end = script.index('  echo "[cache] refreshing operator e2e environment before cluster doctor"', start)
        cleanup = script[start:end]
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "calls"
            code = '''set -euo pipefail
SERVER_NAME=policy-fixture
OAUTH_SERVER_NAME=oauth-fixture
GO_EXAMPLE_STANDALONE_NAME=go-fixture
MT_TENANT_A=tenant-a-fixture
MT_TENANT_B=tenant-b-fixture
SESSION_ID=session-fixture
MT_SESSION_A=session-a-fixture
MT_SESSION_B=session-b-fixture
PYTHON_EXAMPLE_SERVER_NAME=python-fixture
RUST_EXAMPLE_SERVER_NAME=rust-fixture
GO_EXAMPLE_SERVER_NAME=go-example-fixture
qa_session_delete_if_present() { :; }
kubectl() {
  printf '%s\\n' "$*" >> "$CALL_LOG"
  if [[ "$*" == "get namespace -o name" ]]; then
    printf '%s\\n' namespace/mcp-team-unrelated namespace/mcp-team-old-fixture
  fi
}
''' + cleanup
            result = subprocess.run(["bash", "-c", code], capture_output=True, text=True,
                                    env={**os.environ, "CALL_LOG": str(log), "KUBECONFIG": str(Path(directory) / "empty")})
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = log.read_text().splitlines()
            self.assertTrue(any("delete mcpserver -n mcp-servers-org policy-fixture" in call for call in calls))
            self.assertFalse(any(call.startswith("delete namespace ") for call in calls), calls)
            self.assertFalse(any("--field-selector=status.phase=Pending" in call for call in calls), calls)


if __name__ == "__main__":
    unittest.main()

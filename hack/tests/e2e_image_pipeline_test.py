"""Exercise QA image helpers with fake tools and Bash errexit suppressed."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "test/e2e/qa-e2e.sh"


def function(name):
    match = re.search(r"(?ms)^" + re.escape(name) + r"\(\) \{.*?^\}", SCRIPT.read_text())
    if not match:
        raise AssertionError(f"missing QA helper {name}")
    return match.group(0)


class ImagePipeline(unittest.TestCase):
    def run_pipeline(self, ensure=0, architecture=0, publish=0, node=0):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            (root / "bin/e2e-image-cache").write_text(
                '#!/bin/bash\necho ensure >> "$CALL_LOG"\nexit "$ENSURE_STATUS"\n')
            (root / "bin/e2e-image-cache").chmod(0o755)
            log = root / "calls"
            code = '''set -euo pipefail
PLATFORM_ROOT="$FIXTURE_ROOT"
SCRIPT_DIR="$FIXTURE_ROOT"
kubectl() { echo arm64; return "$NODE_STATUS"; }
python3() { echo architecture >> "$CALL_LOG"; return "$ARCHITECTURE_STATUS"; }
publish_image_to_local_registry() { echo publish >> "$CALL_LOG"; return "$PUBLISH_STATUS"; }
''' + function("build_and_publish_image") + '''
# A conditional caller suppresses errexit inside the function, as QA's
# asynchronous failure handling does. Every prerequisite must return explicitly.
if build_and_publish_image docker.io/library/mcp-gateway:latest Dockerfile .; then
  exit 0
else
  exit "$?"
fi
'''
            env = {**os.environ, "FIXTURE_ROOT": directory, "CALL_LOG": str(log),
                   "ENSURE_STATUS": str(ensure), "ARCHITECTURE_STATUS": str(architecture),
                   "PUBLISH_STATUS": str(publish), "KUBECONFIG": str(root / "empty-kubeconfig")}
            env["NODE_STATUS"] = str(node)
            result = subprocess.run(["bash", "-c", code], env=env, text=True, capture_output=True)
            calls = log.read_text().splitlines() if log.exists() else []
            return result, calls

    def test_failed_build_or_stamp_prevents_architecture_and_publish(self):
        result, calls = self.run_pipeline(ensure=47)
        self.assertEqual(result.returncode, 47, result.stderr)
        self.assertEqual(calls, ["ensure"])

    def test_failed_architecture_prevents_publish(self):
        result, calls = self.run_pipeline(architecture=48)
        self.assertEqual(result.returncode, 48, result.stderr)
        self.assertEqual(calls, ["ensure", "architecture"])

    def test_node_discovery_failure_prevents_architecture_and_publish(self):
        result, calls = self.run_pipeline(node=50)
        self.assertEqual(result.returncode, 50, result.stderr)
        self.assertEqual(calls, ["ensure"])

    def test_tag_failure_prevents_push(self):
        code = '''set -euo pipefail
local_registry_target() { echo localhost:5001/library/fixture:latest; }
ensure_local_registry_running() { :; }
docker() { echo "docker $*"; return 51; }
run_with_retry() { echo unexpected-push; return 0; }
''' + function("publish_image_to_local_registry") + '''
if publish_image_to_local_registry fixture:latest; then exit 0; else exit "$?"; fi
'''
        result = subprocess.run(["bash", "-c", code], text=True, capture_output=True)
        self.assertEqual(result.returncode, 51, result.stderr)
        self.assertNotIn("unexpected-push", result.stdout)

    def test_registry_import_targets_selected_kind_cluster_and_preserves_failure(self):
        code = '''set -euo pipefail
CLUSTER_NAME=selected-fixture
kind() { printf '%s\\n' "$*"; return 52; }
''' + function("load_image_into_kind") + '''
if load_image_into_kind docker.io/library/mcp-runtime-registry:latest; then exit 0; else exit "$?"; fi
'''
        result = subprocess.run(["bash", "-c", code], text=True, capture_output=True)
        self.assertEqual(result.returncode, 52, result.stderr)
        self.assertIn("load docker-image --name selected-fixture docker.io/library/mcp-runtime-registry:latest", result.stdout)

    def test_success_runs_all_stages(self):
        result, calls = self.run_pipeline()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, ["ensure", "architecture", "publish"])

    def test_publication_failure_is_preserved(self):
        result, calls = self.run_pipeline(publish=49)
        self.assertEqual(result.returncode, 49, result.stderr)
        self.assertEqual(calls, ["ensure", "architecture", "publish"])


if __name__ == "__main__":
    unittest.main()

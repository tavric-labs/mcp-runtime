#!/usr/bin/env bash
# Configuration adapter only. Verification and cleanup live in hack/prod-audit.
set -euo pipefail
umask 077

: "${PROD_AUDIT_BIN:?Set the reviewed development auditor executable}"
: "${PROD_AUDIT_RUNTIME_CLI:?Set the reviewed Runtime audit CLI executable}"
: "${KUBECONFIG:?Set the dedicated production kubeconfig}"
: "${PROD_AUDIT_CONTEXT:?Set the explicit context within that kubeconfig}"
: "${MCP_EXPECTED_CLUSTER_UID:?Set the hosted cluster UID}"
: "${RELEASE_TAG:?Set the expected release}"
: "${PROD_AUDIT_MANIFEST:?Set the published release manifest path}"
: "${RUNNER_TEMP:?Requires a disposable runner directory}"

args=(--runtime-cli "$PROD_AUDIT_RUNTIME_CLI"
  --kubeconfig "$KUBECONFIG" --context "$PROD_AUDIT_CONTEXT"
  --expected-cluster-uid "$MCP_EXPECTED_CLUSTER_UID"
  --release "$RELEASE_TAG" --release-manifest "$PROD_AUDIT_MANIFEST"
  --cluster-login --report "$RUNNER_TEMP/post-deploy-audit.json")
if [[ -n "${MCP_AUTH_VERSION:-}" ]]; then
  args+=(--mcp-auth-version "$MCP_AUTH_VERSION")
fi
exec "$PROD_AUDIT_BIN" "${args[@]}"

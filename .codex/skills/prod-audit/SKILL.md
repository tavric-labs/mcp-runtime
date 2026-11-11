---
name: prod-audit
description: Audit the production MCP Runtime platform using the dedicated prod-audit MCP fixture for protocol, authorization, sessions, synthetic writes and telemetry. Use for production verification and production test planning; do not use LinkedIn, Cully or another real MCP service as a test fixture.
---

# Production audits with prod-audit

The only production MCP test target is `prod-audit` in `mcp-servers-org`.
Use LinkedIn for real LinkedIn work and Cully for memory/session work, never
for synthetic platform tests. Keep supported platform CLI, UI and read-only
cluster diagnostics available for setup and evidence; they are not additional
MCP test targets.

## Choose the check

- Protocol/transport: initialize and list tools; read `audit://guide`; get
  `audit_plan`; echo a synthetic marker. Test SSE or JSON on this same fixture.
  `audit_wait` bounds delay and can emit SSE progress; cancel its request to
  test cancellation. `audit_fail` deliberately returns a tool error.
- Authorization: use a dedicated managed agent and an expiring grant that
  names only this server. Verify an allowed read, an ungranted tool denial,
  trust limits and write/destructive side-effect limits. Check anonymous
  denial at the public route. Do not broaden a real user's grants to make a
  test pass.
- Sessions: enroll through the supported adapter, inspect the created
  session, revoke the audit grant/session and confirm the same adapter loses
  access. A new session must not revive a revoked grant. Restore only the
  audit agent's intended grant after the test.
- Synthetic writes/concurrency: open a unique run, put/get a record, race
  updates with the same `expected_revision` and require one winner. Verify
  a missing key in another run, expired-run refusal and cleanup. Use only
  synthetic strings; no credentials, LinkedIn posts or Cully memory records.
- Observability: correlate the same fixture request and time window across
  gateway audit, tool outcome, analytics and traces. Gateway `allow` proves
  authorization, not tool success. A `200` JSON-RPC response may contain
  `isError: true`. Test both deliberate errors and successful calls.

## Execute and clean up

For routine post-deployment acceptance, use the development auditor
(`hack/prod-audit/cmd/postdeploy`) with the reviewed release manifest, dedicated
kubeconfig/context and expected cluster UID. `docs/prod-audit.md` contains the
command. It owns verification, bounded access setup, fixture calls, revocation
and cleanup; keep shell/workflow wrappers limited to configuration and invoking
the pinned program. Reuse these Go checks instead of duplicating assertions
in Bash/Python. A Runtime CLI with `--requested-trust` is required for the
temporary write/destructive tests. The runner explicitly requests high trust
for those tests while retaining platform-enforced grant ceilings.

1. Read the current production runbook and choose the exact deployed Runtime
   release. Inspect prod-audit readiness, policy and tool inventory first.
2. State the exact test, expected result, bounds and cleanup. Reuse this one
   MCP; do not publish extra `qa-*` MCP servers. A temporary access record is
   acceptable when it targets prod-audit only and has an expiry.
3. Use the platform CLI's managed-agent/grant/session workflow and local
   adapter, then the official-SDK checker (`hack/prod-audit/cmd/check`). Its
   `--write` flag opts into temporary writes and cleanup; it otherwise reads.
4. Close each exact run in a finally/defer block, revoke/delete temporary
   grants and sessions, and deactivate any audit-created agent when done.
   Never reset all runs or delete real users/teams to tidy an audit.
5. Report checks as passed, failed or skipped with timestamp, release/ref,
   target, observed result and evidence location. A skipped check remains
   unverified. Record residual fixture data and its expiry if cleanup fails.

Use the existing issue-explainer workflow when a real issue is established;
explain hypotheses as hypotheses. For confirmed product defects, add a
focused regression test and prepare a separate PR. GitHub posting and
production changes retain the user's approval and identity rules.

## Fixture constraints

No platform-admin tokens, Kubernetes credentials, shell, filesystem browsing,
arbitrary outbound URLs or real service access belong inside this MCP.
Its application backend relies on the private Runtime gateway; keep gateway
enabled, required sessions and allow-list/default-deny policy. There is one
replica and no persistent volume. Run IDs isolate synthetic datasets, not
authenticated subjects; never claim cross-user data isolation from a run ID.

The source of truth for exact tool schemas, limits and operation effects is
`docs/prod-audit.md`, `config/prod-audit/servers.yaml` and SDK `tools/list`.

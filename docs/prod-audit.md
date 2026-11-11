# Production auditing with prod-audit

A dedicated MCP fixture for auditing MCP Runtime in production. It replaces
disposable demo MCP servers: tests act on synthetic data here, while LinkedIn
and Cully continue their real work.

The fixture supports Streamable HTTP in SSE or JSON response mode, tools,
resources and prompts. It has no administrator token, Kubernetes client,
external HTTP client, shell or filesystem tools. Its job is to produce
controlled successes, writes, revision conflicts, errors and cancellations so
the platform's gateway, grants, sessions and telemetry can be checked.

## Tools

| Tool | Effect | Purpose |
| --- | --- | --- |
| `audit_inspect` | Read | Version, transport mode, limits and current fixture counts |
| `audit_echo` | Read | Round-trip a synthetic marker |
| `audit_record_get` | Read | Read a record in one exact run |
| `audit_wait` | Read | Delay up to 2 seconds, cancellation and SSE progress |
| `audit_fail` | Read | Deliberate tool-level error for attribution checks |
| `audit_run_open` | Write | Open an isolated expiring run |
| `audit_record_put` | Write | Create/update a synthetic record, optionally with compare-and-swap |
| `audit_record_delete` | Destructive | Delete one synthetic record |
| `audit_run_close` | Destructive | Delete one exact run and its records |

`audit://guide` is a readable resource. `audit_plan` is a prompt describing the
platform audit checklist. Tool annotations and deployment metadata declare the
effects independently; Runtime grants remain the authority for permission.

## Data and limits

Each run has a random 128-bit identifier. Use a separate run per test; record
keys in different runs never overlap. These identifiers isolate test runs, not
authenticated users: authorized auditors who possess a run identifier can use
that run. Do not store real user data or secrets here.

Data lives only in memory: 128 runs, 64 records per run, 4 KiB per value and
64 ASCII characters per key. Runs expire after 15 minutes by default, at most
one hour. Expired records are inaccessible immediately and are swept at least
every 30 seconds while the process is running. Restarting the single replica
clears all runs. A write with `expected_revision: 0` requires a missing key;
other revisions enable compare-and-swap. Retrying an identical unconditional
write leaves its revision unchanged. Revisions do not reset after deletion.

Close a run in a finally/defer block. A run can be closed repeatedly. There is
no wildcard delete, global reset, credential echo or arbitrary outbound URL.
Logs contain tool name, outcome and duration, never inputs, record values,
request headers, bearer tokens or run identifiers.

## Local checks

```sh
hack/ci/go-test.sh . -race -count=1 ./hack/prod-audit/...
go vet ./hack/prod-audit/...
go build ./hack/prod-audit/cmd/...
go run ./hack/prod-audit/cmd/server
```

The default listener is `127.0.0.1:8080`. In another terminal:

```sh
go run ./hack/prod-audit/cmd/check --endpoint http://127.0.0.1:8080/mcp --write
```

Set `PROD_AUDIT_JSON_RESPONSE=true` to test JSON instead of the default SSE.
`MCP_PATH` selects the MCP route (default `/mcp`); Runtime injects the deployed
route, such as `/prod-audit/mcp`, and the server serves that exact path.
With SSE, `audit_wait` sends progress when the caller provides a progress
token. Request bodies are capped at 32 KiB. These are local fixture tests;
they do not establish platform authentication or audit-log correctness.

## Production deployment

Use the reviewed release CLI and the checked-in `config/prod-audit/servers.yaml`:

```sh
mcp-runtime server validate --metadata-dir config/prod-audit
mcp-runtime server build image prod-audit --metadata-dir config/prod-audit --dockerfile Dockerfile.prod-audit --context . --tag v0.1.1
mcp-runtime server push --image registry.mcpruntime.org/org/prod-audit:v0.1.1 --scope org
mcp-runtime server deploy prod-audit --scope org --metadata-dir config/prod-audit
```

Use the exact image reference the build command prints. The fixture is
organization-scoped and must stay behind Runtime's private gateway with
allow-list/default-deny policy and required managed sessions. The container's
`PROD_AUDIT_LISTEN=:8080` setting is for that private backend; do not publish it
directly. No persistent volumes or application credentials are needed.

Use a managed audit agent and a narrow grant for `prod-audit` only. Read-only
checks need the five read tools at low trust; temporary write tests additionally
need the two write tools at medium trust and two cleanup tools at high trust,
plus `read`, `write`, `destructive` side effects. Temporary grants must expire.
Enroll through the supported Runtime adapter:

```sh
mcp-runtime adapter proxy --server prod-audit --namespace mcp-servers-org \
  --agent <managed-audit-agent-id> --requested-trust high \
  --runtime-url https://mcp.mcpruntime.org/prod-audit/mcp \
  --listen 127.0.0.1:8099 --auto-refresh
go run ./hack/prod-audit/cmd/check --endpoint http://127.0.0.1:8099/mcp --write
```

The checker refuses an endpoint whose MCP implementation name is not
`prod-audit`. The fixture checker does not create agents, grants or platform sessions itself; the post-deployment helper manages the full lifecycle.
See the checked-in audit skill (`.codex/skills/prod-audit/SKILL.md`) for the actual platform checks
and the difference between an allowed request and a successful tool call.

## Shared post-deployment auditor

The development auditor (`hack/prod-audit/cmd/postdeploy`) owns the checks previously split between Runtime's
`post-deploy-check.sh`, `check-calls.py` and its deployment workflow. The
workflow supplies reviewed configuration and invokes this program.

```sh
go build -o /tmp/prod-audit-postdeploy ./hack/prod-audit/cmd/postdeploy
/tmp/prod-audit-postdeploy \
  --runtime-cli /path/to/reviewed/mcp-runtime \
  --kubeconfig /path/to/dedicated-production-kubeconfig --context prod-mcp-runtime \
  --expected-cluster-uid "$MCP_EXPECTED_CLUSTER_UID" \
  --release "$RELEASE_TAG" --release-manifest /path/to/platform-manifest.json \
  --mcp-auth-version 0.9.1 --cluster-login --report /private/path/audit.json
```

Use a Runtime CLI that supports `adapter proxy --requested-trust`; the
v0.17.0 CLI does not have this option. The auditor checks support before
creating any test access. A medium/high-capable grant is a ceiling, so the
audit session explicitly requests high trust for its synthetic cleanup tools.
The platform still caps consent at the grant's ceiling.

The auditor verifies the exact cluster UID, completed deployment generations
and replica rollouts, release-manifest images, anonymous registry denial,
authorization metadata and `cluster doctor`. It creates a unique managed agent
in the existing audit team, a 30-minute grant targeting prod-audit, and a local
certificate adapter. It waits for the newly created grant to reach the gateway
policy before checking real tool results, explicit ungranted-tool and
anonymous denials, temporary CRUD/revision conflicts, session revocation and
cleanup. It never builds, deploys or retires another MCP server.

Deployment namespaces, image pins and public endpoint checks run in the
operator's runner. `--cluster-login` explicitly authorizes reading the selected
cluster's configured administrator credentials and obtaining a fresh user
token through the normal login endpoint. They stay in memory and the CLI child
environment, never process arguments, fixture requests, logs or reports. This
mode is restricted to the known Tavric production platform/fixture origins.
Alternatively provide a real user `MCP_PLATFORM_API_TOKEN`. Operator service
keys can manage deployments but cannot stand in for a user session.

Use a dedicated production kubeconfig whose current context matches
`--context`; the auditor never changes the workstation's default context.
Cleanup runs on failure too, removes the run and temporary grant/session, and
deactivates the audit-created agent. Audit history is retained. The overall
deadline is five minutes; an unsuccessful check or incomplete cleanup exits
nonzero and writes a failed JSON report. Local fixture TTL is a cleanup backstop.

Reports list completed checks and their timestamps/durations. Browser/UI and
manual analytics/trace correlation use the skill's additional checks and must
be recorded separately. The production audit exercises the persistent fixture;
the release gate's isolated Kind/staging runs cover new server publishing.

The fixture and shared checks belong to the MCP Runtime Go module: `hack/prod-audit/internal/{fixture,server,audit}`. Development entry points are `hack/prod-audit/cmd/{server,check,postdeploy}`. They are not customer CLI commands or release assets; there is no separate repository or auditor release dependency.

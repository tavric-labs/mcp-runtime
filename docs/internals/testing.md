# Tests

This page maps the main test suites to the code they protect. Prefer the
narrowest suite while iterating, then broaden before pushing when the change
touches shared contracts.

## Isolate unit tests from live clusters

Before running Go tests, pre-commit, or a commit whose hooks run tests, use an
empty kubeconfig in a subshell so tests cannot reach the contributor or
production cluster:

```bash
(
  export KUBECONFIG=$(mktemp)
  trap 'rm -f "$KUBECONFIG"' EXIT
  go test ./... -count=1 -race
)
```

Run integration tests with envtest assets. Run live E2E separately with the
explicit isolated Kind kubeconfig from the contributor guide.

## Fast Package Tests

| Surface | Tests | Use when |
|---|---|---|
| API types | `go test ./api/v1alpha1/... -count=1` | CRD structs, validation, deepcopy, scheme registration |
| CLI | `go test ./internal/cli/... -count=1` | command routing, command behavior, setup planning, registry helpers, doctor checks |
| Operator | `go test ./internal/operator/... -race -count=1` | reconciliation defaults, owned resources, status, registry/image behavior |
| Metadata | `go test ./internal/blueprint/... -count=1` | `.mcp` loading, host resolution, manifest generation |
| Platform services | `go test -race -count=1 ./...` in each service module | API, UI, ingest, processor, proxy service logic |

### Gateway filter boundaries

The gateway's `Filter[In, Out]` contract in `services/mcp-gateway/filter.go`
models five phases: `RequestInput` → `InspectedRequest` → `PolicyRequest` →
`AuthenticatedRequest` → `AuthorizedRequest` → `PermittedRequest` → upstream. Each filter returns
its phase output and an optional `*FilterResponse`: `nil` continues; a non-nil
response describes a terminal outcome and carries its decision. The runner
renders denials and public metadata; the upstream stage owns proxy streaming.
Even a terminal output must retain partial state used by audit and metrics.

Call a filter directly with its typed input to test a boundary. To test
orchestration, start with `server.buildPipeline()` and replace a named slot:

```go
pipeline := server.buildPipeline()
pipeline.Upstream = FilterFunc[PermittedRequest, AuthorizedRequest](
    func(permitted PermittedRequest) (AuthorizedRequest, *FilterResponse) {
        in := permitted.request
        // Assert the captured policy, identity and authorization decision.
        return in, forwardedResponse(in.Decision)
    },
)
pipeline.Run(exchange)
```

The runner fixes stage order and copies completed outputs into `Exchange` for
observability; pre-dispatch filters never receive that observation record or
an HTTP writer. Only the permitted upstream input carries the writer. Request state is shared,
while policy documents and session bindings are treated as immutable snapshots.
Upstream dispatch requires an explicit allow. Audit and request
metrics finalize in `handleGateway`, including early exits and proxy aborts.
Dispatch outcomes preserve credential-injection and idempotency results during
stream failure; policy metrics retain the authorization decision separately
from operational denials such as upstream backpressure.

`inspectRPCBody([]byte)` extracts RPC inputs without HTTP or I/O;
`inspectRPCRequest` owns body capture and replay. `authorizeRequest` evaluates
policy facts at an explicit time without an HTTP writer or live session cache.
The filters adapt these results to HTTP. Set `gatewayServer.now` before serving
requests to control grant/session expiry in filter tests; session-cache reads
also take an explicit time. Session lookup uses the read-only `SessionReader`
interface with a typed `SessionRef`. OAuth authentication uses
`OAuthAuthenticator`; tests can replace both without network access.
OAuth JWT verification retains its verifier's clock.

Tests are named for the behavior they verify. `rpc_test.go` exercises extraction,
malformed inputs, exact size limits, bounded reads and body replay.
`authorization_test.go` checks grants, consent, side effects and expiration;
`filter_auth_test.go` and `session_sync_test.go` check expiry and staleness at exact
instants without sleeps. `filter_test.go` checks early exits, audit state, policy
reloads and denied dispatch; `stage_trace_test.go` checks trace context and order.
`contracts_test.go` checks dependency injection, wrapped errors, zero dispatch
capabilities, invalid terminal outcomes, and method observation with both dated
protocol revisions. Those pass-through checks do not certify full protocol
conformance or implement an extension.
Run the full gateway suite with an isolated kubeconfig:

```bash
hack/ci/go-test.sh services/mcp-gateway -race -count=1 ./...
```

## macOS Adapter form checks

On macOS with the Swift command-line tools and Python 3.11 or later (for TOML
parsing), check profile validation, client config preservation, custom MCP
endpoint construction, and timestamp display inputs from the repository root:

```bash
swiftc desktop/macos/MCPRuntimeAdapter/ProfileDraft.swift desktop/macos/Tests/ProfileDraftTests.swift -o /tmp/mcp-adapter-form-tests
/tmp/mcp-adapter-form-tests
swiftc desktop/macos/MCPRuntimeAdapter/ControlClient.swift desktop/macos/Tests/ControlClientTests.swift -framework AppKit -o /tmp/mcp-adapter-client-tests
/tmp/mcp-adapter-client-tests
```

The macOS Companion Build workflow runs these checks and builds the universal
app. These checks do not establish desktop interaction, OAuth browser redirects,
or sleep/wake acceptance; exercise those flows with the app on a real Mac.

## Frontend and browser regressions

In `services/ui/frontend`, run `npm ci`, `npm test -- --maxWorkers=4`, and
`npm run build`. The build replaces committed assets under `services/ui/static`;
commit the generated HTML, JavaScript and CSS together.

Install the pinned browser with `npx playwright install chromium`, then run
`npm run test:browser`. The `@playwright/test` version in `package-lock.json`
owns the browser revision; follow the [official Playwright CI guide](https://playwright.dev/docs/ci)
when updating it. CI installs system browser dependencies too. These tests
exercise the rendered application with explicit API fixtures, including agent
lifecycle failures and namespace-qualified access links. They do not establish
real service authorization, certificate enforcement or live cluster readiness.
Use Kind/Staging E2E and the dashboard QA skill for those gates. Failed browser
runs retain screenshots and traces in `frontend/test-results` (ignored by Git).

## Shared infrastructure boundaries

Existing helper tests move with their owner: environment parsing to `pkg/envcfg`,
TLS validation and renewal to `pkg/transport/tlsconfig`, outbound HTTP propagation
to `pkg/transport/httptrace`, request metrics/logging/correlation to
`pkg/telemetry`, Kafka propagation to `pkg/telemetry/kafkatrace`, and discovery to
`pkg/oidcdiscovery`. Generic HTTP primitives live in `pkg/httpx`; grant/session
routing tests live beside runtime-api's access handlers. These are source moves,
with the existing timeout, error, authentication and telemetry contracts retained.

The root regression suite also checks transitive dependency boundaries using
`go list` in the root and gateway modules. It does not create clients, contact
services or discover a cluster:

```bash
hack/ci/go-test.sh . -count=1 ./test/regression -run TestSharedInfrastructureDependencyBoundaries
```

### Package layout guards

Three offline tests in `test/regression` keep `pkg/` from drifting. They parse
imports and `go list` output only; they do not contact a cluster.

| Test | Fails when |
|---|---|
| `TestSharedPackageLayerDesign` | a `pkg/` package imports a higher layer, imports Kubernetes libraries below the adapter layer, or is not assigned a layer |
| `TestPkgHoldsOnlyCodeSharedAcrossModules` | a `pkg/` package is used by fewer than two Go modules (the root module and each `services/*` module count separately; test files and use through another `pkg/` package count). Move single-consumer code to that module's `internal/` tree, or add a reasoned `soleModuleExceptions` entry |
| `TestEveryPackageDocumentsItsPurpose` | a `pkg/` package has no package doc, or its first sentence is too short to say what the package does |

```bash
hack/ci/go-test.sh . -count=1 ./test/regression -run 'TestSharedPackageLayerDesign|TestPkgHoldsOnlyCodeSharedAcrossModules|TestEveryPackageDocumentsItsPurpose'
```

## Golden CLI Tests

CLI help and stable output snapshots live under `test/golden/cli`.

Run:

```bash
go test ./test/golden/... -count=1
```

Update golden files only when the CLI output intentionally changes. For help
text, verify the actual command output from `./bin/mcp-runtime <command> --help`
before editing snapshots.

## Integration Tests

Integration tests live under `test/integration` and use envtest assets. They are
the right place for Kubernetes API behavior that fake clients cannot represent.

Run:

```bash
go test ./test/integration/... -count=1
```

If envtest assets are missing, follow the setup in `Makefile.operator` or the CI
workflow.

## QA E2E

The `adapter-certificates` scenario includes cross-team delegation: resource
owner A publishes and deploys through the CLI, grants a known team-B human/agent
time-bound access, verifies B's allowed and ungranted calls and unrelated C's
denial, revokes linked sessions, and checks certificate denial within one second
and persisted actor-B/resource-A audit fields. It runs only on its explicit Kind
target. The offline helper checks are `python3 test/e2e/cross_team_assertions_test.py`;
they verify assertions and timing logic without replacing the live journey.

`test/e2e/qa-e2e.sh` creates a Kind cluster, configures a local registry mirror,
builds and publishes runtime images, runs `setup --test-mode`, deploys example
servers, exercises MCP requests, and verifies governance/observability paths.
When a rollout or MCPServer readiness wait fails, it records allowlisted,
bounded MCPServer conditions, workload readiness, matching Warning Events, and
recent operator logs under the run's `rollout-diagnostics` artifact directory.
Collection uses only the explicit E2E kubeconfig after confirming its
`kind-${CLUSTER_NAME}` context; failed or unauthorized reads produce explicit
collection-gap files. Failed setup also captures redacted platform workload
status and warning events before teardown, including in the scoped Cilium
workflow. It also retains only allowlisted runtime initialization error lines,
with credentials redacted. Any failed E2E run additionally saves the last 15
minutes (200 lines) of the log of every pod in the platform namespaces
(`mcp-runtime`, `mcp-platform`, `mcp-observability`, `mcp-log-collector`,
`registry`, `traefik`, `cert-manager` and the `mcp-servers*` namespaces) as
`pod-logs/<namespace>/<pod>.log`, plus `<pod>.previous.log` for a pod whose
container restarted. Pods are discovered, so a new service is covered without
an edit, and an API `query_failed` response comes with the backend error. The
same redaction and size cap apply; collection is limited to 30 pods per
namespace and 150 seconds, and a namespace that cannot be read is recorded in
`pod-logs/<namespace>/_pods.txt`. The fake-tool regression
`bash test/e2e/rollout_diagnostics_test.sh` checks failure preservation,
redaction, and collection before cleanup without contacting a cluster.

For the component-level request paths behind each scenario, see [Request Flows](request-flows.md).

Useful local runs (first prepare the cluster with
[Local Kind and Test Mode](../contributor/local-kind.md)):

```bash
export KUBECONFIG="$HOME/.kube/test-mcp-runtime-config"
export CLUSTER_NAME=mcp-runtime
E2E_SCENARIOS=smoke-auth bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=api-platform bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=ui-auth bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=adapter-proxy bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=cli-platform bash test/e2e/qa-e2e.sh
E2E_SCENARIOS=all MCP_DEPLOYMENT_TIMEOUT=900s bash test/e2e/qa-e2e.sh
E2E_DEEP_REQUEST_FLOWS=1 E2E_SCENARIOS=all bash test/e2e/qa-e2e.sh
E2E_KEEP_CLUSTER=1 E2E_SCENARIOS=smoke-auth bash test/e2e/qa-e2e.sh
E2E_CACHE_MODE=1 E2E_SCENARIOS=smoke-auth bash test/e2e/qa-e2e.sh
```

Supported scenario selectors are `all`, `smoke-auth`, `governance`, `trust`,
`oauth`, `observability`, `multitenancy`, `api-platform`, `ui-auth`,
`adapter-proxy`, and `cli-platform`. The targeted PR-only selectors map to
request-path surfaces: `api-platform` covers platform API auth, user, registry,
team, runtime, deployment, and admin routes; `ui-auth` covers direct UI and
gateway cookie auth/static routes, including the real Traefik Grafana gate:
signed-out HTML page requests must redirect through platform sign-in with the
original trace query, authenticated administrators must reach Grafana's login,
and API requests must retain JSON 401 responses. The cluster check inspects the
redirect Location: Traefik's forward-auth middleware needs
`preserveLocationHeader: true` to avoid an internal-service redirect. The setup
unit tests cover both managed and external controller manifests.
Frontend Playwright regressions also verify administrator return and
non-administrator rejection after sign-in;
these API fixtures complement the cluster path checks. `adapter-proxy` covers platform-issued
adapter sessions plus the local adapter proxy MCP path; `cli-platform` covers
the platform-backed CLI request flow.

`E2E_CACHE_MODE=1` is for repeated runs on one machine. It implies
`E2E_KEEP_CLUSTER=1` and reuses the existing Kind cluster and upstream-image
mirror. Platform setup runs when the platform is not ready or an image hash
misses. It is skipped only when the platform is ready and every component
matches the last build.

### Local latest image cache

For repeated runs on one machine (for example a contributor's Kind cluster),
`E2E_IMAGE_CACHE=local` avoids GitHub Container Registry. Each component keeps one local Docker tag,
`:latest`, labeled `mcp-runtime.e2e-content-hash`. The next run compares the
checkout hash to that label: a match reuses the image, a miss rebuilds and
replaces the same tag. Dangling previous image IDs are pruned. BuildKit cache
is capped (`E2E_DOCKER_BUILD_CACHE_MAX`, default 8GB) when Staging E2E reclaims
disk, and those labeled images are kept.

`E2E_IMAGE_CACHE=0` builds every image. Pull-request CI uses the GHCR cache
below instead, because each GitHub runner starts with an empty Docker cache.

### Content-hash GHCR image cache

Pull-request QA E2E and Staging E2E skip rebuilding
unchanged platform images by pulling content-hash tags from GitHub Container
Registry. QA E2E pushes a tag after each miss; Staging E2E only reads them:

| Env | Meaning |
|-----|---------|
| `E2E_IMAGE_CACHE=1` | Enable pull-or-build in QA prebuild and setup; enabled by default in both E2E runners |
| `MCP_SETUP_IMAGE_CACHE=1` | Same cache inside `setup` image publish, including Staging E2E's strict production setup |
| `E2E_GHCR_PUSH=1` | After a cache miss build, push `ghcr.io/<owner>/mcp-runtime/<component>:<hash>-<architecture>` |
| `E2E_IMAGE_CACHE_REGISTRY` | Override registry prefix (default `ghcr.io/<owner>/mcp-runtime`) |
| `E2E_IMAGE_CACHE=0` | Force-disable even if setup cache is set |

Each hash covers the component's Dockerfile, `.dockerignore`, module files,
and the Go files in its transitive import graph for the target architecture.
The UI hash also covers its frontend files. An edit to an unused package
does not rebuild that service; an edit to an imported shared package rebuilds
every service that imports it (see `internal/cli/setup/platform/imagecache`).
Cluster tags stay `:latest` in
test-mode; only GHCR uses the hash tag. Forks without `packages: write` to the
parent org can pull existing cache entries but cannot publish misses. Bust the cache with
`E2E_IMAGE_CACHE=0` or by changing a hashed input file.

`E2E_DEEP_REQUEST_FLOWS=1` is for explicit full Kind sweeps, not normal PR feedback.
It requires `E2E_SCENARIOS=all` and adds broader CLI help, adapter proxy,
platform API, UI auth, registry authz, team, deployment, and item-level runtime
request flows.

Image mirroring and local runtime/platform image builds run with bounded
parallelism. `E2E_IMAGE_PREP_PARALLELISM=<n>` tunes the shared prep default,
`E2E_IMAGE_MIRROR_PARALLELISM=<n>` tunes pull/push mirroring, and
`E2E_IMAGE_BUILD_PARALLELISM=<n>` tunes local Docker builds. CI sets mirroring
to one worker to avoid Docker/local-registry push contention on shared runners
and builds to two workers because builds are heavier on runner CPU, memory, and
Docker. CI also sets `MCP_POLICY_WAIT_TRIES=45` so stuck gateway-policy waits
fail sooner than the local default of 90, and `MCP_HTTP_TIMEOUT=15` so Traefik
504/502 retries on the ingress path recover faster than the local 30s default.

QA E2E deploys a single primary MCP server (`policy-mcp-server`) for most PR
paths. That primary omits `spec.auth` so adapter-certificate and grant/deny
checks exercise cert-first identity; do not apply empty `auth: {}` (that turns
OAuth on via the derived issuer and changes expected `401` reason codes). CI
sets `E2E_MAX_MCP_SERVERS=2` so multitenancy can reuse that server as
tenant-a and deploy only `mt-tenant-b` as the second workload. The older
data-utility, text-analysis, and workspace-assistant sample deploys are not used
anymore; ingress checks exercise multiple tools on the primary server instead.
When `MCP_ADAPTER_CERTIFICATES=true`, adapter and DIRECT/header-proxy probes use
Traefik `websecure` at `https://127.0.0.1:18443/...` (insecure TLS skip for the
local default cert); plain Ingress and platform routes stay on
`http://127.0.0.1:18080/...`.
Set `E2E_MAX_MCP_SERVERS=0` locally for unlimited servers during full
explicit `E2E_SCENARIOS=all` Kind runs.

**What runs in parallel (safe):** local Docker image builds and registry mirroring
(`E2E_IMAGE_BUILD_PARALLELISM`, `E2E_IMAGE_MIRROR_PARALLELISM`); read-only grant
rule ConfigMap polls (`E2E_GRANT_RULE_PARALLELISM`); and api-platform plus ui-auth
HTTP scripts when `E2E_HTTP_FLOW_PARALLELISM>1` (workers clear the inherited EXIT
trap so they do not delete the shared kubeconfig).

**What stays sequential (shared MCP state):** every `run_parallel_mcp_tool_checks`
batch. Those calls share one session URL, header proxy, and gateway reload window,
so trust/ingress tool waits always run one at a time regardless of other
parallelism knobs.

Parallel worker output is buffered under `stage-logs/` in the e2e workdir and
copied into `E2E_ARTIFACT_DIR` when artifacts are enabled. Live output prints
colored `START`, `RUNNING`, `DONE`, and `FAILED` lifecycle lines plus short
stdout/stderr previews; tune those previews with `E2E_LOG_PREVIEW_LINES` and
`E2E_LOG_FAILURE_LINES`. Major sequential stages such as local registry startup,
Kind cluster creation, CLI rebuilds, setup, cluster doctor, and MCP server
deploys are also mirrored to `stage-logs/` in the same artifact.

E2E output uses ANSI color for interactive terminals and GitHub Actions by
default. Set `E2E_COLOR=always` or `E2E_COLOR=never` to override
auto-detection; `NO_COLOR` disables color.

QA E2E traffic uses deterministic curl-based MCP requests. The previous
real-client agent prompts are disabled so CI and local runs do not consume
OpenAI or Anthropic tokens while validating gateway policy, auth, audit, and
observability paths.

The `observability` scenario validates the trace backend through both direct
Tempo and Grafana's Tempo datasource. It must find a single request trace that
contains the gateway service, `mcp-ingest`, `mcp-processor`,
and the `kafka.produce`, `kafka.consume`, `clickhouse.insert_event`, and
`clickhouse.insert_batch` spans.

For [#498](https://github.com/mcp-runtime/mcp-runtime/issues/498), the same
scenario follows the controlled invalid-token MCP request by its audit trace ID
through Grafana. It requires an error span from the OAuth-protected gateway and
a Loki `event=gateway.request` log with the matching trace ID, HTTP 401, and
bounded `invalid_token` reason. OAuth refresh failures inside a separate
provider or client cannot be generated or instrumented by this repository; see
the documented boundary in [Follow an MCP request](../request-debugging.md).

PRs with runtime or CI changes run short QA E2E with `smoke-auth` as the baseline;
documentation-only PRs skip the Kind job. Changelog, contributor guide, article,
and skill reference edits do not force all scenarios when changed alongside code.
For code PRs,
`.github/workflows/ci.yaml` calls `test/e2e/select_pr_scenarios.sh` to add
targeted scenarios based on the changed files. API, UI, adapter, CLI, OAuth,
observability, and multi-tenancy changes get the matching request-path mode;
shared install paths (API types, operator, manifests, module files, and the
E2E harness) stay on `smoke-auth`, which is the setup and placement check.
The `multitenancy` scenario checks that two team users can read only their own
team and namespace and that their registry credentials cannot access the other
team's repositories. Pre-release Regression runs full Kind sweeps across
org/public modes and single/multiple replicas, plus serialized staging runs
across org/public and bundled OAuth enabled/disabled. Staging exercises the
production setup, TLS, registry, tenant image build, push, deploy, and adapter
path on the disposable VM. See [Release Gate](../contributor/release-gate.md)
for required combinations, evidence boundaries and completion criteria.

The script writes artifacts when `E2E_ARTIFACT_DIR` is set. In CI, those
artifacts are uploaded from `.e2e-artifacts/qa`.

## CI Coverage

The main CI workflow runs:

- formatting check
- `go vet`
- `staticcheck`
- unit and integration tests
- golden CLI tests
- service module tests
- generated file drift
- repository SBOM generation
- path-selected short QA E2E on code PRs and manual CI runs

Pull-request QA E2E runs Kind test-mode on a fresh GitHub runner. Each run uses
its own cluster and registry names, so PRs run in parallel, and unchanged
platform images come from the content-hash GHCR cache. Staging E2E runs on the
disposable VM from Pre-release Regression, not on merges.

The manual Pre-release Regression workflow runs the full Kind and staging matrices, benchmarks,
repository and platform-image SBOMs, gosec, Gitleaks, and Trivy scans alongside
static, unit, integration, and service checks. It also runs frontend/browser,
plugin, native registry, macOS preview, Cilium and protocol/transport checks.
Every required check must succeed on the exact publication commit. Security workflows add pinned gosec,
Trivy repository/image scans with SARIF upload, pinned Gitleaks secret
scanning, operator-image SBOM artifacts, and pull-request dependency review.

Use the [request-flow matrix](request-flows.md#use-case-matrix) to choose additional focused QA when a change needs coverage beyond the staging journey.

## Pre-commit Hooks

`.pre-commit-config.yaml` mirrors the local contributor gates: Go formatting,
`staticcheck`, `go vet`, targeted Go tests, generated-file drift checks, and a
pinned Gitleaks hook for staged secret detection. Install with
`pre-commit install`; run `pre-commit run --all-files` before pushing when you
want the full local hook suite. The full suite includes integration hooks that
install envtest assets and set `KUBEBUILDER_ASSETS`.

The cross-team certificate fixture uses the shared, bounded initialization wait
for Traefik TLS/session readiness. It supplies the recipient certificate and
full initialize payload without reusing another fixture's bearer. Tool calls
and the one-second revocation assertion retain their original checks.

## Choosing Coverage

| Change | Minimum local check |
|---|---|
| Cobra help or flags | `go test ./internal/cli/... ./test/golden/... -count=1` |
| setup, registry, or cluster doctor | targeted `internal/cli` tests plus a local Kind or k3s smoke when behavior affects pulls |
| CRD schema | `make -f Makefile.operator generate manifests`, API tests, operator tests |
| reconciliation | operator tests plus integration or e2e when resource ownership changes |
| gateway policy | service tests plus the governance e2e scenario |
| docs-only | `git diff --check`; MkDocs build when available |

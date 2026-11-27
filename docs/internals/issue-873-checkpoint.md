# Durable runtime migration checkpoint

Issue: https://github.com/tavric-labs/mcp-runtime/issues/873

Working log for turning `runtime-api` into an internal durable runtime. Not a published guide. Do not add this file to `docs/mkdocs.yml`.

The design lives in the [issue design comment](https://github.com/tavric-labs/mcp-runtime/issues/873#issuecomment-6465833216) (source audit: 2026-10-28 against commit `4fb845594f071212496aec4bd08c9d503612d6f9`; revision IDs and ownership accepted: 2026-10-29). This file is how a later session continues that design. Read it first. Update **Current phase**, the checkboxes, **Evidence**, and **Do not redo** before stopping.

- **Current phase:** Backend journals/workers/observers from `#980`–`#982`, operator corrections `#983`, and spike cleanup `#985` are on `main`. The missing legacy team-handler characterization is now covered offline. Public cutover remains unfinished; `#984` contains startup hardening and a separate deadline fix.
- **Next task:** Add durable operation acceptance/idempotency and supported worker wiring, then move directory handlers, contracts, and callers to `platform-api` using the accepted HTTP 202 contract below. Complete the remaining Phase 1 and Phases 3–5 acceptance gates before Phase 6 public-route retirement and rename.
- **Stop rule:** a failed Kind gate or an open decision blocks the phase. Record the failure here and stop. Do not start the next phase.

## What changes, in one pass

Today `runtime-api` is both the public API and the worker. Several handlers do multi-step work inline. `platform-api` already sits on the auth path (`database/sql` + `github.com/lib/pq` in `services/platform-api/internal/platformstore/store.go`).

Target, once the gates below pass:

- `platform-api` is the only public management API. It authorizes the request and, in one Postgres transaction, writes desired state, an operation record, and a River job.
- `runtime` (today's `runtime-api`) consumes that queue, reconciles idempotently, and writes receipts back through a typed internal API. It does not get called by platform-api to accept work.
- A finished River job means one bounded step was applied. It does not mean the resource is healthy. Health comes from a separate observer.
- The existing MCP operator keeps reconciling `MCPServer` objects into workloads. Runtime applies the custom resource. It does not become a second Deployment controller.
- Kafka and ClickHouse stay the analytics path. Queue state does not move there.
- Image bytes and registry credentials never go in job arguments.

A completed job and current health are different facts. Example: revision `rev-B` is applied and the job completes, the server later becomes Ready, then a node failure marks it Degraded. The original job stays completed.

## Three ownership layers

| Layer | Owns | Writes |
|---|---|---|
| Platform API — intent | What the user is authorized to request; current desired revision and business lifecycle | Domain transactions, immutable desired revisions, operations, security authority and atomic River insertion |
| Runtime — projection | Applying current platform intent to infrastructure | MCPServer/MCPAccessGrant specifications, namespaces and other runtime-owned prerequisites; application receipts through platform internal APIs |
| MCP operator — realization | Workloads and policy derived from MCPServer specifications | Child Deployments, Services, routes, policy artifacts and CR status conditions |

Status Sync is the observation responsibility: it reads infrastructure facts and records their mapped revision, conditions and freshness through the platform internal API. It cannot change intent or authority. Runtime must not compete with the operator for its child resources. Raw managed Deployments remain a separate runtime path. Replicas within each layer still need concurrency controls.

## Revision identity and write safety

Use an opaque platform revision ID instead of a numeric platform generation. A meaningful accepted desired-state change creates an immutable revision. In one transaction, the platform creates the revision, conditionally changes the resource's `desiredRevisionId` against the expected current revision, records the operation and inserts the job. The current pointer determines desired intent; revision IDs are compared for equality, not sorted to determine the winner.

| Marker | Owner and meaning |
|---|---|
| `desiredRevisionId` | Platform API: current accepted desired state |
| `appliedRevisionId` | Runtime receipt, persisted by Platform API: exact revision accepted by the external API |
| `observedRevisionId` | Status Sync, persisted by Platform API: revision associated with fresh observations |
| Worker fencing token | Execution coordinator plus enforcing destination: rejects a stale execution owner |
| Kubernetes `metadata.resourceVersion` | Kubernetes: object concurrency and watch token; not worker ownership |
| Kubernetes `metadata.generation` | Kubernetes: desired-spec counter for the CR |
| CR `status.conditions[].observedGeneration` | Operator: CR generation evaluated by a condition; does not itself imply readiness |

For example, `desiredRevisionId=rev-B`, `appliedRevisionId=rev-A` and `observedRevisionId=rev-A` means B was accepted but application/observation still describe A. When B is applied, its receipt maps `rev-B` to the target CR UID, Kubernetes generation and specification digest. Status Sync checks that mapping and the required conditions before associating fresh health with B. No-op CR updates and coalesced platform changes need not advance Kubernetes generation.

Retries and drift repair retain the revision ID; request-idempotent retries return the same acceptance. Operation/job/attempt IDs stay separate. Rollback to old configuration creates a new accepted revision referencing those contents; a digest identifies content, not acceptance identity. Receipts are conditional on incarnation, captured revision and execution fence. IDs are neither authorization nor fencing, and a conditional database receipt does not fence a Kubernetes write. Kubernetes fields remain unchanged; no top-level CR `status.observedGeneration` or extra platform sequence is proposed.

Reviewed prior art (2026-10-29): [Knative revisions](https://knative.dev/docs/serving/revisions/) and [Crossplane composition revisions](https://docs.crossplane.io/latest/composition/composition-revisions/) identify configuration snapshots; [Lambda RevisionId](https://docs.aws.amazon.com/lambda/latest/api/API_UpdateFunctionConfiguration.html) guards conditional updates. These examples inform the proposed platform contract; they do not replace it.

## How a session runs

1. Read this file and the issue section for the current phase only.
2. Spawn at most 3 subagents. Give each a file slice from the phase, not the whole issue.
3. The parent integrates, runs tests, and is the only editor of this file.
4. Unit tests use `export KUBECONFIG=$(mktemp)` in that shell. Kind uses an explicit `kind-*` kubeconfig from the contributor-cluster skill. Never leave a production context current.
5. Pure moves stay in their own commits, separate from revision identity, status, or HTTP behavior changes.

```mermaid
flowchart LR
  readFile[Read checkpoint]
  slice[Spawn at most 3 subagents]
  integrate[Parent integrates and tests]
  kind[Kind gate on kind context]
  writeFile[Update checkpoint]
  readFile --> slice --> integrate --> kind --> writeFile
```

## What the code does today

These are the flows phase 0 locks before anything moves.

**Team create** is a distributed transaction with a manual rollback. `services/runtime-api/internal/runtimeapi/teams.go` `handleRuntimeTeamCreate` calls `identity.CreateTeam`, then `Deployments().ensureTeamNamespace`. On namespace failure it calls `DeleteTeamBySlug` and returns 500. Success returns 200. `CreateTeam` in `services/platform-api/internal/platformstore/teams.go` already commits the team, namespace row, and owner membership in one transaction. A crash after that commit and before namespace provisioning leaves a team with no namespace and nothing retries. Cleanup failure is also only logged.

**Team delete** in `services/runtime-api/internal/runtimeapi/team_members.go` `handleRuntimeTeamDelete` soft-deletes the platform team first, then best-effort removes Traefik watches via `k8sprovision.RemoveTraefikDeploymentWatchesNamespace`. Watch removal failure is logged. The handler still returns 204. It does not delete the Kubernetes namespace.

**Agent deactivate** in `services/runtime-api/internal/runtimeapi/agents.go` sets the agent inactive through `SetAgentStatus`, then calls `revokeAgentSessions`. If revocation fails, the response says the agent is inactive but revocation is incomplete. Platform store deactivation of a user is a separate atomic path (`services/platform-api/internal/platformstore/agents.go`). Reactivation must not restore revoked sessions. Existing coverage: `agents_test.go` `TestDeactivateAgentRevokesOnlyMatchingSessionsAndAuditsEach`.

**Server retirement** in `services/runtime-api/internal/runtimeapi/servers.go` and `server_retirement.go` marks the server retiring, fences and revokes sessions, retires grants, removes exclusively owned credentials, then UID-conditionally deletes the custom resource. Existing coverage: `server_retirement_test.go`. A generic async delete would weaken the UID fence.

**Registry push** in `services/runtime-api/internal/runtimeapi/registry_push.go` `HandleRuntimeRegistryPush` writes a local tar (`registryPushRequest.TarPath`), registers transfer access, starts a helper, and waits. `registry_push_transfer.go` stores a local path, Pod IP, and expiry. `defer os.Remove(req.TarPath)` cleans the tar for that process. A River retry cannot recover bytes that lived only on that Pod.

**Sessions** already live in the platform store (`session_backend.go`). Kubernetes session YAML is not gateway authority. Do not queue the authoritative revoke.

## Decisions

Accepted until overridden in this section:

- Queue is River (https://riverqueue.com/), inserted with `InsertTx` on the same `*sql.Tx` as the resource write. Proven in product code (`github.com/riverqueue/river v0.51.0` + `riverdatabasesql` against `lib/pq` and PostgreSQL 17 across `#980`, `#981`, `#982`). Do not add a custom outbox or move control-plane work onto Kafka.
- Runtime's database role can read and update River tables only. Domain tables stay behind platform-api internal APIs.
- Team delete keeps today's cleanup: Traefik watches only. Namespace and user data stay.
- Jobs carry kind, resource id, incarnation, target cluster, requested revision ID, operation id, and trace correlation. They do not carry passwords, tokens, private keys, registry credentials, or archives.
- Platform desired state uses opaque revision IDs with immutable contents and a transactional current pointer, as specified above. No numeric platform generation is required. Preserve Kubernetes version fields and separate execution fencing.
- The operator is not folded into the runtime reconcile loop. Share only a small helper if a later phase shows a real one.
- **Public cutover contract (owner accepted 2026-11-27):** asynchronous acceptance returns HTTP **202** with an operation response. CLI mutations wait by default and provide `--no-wait`. Retain accepted teams when provisioning fails and surface retryable provisioning status instead of deleting the accepted team. Operation completion records a bounded execution result; resource readiness remains an independent observation. This approves the public cutover behavior, not production deployment or a reference-platform reset.

Still open. The phase that needs the answer does not start until the answer is written here:

- **Readiness/wait details.** HTTP 202, operation responses, default CLI waiting, and `--no-wait` are accepted above. Define each command's readiness predicate and terminal-error behavior explicitly in its API/CLI contract tests; do not equate a completed job with a Ready resource.
- **Phase 3 staging.** `pkg/registrystaging/` durable filesystem-backed staging store and publication intent journal/worker are merged behind `PLATFORM_WORKLOAD_INTENT_JOURNAL=true` (`#982`). Install-supported shared persistent volume vs object storage deployment policy, retention/capacity, and mutable-tag rule remain to be locked before public registry cutover. A per-Pod filesystem, `emptyDir`, or a path inside a Secret does not count as durable.
- **Phase 5 grants.** Durable grant intent and observer semantics are merged (`#981`): synchronous `RevocationCommitted` stays separate from `ProjectionApplied` and `PropagationVerified`, and unknown/absent serving gateways report `NoServingGatewayReplicas` / `propagation_verified=false`. Live serving-gateway deny propagation during replica churn remains to be verified on Kind before cutover.
- **Budgets.** Retry cap, jitter, max delay, observation freshness, and per-tenant queue limits. Tune from live Kind/staging measurements before enabling intent journals by default.
- **Rename** waits for phase 6. Module `mcp-runtime-api`, image, Deployment, NetworkPolicy, RBAC, and docs move together, after no caller still uses the public management routes.

## Phases

### Phase 0 — Characterize

- [x] Offline legacy behavior covered: team create/delete characterization added alongside the existing agent deactivation, retirement, and registry-push tests. Live migration gates remain separate.
- No behavior change. Kind is not required.
- Subagent A: team create rollback and team delete. Subagent B: agent deactivate, server retirement order, registry push local tar. Parent runs the tests.

Lock these outcomes on the current code:

- [x] Namespace provisioning failure attempts to delete the team and returns 500. If that delete also fails, the handler still returns 500 and both calls happened (`TestTeamCreateRollsBackIdentityAfterNamespaceProvisioningFailure`).
- [x] Team delete returns 204 after the platform delete even when Traefik watch removal fails. The namespace object is not deleted (`TestTeamDeleteKeepsNamespaceAndReturnsNoContentWhenWatchCleanupFails`).
- [x] Deactivate revokes only sessions for that agent and team, writes one audit per revoked session, and does not un-revoke on reactivate (`agents_test.go` `TestDeactivateAgentRevokesOnlyMatchingSessionsAndAuditsEach`).
- [x] Retirement order stays: retiring marker, session and grant fence, owned credential cleanup, UID-conditional custom-resource delete. A shared pull secret is kept (`server_retirement_test.go`).
- [x] Registry push stages a local tar path and removes it when the handler returns. The test must not require a live cluster (`registry_push_admission_test.go`, `registry_push_test.go`).

Gate command, from the repo root, after `export KUBECONFIG=$(mktemp)`:

`hack/ci/go-test.sh services/runtime-api -count=1 -race ./internal/runtimeapi -run 'TeamCreate|TeamDelete|DeactivateAgent|Retirement|RegistryPush'`

Also run any new platform-store test the agent-deactivate characterization needs. Record the exact test names in **Evidence**.

### Phase 1 — Team durable intent, worker, and observer

- [x] Backend journal, River worker, and independent observer merged on `main` (`#980`, `bc7d3e41`) behind opt-in flags (`PLATFORM_TEAM_INTENT_JOURNAL=true`, `RUNTIME_TEAM_INTENT_WORKER=true`, `RUNTIME_TEAM_INTENT_OBSERVER=true`); throwaway River spike (`#904`, `3eb6184b`) removed after real product verification
- River `v0.51.0` (`riverdatabasesql`) proven on `*sql.Tx` with `lib/pq` and PostgreSQL 17.

Completed work (`#980`):

- Extended team transactions (`services/platform-api/internal/teamintent/`, `platformstore/team_intent.go`, `platforminternal/team_intents.go`) so team intent, its current desired revision, and the River job (`team_intent_queue`) commit atomically or roll back together. A public operation record and request-idempotency contract are still needed; the existing queue row is not that operation API.
- Worker (`services/runtime-api/internal/teamintentworker/`) enforces execution fencing, incarnation isolation, and ownership-checked namespace provisioning (`k8sprovision/namespace.go`).
- Independent observer (`services/runtime-api/internal/teamintentobserver/`) observes cluster namespace state and reports freshness/readiness via fenced internal APIs.

PostgreSQL 17 + Kind gate (`async-team-873`, 2026-11-25):

- [ ] Kill the API after commit and before the HTTP response. One team row, one job, idempotent client retry finds the same operation.
- [x] Roll the transaction back. Neither the team nor a runnable job is visible (`TestPostgresTeamIntentQueueFailureRollsBackDomainChanges` against PostgreSQL 17).
- [x] Scale runtime to zero for a while. The team stays pending, queue age increases, then one namespace appears after recovery (verified live on Kind `async-team-873`, 2026-11-25).
- [ ] Timeout from the Kubernetes API after a successful namespace create. Replay uses the same namespace and does not create a second one.
- [ ] Create races delete. The old create does not recreate the namespace. Cleanup does not delete a replacement.
- [x] A delayed worker for a superseded revision cannot record `appliedRevisionId` for a different current revision (`TestPostgresTeamIntentFencedReceiptsAndIncarnationIsolation`, `TestPostgresTeamIntentIndependentObserverFencing` against PostgreSQL 17).
- [ ] Request-idempotent retry returns the same revision/operation. Worker retries and drift repair retain that revision; a completed job does not suppress later repair.
- [ ] Rollback to prior contents creates a new acceptance revision; delayed receipts for the previous acceptance cannot satisfy it.

### Phase 2 — Platform owns directory commands (public management route cutover)

- [ ] Not started (next task; backend durable intent journals/workers/observers from `#980`, `#981`, `#982` are merged and verified)
- Two subagents, overlapping the end of phase 1. One moves handlers and store calls. One updates OpenAPI, authz, and CLI goldens.
- Move team, membership, agent, and namespace commands and scoped reads onto platform-api, and switch public Ingress management routes onto platform-api. Runtime keeps infrastructure follow-up only.
- Preserve atomic user-plus-membership create (`CreateTeamUser`) and the agent-inactive plus session-revoke transaction. Membership removal is not account deactivation.
- Personal workspace counts and admin counts stay separate. Anonymous catalog reads stay limited to public namespaces.
- Gate: platform-api OpenAPI contract test, authz matrix, CLI goldens under `test/golden/cli/testdata/`, and a Kind smoke that creates a team and lists it. Record the commands in **Evidence**.

### Phase 3 — Registry recovery

- [ ] Partially implemented: durable artifact staging (`pkg/registrystaging/`), publication intent journal, and River worker (`workload_intent_queue`) merged in `#982` (`536c856c`) behind opt-in flags and covered by `TestWorkloadIntentPostgresIntegration` on PostgreSQL 17; shared PV/object-storage deployment policy, public upload route cutover, and live Kind crash/resume gate remain
- Two subagents. One owns upload and finalize. One owns the helper identity and digest check. Use a separate Kind window from ordinary team tests so image pulls do not queue behind them.
- Upload lifecycle is `uploading`, then `finalized`, or `aborted` / `expired`. A job is inserted only after the bytes are durable and the publication row commits. A crash before that commit leaves an unreferenced artifact for garbage collection.
- Helper name is derived from the publication id. A crashed worker resumes that helper instead of creating another. If the registry already has the manifest, verify the digest and finish the same publication.
- Native OCI client push and registry auth stay on their current path.
- Namespace pull credentials stay a separate reconcile step. The job row never contains the credential value.
- Gate: kill the uploader, the API, and the worker after finalize. Another replica reads the same bytes. A partial upload does not enqueue. Two publications of one tag follow the rule written above.

### Phase 4 — Servers and raw deployments

- [ ] Partially verified: durable `MCPServer` (+ 5 ordered retirement checkpoints), raw `Deployment`/`Service`, and fixed-token `RestartOperationIntent` journals, River workers (`workload_intent_queue`), and independent observer merged in `#982` (`536c856c`) behind opt-in flags; verified on PostgreSQL 17 (`TestWorkloadIntentPostgresIntegration`) and live on Kind `async-team-873` (`MCPServer` apply + 5-step ordered retirement + CR deletion; raw `Deployment` + `Service` apply/Ready/delete); remaining Kind fault gates and public cutover remain
- Three subagents: `ReconcileServer` (`servers.go`), raw Deployment/Service apply (`deployments.go` around the upsert path), and the status observer.
- Platform accepts an immutable desired server revision and updates `desiredRevisionId` in one transaction with the operation and job. The worker applies the `MCPServer` custom resource with UID and resourceVersion preconditions. The operator still owns child Deployments.
- Receipt stores `appliedRevisionId`, target UID, Kubernetes generation, and spec digest. Observer stores `observedRevisionId`, revision-tagged platform conditions and freshness. Map the platform revision to the CR identity/spec/generation; preserve operator conditions' Kubernetes `observedGeneration`.
- A node failure or `ImagePullBackOff` updates health to degraded and does not enqueue another apply. A missing desired custom resource does enqueue one repair. An operator-owned Deployment disappearing does not.
- Raw Deployment and Service upserts become `ReconcileDeployment`. They do not go through the MCP operator.
- Publication-dependent deploy waits until phase 3 has passed. Deploy by verified digest once that phase exists.
- Gate: apply job is completed while status is still progressing. Later node loss flips health only. Replacing a server with a new UID does not satisfy Ready from the old object. Delete uses the UID precondition so cleanup cannot remove the replacement.

### Phase 5 — Grants and enrollment

- [ ] Partially verified: durable `MCPAccessGrant` (distinct `RevocationCommitted` / `ProjectionApplied` / `PropagationVerified`), idempotent CSR enrollment with post-issuance authority revalidation, atomic agent deactivation + cleanup, River workers (`access_intent_queue`), and independent gateway propagation observer merged in `#981` (`e2042cfb`) behind opt-in flags; verified on PostgreSQL 17 (`TestAccessIntentPostgresIntegration`) and live on Kind `async-team-873` (`MCPAccessGrant` apply + strict `NoServingGatewayReplicas` / `propagation_verified=false` reporting + synchronous `revocation_committed=true` + CR cleanup); live gateway allow/deny + replica-churn + live CSR retry gate and public cutover remain
- Two subagents. One for grants, one for certificate enrollment (`adapter_certificate.go`).
- Do not put session revoke or agent deactivation authority into River. Those commits stay synchronous. A follow-up job may audit or project. A failed follow-up is not evidence that the revoke failed.
- Grant states to keep distinct: revocation committed, projection applied, propagation verified. Unknown replicas are not counted as acknowledged.
- Enrollment: one operation keyed by session UID, server UID, and CSR identity. The private key stays on the client. Persist the `CertificateRequest` name. Re-check session, agent, and server authority before activation. A delayed job must not un-revoke.
- Gate on Kind: an allowed tool call, a denied call, revoke during replica churn, and a retried enrollment that finds the original CSR instead of creating a second one.

### Phase 6 — Internalize and rename

- [ ] Blocked on Phase 2 public management route cutover and remaining Phases 3–5 live cutover gates
- Two subagents. One removes public management routes and updates wiring. One renames module, image, Deployment, NetworkPolicy, and RBAC, then docs.
- Do this only after no remaining caller uses the removed routes. Logs, metrics, Grafana, resource-history scrape, and registry byte streams keep their own bounded paths. They do not become River jobs.
- Startup network-policy sweep stays until a replacement gate has the same evidence.
- Changelog entry under `Unreleased`, plus the fresh-setup note if the release is a breaking `0.x` minor. This work does not authorize a reset of the hosted reference platform.
- Gate: fresh Kind install, login, team, server, grant, session, one allowed call, one denied call. Record the kubeconfig path and the commands here.

## Remaining PR slices — source audit 2026-11-27

Baseline: `main` at `67a88cfa`; open startup PR `#984` (rebased by merging current `main`). Existing unit/fake-client tests do not count as the unchecked live fault gates.

| Slice | Existing evidence | Remaining implementation / gate |
|---|---|---|
| Legacy handler characterization | Team create rollback and best-effort delete now have public-handler tests; existing agent/retirement/registry tests remain | Preserve these contracts in pure-move commits; make the accepted async behavior change separately |
| Operation acceptance and request replay | `teamintent/writer.go` writes a current desired row and River job; workload/access writers also persist current intent | Atomic public operation records, immutable accepted contents, actor-scoped idempotent replay/conflict handling, authorized operation inspection, and CLI wait/no-wait contracts |
| Drift repair and retry administration | Team observer detects drift; worker replay and retry-policy tests exist | Supported same-revision repair scheduling and terminal retry inspection, without continuously resetting retry budgets; crash-after-write and completed-job repair acceptance |
| Supported worker deployment | Workers and observers are opt-in in `services/runtime-api/main.go` | Setup/update wiring, dedicated queue-only database privileges and Secret references, bounded database egress, and readiness; do not reuse the platform domain database credential for runtime workers |
| Public directory ownership | `platform-api/routes.go` still lacks team/agent public routes; runtime handlers still provision inline | Platform-owned authorized team/membership/agent/namespace APIs, OpenAPI and consumer contracts, scoped UI/CLI changes, and Ingress cutover with the accepted 202 contract |
| Durable publication | `pkg/registrystaging` and publication writer/worker exist | Install-supported shared storage, retention/capacity and mutable-tag decisions, public upload/finalize route cutover, and uploader/API/helper crash-resume acceptance |
| Workload lifecycle | Server/deployment/restart writer and fake-client worker tests exist | Public handoff and status reads, destination-side stale-writer/UID protection and repair evidence, health-only degradation after apply, and real retirement/restart recovery gates |
| Access and enrollment | Grant journaling, receipt fencing, observer and CSR replay unit tests exist | Public authority/handoff cutover and live allow/deny/churn acceptance. The real access worker starts without a `CSRProvider`, so first CSR submission still needs a durable validated-CSR handoff and certificate delivery; a deterministic CR name alone does not complete enrollment |
| Operations and recovery | Request/trace correlation is carried by job args and receipts | Queue age/depth/retry/failure instrumentation, actionable terminal operation state, measured budgets, and restore/epoch-fencing rehearsal |
| Internalization and rename | Public runtime routes and `mcp-runtime-api` module/image/Deployment remain | Remove management callers/routes after the cutovers; preserve bounded live read/stream paths; rename module/image/wiring/RBAC/docs together and rehearse installation/recovery |

## Evidence

- 2026-10-29: documentation-only amendment accepts platform revision IDs and explains the three ownership layers. Updated the design comment and this checkpoint; source audit baseline unchanged. No implementation, unit tests, River version, Kind acceptance or production changes for this migration.
- 2026-11-27: current `main` and open PRs re-audited at `67a88cfa`; owner accepted HTTP 202 operation responses with default CLI waiting and `--no-wait`. `TestTeamCreateRollsBackIdentityAfterNamespaceProvisioningFailure` (including cleanup failure) and `TestTeamDeleteKeepsNamespaceAndReturnsNoContentWhenWatchCleanupFails` passed with `-race`, fake Kubernetes, and isolated kubeconfig. No public behavior or deployment changed in this characterization slice. The later local full-platform build attempt stopped before setup on a base-image content fetch error; it is not new live acceptance evidence.
- Verification: issue description and design comment read back exactly match the reviewed drafts. Stale platform-generation terminology, JSON revision examples, preserved source links, Markdown fences and whitespace checks passed. Kubernetes generation/resourceVersion and condition-level observedGeneration remain distinct from platform revision identity and worker fencing.
- 2026-11-25 (merged PRs on `main`):
  - `#904` (`3eb6184b`): throwaway River team-provisioning spike (prototype + fault-injection evidence); superseded and removed after real product verification.
  - `#982` (`536c856c`): durable workload intents (`MCPServer` + 5 ordered retirement checkpoints, raw `Deployment`/`Service`, durable registry artifact staging `pkg/registrystaging/`, fixed-token `RestartOperationIntent`), River workers (`workload_intent_queue`), and independent observer.
  - `#981` (`e2042cfb`): durable access intents (`MCPAccessGrant` with distinct `RevocationCommitted` / `ProjectionApplied` / `PropagationVerified`, idempotent CSR enrollment with post-issuance authority revalidation, atomic agent deactivation + cleanup), River workers (`access_intent_queue`), and independent gateway propagation observer.
  - `#980` (`bc7d3e41`): durable team namespace intent journal (`team_intent`), River worker (`team_intent_queue`), execution fencing, incarnation isolation, and independent observer.
- 2026-11-25 (PostgreSQL 17 integration + live Kind verification):
  - Environment: Kind cluster `async-team-873` (`kindest/node:v1.36.4`), PostgreSQL 17 in `async-team-spike/postgres-0`, `linux/amd64` images built from `cb762a53` and ELF-verified via `test/e2e/image_architecture.py`.
  - Real PostgreSQL 17 integration suites: all 3 passed — `TestPostgresTeamIntent*` (7/7), `TestAccessIntentPostgresIntegration`, `TestWorkloadIntentPostgresIntegration`.
  - Live deployment: 1-replica `mcp-platform-api` (all 3 `PLATFORM_*_INTENT_JOURNAL=true`) + 2-replica `mcp-runtime-api` (all 3 workers + all 3 observers).
  - Verified live behaviors: team create + Ready observation; scale-to-zero durable River queue + automatic recovery; `MCPServer` apply + 5-step ordered retirement + CR deletion; raw `Deployment` + `Service` apply/Ready/delete; `MCPAccessGrant` apply + strict `NoServingGatewayReplicas` / `propagation_verified=false` reporting + synchronous `revocation_committed=true` + CR cleanup; team soft-delete preserving namespace; 9/9 completed River jobs across all 3 queues (`team_intent_queue`, `access_intent_queue`, `workload_intent_queue`) with 0 stuck/failed jobs and observer leases split across both `mcp-runtime-api` replicas.
  - Still pending: Phase 2 public management route cutover onto `platform-api` (public handlers + Ingress switch), remaining fault/gateway/upload Kind gates, and Phase 6 public route retirement + `runtime-api` -> `runtime` rename.

When a phase finishes, append:

- Date
- Commit SHA
- Test command and result
- Kind context name, if any
- What failed and was left blocked

## Do not redo

- The revision-ID and three-layer ownership design decision is accepted (2026-10-29). Preserve the distinction between platform revision identity, Kubernetes version fields, and worker fencing.
- River `v0.51.0` (`riverdatabasesql`) transactional enqueue on `*sql.Tx` with `lib/pq` and PostgreSQL 17 is proven in product code; do not re-create the removed throwaway spike (`#904`).
- Durable intent journals, fenced internal APIs, River workers, and independent observers for teams (`#980`), access (`#981`), and workloads/staging/restarts (`#982`) are already merged on `main` behind disabled-by-default opt-in flags and verified live against PostgreSQL 17 and Kind `async-team-873`. Continue from Phase 2 public management route cutover onto `platform-api`, followed by Phase 6 route retirement and rename.

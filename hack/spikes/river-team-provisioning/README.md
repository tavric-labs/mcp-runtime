# River spike: team provisioning

Throwaway experiment. Question: does [River](https://riverqueue.org) (Postgres-backed Go job queue,
v0.51.0) fit MCP Runtime's async provisioning, and what protections do our workers still need?
Scope is team provisioning only. Not wired into any product module and not part of CI.

**Flow:** `create-team` inserts the team row, a status row and a River job in one Postgres
transaction → `worker` re-reads the team's *current* state → ensures namespace `team-<slug>`
(labelled `spike/team-id`) → records the result in `team_provisioning`.
Job args carry only the team ID, never desired state.

`-naive` disables the two protections found necessary below (post-check, deletion convergence).

## Run it (disposable resources only)

```bash
docker run -d --name river-spike-pg -e POSTGRES_PASSWORD=spike -e POSTGRES_DB=spike -p 55432:5432 postgres:17-alpine
kind create cluster --name river-spike --kubeconfig /tmp/river-spike.kubeconfig
go build -o /tmp/spike .
export KUBECONFIG=/tmp/river-spike.kubeconfig   # worker refuses any context not named kind-*
/tmp/spike migrate; /tmp/spike worker -name w1 &
/tmp/spike create-team alpha; /tmp/spike status
```

Fault injection: `SPIKE_CRASH_AFTER_NS=1` (exit 137 after namespace creation),
`SPIKE_DELAY_BEFORE_K8S=4s` (widen the read→act window). Outage: `docker pause river-spike-control-plane`.

## Evidence (run 2026-11-02, Kind v1.34, Postgres 17, River 0.51.0)

| # | Experiment | Result |
|---|---|---|
| 1 | Create team with no worker, then start one | Team + job committed (`available`, 0 attempts), no namespace. Worker start → `completed`, namespace Active, status `ready`. **Resumes.** |
| 2 | `exit 137` right after namespace creation | Job stays `running` (owner dead). Only after `RescueStuckJobsAfter` (set to 15s; **River default 1h**) does a live worker retry it: `attempt=2`, `namespace_created_by_this_attempt=false`, `ready`. **Safe, because `ensureNamespace` is idempotent** (label check). |
| 3 | 2 workers × concurrency 4, 30 teams | 30/30 `ready`, all `attempts=1`, 15/15 split across workers, 30 namespaces, 0 duplicate executions. **Distinct jobs never double-run** (`SKIP LOCKED`). |
| 4a | Delete team during provisioning, **naive** worker | Worker had read the team before the delete, then created the namespace. Team `deleted=true`, status `ready`, **namespace `team-epsilon` orphaned and Active.** Stale work does provision after deletion. |
| 4b | Same, **safe** worker | Delete enqueues a reconcile job; worker re-reads desired state *after* acting and rolls back what it created (only if label matches). Team deleted, status `deleted`, namespace gone. |
| 5a | API server unreachable ~14s | Job `available`, `errs=2`, retried on backoff; recovered on its own to `ready` (3 attempts). |
| 5b | API server unreachable ~100s | After 8/8 attempts job is `discarded`; team status `failed` with last error; **no automatic recovery** after the API returns. `spike retry <job-id>` (River `JobRetry`) fixed it: `ready`, `9/9`. |

## Findings

River gives us what we wanted: transactional enqueue (no lost provisioning intent), durable
resume, safe multi-worker claiming, retry with per-attempt error history in `river_job.errors`.
It does **not** solve correctness against the outside world. Protections our workers still need:

1. **Idempotent, ownership-checked effects.** Retries after a crash are only safe because the
   worker treats "exists with my team-id label" as success and refuses to adopt foreign objects.
2. **Reconcile, don't replay.** Jobs carry an ID; the worker reads current desired state, and
   re-checks it after acting. Without the post-check, deletion races leak infrastructure (4a).
3. **Deletion must enqueue convergence in the same transaction as the delete.** Do **not** use
   River `UniqueOpts` for it with `running` in the unique states: a delete job deduped against an
   in-flight create job that is already past its post-check would be lost. Let reconcile jobs be
   duplicated; they are idempotent. Concurrent jobs for the *same* team are allowed by OSS River
   (no per-key concurrency limit; that is River Pro). If strict serialization is wanted, hold a
   `SELECT … FOR UPDATE` on the team row or a Postgres advisory lock, at the cost of a transaction
   open across Kubernetes calls. Not evaluated here.
4. **Tune stuck-job rescue.** Default 1h means a crashed worker leaves provisioning hanging for an
   hour. Must be > max job duration (`JobTimeout`; River validates this at startup).
5. **Failure visibility is ours to build.** `discarded` jobs are silent. Needs product-side
   status (we mirror to `team_provisioning`), an alert on `river_job` discarded/retryable counts,
   and an operator retry path. Never leave a team forever `pending`/`failed` without a surfaced reason.
6. **Namespace deletion is asynchronous** (`Terminating`); recreating the same slug immediately
   will error and retry. Not exercised here.

## Not covered / caveats

- Single Postgres, no failover or connection-loss tests. Worker graceful shutdown (SIGTERM) only
  lightly observed. Same-team concurrent jobs and delete→recreate same slug untested.
- Prototype uses a simplified schema, not `services/platform-api/migrations`, and calls
  `client-go` directly rather than `ensureTeamNamespace` (no PSS labels, NetworkPolicy, Traefik watch).
- Adoption would add a module dependency and `river_*` tables to the platform-api DB, plus River's
  own migrations in our upgrade path (`rivermigrate`); `lib/pq` in platform-api would need to move to pgx.
- Per `hack/README.md` rules this directory is not in the script catalogue; delete it or catalogue it
  before merging anything.

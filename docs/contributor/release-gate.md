# Release gate

For maintainers preparing a release. Start with a merged candidate, green CI,
and the [staging prerequisites](staging-e2e.md). Production deployment is a
separate operation; these workflows use GitHub runners and the disposable VM.

## Completion criteria

The improvement loop is: identify a concrete coverage or enforcement gap, add
a regression that exposes it, make the smallest harness change, run the focused
checks, and inspect CI evidence. Repeat for failing or missing requirements.
Stop when all required cells below pass on the publication commit, the offline
automation and workflow checks pass, and no release blocker remains. Do not add
unrelated product changes or treat a skipped check as evidence.

| Required surface | Combinations and evidence |
| --- | --- |
| Full Kind QA | `org` and `public` × single and multiple replicas; `all` scenarios with deep request flows and no reduced MCP-server budget. Includes install, grants/sessions, certificate and OAuth paths, allow/deny traffic, observability, CLI/UI and supported update/idempotency checks. Each cell has its own cluster and artifact. |
| Strict staging | `org` and `public` × bundled MCP Auth explicitly `disabled` and `enabled`; four runs serialized on the guarded disposable VM. Reuse TLS recovery material; run multitenancy and real certificate enrollment. Explicit settings override the preserved VM profile for that run only. |
| Protocol and HTTP responses | `2025-06-18`, `2025-11-25`, `2026-07-28` × JSON and SSE responses. Adapter certificate/header proxy modes preserve headers, body, status and session handles. Gateway certificate, OAuth, and combined authentication exercise granted, ungranted, revoked and expired platform sessions. These use real loopback upstreams; live suites separately verify PKI/ingress. |
| Distributed CLI | Linux amd64/arm64, macOS amd64/arm64, Windows amd64 builds. This is compilation coverage; it does not claim native execution of every binary. |
| Desktop preview | Universal macOS companion packaging and existing Swift form/client tests. Archives remain unsigned previews; this is not notarization or an interactive desktop lifecycle test. |
| Other checks | Static/generated checks, root/CLI/golden/integration tests, every service module with disposable Postgres, Traefik plugins, frontend unit/accessibility/browser checks, native registry authorization, Cilium scope isolation, benchmarks, SBOM and security scans. |

The protocol matrix checks Runtime's forwarding and authorization contract,
not full upstream MCP conformance. Upstream servers own negotiation and session
state. The adapter exposes Streamable HTTP; arbitrary stdio process launching
and legacy SSE session brokerage are not supported release combinations.
Specialized optional scenarios retain their explicit prerequisites: for example,
the credential-broker live scenario needs an HTTPS origin trusted by gateway
roots and reports `SKIPPED, NOT TESTED` without one. Its service-level tests
still run; a green gate must not be described as live credential-broker evidence.

## Run and publish

Run Pre-release Regression on the exact merged candidate. A branch run is useful
while iterating, but a merge commit needs its own evidence:

```bash
gh workflow run pre-release-regression.yaml --ref main
# Inspect every job and its artifacts, including all four Kind and staging cells.
python3 hack/release/verify_gate.py --repository tavric-labs/mcp-runtime --commit <full-sha>
gh workflow run release.yaml --ref main -f tag=v0.15.0 -f ref=<full-sha>
```

The manual Release workflow resolves and pins the source, requires it to be
merged to `main`, checks its dated changelog entry, and verifies the latest
Pre-release Regression for that exact commit. Every required named job must be
present exactly once and completed successfully. Older successes, incomplete
reruns, fork runs, missing cells, skipped cells and failures block publication.
The final regression job also requires every dependency to succeed.

Only after this gate passes does Release build the assets from that immutable
commit. It validates the component manifest and CRD bundle, checksums the
assets, rechecks regression evidence, and creates the tag and release together
with the assets. It does not create a tag while tests are still running.
If someone publishes a GitHub release externally, the release-event path checks
the same gate before building/uploading assets; it cannot prevent a maintainer
from manually creating an already-public release in GitHub.

Authenticate `gh` with repository Actions read access for local verification.
`hack/release/verify_gate.py` is read-only and fails closed on missing API
evidence. Its fake-API regressions run through `hack/ci/check-scripts.sh`.
Validate workflow edits with
[`actionlint` v1.7.11](https://github.com/rhysd/actionlint/releases/tag/v1.7.11)
(source/version reviewed 2026-10-30) and the scenario selector checks.

Use the changelog's release-specific deployment note after publication. In
0.15.0, platform sessions stay in Postgres and the obsolete session CRD cleanup
requires separate verification and a deliberate administrative action.

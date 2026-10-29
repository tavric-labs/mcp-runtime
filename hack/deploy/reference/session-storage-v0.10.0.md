# Live preview history: v0.10.0 session storage

This is a historical deployment note for our live preview, not the current
installation path or a compatibility commitment. Current CRD cleanup is covered
by [session CRD removal](session-crd-removal.md).

The source/install revision is `v0.10.0`. Use its CLI and manifests together;
policy schema v4 does not support mixed older gateways. The new platform sync
listener, Pod-bound token mounts, RBAC and network policy require fresh setup
for this breaking release, followed by supported CLI server deployment.

Before an authorized deployment, record the old and new commit IDs, cluster UID,
and a protected copy of the existing deployment profile, including platform
mode, domains, ingress, storage, registry, OIDC and whether `mcp-auth` is installed.
Use the same reviewed configuration for setup from `v0.10.0`; retain the
separately versioned `mcp-auth` choice. Do not substitute example credentials.

1. Complete the [full backup](../../../docs/reference-deployment.md#production-backups) plus database-native exports for Postgres,
   ClickHouse and any other persisted application stores. Record the immutable
   snapshot/export locations and checksums in the private deployment record;
   copy them to protected off-host storage. Preserve registry image contents,
   TLS/CA keys, pull credentials and required platform Secrets. Separately
   snapshot Keycloak data and Caddy configuration/certificate state.
2. Rehearse fresh setup and recovery on disposable infrastructure with the
   recorded configuration. Restore required users, teams, agents, grants and
   application data. Kubernetes session objects are recovery evidence only:
   this release intentionally does not import them into the new session model.
   If restoring a v0.10.0 Postgres database, retain session identity reservations,
   revocation records and retirement fences alongside session rows.
3. After recovery is proven and the reset is explicitly authorized, use the
   [guarded reset procedure](README.md), then the installation commands in the reference deployment guide
   from `v0.10.0` with the recorded profile. Restore required database/registry
   contents and selected configuration/certificates before accepting traffic.
   The configuration restore helper does not restore database contents.
4. Restart platform-api after every Postgres restore, before gateway reconnection,
   to rotate the sync epoch. Redeploy servers through the CLI to install matching
   gateway images, tokens and egress rules. Re-enroll adapters to issue new
   session names and certificates; do not reuse deleted session names.
5. Verify login, registry publish and fresh pulls, server deployment, enrollment,
   grant/session creation, allowed and denied calls, revocation and cross-team
   isolation. Check gateway sync freshness and correlated audit/log/trace data.

If recovery or acceptance fails, keep traffic closed and recover the prior
recorded release with its matched databases, configuration and certificates from
the rehearsed backup. Do not reconnect old gateways to schema v4 or copy old
Kubernetes sessions into the new store. This release procedure does not itself
authorize a reset or deployment of the hosted reference platform.

# Live preview: remove the session CRD

This deployment note is only for our hosted preview at
`platform.mcpruntime.org`. The product stores sessions in platform-api's
Postgres database and synchronizes them to each gateway. Only servers and
access grants are Kubernetes CRDs. There is no session import, dual-write or
Kubernetes fallback.

## Prepare

Record the exact target tag and commit, the current deployed revision, the
private installation profile, explicit kubeconfig/context and expected cluster
UID. Use the [reference operations](README.md) backup procedure before changes.
Keep a recoverable Postgres backup, including session records, deleted session
identity reservations, retirement fences and sync state. Keep registry data,
required Secrets/certificates and identity-provider recovery material with the
deployment's existing backup plan; do not commit any of it.

If the old `mcpagentsessions.mcpruntime.org` CRD is still installed, save its
definition and objects in the private snapshot for historical evidence. They
are not the authorization store and must not be restored as active sessions.

Rehearse setup and the CLI acceptance path on disposable infrastructure before
changing the preview. No production operation is authorized by this document.

## Deploy and remove the obsolete definition

Use `mcp-runtime update` from the recorded revision with the preview's explicit
kubeconfig, expected cluster UID and reviewed installation configuration. This
delivers matching operator/runtime images, the two supported CRDs, webhook
configuration and RBAC. Update applies current definitions; it does not prune
an obsolete session CRD.

Verify platform session list/get and a real adapter's allowed and denied calls
before removing that inactive definition. Then, as a deliberate administrative
cleanup using the reviewed preview profile:

```bash
kubectl --kubeconfig "$MCP_SETUP_KUBECONFIG" --context "$MCP_KUBE_CONTEXT" \
  delete customresourcedefinition mcpagentsessions.mcpruntime.org
```

Deleting a CRD also deletes its Kubernetes objects. This command does not
delete platform sessions in Postgres. Do not copy old CR objects into the
database or add a compatibility backend. Normal session management uses the
platform CLI/UI.

## Verify and recover

- Verify the session CRD, its webhook entry and operator/runtime session RBAC
  are absent. Keep `MCPServer` and `MCPAccessGrant` registered.
- Verify login, grants, adapter enrollment, session inspection, allowed and
  denied calls, revocation after gateway sync, and correlated audit events.
- Verify gateway session-sync freshness and deny behavior for cold/stale caches.

If acceptance fails, stop and diagnose it before further cleanup. Recover with
the recorded revision, installation configuration and matching database/PKI
snapshot using the existing reference recovery procedure. Restart platform-api
after restoring Postgres, before gateways reconnect, so its sync epoch rotates.

The shared session protocol and failure rules are documented in
[gateway session sync](../../../docs/contributor/gateway-session-sync.md).

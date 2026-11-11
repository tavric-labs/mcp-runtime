# Live preview: retire the `mcp-servers` base namespace

This deployment note is only for our hosted preview at
`platform.mcpruntime.org`. The product no longer has a base MCP server
namespace. Servers live in `mcp-servers-org` (org scope) and `mcp-team-*`
namespaces. The old `mcp-servers` namespace on the preview holds only leftovers:

| Object | Replacement |
|---|---|
| Secret `mcp-adapter-client-ca` (operator trust bundle) | The same Secret in `mcp-platform`, selected with `MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE=mcp-platform`. |
| Secret `mcp-runtime-registry-pull` | Nothing to move. Runtime API provisions a per-namespace copy in `mcp-servers-org` and every team namespace on deploy. |
| ServiceAccounts `default`, `mcp-workload` | Nothing to move. Runtime API provisions `mcp-workload` per server namespace. |
| RoleBindings `mcp-runtime-operator-managed-secrets`, `-managed-workloads` | Setup binds them in every managed namespace (catalogs and teams). |
| RoleBinding `mcp-runtime-api-team-secrets` | Runtime API binds it per managed namespace on provisioning. |
| RoleBinding and Role `mcp-runtime-operator-trust-bundle` | Setup creates them next to the trust bundle in `mcp-platform`. |
| `traefik-watch` Role and RoleBinding, the entry in Traefik's watched-namespace flags | Removed from the shipped Traefik manifests. |

No production operation is authorized by this document. Use an explicit
`KUBECONFIG` per command, never the ambient context, and follow the
`production-platform` skill (confirm the ref, snapshot, preserve certificates).

## Why the trust bundle moves to `mcp-platform`

The bundle must sit in the namespace of Traefik's global `default` TLSOption and
`TLSStore`, and Traefik must watch it. Only the operator writes it (named
get/update/patch Role); Traefik reads it; runtime-api and the doctor read the
TLSOption through cluster-wide `get` and never read the Secret. Nothing copies
it into workload namespaces. `mcp-platform` is already watched by Traefik with
Secret read, hosts the other platform TLS material, is never a tenant namespace
and needs no new cross-namespace grant. `mcp-runtime` is refused by setup
(operator and webhook key material) and `traefik` would need new watch flags and
RBAC.

## Prepare

Record the target tag and commit, the current deployed revision, the private
installation profile, explicit kubeconfig and expected cluster UID. Take the
backup described in the [reference operations](README.md). In addition save,
privately, read-only listings of the objects that will be removed, with Secret
data left out:

```bash
export KUBECONFIG=<prod kubeconfig file>      # per command, never the default
kubectl get secret,sa,role,rolebinding,networkpolicy -n mcp-servers -o yaml \
  | grep -v '^\s*\(ca.crt\|tls.ca\|.dockerconfigjson\):' > mcp-servers-before.yaml
kubectl get tlsoption,tlsstore -A
kubectl get mcpservers,mcpaccessgrants -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,REF:.spec.serverRef.namespace
```

Stop if anything in `mcp-servers` other than the objects in the table exists, or
if an MCPServer, grant or session still references namespace `mcp-servers`.

## Order: create the new first, verify, then delete the old

1. Deliver the release with `mcp-runtime update`. It changes images, CRDs and
   declared wiring only. It does not change operator environment or create the
   trust bundle, and it no longer declares the two `mcp-servers` RoleBindings;
   it does not delete them either. Nothing changes for the trust bundle yet.
2. Create the new trust bundle location. Run `mcp-runtime setup` from the same
   revision with the preview's reviewed profile and
   `MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE=mcp-platform`. Setup pre-creates the
   empty `mcp-adapter-client-ca` Secret, the named Role and RoleBinding in
   `mcp-platform`, and sets the operator environment, which rolls the operator.
   Setup does not touch `mcp-servers` any more. If a targeted rollout is
   preferred, the same three objects plus the operator environment variable are
   the only change; record that limitation.
3. Verify before deleting anything:
   - `kubectl -n mcp-platform get secret mcp-adapter-client-ca` has `ca.crt`
     and `tls.ca`, and its certificate fingerprints equal the old Secret's
     (the operator copies the workload CA on reconcile, which the operator
     restart triggers for every adapter-certificate server).
   - `kubectl -n mcp-platform get tlsoption default -o yaml` is managed by
     `mcp-runtime`, requests `VerifyClientCertIfGiven` and lists
     `mcp-adapter-client-ca`. While the old `default` TLSOption still exists
     there are two with that name; both trust the same CA. Delete the old one
     (`kubectl -n mcp-servers delete tlsoption default`, and a `default` TLSStore
     only if it was there) as soon as the new one is confirmed.
   - Operator logs show no `TLSOption ... is not managed by mcp-runtime` error.
   - `mcp-runtime cluster doctor` passes the workload identity check (it reads
     the operator environment) and the new server-namespace checks.
   - Run the hosted acceptance path: an adapter session with a client
     certificate gets an allowed call and a denied call.
4. Remove `mcp-servers` from Traefik's watched namespaces **before** deleting
   the namespace. Traefik v2.10 stalls the whole CRD provider on a forbidden
   list/watch, which makes every IngressRoute return 404. Apply the new Traefik
   manifests or edit `--providers.kubernetesingress.namespaces` and
   `--providers.kubernetescrd.namespaces`, wait for the rollout, and confirm the
   platform, registry and an MCP route still answer.
5. Delete the old objects. Check first that nothing else is in the namespace:

   ```bash
   kubectl get all,ingress,ingressroute,mcpservers,mcpaccessgrants -n mcp-servers
   kubectl delete namespace mcp-servers
   ```

   The namespace delete removes the two Secrets, both ServiceAccounts, the four
   RoleBindings, the trust-bundle Role and the `traefik-watch` Role.

## What breaks if the old namespace is left behind

Nothing reads it. The operator reads the trust bundle from the configured
namespace, runtime-api and the doctor inspect the TLSOption the route selects,
setup no longer seeds bindings there and the doctor no longer probes it.
Leftovers are inert but not harmless: the old operator RoleBindings keep Secret
and workload write access in an empty namespace, an old `default` TLSOption
competes with the new one, and a Traefik watch entry whose `traefik-watch` Role
is later removed stalls the CRD provider (step 4). Remove them.

## Rollback

Until step 5, point the operator back with
`MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE=mcp-servers` (setup) and keep the old
Secret and TLSOption. The operator re-copies the public CA either way; nothing
here holds private keys. After step 5 recreate the namespace, rerun setup with
the old value and let the operator repopulate the bundle.

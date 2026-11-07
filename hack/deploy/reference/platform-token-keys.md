# Live preview: platform token signing keys

This note is only for our hosted preview at `platform.mcpruntime.org`. The
product has one mechanism: platform-api signs platform tokens with an ES256
private key (`JWT_SIGNING_KEY`) that only it receives, and runtime-api and
analytics-api verify them with public keys (`JWT_VERIFY_KEY`). The shared
`JWT_SECRET` no longer exists, so a compromised backend cannot mint tokens. No
production operation is authorized by this document.

## Effect of the cutover

- Every platform login token signed with the old shared secret stops verifying.
  Users sign in again. API keys, OIDC identities and stored sessions are
  unaffected, and exchanged runtime or analytics tokens already live for at most
  60 seconds.
- There is no dual-verification period and no HS256 fallback.
- The private key is a new required secret. Back it up with the platform
  credentials Secret, keep it out of git and logs, and never print it.

## Prepare

Record the exact target tag and commit, the deployed revision, the private
installation profile, the explicit kubeconfig/context and the expected cluster
UID. Take the [reference operations](README.md) backup before changing anything
and rehearse the update on disposable infrastructure first.

## Deploy

Run `mcp-runtime update` from the recorded revision with the preview's explicit
kubeconfig, expected cluster UID and reviewed installation configuration. When
the release rolls platform-api, runtime-api or analytics-api, update first keeps
an existing key pair, generates one when none exists, and adds the signing key's
public key to the verification set, writing `JWT_SIGNING_KEY` only to
platform-api's Secret and `JWT_VERIFY_KEY` to all three Secrets before any
workload rolls. runtime-api and analytics-api roll before platform-api. `--dry-run`
reports this without writing. The legacy `JWT_SECRET` key is left in place. Update
releases before the one that added this step (v0.16.0) never provisioned the
pair: on a platform that only holds `JWT_SECRET`, platform-api crash-loops until
the pair exists, and rerunning update from the fixed release (or `mcp-runtime
setup`) provisions it. Setup uses the same rules.

Verify without printing values:

- `mcp-platform-api-credentials` lists `JWT_SIGNING_KEY` and `JWT_VERIFY_KEY`.
- `mcp-runtime-api-credentials` and `mcp-analytics-api-credentials` list
  `JWT_VERIFY_KEY` and do not list `JWT_SIGNING_KEY`.
- None of the three still lists `JWT_SECRET`. If one does, remove that key
  deliberately after the services are healthy.
- A fresh login token's header names `ES256` and a `kid`. Decode only the first
  segment.
- Login, the dashboard, a runtime route, an analytics route, and an allowed and
  a denied tool call through a real adapter session all work.

## Rotate the signing key

1. Generate a new pair and add the new public key to `JWT_VERIFY_KEY` in the
   owner Secret, keeping the old one.
2. Roll runtime-api and analytics-api so every verifier trusts both keys.
3. Replace `JWT_SIGNING_KEY` with the new private key and roll platform-api.
4. Wait longer than the longest token lifetime signed by the old key.
5. Remove the old public key from `JWT_VERIFY_KEY` and roll the verifiers again.

Setup never removes a verification key on its own and never overwrites a signing
key it cannot parse.

## Recover

Restore the platform credentials Secret from the private backup together with
the matching database snapshot and recorded revision, then follow the existing
reference recovery procedure. If the private key is lost, generate a new pair
and update the Secrets; users sign in again and no stored data is affected.

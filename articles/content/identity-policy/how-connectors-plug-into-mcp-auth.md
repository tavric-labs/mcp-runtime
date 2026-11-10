---
title: "How connectors plug into MCP Auth"
description: "What a connector is, how it relates to login methods, resource clients and token exchange in MCP Auth, and exactly which objects MCP Runtime writes when you configure one."
category: "Identity & Policy"
published: "2026-11-10"
reading_time: "12 min"
---

Sooner or later an MCP server needs to know **who** is calling and, often, to call another service **as that person**. MCP Auth is the OAuth server that answers the first question. A **connector** is the piece that lets it answer with *your* identity provider instead of its own user database. This article explains what a connector is, how it fits with the other moving parts, and which Kubernetes objects MCP Runtime writes when you configure one.

It is written for engineers who deploy MCP servers on MCP Runtime. Everything here comes from the [MCP Auth](https://github.com/tavric-labs/mcp-auth) and [MCP Runtime](https://github.com/tavric-labs/mcp-runtime) source and docs at the versions named in each section.

## What MCP Auth is

MCP clients (an editor, a coding agent, a command-line tool) need a standard way to get permission to call a protected MCP server. The MCP authorization profile builds on OAuth 2.1: the client discovers an **authorization server**, sends the person through a sign-in, and receives an **access token**.

MCP Auth is that authorization server. Its core job is small and strict:

- It issues short-lived access tokens, and each token is **bound to one resource**, the canonical URL of one MCP server. That binding is the token's audience. A token minted for server A is not accepted by server B.
- It publishes the standard metadata and signing keys that clients and servers use to discover it and verify its tokens.
- It does **not** keep a user directory and does not decide who may do what. Who a person is comes from somewhere else, and what they may do is decided by the resource server and, on MCP Runtime, by the gateway's grants and policy.

That "somewhere else" is the problem connectors solve.

## What a connector is

A **connector** is a named description of one upstream OAuth 2.0 or OIDC provider: its endpoints, the client id MCP Auth uses with it, the *name* of the environment variable that holds the client secret, the scopes to request, how to authenticate to the token endpoint, whether to use PKCE, and which claim identifies a person. It carries no behaviour of its own. Think of it as a driver for one provider.

[![MCP Auth sits between the MCP client and the identity provider. Login methods choose how people sign in, connectors describe the providers, and the MCP server behind the gateway exchanges its access token for the provider's token.](/static/articles/identity-policy/how-connectors-plug-into-mcp-auth/01-architecture.png)](/static/articles/identity-policy/how-connectors-plug-into-mcp-auth/01-architecture.svg)

The same connector schema covers three kinds of upstream. They are told apart by their settings, not by a `type` field:

| Profile | How to recognise it | Typical upstream |
|---|---|---|
| OIDC | `scopes` includes `openid` (or is empty). The provider returns an ID token and MCP Auth verifies it: signature, issuer, audience, expiry, nonce. | A corporate IdP such as Keycloak |
| Plain OAuth 2.0 | `scopes` is set and omits `openid`. There is no ID token, so identity comes from the `userinfo_endpoint` and the claims named in `identity_claims`. | A provider that only speaks OAuth 2.0 |
| Gateway sign-in endpoints | A plain OAuth 2.0 connector pointing at a service's own `/login/authorize`, `/login/token` and `/login/userinfo`. | The MCP Runtime credential gateway's header form |

There are no built-in presets for named providers in the connectors file. `google` and `github` exist as separate login method types with fixed endpoints, and everything else is a generic connector.

## The other moving parts

A connector alone signs nobody in. Five other objects decide how it is used.

| Term | What it is | Where MCP Auth reads it |
|---|---|---|
| **Connector** | Description of one upstream provider. | `MCP_AUTH_CONNECTORS_FILE`, a JSON object keyed by connector name |
| **Login method** | One way to prove who you are: a redirect to a provider (`oidc`, `google`, `github`) or a form on MCP Auth's own page (`ldap`). An `oidc` method names a connector. | `MCP_AUTH_LOGIN_METHODS_FILE`, or `MCP_AUTH_CONNECTOR` for the single-connector mode |
| **Resource login** | A login method scoped to particular MCP resources with `resources`. A resource with a scoped method must sign in through it and never falls back to another method. | The same methods list, or `MCP_AUTH_RESOURCE_LOGINS_FILE` next to a single connector |
| **Resource client** | An MCP server pre-registered to call MCP Auth's token endpoint with `private_key_jwt`. It is not a way to sign in. | `MCP_AUTH_RESOURCE_CLIENTS_FILE` |
| **External credential connector** | Lets a resource client get a short-lived credential-access assertion instead of an upstream token. Used by the credential gateway. | `MCP_AUTH_EXTERNAL_CREDENTIALS_FILE` |

And one more idea, the **downstream token strategy**, which says what happens to the provider's token after sign-in:

- `upstream_session` (the default for a connector): MCP Auth keeps the provider's token set, hands it out through RFC 8693 token exchange and refreshes it with the refresh token. It works with any provider.
- `rfc8693`: MCP Auth asks the provider itself to exchange tokens. Only for providers that implement it.
- No strategy (the default for login methods): the provider's token is discarded and only identity is kept.
- A method-level `downstream_token: upstream_session` does the first of these for one `oidc` method scoped to specific resources. This is what MCP Runtime's per-resource command uses.

MCP Auth runs in exactly one sign-in mode per process: a single connector (`MCP_AUTH_CONNECTOR`), a list of login methods (`MCP_AUTH_LOGIN_METHODS_FILE`, which cannot be combined with the single connector), or a single connector plus resource logins (`MCP_AUTH_CONNECTOR` and `MCP_AUTH_RESOURCE_LOGINS_FILE`). MCP Runtime's bundled install uses the single connector, and adds resource logins for the servers that need their own sign-in.

## The sign-in, step by step

[![A sequence of eleven steps: authorize with a resource, consent, redirect to the connector's provider, identity callback, code exchange, mcp-auth code, access token, the MCP request, the RFC 8693 exchange with private_key_jwt, the provider token, and the provider API call.](/static/articles/identity-policy/how-connectors-plug-into-mcp-auth/02-sign-in-sequence.png)](/static/articles/identity-policy/how-connectors-plug-into-mcp-auth/02-sign-in-sequence.svg)

1. **Authorize.** The client sends the person to MCP Auth's `/authorize` with the `resource` it wants a token for and a PKCE challenge. MCP Auth only issues tokens for resources it was told about (`MCP_AUTH_RESOURCES`).
2. **Consent.** MCP Auth shows a consent page naming the resource and scopes. The person approves.
3. **Redirect to the provider.** MCP Auth picks the login method for that resource. A method scoped to the resource wins; otherwise the unscoped methods are used. One redirecting method skips the picker, anything else shows a login page. The browser is sent to the connector's `authorization_endpoint`.
4. **Identity callback.** After sign-in, the provider returns the browser to `<issuer>/identity/callback` with a code. That exact URL must be registered as a redirect URI at the provider.
5. **Code exchange.** MCP Auth exchanges the code at the connector's token endpoint, authenticating as the connector says (`client_secret_basic`, `client_secret_post` or `none`). For OIDC it verifies the ID token. For plain OAuth 2.0 it calls userinfo and reads `identity_claims`. The subject gets the login method's prefix, such as `res-example-api:`.
6. **MCP Auth code.** MCP Auth redirects back to the client with its own authorization code. The provider's code and tokens never reach the client.
7. **Access token.** The client posts the code and PKCE verifier to `/token` and receives an access token whose audience is the resource, plus a rotating refresh token.
8. **The MCP call.** The client sends the token to the MCP server. On MCP Runtime, the gateway checks issuer, audience and scopes before anything else, then applies grants and policy.
9. **Token exchange.** When the server needs to call the provider as the person, it posts to MCP Auth's `/token` with the RFC 8693 grant. The access token is the subject token, and the server proves *which server it is* with a signed `private_key_jwt` assertion. It does not send a client secret. The server's key says which server is asking. It does not identify the person: the validated access token stays the subject credential.
10. **Provider token.** With the `upstream_session` strategy, MCP Auth answers with the provider token set it stored at step 5, refreshed if it can be.
11. **The provider API call.** The server calls the provider's API as the person.

Steps 1 to 7 happen once per sign-in. Steps 8 to 11 happen each time the server needs the provider.

## How MCP Runtime attaches this

MCP Auth reads its configuration from files and environment variables when it **starts**. MCP Runtime's job is to put the right files and variables into the `mcp-auth-server` Deployment in the `mcp-platform` namespace and to restart it when they change. The objects:

| Object | Holds | Mounted or read as |
|---|---|---|
| ConfigMap `mcp-auth-connectors` | `connectors.json`, every connector | Volume at `/etc/mcp-auth`, named by `MCP_AUTH_CONNECTORS_FILE` |
| Secret `mcp-auth-connector-secrets` | The values of the variables named by `client_secret_env` (and `client_id_env`) | `envFrom` on the container |
| ConfigMap `mcp-auth-gateway-wiring` | `resource-logins.json`, `resource-clients.json`, and for the credential gateway `external-credentials.json` plus one `<id>.public.pem` per service | Volume at `/etc/mcp-auth-gateway-wiring`, named by `MCP_AUTH_RESOURCE_LOGINS_FILE`, `MCP_AUTH_RESOURCE_CLIENTS_FILE`, `MCP_AUTH_EXTERNAL_CREDENTIALS_FILE` |
| Deployment `mcp-auth-server` | The environment, volumes and pod annotations that tie the above together | Rolled out when a wiring hash annotation changes |

Four commands write these. Verify the flags against `--help` of your CLI release.

**`mcp-runtime setup --with-mcp-auth-server`** installs MCP Auth with the main identity connector. You give it a connectors file and the connector's name:

```bash
KEYCLOAK_CLIENT_SECRET="$(tr -d '\n' < /secure/keycloak-client-secret)" \
mcp-runtime setup --with-tls --tls-cluster-issuer letsencrypt-prod \
  --with-mcp-auth-server \
  --mcp-auth-signing-key-secret mcp-auth-signing-key \
  --mcp-auth-connectors-file /secure/mcp-auth-connectors.json \
  --mcp-auth-connector keycloak
```

Setup reads the environment variables the selected connector names, fails before applying anything if one is unset, writes the file into `mcp-auth-connectors` and the values into `mcp-auth-connector-secrets`, and sets `MCP_AUTH_CONNECTORS_FILE` and `MCP_AUTH_CONNECTOR` on the Deployment. It requires `exchange_client_id` in the connector because MCP Auth does.

**`mcp-runtime cluster mcp-auth connector apply`** replaces the main connector's file on an existing install without touching the Secret. It requires `--file`, `--connector`, `--kubeconfig` and `--context`, and `--yes` to write or `--dry-run` to preview. It refuses a literal `client_secret`, the removed `mcp_scopes` field and an empty `client_secret_env`, and it keeps connectors whose names start with `gateway-login-` or `resource-login-`, which other commands own. It updates the ConfigMap only. MCP Auth reads the file at startup, so roll the Deployment afterwards (`kubectl -n mcp-platform rollout restart deployment/mcp-auth-server`, with an explicit `--context`) for it to take effect.

**`mcp-runtime cluster mcp-auth resource apply`** wires one OAuth MCP server that needs its own sign-in and its own provider token. It takes a resource file (YAML or JSON, `--file`) plus `--kubeconfig`, `--context` and `--yes` or `--dry-run`. It adds three entries it owns: the login method `res-<id>`, the resource client named `resource:<id>`, and the connector `resource-login-<id>`. It writes the first two into `mcp-auth-gateway-wiring`, merges the connector into `mcp-auth-connectors`, points `MCP_AUTH_RESOURCE_LOGINS_FILE` and `MCP_AUTH_RESOURCE_CLIENTS_FILE` at those files, and stamps the pod template with a hash annotation so MCP Auth restarts only when something changed. Before it writes, it checks that MCP Auth is in single-connector mode, already serves the `resource_url`, and that the connector's secret key exists in the Secret by name (it never reads a value).

**`mcp-runtime gateway apply`** is the credential gateway's form login. It installs the gateway and wires MCP Auth to it: a `gateway-login-<id>` connector pointing at the gateway's sign-in endpoints, a `form-<id>` resource login, the assertion client's public key and the allowed resources. It uses the external credentials file and a hash annotation the same way. Values people type into the form stay in the gateway's memory and never reach MCP Auth.

## A worked example

Suppose `example-api` is an MCP server that calls a provider's API as the person. The provider speaks plain OAuth 2.0, with no ID token. The server author keeps a resource file next to the server's manifest. By convention we call it `.mcp/auth-resource.yaml`; the command takes any path.

```yaml
id: example-api
resource_url: https://mcp.example.com/example-api/mcp
required_scopes: [tools:read, tools:write]
resource_client:
  client_id: example-api-mcp
  public_key_pem: |
    -----BEGIN PUBLIC KEY-----
    ...the public half of the server's key...
    -----END PUBLIC KEY-----
login:
  label: Sign in with Example IdP
  downstream_token: upstream_session
connector:
  issuer: https://idp.example.com
  authorization_endpoint: https://idp.example.com/oauth/authorize
  token_endpoint: https://idp.example.com/oauth/token
  userinfo_endpoint: https://idp.example.com/api/userinfo
  client_id: mcp-auth-for-example-api
  client_secret_env: EXAMPLE_IDP_CLIENT_SECRET
  token_endpoint_auth_method: client_secret_post
  exchange_client_id: unused
  scopes: [profile, email]
  identity_claims: [sub, email]
  pkce: false
  id_token_nonce_policy: disabled
  allowed_upstream_callback_uris:
    - https://auth.example.com/mcp-auth/identity/callback
```

There is no secret in this file. The `connector` block is an ordinary MCP Auth connector, so provider quirks (here: no PKCE with a client secret, no nonce) stay in that block, and MCP Runtime passes it through without naming any provider.

The steps, in order:

1. Register an OAuth app at the provider. Its redirect URI is the issuer plus `/identity/callback`, here `https://auth.example.com/mcp-auth/identity/callback`.
2. Generate the server's key pair. MCP Auth defaults the algorithm to `RS256`, so an RSA key works. The private key stays with the server. Only the public half goes in the file.
3. Deploy the MCP server first, so the operator registers its resource with MCP Auth.
4. Add the connector secret to `mcp-auth-connector-secrets` under the key `EXAMPLE_IDP_CLIENT_SECRET`. `setup` writes the secret variables of the main connector only, and no Runtime command writes a resource connector's, so this is a `kubectl` step. Use an explicit context and read the value from a protected file.
5. Preview, then apply:

```bash
mcp-runtime cluster mcp-auth resource apply --file .mcp/auth-resource.yaml \
  --kubeconfig /path/to/cluster.yaml --context selected-context --dry-run
mcp-runtime cluster mcp-auth resource apply --file .mcp/auth-resource.yaml \
  --kubeconfig /path/to/cluster.yaml --context selected-context --yes
```

The result is three entries plus a restart. In `mcp-auth-connectors`, `connectors.json` gains `resource-login-example-api` with the connector block as written. In `mcp-auth-gateway-wiring`, `resource-logins.json` gains:

```json
{
  "id": "res-example-api",
  "type": "oidc",
  "connector": "resource-login-example-api",
  "subject_prefix": "res-example-api:",
  "resources": ["https://mcp.example.com/example-api/mcp"],
  "label": "Sign in with Example IdP",
  "downstream_token": "upstream_session"
}
```

and `resource-clients.json` gains the client, which carries only the public key:

```json
{
  "client_id": "example-api-mcp",
  "name": "resource:example-api",
  "resource": "https://mcp.example.com/example-api/mcp",
  "public_key_pem": "-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----\n"
}
```

Now trace a request. A client asks for a token for `https://mcp.example.com/example-api/mcp`. MCP Auth finds the scoped method `res-example-api`, sends the person to the provider through `resource-login-example-api`, and stores the provider token set under the subject `res-example-api:<provider subject>`. The server later posts the RFC 8693 exchange signed with its private key. MCP Auth matches the key to the client `example-api-mcp`, checks it is bound to this resource, and returns the stored provider token.

## Trust and safety properties

These are properties of the code, not promises about your provider.

- **Secrets by reference only.** A connector names an environment variable in `client_secret_env`. MCP Auth rejects a literal `client_secret` at load. `connector apply` and `resource apply` reject one before writing. The value lives in the Secret and reaches the pod through `envFrom`.
- **A per-resource client without a shared secret.** A resource client is authenticated with `private_key_jwt`. MCP Auth stores only the public key. A resource client can only be registered for a resource MCP Auth serves.
- **Audience binding.** Each access token is issued for one resource. A scoped login never falls back: if the scoped method for a resource is disabled, authorization for that resource is refused with `access_denied` instead of quietly using another method.
- **The provider's token stays server-side.** The client sees only MCP Auth's code and tokens. With `upstream_session`, the provider token set is stored in MCP Auth's database and handed out only through the exchange endpoint, to a resource client registered for that resource. That table holds usable secrets, so protect it like a credential store. A provider that issues no refresh token cannot be renewed silently: after the access token expires the person signs in again.
- **Callbacks can be pinned.** `allowed_upstream_callback_uris` restricts which `/identity/callback` URLs may be registered with the provider.
- **HTTPS by default.** Connector URLs must be `https` unless a local-development or insecure-connectors setting is on.
- **A broken login method is disabled, not fatal (since MCP Auth v0.9.0).** A login method whose own configuration is invalid, such as one naming a connector missing from the connectors file, is logged at ERROR with its id and reason, left off the login page and refused if requested directly. Every valid method keeps working. `/readyz` still returns 200 and lists `login_methods_disabled`. Set `MCP_AUTH_STRICT_LOGIN_METHODS=true` to restore fail-fast loading, for example in a CI check. Before v0.9.0 one such method made the whole process exit at startup. MCP Runtime's `setup --mcp-auth-server-image` default is a release older than v0.9.0 at the time of writing, so check which image you actually run.

Some failures are still fatal even in v0.9.0: an unreadable or invalid connectors file, a missing or invalid `MCP_AUTH_CONNECTOR`, colliding subject prefixes, and a methods file that cannot be parsed. A misspelled field inside a connector entry invalidates the whole connectors file.

## Common mistakes

| Mistake | What happens | Fix |
|---|---|---|
| A login method names a connector that is not in the connectors file (a *dangling* method). | Before v0.9.0 MCP Auth exits at startup. From v0.9.0 the method is disabled and its resource cannot sign in. | Keep the method and its connector in step. After any apply, look for `login method disabled` in the log and `login_methods_disabled` in `/readyz`. |
| A connector is put in the login methods file, or a method in `connectors.json`. | Unknown fields are rejected. A connector entry with a method's fields fails to load. | Connectors go in `MCP_AUTH_CONNECTORS_FILE`. Methods go in the methods or resource logins file. A method refers to a connector by name. |
| The connector's secret key is missing from `mcp-auth-connector-secrets`. | `resource apply` refuses before writing. A hand-edited connector fails when the pod starts. | Add the key named by `client_secret_env` first, then apply. |
| The redirect URL is not registered at the provider. | The provider rejects step 3 or step 5. | Register exactly `<issuer>/identity/callback`, including any path prefix such as `/mcp-auth`. |
| `connector apply` succeeds but nothing changes. | The ConfigMap changed, but MCP Auth read the old file at startup. | Roll the `mcp-auth-server` Deployment. |
| Re-running `setup` after wiring resources. | `setup` renders `connectors.json` from the file you pass. Check your release notes for whether it keeps the connectors other commands own. | Re-run `resource apply` for your resources afterwards. It is idempotent. |
| The resource is not served yet. | `resource apply` refuses: deploy the MCP server first so its resource is registered. | Deploy the server with its `auth` block, then apply. |
| A service id copied from an example. | The generated names (`form-<id>`, `gateway-login-<id>`, `res-<id>`) are derived from it, and the subject prefix is stored with sessions. | Use the real service name from the start, at most 27 characters. Renaming later signs people out of that resource. |

## Where to read more

- [MCP Auth: connectors, login methods and resource logins](https://github.com/tavric-labs/mcp-auth/blob/main/docs/connectors.md) has the full connector and method schemas.
- The [MCP authorization page](https://docs.mcpruntime.org/mcp-oauth/) in the MCP Runtime docs covers setup, the per-resource command and the Keycloak walkthrough.
- The [CLI reference](https://docs.mcpruntime.org/cli-reference/) lists the exact flags of the commands above.

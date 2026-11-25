---
title: "Agent identity with SPIFFE: why we chose it and what go-spiffe gives us"
description: "Why MCP Runtime names every agent session with a SPIFFE ID, what that buys us, and which parts of the identity protocol go-spiffe implements for us."
category: "Identity & Policy"
published: "2026-11-07"
reading_time: "9 min"
---

An AI agent that calls tools on an MCP server is a caller nobody has met. It has no employee badge, no login screen and no stable address. It might be a script on a laptop, a coding assistant in an editor or a job in a cluster. Yet the server it reaches can read data and change systems, so the platform needs a good answer to one question on every request: **who is making this call?**

This article explains how MCP Runtime answers that question, why we built the answer on [SPIFFE](https://spiffe.io/), and how the [go-spiffe](https://github.com/spiffe/go-spiffe) library carries the protocol details so we do not have to.

## The problem with the obvious answers

The obvious answers each break in a specific way.

- **A header such as `X-Agent: reviewer-bot`.** The caller writes it, so the caller can write anything. A header is a claim, not proof.
- **A shared API key.** A key says "someone who knows the secret". It cannot say which agent, for which human, with which permissions, and it is awkward to rotate and revoke per agent.
- **A bearer token.** Better, but a token that leaks works from any machine until it expires. Nothing ties it to the key that requested it.
- **Inventing our own identity format.** We would then own a naming scheme, a parser, its edge cases and a stream of "is this string really the same identity?" bugs. Identity parsing is a classic place for subtle security errors.

What we wanted instead is a **name** that is standard and unambiguous, a **proof** that the holder controls a private key, and a place to **carry both** that every proxy and TLS library already understands.

## What SPIFFE is, in one paragraph

SPIFFE (the Secure Production Identity Framework for Everyone) is a specification for naming and proving the identity of software. It defines three things:

1. **A SPIFFE ID**, a URI such as `spiffe://example.org/ns/team-a/session/adapter-1f3a9c2b44d07e11`. The part after `spiffe://` is the *trust domain*, the boundary inside which names are issued and trusted. The rest is a path whose meaning is up to the operator.
2. **An SVID** (SPIFFE Verifiable Identity Document), the proof. The X.509 form is an ordinary certificate with exactly one URI SAN holding the SPIFFE ID. A JWT form also exists.
3. **Trust bundles**, the set of CA certificates that a trust domain uses, so a verifier knows which signatures to accept.

Nothing in that list is exotic. An X.509-SVID is a normal certificate that any TLS stack, proxy or gateway can verify, and the extra rules (one URI SAN, a valid ID, a client-auth leaf) make it unambiguous.

## What an agent's identity is in MCP Runtime

When an agent connects through the adapter, it holds a short-lived client certificate. Its single URI SAN names one platform **session**:

```text
spiffe://<trust-domain>/ns/<namespace>/session/<session>
```

We bind identity to a session on purpose, not to an agent or a machine.

- A person (or team member) starts a session for an agent against a server. The platform stores it with an expiry.
- The certificate answers "which authorized session is this call part of?". The session record answers "which human, agent and team, with which grants?".
- Ending the session ends the identity, even if the certificate has not expired yet, because the gateway looks the session up on requests.
- There is no durable per-agent identity. An agent that runs tomorrow gets a new session and a new certificate. That is a deliberate v1 choice: a durable identity needs its own lifecycle and revocation story, and sessions already map cleanly onto "a human authorized this, for now".

The certificate is also never the whole decision. A valid identity for an active session still needs a grant for the specific server and tool. SPIFFE tells us *who*. Platform policy decides *what they may do*.

[![A human starts a platform session. The session names the SPIFFE ID in the agent's certificate and carries the grants. Revoking the session ends the identity.](/static/articles/identity-policy/agent-identity-with-spiffe/01-session-and-grant.png)](/static/articles/identity-policy/agent-identity-with-spiffe/01-session-and-grant.png)

## Our reasoning: why SPIFFE, because of sessions and grants

We did not start from "let's use SPIFFE". We started from what the platform's access model needs, and SPIFFE turned out to fit it closely.

MCP Runtime's access model has two records that carry all the meaning:

- A **session**: a human authorized one agent to work against one server for a limited time. It is stored by the platform, can expire and can be revoked.
- A **grant**: which servers and which tools that agent may use, with a trust level and limits on side effects.

Every request therefore has to answer three separate questions, and the answers live in different places:

1. **Who is calling?** A cryptographic proof, carried by the connection.
2. **On whose behalf, and is that still valid?** The session record.
3. **What may it do?** The grants attached to that session.

That shape puts a specific demand on the identity layer. It must be **a name for the session**, not for the machine or the agent. It must be **cheap to verify on every request** without a database call before TLS has even finished. It must be **short-lived and tied to the session's lifetime**. And it must be a **strict, standard string** that the gateway can turn into "this session" with no room for interpretation, because that string is the key into the session store and, through it, the grants.

A SPIFFE ID fits that demand almost exactly. The path is ours to define, so `ns/<namespace>/session/<name>` is the session's qualified reference, written in a format every TLS stack already carries in a certificate. The trust domain pins it to our issuer. The specification fixes what a legal ID looks like, so the string cannot be bent into someone else's session.

Here is how the alternatives looked against that demand:

| Option | Why it fell short for sessions and grants |
|---|---|
| Identity header set by the client | A claim, not proof. Anyone can write it. |
| Shared or per-agent API key | Says "someone who knows the secret". It does not name a session, does not expire with one, and revoking a key is not the same as ending a consented session. |
| Bearer JWT per session | Close, but a leaked token works from any machine until it expires, because nothing binds it to a private key the holder keeps. |
| mTLS with our own subject or SAN format | The right mechanism with the wrong naming. We would own the format, the parser and every confusable-ID bug. |
| Full workload identity with SPIRE | Solves a different problem: long-lived workload attestation. Our identities are per session and issued after a human authorizes them. It adds a system to run for a lifetime we deliberately do not want. |
| **SPIFFE ID in an X.509 certificate** | Proof of key possession during the TLS handshake, a standard strict name for the session, short-lived by construction, and nothing else to operate. |

So the decision is really two decisions that happen to meet. The **session** decides what an identity *means*: a person's time-limited consent. The **grant** decides what that identity *may do*. SPIFFE is how we **write the session's name down** so it can be proven and checked everywhere, with a standard we did not have to invent.

## Why SPIFFE, concretely

**1. The agent's key never leaves its machine.** The adapter generates a P-256 key locally and sends only a certificate signing request. The platform validates the request, has a workload issuer sign it, and returns a certificate. A stolen certificate without the key is useless, which a leaked bearer token is not.

**2. The identity is proven by the transport.** Because the proof is an X.509 certificate, the ingress verifies it during the TLS handshake. We do not write signature checks for a custom token format, and we do not trust a header the caller can edit. The ingress strips any client-supplied identity header and sets its own only for a verified chain.

**3. One strict, shared definition of a valid name.** SPIFFE defines exactly which characters, segments and trust domains are legal. Two components cannot disagree about whether an identity is well formed, and malformed or confusable forms (userinfo, ports, dot segments, percent escapes, uppercase trust domains) are refused rather than interpreted.

**4. Short-lived by construction.** Certificates last at most 24 hours and never outlive their session. A leaked certificate has a small window, and revoking the session denies it immediately.

**5. The issuer is constrained.** In production, a dedicated issuer signs these certificates under an approval policy limited to the session URI shape. Even a compromised request path cannot get the issuer to mint an arbitrary identity, and the platform re-validates every issued leaf against the original request.

**6. Standard tooling and an exit path.** `openssl x509 -noout -ext subjectAltName` shows an agent's identity. Meshes, proxies and other platforms understand SPIFFE IDs. If a future deployment wants SPIRE or workload-level identities, we are already speaking the same language.

## How the identity travels

[![The agent presents a SPIFFE certificate over mTLS. The ingress verifies the chain. The gateway checks the identity, the session and the grant, and denies on any failure.](/static/articles/identity-policy/agent-identity-with-spiffe/02-request-path.png)](/static/articles/identity-policy/agent-identity-with-spiffe/02-request-path.png)

Before any of that, the agent enrolls: it sends a CSR with one SPIFFE URI SAN, the runtime API validates ownership, session and grant, and the workload issuer signs it. Then each request follows the path above. Three checks make this safe, and each fails closed:

- **Before signing**, the runtime API recomputes the expected ID from its configured trust domain plus the namespace and session in the request. It accepts the CSR only if the CSR carries exactly that URI and no other SAN of any kind.
- **After signing**, the platform and the adapter both re-check the issued certificate: one URI SAN equal to the expected ID, a non-CA, client-auth-only leaf, bound to the CSR's key, within the session lifetime.
- **At the gateway**, the ID must parse, belong to the configured trust domain, and name a live session. Anything else is denied as `invalid_spiffe_identity`.

## What go-spiffe gives us

go-spiffe is the reference Go implementation of the SPIFFE specifications. The library covers a lot: IDs and trust domains, X.509 and JWT SVIDs, trust bundles, the Workload API for talking to a SPIRE agent, and SPIFFE-aware TLS configuration. MCP Runtime uses the identity-document half of it, and it is worth being exact about which parts:

| We need to... | go-spiffe provides |
|---|---|
| Mint an agent identity | `spiffeid.FromSegments(trustDomain, "ns", ns, "session", name)` builds a valid ID and returns an error for illegal input |
| Parse an ID from a header or SAN | `spiffeid.FromString` and `spiffeid.FromURI` validate the whole URI against the specification |
| Check the trust domain | `id.MemberOf(trustDomain)` is an exact comparison, not a string prefix match |
| Read the identity from a certificate | `x509svid.IDFromCert` requires exactly one URI SAN and a valid SPIFFE ID |
| Pin the ingress identity | the pin is parsed as a SPIFFE ID and compared with the certificate's ID, so an invalid pin or an extra URI SAN never matches |
| Build the ingress pin in the operator | `spiffeid.FromSegments`, and invalid input fails the reconcile instead of silently disabling the pin |

Before adopting the library we had hand-written `net/url` parsing. It worked, but it embodied our own interpretation of what a valid SPIFFE ID is, including a case-insensitive trust domain match and escaping of unusual characters. Delegating to go-spiffe removes that interpretation. The behavior is now stricter and the rules belong to the specification, not to us. Kubernetes namespaces and platform session names already fit the allowed alphabet, so valid deployments are unaffected, and anything outside it is refused at issuance.

### What we deliberately do not use

Honesty about scope matters more than a long feature list, so here is what MCP Runtime does **not** do with SPIFFE today:

- **No SPIRE and no Workload API.** Identities are issued per session by the platform through a cert-manager issuer, not attested to a long-running workload.
- **No JWT-SVIDs.** Platform tokens are a separate mechanism with their own signing keys and audiences.
- **No federation.** One trust domain, one cluster.
- **No durable workload identity.** An identity is exactly as long-lived as the session it names.

Keeping `pkg/identity` to a single go-spiffe dependency (`spiffeid`) and putting the fuller certificate rules in a small `pkg/spiffecert` package that uses `x509svid` keeps the dependency surface small, and architecture tests enforce which imports each package may have.

## What this does and does not protect

SPIFFE-based identity gives you these properties:

- An agent cannot claim another session, because the ID comes from a certificate signed only after the platform checked ownership.
- A stolen header is useless, because only the verified ingress hop is trusted.
- A malformed or confusable ID is rejected, because parsing is strict and a failure denies.
- A leaked certificate is short-lived and dies with its session.

It does **not** give you authorization on its own, a durable identity across sessions, or safety if the ingress or the gateway's network isolation is bypassed, since the gateway trusts the header from the ingress hop. It also does not replace OAuth, which covers a different job: access tokens for upstream services.

## The takeaway

SPIFFE let us stop inventing identity. We get a standard name, a standard proof carried by TLS, strict validation rules written by people who think about this full time, and a library that implements them. What we keep for ourselves is the part only we can decide: which human started the session, which grants it carries, and which tool calls it may make.

If you want the implementation details, the [SPIFFE agent identity page in the docs](https://docs.mcpruntime.org/spiffe-agent-identity/) walks through enrollment, the request path, operating notes and a source map. The code is at [github.com/mcp-runtime/mcp-runtime](https://github.com/mcp-runtime/mcp-runtime).

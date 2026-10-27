// Package passport is the platform's bearer-authentication contract: who a
// caller is, how that is proven, and how it is carried between services.
//
// Identity. Principal and Claims describe an authenticated caller (user id,
// role, teams, whether it is a service). A Principal travels in a context
// (WithPrincipal, FromContext) and in audit fields (AuditSource,
// AuditIdentityLabel, RequestIP).
//
// Proof. Sign and Verify issue and check short-lived HS256 JWTs. The
// algorithm is pinned, the issuer is fixed, and a token carries exactly one
// audience (platform-api, runtime-api or analytics-api), so a token minted for
// one service is rejected by another. JWTSecretFromEnv reads the shared signing
// secret.
//
// Enforcement. An Authenticator is built for one audience
// (NewAudienceAuthenticator) and wraps handlers with authentication and role
// checks. API keys resolve to a Principal through a PrincipalResolver;
// HTTPUserKeyResolver is the cached, network-backed implementation.
//
// Exchange. ExchangeHandler is the Traefik forward-auth endpoint that trades a
// platform user bearer for a one-minute JWT scoped to the runtime or analytics
// audience. Internal service secrets never reach the gateway or the UI.
//
// Wire. AuthResolve* and PrincipalResolve* are the request and response types
// of the platform-api principal endpoints the resolvers call.
//
// Failure modes are explicit: missing or invalid credentials map to 401,
// insufficient role to 403, and an unavailable resolver or secret to 503, all
// through apikit's error envelope.
package passport

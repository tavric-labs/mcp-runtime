// Package platformauth is platform bearer authentication.
//
// It signs and verifies short-lived HS256 JWTs (algorithm pinned, issuer fixed,
// exactly one audience), models Principal and Claims, builds audience-scoped
// Authenticators and middleware, resolves user API keys through
// HTTPUserKeyResolver, and provides ExchangeHandler, the Traefik forward-auth
// endpoint that exchanges a user bearer for a one-minute JWT scoped to the
// runtime or analytics audience. JWTSecretFromEnv reads the shared secret.
//
// Request IP and audit-source helpers keep audit fields consistent across
// services.
package platformauth

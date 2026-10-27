package main

import policypkg "mcp-runtime/pkg/policy"

// CallerKind records how a caller was established. Authentication completing
// does not imply a Runtime identity: delegated-header and anonymous/observe
// traffic are explicit, supported caller categories.
type CallerKind uint8

const (
	// CallerAnonymous is the zero value: no identity was established.
	CallerAnonymous CallerKind = iota
	// CallerDelegated is a request whose credentials are forwarded upstream
	// unverified; the gateway never attributes a Runtime identity to it.
	CallerDelegated
	// CallerOAuth is a caller verified by an OAuth bearer token.
	CallerOAuth
	// CallerCertificate is a caller verified by a session-bound adapter
	// certificate (and by OAuth too when the server requires both).
	CallerCertificate
	// CallerRejected is a verified actor whose authentication failed, for
	// example a revoked session. The actor is kept for denial audit only; it
	// is never a usable session.
	CallerRejected
)

// Caller couples provenance with the identity it vouches for, so the two
// cannot disagree and a token can only accompany a verified OAuth caller.
// Fields are unexported: build one with the constructors below. Go cannot
// stop code in this package from assembling a Caller literal; the constructors
// are the only paths production code uses, and the zero value is anonymous.
type Caller struct {
	kind  CallerKind
	actor identityContext
	token string
}

func anonymousCaller() Caller { return Caller{} }

func delegatedCaller() Caller { return Caller{kind: CallerDelegated} }

func oauthCaller(actor identityContext, token string) Caller {
	return Caller{kind: CallerOAuth, actor: actor, token: token}
}

func certificateCaller(actor identityContext, oauthToken string) Caller {
	return Caller{kind: CallerCertificate, actor: actor, token: oauthToken}
}

// rejectedCaller keeps the verified actor for audit attribution after an
// authentication failure. It carries no token and Usable reports false.
func rejectedCaller(actor identityContext) Caller {
	return Caller{kind: CallerRejected, actor: actor}
}

func (c Caller) Kind() CallerKind { return c.kind }

// Identity is the actor for authorization and audit. It is empty for
// anonymous and delegated callers.
func (c Caller) Identity() identityContext { return c.actor }

// OAuthToken is the verified bearer; it is empty unless the caller was
// verified by OAuth.
func (c Caller) OAuthToken() string { return c.token }

// Usable reports whether the caller may proceed to authorization.
func (c Caller) Usable() bool { return c.kind != CallerRejected }

// withSession returns the caller with a validated session binding attached.
func (c Caller) withSession(binding *policypkg.Binding) Caller {
	c.actor.Session = binding
	return c
}

package identity

import (
	"fmt"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

const (
	namespaceSegment = "ns"
	sessionSegment   = "session"
)

// SessionSPIFFEID returns the canonical session-bound SPIFFE ID:
// spiffe://<trustDomain>/ns/<namespace>/session/<session>.
//
// The ID is built and validated by go-spiffe, so every component must be a
// valid SPIFFE value: the trust domain is lowercase and the namespace and
// session use only letters, digits, '.', '-' and '_'. Kubernetes namespace
// names and platform session names already satisfy this. Anything else is an
// error rather than being escaped.
func SessionSPIFFEID(trustDomain, namespace, session string) (string, error) {
	td, err := spiffeid.TrustDomainFromString(strings.TrimSpace(trustDomain))
	if err != nil {
		return "", fmt.Errorf("session SPIFFE ID: %w", err)
	}
	id, err := spiffeid.FromSegments(td, namespaceSegment, strings.TrimSpace(namespace), sessionSegment, strings.TrimSpace(session))
	if err != nil {
		return "", fmt.Errorf("session SPIFFE ID: %w", err)
	}
	return id.String(), nil
}

// ParseSessionSPIFFE parses a session-bound SPIFFE ID of the form
// spiffe://<trustDomain>/ns/<namespace>/session/<sessionID>. It fails closed
// on any deviation: go-spiffe rejects malformed IDs (wrong scheme, query,
// fragment, port, userinfo, empty or dot segments, escapes), and the trust
// domain and path shape must match exactly.
func ParseSessionSPIFFE(raw, trustDomain string) (namespace, sessionID string, ok bool) {
	td, err := spiffeid.TrustDomainFromString(strings.TrimSpace(trustDomain))
	if err != nil {
		return "", "", false
	}
	id, err := spiffeid.FromString(raw)
	if err != nil || !id.MemberOf(td) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(id.Path(), "/"), "/")
	if len(parts) != 4 || parts[0] != namespaceSegment || parts[2] != sessionSegment {
		return "", "", false
	}
	return parts[1], parts[3], true
}

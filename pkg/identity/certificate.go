package identity

import (
	"crypto/x509"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

// CertificateHasURI reports whether cert carries the SPIFFE ID want as a URI
// SAN. want must itself be a valid SPIFFE ID; anything else never matches, so a
// misconfigured pin fails closed instead of matching a look-alike string.
func CertificateHasURI(cert *x509.Certificate, want string) bool {
	if cert == nil {
		return false
	}
	wantID, err := spiffeid.FromString(want)
	if err != nil {
		return false
	}
	for _, uri := range cert.URIs {
		if uri == nil {
			continue
		}
		if id, err := spiffeid.FromURI(uri); err == nil && id == wantID {
			return true
		}
	}
	return false
}

// FirstClientSPIFFEURI returns the first valid SPIFFE ID URI SAN on cert whose
// trust domain matches trustDomain when trustDomain is non-empty. URI SANs that
// are not valid SPIFFE IDs are skipped. Returns "" when no matching URI is
// present.
func FirstClientSPIFFEURI(cert *x509.Certificate, trustDomain string) string {
	if cert == nil {
		return ""
	}
	td, ok := optionalTrustDomain(trustDomain)
	if !ok {
		return ""
	}
	for _, uri := range cert.URIs {
		if uri == nil {
			continue
		}
		id, err := spiffeid.FromURI(uri)
		if err != nil || (!td.IsZero() && !id.MemberOf(td)) {
			continue
		}
		return id.String()
	}
	return ""
}

// optionalTrustDomain parses a configured trust domain. An empty value means
// "any trust domain" and yields the zero TrustDomain; ok is false when a
// non-empty value is not a valid trust domain.
func optionalTrustDomain(raw string) (td spiffeid.TrustDomain, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return spiffeid.TrustDomain{}, true
	}
	td, err := spiffeid.TrustDomainFromString(raw)
	return td, err == nil
}

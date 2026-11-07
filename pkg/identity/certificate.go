package identity

import (
	"crypto/x509"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

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

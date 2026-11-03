package identity

import (
	"crypto/x509"
	"net/url"
	"testing"
)

func certWithURIs(t *testing.T, raw ...string) *x509.Certificate {
	t.Helper()
	cert := &x509.Certificate{}
	for _, r := range raw {
		u, err := url.Parse(r)
		if err != nil {
			t.Fatalf("parse %q: %v", r, err)
		}
		cert.URIs = append(cert.URIs, u)
	}
	return cert
}

func TestCertificateHasURI(t *testing.T) {
	t.Parallel()
	const proxy = "spiffe://example.org/ns/traefik/sa/traefik"
	cases := []struct {
		name string
		cert *x509.Certificate
		want string
		ok   bool
	}{
		{"exact pin", certWithURIs(t, proxy), proxy, true},
		{"pin among several URI SANs", certWithURIs(t, "https://other.example", proxy), proxy, true},
		{"different workload", certWithURIs(t, "spiffe://example.org/ns/traefik/sa/other"), proxy, false},
		{"different trust domain", certWithURIs(t, "spiffe://attacker.org/ns/traefik/sa/traefik"), proxy, false},
		{"no URI SAN", certWithURIs(t), proxy, false},
		{"nil certificate", nil, proxy, false},
		{"empty pin", certWithURIs(t, proxy), "", false},
		// A pin that is not a valid SPIFFE ID can never match, even when a
		// certificate carries the identical string.
		{"non-SPIFFE pin", certWithURIs(t, "https://proxy.example/id"), "https://proxy.example/id", false},
		{"pin with trailing slash", certWithURIs(t, proxy+"/"), proxy + "/", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CertificateHasURI(tc.cert, tc.want); got != tc.ok {
				t.Fatalf("CertificateHasURI(%q) = %v, want %v", tc.want, got, tc.ok)
			}
		})
	}
}

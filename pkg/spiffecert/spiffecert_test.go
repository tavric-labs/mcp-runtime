package spiffecert

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

func TestHasID(t *testing.T) {
	t.Parallel()
	const proxy = "spiffe://example.org/ns/traefik/sa/traefik"
	cases := []struct {
		name string
		cert *x509.Certificate
		want string
		ok   bool
	}{
		{"exact pin", certWithURIs(t, proxy), proxy, true},
		{"different workload", certWithURIs(t, "spiffe://example.org/ns/traefik/sa/other"), proxy, false},
		{"different trust domain", certWithURIs(t, "spiffe://attacker.org/ns/traefik/sa/traefik"), proxy, false},
		{"no URI SAN", certWithURIs(t), proxy, false},
		{"nil certificate", nil, proxy, false},
		{"empty pin", certWithURIs(t, proxy), "", false},
		// An X.509-SVID carries exactly one URI SAN. A certificate with extra
		// URI SANs is not an SVID, even if one of them is the pinned ID.
		{"pin among several URI SANs", certWithURIs(t, "https://other.example", proxy), proxy, false},
		{"non-SPIFFE URI SAN", certWithURIs(t, "https://proxy.example/id"), "https://proxy.example/id", false},
		{"non-SPIFFE pin", certWithURIs(t, proxy), "not a spiffe id", false},
		{"pin with trailing slash", certWithURIs(t, proxy+"/"), proxy + "/", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := HasID(tc.cert, tc.want); got != tc.ok {
				t.Fatalf("HasID(%q) = %v, want %v", tc.want, got, tc.ok)
			}
		})
	}
}

func TestIDFromCertificate(t *testing.T) {
	t.Parallel()
	const id = "spiffe://example.org/ns/team-a/session/s1"
	got, err := IDFromCertificate(certWithURIs(t, id))
	if err != nil || got.String() != id {
		t.Fatalf("IDFromCertificate() = (%v, %v), want %s", got, err, id)
	}
	for name, cert := range map[string]*x509.Certificate{
		"nil":      nil,
		"no SAN":   certWithURIs(t),
		"two SANs": certWithURIs(t, id, "spiffe://example.org/ns/x/session/y"),
		"not ID":   certWithURIs(t, "https://example.org/x"),
	} {
		if _, err := IDFromCertificate(cert); err == nil {
			t.Errorf("IDFromCertificate(%s) succeeded, want error", name)
		}
	}
}

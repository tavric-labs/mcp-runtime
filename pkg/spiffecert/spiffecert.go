// Package spiffecert reads SPIFFE identities out of X.509 certificates using
// go-spiffe's x509svid rules: a certificate carries exactly one URI SAN and it
// must be a valid SPIFFE ID.
//
// It lives apart from pkg/identity so that foundational package keeps its
// single go-spiffe dependency (spiffeid). Certificate-handling code that wants
// the fuller x509svid behavior imports this package instead.
package spiffecert

import (
	"crypto/x509"
	"errors"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

// IDFromCertificate returns the SPIFFE ID of cert. It fails unless the
// certificate has exactly one URI SAN that is a valid SPIFFE ID.
func IDFromCertificate(cert *x509.Certificate) (spiffeid.ID, error) {
	if cert == nil {
		return spiffeid.ID{}, errors.New("certificate is nil")
	}
	return x509svid.IDFromCert(cert)
}

// HasID reports whether cert's SPIFFE ID equals want. want must itself be a
// valid SPIFFE ID and cert must have exactly one URI SAN; any other input
// reports false, so a misconfigured pin or a certificate with extra URI SANs
// fails closed.
func HasID(cert *x509.Certificate, want string) bool {
	wantID, err := spiffeid.FromString(want)
	if err != nil {
		return false
	}
	id, err := IDFromCertificate(cert)
	return err == nil && id == wantID
}

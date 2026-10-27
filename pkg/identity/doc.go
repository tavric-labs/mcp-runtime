// Package identity is the shared identity vocabulary of the platform.
//
// It formats and parses SPIFFE session identities
// (spiffe://<trust-domain>/.../<namespace>/<session>), finds them in client
// certificates, builds and validates session-bound certificate signing requests
// (csr.go), and defines the credential-header and OAuth protected-resource
// contracts plus bearer-token extraction.
//
// It imports only the standard library and other leaf packages, so the gateway,
// platform API, runtime API and CLI adapter all agree on what an identity is.
package identity

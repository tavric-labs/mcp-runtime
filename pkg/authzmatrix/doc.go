// Package authzmatrix loads the authn/authz expectation matrix kept in
// docs/security/authz-matrix.json and applies a role's credentials to a test
// request. The services' conformance tests use it to check every route against
// the documented allow and deny outcome for anonymous, user, admin and ingest
// callers.
package authzmatrix

package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

// Interfaces belong to the consumers. Request filters cannot mutate the
// synchronization cursor, invalidate the cache, or issue/revoke sessions here.
type SessionReader interface {
	Lookup(context.Context, SessionRef, time.Time) (*policypkg.Binding, error)
	Fresh(time.Time) bool
}

type SessionRef struct {
	Namespace policypkg.Namespace
	ID        policypkg.SessionID
}

func (r SessionRef) key() string { return string(r.Namespace) + "/" + string(r.ID) }

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type OAuthAuthenticator interface {
	Authenticate(*http.Request, *policypkg.Document, policypkg.TeamID) (oauthCredentials, error)
}

type oauthAuthenticatorFunc func(*http.Request, *policypkg.Document, policypkg.TeamID) (oauthCredentials, error)

func (f oauthAuthenticatorFunc) Authenticate(r *http.Request, p *policypkg.Document, team policypkg.TeamID) (oauthCredentials, error) {
	return f(r, p, team)
}

func (s *gatewayServer) sessionReader() SessionReader {
	if s.sessions != nil {
		return s.sessions
	}
	return s.sessionSync
}

func (s *gatewayServer) oauthAuthenticator() OAuthAuthenticator {
	if s.oauth != nil {
		return s.oauth
	}
	return oauthAuthenticatorFunc(s.authenticateOAuthForSubjectTeam)
}

// Failure codes are stable wire reasons and comparable errors. Wrapping one
// preserves errors.Is; transport details never become a client denial reason.
type sessionFailure string

func (e sessionFailure) Error() string { return string(e) }

const (
	errSessionUnavailable      sessionFailure = "policy_unavailable"
	errSessionNotFound         sessionFailure = "session_not_found"
	errSessionRevoked          sessionFailure = "session_revoked"
	errSessionExpired          sessionFailure = "session_expired"
	errSessionMismatch         sessionFailure = "session_identity_mismatch"
	errMissingCertificate      sessionFailure = "missing_client_certificate"
	errUntrustedProxy          sessionFailure = "untrusted_proxy"
	errMissingVerifiedIdentity sessionFailure = "missing_verified_identity"
	errInvalidSPIFFE           sessionFailure = "invalid_spiffe_identity"
)

type authenticationFailure struct {
	status int
	reason string
}

func (e *authenticationFailure) Error() string { return e.reason }

func authenticationDecision(err error, version string) policypkg.Decision {
	status, reason := http.StatusServiceUnavailable, "authentication_unavailable"
	var failure *authenticationFailure
	var session sessionFailure
	switch {
	case errors.As(err, &failure) && failure != nil:
		if failure.status >= 400 && failure.status <= 599 && failure.reason != "" {
			status, reason = failure.status, failure.reason
		}
	case errors.As(err, &session):
		status, reason = http.StatusUnauthorized, string(session)
		if errors.Is(err, errSessionUnavailable) {
			status = http.StatusServiceUnavailable
		}
	}
	return policypkg.Deny(status, reason, version)
}

// optionalDeadline distinguishes no expiry from a successfully parsed expiry.
// The zero value means no expiry; malformed external timestamps return an error.
type optionalDeadline struct {
	at time.Time
}

func parseDeadline(value string) (optionalDeadline, error) {
	if value == "" {
		return optionalDeadline{}, nil
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return optionalDeadline{}, err
	}
	return optionalDeadline{at: at}, nil
}

func (d optionalDeadline) expired(now time.Time) bool {
	return !d.at.IsZero() && !now.Before(d.at)
}

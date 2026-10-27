package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"mcp-runtime/pkg/identity"
	policypkg "mcp-runtime/pkg/policy"
)

// authFilter establishes identity from OAuth and/or verified adapter certificates.
// Terminal outputs retain trusted partial identity for denial audit.
func (s *gatewayServer) authFilter(in PolicyRequest) (AuthenticatedRequest, *FilterResponse) {
	ex := AuthenticatedRequest{PolicyRequest: in, CallerKind: CallerAnonymous}
	version := policypkg.ChoosePolicyVersion(policypkg.PolicyVersion(ex.Policy), s.defaultPolicyVersion)
	deny := func(err error) (AuthenticatedRequest, *FilterResponse) {
		return ex, denyResponse(authenticationDecision(err, version))
	}
	if ex.Policy == nil {
		return deny(errSessionUnavailable)
	}
	if policypkg.UsesDelegatedHeaders(ex.Policy) {
		ex.CallerKind = CallerDelegated
		credentials, err := identity.NewCredentialPolicy(ex.Policy.Auth.Headers, ex.Policy.Auth.CredentialPresence)
		if err != nil {
			return deny(&authenticationFailure{status: http.StatusUnauthorized, reason: string(identity.ErrInvalidCredentialConfig)})
		}
		if err := credentials.Check(ex.R.Header); err != nil {
			return deny(&authenticationFailure{status: http.StatusUnauthorized, reason: err.Error()})
		}
		return ex, nil
	}
	var certificateIdentity *identityContext
	if ex.Policy.Auth != nil && strings.TrimSpace(ex.Policy.Auth.TrustDomain) != "" &&
		strings.TrimSpace(ex.R.Header.Get(s.verifiedSPIFFEHeaderName())) != "" {
		verifiedIdentity, err := s.authenticateAdapterCertificate(ex.R, ex.Policy)
		if err != nil {
			// A known revoked/expired binding identifies the actor for audit,
			// while the terminal failure prevents using it as an active session.
			ex.Identity = verifiedIdentity
			if verifiedIdentity.Session != nil {
				ex.CallerKind = CallerCertificate
			}
			return deny(err)
		}
		certificateIdentity = &verifiedIdentity
	}
	if !policypkg.PolicyUsesOAuth(ex.Policy) {
		if certificateIdentity != nil {
			ex.Identity, ex.CallerKind = *certificateIdentity, CallerCertificate
		}
		return ex, nil
	}
	var subjectTeam policypkg.TeamID
	if certificateIdentity != nil {
		subjectTeam = certificateIdentity.TeamID
	}
	credentials, err := s.oauthAuthenticator().Authenticate(ex.R, ex.Policy, subjectTeam)
	reason := ""
	if err != nil {
		reason = authenticationDecision(err, version).Reason
	}
	s.metrics.recordOAuthOutcome(s.metricScope(ex.Policy), err == nil, reason)
	if err != nil {
		return deny(err)
	}
	ex.Identity, ex.OAuthToken, ex.CallerKind = credentials.Identity, credentials.Token, CallerOAuth
	if certificateIdentity != nil {
		if reason := certificateOAuthMismatch(*certificateIdentity, credentials.Identity); reason != "" {
			return deny(&authenticationFailure{status: http.StatusUnauthorized, reason: reason})
		}
		ex.Identity, ex.CallerKind = *certificateIdentity, CallerCertificate
	} else if ex.Identity.SessionID != "" && ex.Policy.Session != nil && ex.Policy.Session.Store == "platform" && ex.Inspection.RequiresAuthorization() {
		binding, err := s.sessionReader().Lookup(ex.R.Context(), SessionRef{Namespace: ex.Policy.Server.Namespace, ID: ex.Identity.SessionID}, s.currentTime())
		if err == nil {
			err = validateSessionBinding(binding, ex.Identity, s.currentTime())
		}
		if err != nil {
			if !ex.Policy.Session.Required && !errors.Is(err, errSessionUnavailable) {
				return ex, nil
			}
			return deny(err)
		}
		ex.Identity.Session = binding
	}
	return ex, nil
}

// validateSessionBinding is pure: identity matching and lifetime checks use an
// explicit instant, and the error retains its stable category through wrapping.
func validateSessionBinding(binding *policypkg.Binding, actor identityContext, now time.Time) error {
	if binding == nil {
		return errSessionUnavailable
	}
	// A malformed expiry is treated as expired, matching pkg/policy's evaluator.
	deadline, deadlineErr := parseDeadline(strings.TrimSpace(binding.ExpiresAt))
	switch {
	case binding.Revoked:
		return errSessionRevoked
	case deadlineErr != nil || deadline.expired(now):
		return errSessionExpired
	case binding.HumanID != "" && binding.HumanID != actor.HumanID,
		binding.AgentID != "" && binding.AgentID != actor.AgentID,
		binding.TeamID != "" && binding.TeamID != actor.TeamID:
		return errSessionMismatch
	default:
		return nil
	}
}

func certificateOAuthMismatch(certificateIdentity, oauthIdentity identityContext) string {
	if strings.TrimSpace(string(oauthIdentity.HumanID)) == "" || oauthIdentity.HumanID != certificateIdentity.HumanID {
		return "certificate_oauth_subject_mismatch"
	}
	if oauthIdentity.TeamID != "" && certificateIdentity.TeamID != "" && oauthIdentity.TeamID != certificateIdentity.TeamID {
		return "certificate_oauth_team_mismatch"
	}
	return ""
}

// authenticateAdapterCertificate validates the verified ingress assertion for
// a session-bound adapter certificate. On an OAuth-configured server, OAuth
// validation also runs and binds the token subject to this session identity.
//
// TLS is terminated at the ingress (Traefik), which verifies the caller's
// client certificate against the identity CA and injects the caller's SPIFFE
// identity as a trusted header. The ingress→gateway hop is itself re-encrypted
// with mTLS, so the gateway can prove the request actually came through the
// ingress rather than from a peer that bypassed it. The gateway therefore:
//
//  1. Requires the request to arrive over a verified mTLS hop. A plaintext or
//     unverified connection — e.g. a pod connecting directly to the gateway and
//     forging the identity header — is rejected before the header is ever read.
//     This is the cryptographic half of the trusted-header guarantee; the
//     NetworkPolicy that restricts the gateway to ingress-only traffic is the
//     defense-in-depth half.
//  2. Optionally pins the ingress identity: when trustedProxySPIFFE is set, the
//     verified peer certificate must present exactly that SPIFFE URI SAN. This
//     matters when the identity CA also signs non-ingress certificates (e.g.
//     adapter certs), so a verified chain alone is not proof of "came from the
//     ingress."
//  3. Reads the caller identity from the verified SPIFFE header, validates its
//     trust domain, and maps it to a rendered session binding.
//
// Client-supplied governance headers (human/agent/team/session) are never
// consulted in this mode; identity comes only from the verified SPIFFE header.
func (s *gatewayServer) authenticateAdapterCertificate(r *http.Request, policy *policypkg.Document) (identityContext, error) {
	// (1) The connection must be an ingress-authenticated mTLS hop.
	if r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return identityContext{}, errMissingCertificate
	}
	// (2) Optionally pin the trusted ingress identity.
	if expected := strings.TrimSpace(s.trustedProxySPIFFE); expected != "" {
		if !identity.CertificateHasURI(r.TLS.PeerCertificates[0], expected) {
			return identityContext{}, errUntrustedProxy
		}
	}

	// (3) Read the caller identity from the ingress-injected verified header.
	rawID := strings.TrimSpace(r.Header.Get(s.verifiedSPIFFEHeaderName()))
	if rawID == "" {
		return identityContext{}, errMissingVerifiedIdentity
	}
	namespace, sessionID, ok := identity.ParseSessionSPIFFE(rawID, policy.Auth.TrustDomain)
	if !ok {
		return identityContext{}, errInvalidSPIFFE
	}

	bindings := policy.Sessions
	if policy.Session != nil && policy.Session.Store == "platform" {
		binding, err := s.sessionReader().Lookup(r.Context(), SessionRef{Namespace: policypkg.Namespace(namespace), ID: policypkg.SessionID(sessionID)}, s.currentTime())
		if err != nil {
			return identityContext{}, err
		}
		if binding == nil {
			return identityContext{}, errSessionUnavailable
		}
		bindings = []policypkg.Binding{*binding}
	}
	for _, binding := range bindings {
		if string(binding.Namespace) != namespace || string(binding.Name) != sessionID {
			continue
		}
		boundIdentity := identityContext{HumanID: binding.HumanID, AgentID: binding.AgentID, TeamID: binding.TeamID, SessionID: binding.Name, Session: &binding}
		if err := validateSessionBinding(&binding, boundIdentity, s.currentTime()); err != nil {
			return boundIdentity, err
		}
		return boundIdentity, nil
	}
	return identityContext{}, errSessionNotFound
}

// verifiedSPIFFEHeaderName returns the configured trusted-identity header,
// falling back to the default when unset.
func (s *gatewayServer) verifiedSPIFFEHeaderName() string {
	if h := strings.TrimSpace(s.verifiedSPIFFEHeader); h != "" {
		return h
	}
	return defaultVerifiedSPIFFEHeader
}

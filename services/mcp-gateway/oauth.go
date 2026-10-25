package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	mcpauth "github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"
	"github.com/golang-jwt/jwt/v4"

	"mcp-runtime/pkg/identity"
	policypkg "mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/serviceutil"
)

type oauthMetadataResponse struct {
	status               int
	resource             string
	authorizationServers []string
	scopes               []string
	errorCode            string
	message              string
}

// protectedResourceMetadata describes the response without writing HTTP state.
func protectedResourceMetadata(r *http.Request, policy *policypkg.Document) *oauthMetadataResponse {
	if !isOAuthProtectedMetadataPath(r.URL.Path) {
		return nil
	}
	if !policypkg.PolicyUsesOAuth(policy) {
		return &oauthMetadataResponse{status: http.StatusNotFound, errorCode: "oauth_not_enabled", message: "This MCP server has no OAuth configuration."}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return &oauthMetadataResponse{status: http.StatusMethodNotAllowed}
	}
	audience := strings.TrimSpace(policy.Auth.Audience)
	if audience == "" {
		return &oauthMetadataResponse{status: http.StatusServiceUnavailable, errorCode: "oauth_audience_missing", message: "This MCP server has no auth.audience. Set spec.auth.audience to the canonical resource URI."}
	}
	return &oauthMetadataResponse{status: http.StatusOK, resource: audience,
		authorizationServers: []string{strings.TrimSpace(policy.Auth.IssuerURL)}, scopes: append([]string(nil), policy.Auth.Scopes...)}
}

func (s *gatewayServer) authenticateOAuthForSubjectTeam(r *http.Request, policy *policypkg.Document, subjectTeam policypkg.TeamID) (oauthCredentials, error) {
	fail := func(status int, reason string) (oauthCredentials, error) {
		return oauthCredentials{}, &authenticationFailure{status: status, reason: reason}
	}
	if policy == nil || policy.Auth == nil {
		return fail(http.StatusServiceUnavailable, "oauth_config_missing")
	}
	issuerURL := strings.TrimSpace(policy.Auth.IssuerURL)
	if issuerURL == "" {
		return fail(http.StatusServiceUnavailable, "oauth_issuer_missing")
	}
	tokenHeader := oauthTokenHeader(policy)
	token := extractToken(tokenHeader, r.Header.Get(tokenHeader))
	if token == "" {
		return fail(http.StatusUnauthorized, "missing_bearer_token")
	}
	// The audience is the configured resource, never an untrusted request host.
	audience := strings.TrimSpace(policy.Auth.Audience)
	if audience == "" {
		return fail(http.StatusServiceUnavailable, "oauth_audience_missing")
	}
	provider, err := s.oauthProviderForIssuer(r.Context(), issuerURL, audience)
	if err != nil {
		log.Printf("oauth provider lookup failed for %s: %v", issuerURL, err)
		return fail(http.StatusServiceUnavailable, "oauth_provider_unavailable")
	}
	claims, err := provider.verifier.VerifyContext(r.Context(), token)
	if err != nil {
		return fail(http.StatusUnauthorized, "invalid_token")
	}
	teamID, teamAllowed := oauthTeamIDForSubject(jwt.MapClaims(claims.Raw), policy, string(subjectTeam))
	if !teamAllowed {
		return fail(http.StatusUnauthorized, "certificate_oauth_team_mismatch")
	}
	return oauthCredentials{
		Token: token,
		Identity: identityContext{
			HumanID:   policypkg.HumanID(claims.Subject),
			AgentID:   policypkg.AgentID(policypkg.FirstNonEmpty(stringClaim(claims.Raw, "azp"), stringClaim(claims.Raw, "client_id"))),
			TeamID:    policypkg.TeamID(teamID),
			SessionID: policypkg.SessionID(stringClaim(claims.Raw, "sid")),
		},
	}, nil
}

// A certificate already binds the caller's subject team. When OAuth lists
// multiple memberships, select that asserted team rather than the resource's
// owning team; explicit single-team claims remain authoritative.
func oauthTeamIDForSubject(claims jwt.MapClaims, policy *policypkg.Document, subjectTeam string) (string, bool) {
	if subjectTeam == "" {
		return oauthTeamID(claims, policy), true
	}
	if explicit := policypkg.FirstNonEmpty(stringClaim(claims, "team_id"), stringClaim(claims, "tenant_id"), stringClaim(claims, "tid")); explicit != "" {
		return explicit, true
	}
	teams := stringClaims(claims, "team_ids")
	for _, team := range teams {
		if team == subjectTeam {
			return subjectTeam, true
		}
	}
	if len(teams) > 0 {
		return "", false
	}
	return "", true
}

func oauthTeamID(claims jwt.MapClaims, policy *policypkg.Document) string {
	if teamID := policypkg.FirstNonEmpty(stringClaim(claims, "team_id"), stringClaim(claims, "tenant_id"), stringClaim(claims, "tid")); teamID != "" {
		return teamID
	}
	teamIDs := stringClaims(claims, "team_ids")
	if policyTeamID := policypkg.PolicyServerTeamID(policy); policyTeamID != "" {
		for _, teamID := range teamIDs {
			if teamID == policyTeamID {
				return teamID
			}
		}
	}
	if len(teamIDs) == 1 {
		return teamIDs[0]
	}
	return ""
}

func stringClaims(claims jwt.MapClaims, name string) []string {
	value, ok := claims[name]
	if !ok {
		return nil
	}
	values := make([]string, 0)
	switch typed := value.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		for _, item := range typed {
			if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
				values = append(values, strings.TrimSpace(value))
			}
		}
	}
	return values
}

func (s *gatewayServer) oauthProviderForIssuer(ctx context.Context, issuerURL, audience string) (*oauthProvider, error) {
	issuerURL = strings.TrimSpace(issuerURL)
	if issuerURL == "" {
		return nil, errors.New("issuer URL is required")
	}

	cacheKey := issuerURL + "\x00" + audience
	s.oauthMu.Lock()
	provider, ok := s.oauthProviders[cacheKey]
	s.oauthMu.Unlock()
	if ok {
		return provider, nil
	}

	metadata, usingInternalIssuer, err := s.fetchAuthServerMetadataWithFallback(ctx, issuerURL)
	if err != nil {
		return nil, err
	}
	if usingInternalIssuer {
		metadata.JWKSURI, err = rewriteOAuthEndpoint(metadata.JWKSURI, metadata.Issuer, s.oauthInternalIssuerURL)
		if err != nil {
			return nil, err
		}
	}
	provider = &oauthProvider{verifier: &mcpauth.JWTVerifier{
		JWKSURL:      metadata.JWKSURI,
		Issuer:       issuerURL,
		Audience:     audience,
		HTTPClient:   s.oauthClient(),
		JWKSCacheTTL: 10 * time.Minute,
	}}
	s.oauthMu.Lock()
	if existing, ok := s.oauthProviders[cacheKey]; ok {
		s.oauthMu.Unlock()
		return existing, nil
	}
	s.oauthProviders[cacheKey] = provider
	s.oauthMu.Unlock()
	return provider, nil
}

// fetchAuthServerMetadataWithFallback prefers the in-cluster first-party issuer
// for local reachability, but still supports MCP servers configured with an
// external or test authorization server. The metadata issuer is validated
// against the configured issuer on every attempt.
func (s *gatewayServer) fetchAuthServerMetadataWithFallback(ctx context.Context, issuerURL string) (*authServerMetadata, bool, error) {
	internalIssuerURL := strings.TrimSpace(s.oauthInternalIssuerURL)
	if internalIssuerURL == "" || internalIssuerURL == issuerURL {
		metadata, err := s.fetchAuthServerMetadataForIssuer(ctx, issuerURL, issuerURL)
		return metadata, false, err
	}

	if metadata, err := s.fetchAuthServerMetadataForIssuer(ctx, internalIssuerURL, issuerURL); err == nil {
		return metadata, true, nil
	} else {
		log.Printf("oauth internal provider lookup failed for %s: %v", internalIssuerURL, err)
	}

	// The internal issuer is only a transport optimization. It must not prevent
	// external providers, including the E2E mock provider, from being used.
	metadata, err := s.fetchAuthServerMetadataForIssuer(ctx, issuerURL, issuerURL)
	return metadata, false, err
}

func (s *gatewayServer) fetchAuthServerMetadataForIssuer(ctx context.Context, lookupIssuerURL, expectedIssuerURL string) (*authServerMetadata, error) {
	var lastErr error
	for _, endpoint := range authServerMetadataCandidates(lookupIssuerURL) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		// mcp-auth rejects discovery on the in-cluster URL without the public
		// Host / forwarded proto (TRUST_PROXY_TLS). When looking up via the
		// internal issuer, present the expected public issuer identity.
		if expectedIssuerURL != "" && lookupIssuerURL != expectedIssuerURL {
			if u, err := url.Parse(expectedIssuerURL); err == nil && u.Host != "" {
				req.Host = u.Host
				req.Header.Set("X-Forwarded-Host", u.Host)
				req.Header.Set("X-Forwarded-Proto", "https")
			}
		}
		resp, err := s.oauthClient().Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("%s returned status %d", endpoint, resp.StatusCode)
			continue
		}
		if readErr != nil {
			lastErr = readErr
			continue
		}
		var metadata authServerMetadata
		if err := json.Unmarshal(body, &metadata); err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(metadata.Issuer) != expectedIssuerURL {
			lastErr = fmt.Errorf("%s issuer %q does not match configured issuer %q", endpoint, metadata.Issuer, expectedIssuerURL)
			continue
		}
		if strings.TrimSpace(metadata.JWKSURI) == "" {
			lastErr = fmt.Errorf("%s missing jwks_uri", endpoint)
			continue
		}
		return &metadata, nil
	}
	if lastErr == nil {
		lastErr = errors.New("authorization server metadata lookup failed")
	}
	return nil, lastErr
}

func rewriteOAuthEndpoint(endpoint, publicIssuer, internalIssuer string) (string, error) {
	publicBase := strings.TrimRight(strings.TrimSpace(publicIssuer), "/")
	internalBase := strings.TrimRight(strings.TrimSpace(internalIssuer), "/")
	if publicBase == "" || internalBase == "" || !strings.HasPrefix(endpoint, publicBase+"/") {
		return "", fmt.Errorf("OAuth endpoint %q is not under issuer %q", endpoint, publicIssuer)
	}
	return internalBase + strings.TrimPrefix(endpoint, publicBase), nil
}

func isOAuthProtectedMetadataPath(value string) bool {
	return value == oauthProtectedPrefix || strings.HasPrefix(value, oauthProtectedPrefix+"/")
}

func oauthTokenHeader(policy *policypkg.Document) string {
	if policy != nil && policy.Auth != nil && strings.TrimSpace(policy.Auth.TokenHeader) != "" {
		return strings.TrimSpace(policy.Auth.TokenHeader)
	}
	return defaultTokenHeader
}

func shouldChallengeOAuth(policy *policypkg.Document, decision policypkg.Decision) bool {
	if !policypkg.PolicyUsesOAuth(policy) {
		return false
	}
	if decision.Status == http.StatusForbidden {
		return false
	}
	if decision.Status != http.StatusUnauthorized {
		return false
	}
	switch decision.Reason {
	case "missing_bearer_token", "invalid_token":
		return true
	default:
		return false
	}
}

func oauthAuthenticateHeader(policy *policypkg.Document, reason, toolName string, decision policypkg.Decision) string {
	values := []string{
		`realm="mcp-runtime"`,
		fmt.Sprintf(`resource_metadata="%s"`, identity.ProtectedResourceMetadataURL(policy.Auth.Audience)),
	}
	if reason == "invalid_token" {
		values = append(values, `error="invalid_token"`)
	}
	if decision.Status == http.StatusForbidden {
		values = append(values, `error="insufficient_scope"`)
		if scopes := oauthRequiredScopes(decision, toolName); len(scopes) > 0 {
			values = append(values, fmt.Sprintf(`scope="%s"`, strings.Join(scopes, " ")))
		}
		values = append(values, fmt.Sprintf(`error_description="%s"`, oauthScopeDescription(decision.Reason)))
	}
	return "Bearer " + strings.Join(values, ", ")
}

func oauthRequiredScopes(decision policypkg.Decision, toolName string) []string {
	switch decision.Reason {
	case "tool_denied", "tool_not_granted":
		if toolName != "" {
			return []string{"mcp:tool:" + oauthScopeToken(toolName)}
		}
	case "trust_too_low":
		if decision.RequiredTrust != "" {
			return []string{"mcp:trust:" + oauthScopeToken(decision.RequiredTrust)}
		}
	case "side_effect_not_allowed":
		if decision.RequiredSideEffect != "" {
			return []string{"mcp:side-effect:" + oauthScopeToken(decision.RequiredSideEffect)}
		}
	case "no_matching_grant", "grant_without_trust":
		return []string{"mcp:access"}
	}
	return nil
}

func oauthScopeDescription(reason string) string {
	switch reason {
	case "tool_denied", "tool_not_granted":
		return "Tool permission required for this operation"
	case "trust_too_low":
		return "Higher trust permission required for this operation"
	case "side_effect_not_allowed":
		return "Side-effect permission required for this operation"
	default:
		return "Permission required for this operation"
	}
}

func oauthScopeToken(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune("!#$%&'()*+-.^_`|~:", r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func stringClaim(claims jwt.MapClaims, key string) string {
	if raw, ok := claims[key]; ok {
		if value, ok := raw.(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extractToken(headerName, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(headerName), "authorization") {
		return serviceutil.ExtractBearer(value)
	}
	if token := serviceutil.ExtractBearer(value); token != "" {
		return token
	}
	return value
}

func authServerMetadataCandidates(issuerURL string) []string {
	issuerURL = strings.TrimSpace(issuerURL)
	if issuerURL == "" {
		return nil
	}

	var candidates []string
	addCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range candidates {
			if existing == value {
				return
			}
		}
		candidates = append(candidates, value)
	}

	parsed, err := url.Parse(issuerURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	issuerPath := strings.Trim(parsed.EscapedPath(), "/")
	base := url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	if issuerPath == "" {
		addCandidate(base.String() + "/.well-known/oauth-authorization-server")
		addCandidate(base.String() + "/.well-known/openid-configuration")
		return candidates
	}
	// RFC 8414 path insertion is required first for issuers with a path,
	// followed by OIDC path insertion, then OIDC path appending. The order is
	// observable with providers that expose more than one discovery document.
	addCandidate(base.String() + "/.well-known/oauth-authorization-server/" + issuerPath)
	addCandidate(base.String() + "/.well-known/openid-configuration/" + issuerPath)
	trimmed := strings.TrimRight(parsed.String(), "/")
	addCandidate(trimmed + "/.well-known/openid-configuration")
	return candidates
}

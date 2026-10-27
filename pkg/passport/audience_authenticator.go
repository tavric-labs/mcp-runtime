package passport

import "net/http"

// AudienceConfig configures a backend service that platform-api fronts.
type AudienceConfig struct {
	Audience       string
	Secret         []byte
	PlatformAPIURL string
	InternalToken  string
	ServiceAPIKeys map[string]struct{}
	AdminAPIKeys   map[string]struct{}
	PublicFallback func(*http.Request) (Principal, bool)
}

// NewAudienceAuthenticator builds the Authenticator for a service other than
// platform-api. Platform-api's ExchangeHandler re-reads the account for every
// request and mints a token that lives at most ExchangeTTL for exactly one
// audience, so the service verifies that token locally and does not call
// platform-api again. User API keys are still resolved remotely on every call,
// because a revoked key must fail immediately.
func NewAudienceAuthenticator(cfg AudienceConfig) Authenticator {
	return Authenticator{
		Secret:         cfg.Secret,
		Audience:       cfg.Audience,
		ServiceAPIKeys: cfg.ServiceAPIKeys,
		AdminAPIKeys:   cfg.AdminAPIKeys,
		UserKeyResolver: &HTTPUserKeyResolver{
			BaseURL: cfg.PlatformAPIURL,
			Token:   cfg.InternalToken,
		},
		PublicFallback: cfg.PublicFallback,
	}
}

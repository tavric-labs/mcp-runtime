package main

import (
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	mcpauth "github.com/Agent-Hellboy/mcp-auth/auth-client/go/mcpauth"

	"mcp-runtime/pkg/events"
	"mcp-runtime/pkg/identity"
	policypkg "mcp-runtime/pkg/policy"
)

type identityContext struct {
	HumanID   policypkg.HumanID
	AgentID   policypkg.AgentID
	TeamID    policypkg.TeamID
	SessionID policypkg.SessionID
	Session   *policypkg.Binding
}

type oauthProvider struct {
	verifier *mcpauth.JWTVerifier
}

type authServerMetadata struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// oauthCredentials is returned only alongside a successful verification.
type oauthCredentials struct {
	Identity identityContext
	Token    string
}

type analyticsEvent struct {
	Envelope     events.Envelope
	TraceContext map[string]string
}

type gatewayServer struct {
	proxy                  http.Handler
	metrics                *gatewayMetrics
	analyticsURL           string
	apiKey                 string
	analyticsToken         *tokenFileSource
	source                 string
	eventType              string
	analyticsQueue         chan analyticsEvent
	stripPrefix            string
	externalBaseURL        *url.URL
	httpClient             *http.Client
	oauthHTTPClient        *http.Client
	oauthInternalIssuerURL string
	policyFile             string
	serverName             string
	serverNamespace        string
	serverUID              string
	clusterName            string
	verifiedSPIFFEHeader   string
	trustedProxySPIFFE     string
	defaultPolicyMode      string
	defaultPolicyDecision  string
	defaultPolicyVersion   string
	analyticsMu            sync.Mutex
	analyticsOnce          sync.Once
	analyticsWG            sync.WaitGroup
	analyticsClosed        bool
	analyticsDropped       atomic.Uint64
	oauthMu                sync.Mutex
	oauthProviders         map[string]*oauthProvider
	policyState            atomic.Pointer[policySnapshot]
	sessionSync            *gatewaySessionSync
	credentialBroker       *credentialBroker
	upstreamURL            *url.URL
	idem                   idempotencyStore
	idemOnce               sync.Once
	sessions               SessionReader
	oauth                  OAuthAuthenticator
	// now overrides the clock for authorization and session validity checks.
	// Configure before serving requests; nil uses the system clock.
	now func() time.Time
}

func (s *gatewayServer) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

const (
	maxRPCBodyBytes      = 1 << 20
	analyticsQueueSize   = 256
	analyticsWorkerCount = 4
	analyticsEmitTimeout = 5 * time.Second
	// defaultVerifiedSPIFFEHeader carries an optional adapter's SPIFFE identity
	// extracted and injected by the TLS-terminating ingress (Traefik). It is
	// trusted only on an ingress-authenticated mTLS hop.
	defaultVerifiedSPIFFEHeader = "X-MCP-Verified-SPIFFE-ID"
	defaultPolicyMode           = policypkg.DefaultMode
	defaultPolicyDecision       = policypkg.DefaultDecision
	defaultPolicyVersion        = policypkg.DefaultVersion
	oauthProtectedPrefix        = "/.well-known/oauth-protected-resource"
	defaultTokenHeader          = identity.AuthTokenHeader
)

// main initializes and starts the MCP gateway service.

// oauthClient returns the client for OAuth discovery and JWKS fetches, which
// must not inherit the analytics TLS trust settings.
func (s *gatewayServer) oauthClient() *http.Client {
	if s.oauthHTTPClient != nil {
		return s.oauthHTTPClient
	}
	return s.httpClient
}

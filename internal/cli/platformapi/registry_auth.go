package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcp-runtime/internal/cli/clitrace"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"mcp-runtime/pkg/registryauth"
)

type RegistryPullCredential struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NewPlatformClientWithAPIKey returns a client for setup-time administration
// that authenticates with a cluster-held service key instead of a CLI login.
// The base URL must be an HTTPS origin without credentials, query, or fragment.
func NewPlatformClientWithAPIKey(baseURL, apiKey string) (*PlatformClient, error) {
	base := NormalizeBaseURL(strings.TrimSpace(baseURL))
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("platform API base URL must be an HTTPS origin")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("platform API key is required")
	}
	return &PlatformClient{
		baseURL:   base,
		token:     strings.TrimSpace(apiKey),
		http:      &http.Client{Timeout: 30 * time.Second, Transport: clitrace.Transport(nil), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		apiPrefix: "/api/v1",
	}, nil
}

func (c *PlatformClient) CheckRegistryAdmin(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/registry/pull-credentials", "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		return registryPreflightStatusError{code: resp.StatusCode}
	}
	return nil
}

// Registry readiness polling. Variables so tests can shorten them.
var (
	registryReadyInterval = 2 * time.Second
	registryReadyTimeout  = 60 * time.Second
)

// registryReadyStreak is the number of consecutive healthy preflights required
// before issuing credentials, so one lucky request during an endpoint swap
// does not pass the gate.
const registryReadyStreak = 3

// WaitRegistryAdminReady polls the registry administration preflight until it
// succeeds registryReadyStreak times in a row. Ingress gateway errors (502,
// 503, 504) and transport failures are transient right after a platform-api
// rollout and restart the streak; any other unexpected status (401, 403, 404)
// fails immediately. Credential creation is deliberately not retried: every
// POST mints a new credential, so an ambiguous failure could orphan one.
// onRetry, when non-nil, is told why the wait continues.
func (c *PlatformClient) WaitRegistryAdminReady(ctx context.Context, onRetry func(reason string)) error {
	ctx, cancel := context.WithTimeout(ctx, registryReadyTimeout)
	defer cancel()
	streak := 0
	for {
		err := c.CheckRegistryAdmin(ctx)
		switch {
		case err == nil:
			streak++
			if streak >= registryReadyStreak {
				return nil
			}
		case !transientRegistryGatewayError(err):
			return err
		default:
			streak = 0
			if onRetry != nil {
				onRetry(err.Error())
			}
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return err
			}
			return ctx.Err()
		case <-time.After(registryReadyInterval):
		}
	}
}

func transientRegistryGatewayError(err error) bool {
	var status registryPreflightStatusError
	if errors.As(err, &status) {
		return status.code == http.StatusBadGateway || status.code == http.StatusServiceUnavailable || status.code == http.StatusGatewayTimeout
	}
	// Transport-level failure (connection reset or refused, timeout, EOF).
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}

type registryPreflightStatusError struct{ code int }

func (e registryPreflightStatusError) Error() string {
	return fmt.Sprintf("registry administration preflight returned HTTP %d; install the registry-auth API and log in as administrator", e.code)
}

func (c *PlatformClient) CreateRegistryPullCredential(ctx context.Context, scope registryauth.PullScope) (RegistryPullCredential, error) {
	body, err := json.Marshal(scope)
	if err != nil {
		return RegistryPullCredential{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/registry/pull-credentials", "", bytes.NewReader(body))
	if err != nil {
		return RegistryPullCredential{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return RegistryPullCredential{}, fmt.Errorf("create scoped registry pull credential: HTTP %d", resp.StatusCode)
	}
	var record RegistryPullCredential
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return record, fmt.Errorf("invalid registry credential response")
	}
	if record.ID == "" || record.Username == "" || record.Password == "" {
		return record, fmt.Errorf("incomplete registry credential response")
	}
	return record, nil
}

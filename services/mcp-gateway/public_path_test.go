package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

type publicPathUpstream struct {
	mu      sync.Mutex
	calls   int
	method  string
	path    string
	body    string
	headers http.Header
}

func (u *publicPathUpstream) handler(delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.calls++
		u.method, u.path, u.body, u.headers = r.Method, r.URL.Path, string(body), r.Header.Clone()
		u.mu.Unlock()
		time.Sleep(delay)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"asset_id":"abc"}`)
	}
}

func (u *publicPathUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// publicPathGateway builds a gateway for an OAuth server that exposes one public
// upload prefix. The authenticated pipeline's own upstream must never be hit.
func publicPathGateway(t *testing.T, rule policypkg.PublicPath, delay time.Duration) (*gatewayServer, *publicPathUpstream) {
	t.Helper()
	policy := oauthPolicy("https://issuer.example.com")
	policy.Auth.PublicPaths = []policypkg.PublicPath{rule}
	server := newTestGatewayServer(t, policy, func(http.ResponseWriter, *http.Request) {
		t.Error("the authenticated upstream must not receive public-path traffic")
	})
	recorder := &publicPathUpstream{}
	upstream := httptest.NewServer(recorder.handler(delay))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	server.uploadProxy = newPublicUploadProxy(target)
	return server, recorder
}

func publicRule() policypkg.PublicPath {
	return policypkg.PublicPath{PathPrefix: "/mcp/media/u/", MaxBodyBytes: 1 << 20, MaxDurationSeconds: 30}
}

func TestPublicPathForwardsWithoutBearerAndStripsCredentials(t *testing.T) {
	server, upstream := publicPathGateway(t, publicRule(), 0)
	server.stripPrefix = "/youtube"
	server.verifiedSPIFFEHeader = "X-Verified-Spiffe"

	request := httptest.NewRequest(http.MethodPost, "/youtube/mcp/media/u/tok123", strings.NewReader("video-bytes"))
	request.Header.Set("Content-Type", "video/mp4")
	request.Header.Set("Authorization", "Bearer should-not-reach-upstream")
	request.Header.Set("X-Verified-Spiffe", "spiffe://forged")
	recorder := httptest.NewRecorder()
	server.handleGateway(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201 from the upstream", recorder.Code, recorder.Body.String())
	}
	if upstream.callCount() != 1 || upstream.method != http.MethodPost || upstream.path != "/mcp/media/u/tok123" || upstream.body != "video-bytes" {
		t.Fatalf("upstream saw %d calls: %s %s %q", upstream.callCount(), upstream.method, upstream.path, upstream.body)
	}
	if upstream.headers.Get("Authorization") != "" {
		t.Error("Authorization reached the upstream on a public path")
	}
	if upstream.headers.Get("X-Verified-Spiffe") != "" {
		t.Error("the verified-identity header reached the upstream on a public path")
	}
	if upstream.headers.Get("Content-Type") != "video/mp4" {
		t.Errorf("Content-Type = %q, want it forwarded", upstream.headers.Get("Content-Type"))
	}
}

func TestPublicPathLeavesEverythingElseAuthenticated(t *testing.T) {
	server, upstream := publicPathGateway(t, publicRule(), 0)
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"the MCP endpoint", http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"a GET under the prefix", http.MethodGet, "/mcp/media/u/tok123", ""},
		{"a sibling path", http.MethodPost, "/mcp/media/other", "x"},
		{"a dot-dot escape", http.MethodPost, "/mcp/media/u/../../mcp", "x"},
		{"the bare prefix", http.MethodPost, "/mcp/media/u/", "x"},
	} {
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		recorder := httptest.NewRecorder()
		server.handleGateway(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 without a bearer", tc.name, recorder.Code)
		}
	}
	if upstream.callCount() != 0 {
		t.Errorf("public upstream was called %d times for non-public requests", upstream.callCount())
	}
}

func TestPublicPathBoundsTheBody(t *testing.T) {
	server, upstream := publicPathGateway(t, publicRule(), 0)

	tooBig := httptest.NewRequest(http.MethodPost, "/mcp/media/u/tok123", strings.NewReader(strings.Repeat("a", (1<<20)+1)))
	recorder := httptest.NewRecorder()
	server.handleGateway(recorder, tooBig)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status = %d, want 413", recorder.Code)
	}

	unknownLength := httptest.NewRequest(http.MethodPost, "/mcp/media/u/tok123", strings.NewReader("abc"))
	unknownLength.ContentLength = -1
	recorder = httptest.NewRecorder()
	server.handleGateway(recorder, unknownLength)
	if recorder.Code != http.StatusLengthRequired {
		t.Errorf("unknown length: status = %d, want 411", recorder.Code)
	}

	if upstream.callCount() != 0 {
		t.Errorf("rejected uploads still reached the upstream %d times", upstream.callCount())
	}
}

// A request must not run longer than its rule allows, and must not be cut off
// by the 30s unary upstream timeout that governs ordinary tool calls.
func TestPublicPathEnforcesItsOwnDeadline(t *testing.T) {
	rule := publicRule()
	rule.MaxDurationSeconds = 1
	server, _ := publicPathGateway(t, rule, 3*time.Second)

	started := time.Now()
	recorder := httptest.NewRecorder()
	server.handleGateway(recorder, httptest.NewRequest(http.MethodPost, "/mcp/media/u/tok123", strings.NewReader("abc")))
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 once the rule's duration passes", recorder.Code)
	}
	if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
		t.Fatalf("took %s, want the request cut off near 1s", elapsed)
	}
}

func TestPublicUploadTransportHasNoShortResponseTimeout(t *testing.T) {
	transport, ok := newPublicUploadTransport().(*http.Transport)
	if !ok {
		t.Fatalf("newPublicUploadTransport() is %T, want a plain *http.Transport with no unary timeout or retry wrapper", newPublicUploadTransport())
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %s, want 0 (the per-rule deadline governs)", transport.ResponseHeaderTimeout)
	}
}

// The path carries the caller's single-use secret, so it must not appear in the
// audit record, and every public-path request is audited even without RPC.
func TestPublicPathAuditRedactsSecretAndAlwaysEmits(t *testing.T) {
	server, _ := publicPathGateway(t, publicRule(), 0)
	policy, _ := server.currentPolicy()
	request := httptest.NewRequest(http.MethodPost, "/mcp/media/u/SECRET-TOKEN-VALUE", strings.NewReader("abc"))
	ex := newExchange(httptest.NewRecorder(), request, "test-policy")
	server.servePublicPath(ex, policy, publicRule(), "/mcp/media/u/SECRET-TOKEN-VALUE")

	payload := server.auditPayload(ex, time.Millisecond)
	if strings.Contains(payload.Path, "SECRET-TOKEN-VALUE") || payload.Path != "/mcp/media/u/{redacted}" {
		t.Errorf("audit path = %q, want the prefix with the secret redacted", payload.Path)
	}
	if payload.Decision != "allow" || payload.Reason != publicPathAllowReason {
		t.Errorf("audit decision = %q/%q, want allow/%s", payload.Decision, payload.Reason, publicPathAllowReason)
	}
	if !ex.PublicPath {
		t.Error("exchange was not marked as a public-path request")
	}
}

// A real server's read timeout (15s in production) must not cut off a slow
// upload that is still inside its rule's own duration. The response writer is
// wrapped by the exchange, so the per-request deadline must reach the server.
func TestPublicPathUploadOutlivesServerReadTimeout(t *testing.T) {
	server, upstream := publicPathGateway(t, publicRule(), 0)
	gateway := httptest.NewUnstartedServer(http.HandlerFunc(server.handleGateway))
	gateway.Config.ReadTimeout = 300 * time.Millisecond
	gateway.Start()
	t.Cleanup(gateway.Close)

	reader, writer := io.Pipe()
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(200 * time.Millisecond)
			_, _ = writer.Write([]byte("abcd"))
		}
		_ = writer.Close()
	}()
	request, err := http.NewRequest(http.MethodPost, gateway.URL+"/mcp/media/u/tok123", reader)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = 20
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("slow upload failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: the server read timeout cut off an upload within its rule's duration", response.StatusCode)
	}
	if upstream.body != "abcdabcdabcdabcdabcd" {
		t.Fatalf("upstream body = %q, want the full upload", upstream.body)
	}
}

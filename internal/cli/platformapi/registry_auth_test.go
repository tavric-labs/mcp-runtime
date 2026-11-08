package platformapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewPlatformClientWithAPIKeyRequiresHTTPSOrigin(t *testing.T) {
	for _, base := range []string{"", "http://platform.example.com", "https://user:pass@platform.example.com", "https://platform.example.com?x=1", "platform.example.com"} {
		if _, err := NewPlatformClientWithAPIKey(base, "admin"); err == nil {
			t.Fatalf("base %q must be rejected", base)
		}
	}
	if _, err := NewPlatformClientWithAPIKey("https://platform.example.com", " "); err == nil {
		t.Fatal("empty API key must be rejected")
	}
}

func TestNewPlatformClientWithAPIKeySendsKeyToRegistryAdminEndpoint(t *testing.T) {
	client, err := NewPlatformClientWithAPIKey("https://platform.example.com/api/v1/", "admin-key")
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://platform.example.com/api/v1/registry/pull-credentials" {
			t.Fatalf("url = %s", r.URL)
		}
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Fatal("administrator key header missing")
		}
		return &http.Response{StatusCode: http.StatusMethodNotAllowed, Body: http.NoBody, Header: http.Header{}}, nil
	})
	if err := client.CheckRegistryAdmin(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody, Header: http.Header{}}, nil
	})
	if err := client.CheckRegistryAdmin(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("non-admin key must fail preflight, got %v", err)
	}
}

type scriptedReadiness struct {
	statuses []int
	err      error
	calls    int
	posts    int
}

func (s *scriptedReadiness) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		s.posts++
	}
	i := s.calls
	s.calls++
	if s.err != nil && i == 0 {
		return nil, s.err
	}
	status := s.statuses[len(s.statuses)-1]
	if i < len(s.statuses) {
		status = s.statuses[i]
	}
	return &http.Response{StatusCode: status, Body: http.NoBody, Header: http.Header{}}, nil
}

func readinessClient(t *testing.T, rt http.RoundTripper) *PlatformClient {
	t.Helper()
	client, err := NewPlatformClientWithAPIKey("https://platform.example.com", "admin-key")
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = rt
	prevInterval, prevTimeout := registryReadyInterval, registryReadyTimeout
	registryReadyInterval, registryReadyTimeout = time.Millisecond, 2*time.Second
	t.Cleanup(func() { registryReadyInterval, registryReadyTimeout = prevInterval, prevTimeout })
	return client
}

func TestWaitRegistryAdminReadyRidesThroughGatewayErrors(t *testing.T) {
	rt := &scriptedReadiness{statuses: []int{0, 502, 503, 405, 504, 405, 405, 405}, err: errors.New("connection reset by peer")}
	retries := 0
	if err := readinessClient(t, rt).WaitRegistryAdminReady(context.Background(), func(string) { retries++ }); err != nil {
		t.Fatal(err)
	}
	// reset, 502, 503 and 504 each restart the consecutive-success count.
	if rt.calls != 8 || retries != 4 {
		t.Fatalf("calls=%d retries=%d, want 8 and 4", rt.calls, retries)
	}
	if rt.posts != 0 {
		t.Fatal("readiness must never POST")
	}
}

func TestWaitRegistryAdminReadyDoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		rt := &scriptedReadiness{statuses: []int{status}}
		err := readinessClient(t, rt).WaitRegistryAdminReady(context.Background(), nil)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) || rt.calls != 1 {
			t.Fatalf("status %d: err=%v calls=%d, want one failing call", status, err, rt.calls)
		}
	}
}

func TestWaitRegistryAdminReadyReportsLastErrorWhenGatewayStaysDown(t *testing.T) {
	rt := &scriptedReadiness{statuses: []int{502}}
	client := readinessClient(t, rt)
	registryReadyTimeout = 50 * time.Millisecond
	err := client.WaitRegistryAdminReady(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v, want the final HTTP 502", err)
	}
}

func TestWaitRegistryAdminReadyStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt := &scriptedReadiness{statuses: []int{502}}
	if err := readinessClient(t, rt).WaitRegistryAdminReady(ctx, nil); err == nil {
		t.Fatal("cancelled context must fail")
	}
}

package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"mcp-runtime/internal/cli/platformapi"
)

func TestExplicitRequestedTrustReachesAdapterSession(t *testing.T) {
	for _, level := range []string{"low", "medium", "high"} {
		t.Run(level, func(t *testing.T) {
			var f platformSessionFlags
			cmd := &cobra.Command{}
			bindPlatformSessionFlags(cmd, &f)
			if err := cmd.ParseFlags([]string{"--server", "demo", "--namespace", "mcp-team-acme", "--agent", "agt_test", "--requested-trust", level}); err != nil {
				t.Fatal(err)
			}
			var calls int32
			_, client := fakeMTLSServer(t, time.Now().Add(time.Hour), &calls)
			credential, err := issueAdapterCredential(context.Background(), client, f, "")
			if err != nil {
				t.Fatal(err)
			}
			if credential.Session.ConsentedTrust != level {
				t.Fatalf("requested %s was not sent to the session API: got %q", level, credential.Session.ConsentedTrust)
			}
			if atomic.LoadInt32(&calls) != 1 {
				t.Fatal("certificate was not issued")
			}
		})
	}
}

func TestInvalidRequestedTrustFailsBeforePlatformRequests(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer s.Close()
	t.Setenv("MCP_PLATFORM_API_URL", s.URL)
	t.Setenv("MCP_PLATFORM_API_TOKEN", "test-token")
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		t.Fatal(err)
	}
	_, err = issueAdapterCredential(context.Background(), client, platformSessionFlags{server: "demo", agent: "agt_test", requestedTrust: "unbounded"}, "")
	if err == nil || !strings.Contains(err.Error(), "--requested-trust") {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid trust reached the platform")
	}
}

func TestRequestedTrustEnvironmentAndFlagPrecedence(t *testing.T) {
	t.Setenv(EnvAdapterRequestedTrust, "medium")
	var f platformSessionFlags
	cmd := &cobra.Command{}
	bindPlatformSessionFlags(cmd, &f)
	if f.requestedTrust != "medium" {
		t.Fatal("environment trust was not selected")
	}
	if err := cmd.ParseFlags([]string{"--requested-trust", "high"}); err != nil {
		t.Fatal(err)
	}
	if f.requestedTrust != "high" {
		t.Fatal("explicit flag did not override the environment")
	}
}

package audit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewGrantWaitsForPolicyButNotInvalidIdentity(t *testing.T) {
	for _, tc := range []struct {
		reason string
		ok     bool
	}{
		{"tool_not_granted", true},
		{"no_matching_grant", true},
		{"session_expired", false},
		{"trust_too_low", false},
		{"tool_denied", false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(403)
					fmt.Fprintf(w, `{"error":%q}`, tc.reason)
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := waitForGrantedRead(ctx, s.URL); (err == nil) != tc.ok {
				t.Fatalf("err=%v, want success=%v", err, tc.ok)
			}
			if !tc.ok && calls.Load() != 1 {
				t.Fatal("retried a permanent identity/policy failure")
			}
		})
	}
}

func TestAGrantThatNeverAppearsStillFails(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":"no_matching_grant"}`)
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := waitForGrantedRead(ctx, s.URL); err == nil {
		t.Fatal("a grant that never reached the gateway passed the acceptance wait")
	}
	if calls.Load() < 2 {
		t.Fatalf("calls = %d, want the probe retried while waiting", calls.Load())
	}
}

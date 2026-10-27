package httpx

import (
	"context"
	"testing"
)

func TestRequestIDContextKeepsOnlyWellFormedIDs(t *testing.T) {
	ctx := ContextWithRequestID(context.Background(), "client-req_42.a")
	if got := RequestID(ctx); got != "client-req_42.a" {
		t.Fatalf("RequestID() = %q, want client-req_42.a", got)
	}
	if got := RequestID(ContextWithRequestID(context.Background(), "bad id")); got != "" {
		t.Fatalf("RequestID() = %q, want empty for an unsafe id", got)
	}
	if got := RequestID(context.Background()); got != "" {
		t.Fatalf("RequestID() = %q, want empty when no id was stored", got)
	}
}

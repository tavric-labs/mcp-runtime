package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminOperationsFilterAcceptsTraceID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/operations?trace_id=4bf92f3577b34da6a3ce929d0e0e4736", nil)
	filter, err := adminOperationsFilterFromRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if filter.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q", filter.TraceID)
	}
}

func TestAdminOperationsFilterRejectsMalformedTraceID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/operations?trace_id=not-a-trace%27%20OR%201=1", nil)
	if _, err := adminOperationsFilterFromRequest(req); err == nil {
		t.Fatal("expected invalid trace id error")
	}
}

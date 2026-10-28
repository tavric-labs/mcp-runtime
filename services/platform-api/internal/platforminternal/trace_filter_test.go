package platforminternal

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOperationsFilterFromRequestTraceID(t *testing.T) {
	good := httptest.NewRequest(http.MethodGet, "/internal/operations/snapshot?trace_id=4bf92f3577b34da6a3ce929d0e0e4736", nil)
	filter, err := operationsFilterFromRequest(good)
	if err != nil || filter.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("filter = %+v, err = %v", filter, err)
	}
	bad := httptest.NewRequest(http.MethodGet, "/internal/operations/snapshot?trace_id=xyz", nil)
	if _, err := operationsFilterFromRequest(bad); err == nil {
		t.Fatal("expected invalid trace id error")
	}
}

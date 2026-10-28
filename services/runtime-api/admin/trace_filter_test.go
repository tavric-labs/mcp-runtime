package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOperationsFilterAcceptsAndRejectsTraceID(t *testing.T) {
	good := httptest.NewRequest(http.MethodGet, "/admin/operations?trace_id=4bf92f3577b34da6a3ce929d0e0e4736", nil)
	filter, err := adminOperationsFilterFromRequest(good)
	if err != nil || filter.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("filter = %+v, err = %v", filter, err)
	}
	bad := httptest.NewRequest(http.MethodGet, "/admin/operations?trace_id=xyz", nil)
	if _, err := adminOperationsFilterFromRequest(bad); err == nil {
		t.Fatal("expected invalid trace id error")
	}
}

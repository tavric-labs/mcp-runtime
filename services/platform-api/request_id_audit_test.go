package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"mcp-platform-api/internal/platformstore"
	"mcp-runtime/pkg/svcboot"
)

// Creating a key must store the caller's request id on the audit row. The id
// arrives as a header; the handler does not copy it onto the event itself.
func TestCreateUserAPIKeyAuditsTheRequestID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "api_key_create", sqlmock.AnyArg(), sqlmock.AnyArg(), "success", "", sqlmock.AnyArg(), "client-req_42.a", sqlmock.AnyArg(), sqlmock.AnyArg(), "", "", "", "", "", "").
		WillReturnResult(sqlmock.NewResult(1, 1))

	srv := userKeyServer()
	srv.platform = platformstore.NewStoreWithDB(db)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	handler := svcboot.WithRequestID(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/api-keys", strings.NewReader(`{"name":"ci"}`))
	req.Header.Set("x-api-key", "mcpu_user")
	req.Header.Set("X-Request-Id", "client-req_42.a")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s, want 201", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Request-Id"); got != "client-req_42.a" {
		t.Fatalf("X-Request-Id = %q, want client-req_42.a", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

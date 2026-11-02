package runtimeapi

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mcp-runtime/pkg/topology"
)

// countingReader records how many body bytes the handler pulled off the wire.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func pushForm(t *testing.T, tarBytes int) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("target", "registry.example.com/acme/demo:v1"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("scope", string(topology.ScopeTenant)); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("image_tar", "image.tar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(strings.Repeat("x", tarBytes))); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, w.FormDataContentType()
}

func TestReadRegistryPushRequestRejectsBeforeSpoolingTar(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(registryPushTempDirEnv, tmpDir)

	const tarBytes = 1 << 20
	body, contentType := pushForm(t, tarBytes)
	counted := &countingReader{r: body}
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/registry/push", counted)
	req.Header.Set("Content-Type", contentType)

	var gotTarget string
	var gotScope topology.Scope
	_, err := readRegistryPushRequestAuthorized(req, time.Minute, func(target string, scope topology.Scope) error {
		gotTarget, gotScope = target, scope
		return &registryPushRequestError{status: http.StatusForbidden, message: "nope"}
	})

	var reqErr *registryPushRequestError
	if !errors.As(err, &reqErr) || reqErr.status != http.StatusForbidden {
		t.Fatalf("err = %v, want 403 registryPushRequestError", err)
	}
	if gotTarget != "registry.example.com/acme/demo:v1" || gotScope != topology.ScopeTenant {
		t.Fatalf("authorize saw target=%q scope=%q", gotTarget, gotScope)
	}
	if entries, _ := os.ReadDir(tmpDir); len(entries) != 0 {
		t.Fatalf("expected no temp files, found %d", len(entries))
	}
	if counted.n >= tarBytes/2 {
		t.Fatalf("read %d body bytes; the tar must not be consumed after a denial", counted.n)
	}
}

func TestReadRegistryPushRequestSpoolsTarAfterAuthorization(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(registryPushTempDirEnv, tmpDir)

	body, contentType := pushForm(t, 1024)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/registry/push", body)
	req.Header.Set("Content-Type", contentType)

	out, err := readRegistryPushRequestAuthorized(req, time.Minute, func(string, topology.Scope) error { return nil })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	defer os.Remove(out.TarPath)
	if info, err := os.Stat(out.TarPath); err != nil || info.Size() != 1024 {
		t.Fatalf("tar stat = %v, %v", info, err)
	}
}

func TestAdmitRegistryPushDeniesUnauthorizedBeforeTakingSlot(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	lim := newRegistryPushLimiter(1)
	p := principal{Role: roleUser, Teams: []principalTeam{{Slug: "acme", Namespace: "mcp-team-acme", Role: "owner"}}}

	_, err := admitRegistryPush(p, "registry.example.com/beta/demo:v1", topology.ScopeTenant, lim)
	var reqErr *registryPushRequestError
	if !errors.As(err, &reqErr) || reqErr.status != http.StatusForbidden {
		t.Fatalf("err = %v, want 403", err)
	}
	if len(lim) != 0 {
		t.Fatal("a denied push must not hold an upload slot")
	}
}

func TestAdmitRegistryPushCapsConcurrentUploads(t *testing.T) {
	t.Setenv("PLATFORM_MODE", "org")
	lim := newRegistryPushLimiter(1)
	p := principal{Role: roleUser, Teams: []principalTeam{{Slug: "acme", Namespace: "mcp-team-acme", Role: "owner"}}}
	target := "registry.example.com/acme/demo:v1"

	release, err := admitRegistryPush(p, target, topology.ScopeTenant, lim)
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}
	_, err = admitRegistryPush(p, target, topology.ScopeTenant, lim)
	var reqErr *registryPushRequestError
	if !errors.As(err, &reqErr) || reqErr.status != http.StatusTooManyRequests {
		t.Fatalf("second admit err = %v, want 429", err)
	}
	release()
	release() // idempotent
	if release2, err := admitRegistryPush(p, target, topology.ScopeTenant, lim); err != nil {
		t.Fatalf("admit after release: %v", err)
	} else {
		release2()
	}
}

func TestRegistryPushMaxConcurrentFromEnv(t *testing.T) {
	t.Setenv(registryPushMaxConcurrentEnv, "")
	if got := registryPushMaxConcurrent(); got != defaultRegistryPushMaxConcurrent {
		t.Fatalf("default = %d", got)
	}
	t.Setenv(registryPushMaxConcurrentEnv, "2")
	if got := registryPushMaxConcurrent(); got != 2 {
		t.Fatalf("env = %d", got)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		t.Setenv(registryPushMaxConcurrentEnv, bad)
		if got := registryPushMaxConcurrent(); got != defaultRegistryPushMaxConcurrent {
			t.Fatalf("%q = %d, want default", bad, got)
		}
	}
}

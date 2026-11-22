package policy

import (
	"net/http"
	"strings"
	"testing"
)

func publicPathDoc(paths ...PublicPath) *Document {
	return &Document{
		Server: Server{Name: "demo", Namespace: "mcp-servers-org"},
		Auth: &Auth{
			IssuerURL:   "https://issuer.example.com",
			Audience:    "https://resource.example.com/mcp",
			PublicPaths: paths,
		},
		Policy: &Config{Mode: "allow-list", DefaultDecision: "deny"},
	}
}

func goodPublicPath() PublicPath {
	return PublicPath{PathPrefix: "/mcp/media/u/", MaxBodyBytes: 2 << 30, MaxDurationSeconds: 1800}
}

func TestStampRequiresPublicPathsSchemaVersion(t *testing.T) {
	doc := publicPathDoc(goodPublicPath())
	if err := Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if doc.SchemaVersion != SchemaVersionPublicPaths {
		t.Fatalf("SchemaVersion = %q, want %q", doc.SchemaVersion, SchemaVersionPublicPaths)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	// An older gateway would drop public_paths and keep every path behind a
	// bearer: safe, but it must report the mismatch instead of activating.
	doc.SchemaVersion = SchemaVersionCredentialBroker
	revision, err := ComputeRevision(doc)
	if err != nil {
		t.Fatalf("ComputeRevision() error = %v", err)
	}
	doc.Revision = revision
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "requires schema version") {
		t.Fatalf("Validate() error = %v, want schema compatibility rejection", err)
	}
}

func TestStampKeepsOlderSchemaWithoutPublicPaths(t *testing.T) {
	doc := publicPathDoc()
	if err := Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if doc.SchemaVersion == SchemaVersionPublicPaths {
		t.Fatalf("document without public paths was stamped %q", doc.SchemaVersion)
	}
}

func TestValidateRejectsInvalidPublicPaths(t *testing.T) {
	tooMany := make([]PublicPath, 0, MaxPublicPaths+1)
	for i := 0; i <= MaxPublicPaths; i++ {
		tooMany = append(tooMany, PublicPath{PathPrefix: "/mcp/media/" + strings.Repeat("a", i+1) + "/", MaxBodyBytes: 1 << 20, MaxDurationSeconds: 60})
	}
	for _, tc := range []struct {
		name  string
		paths []PublicPath
		want  string
	}{
		{"relative prefix", []PublicPath{{PathPrefix: "mcp/media/", MaxBodyBytes: 1, MaxDurationSeconds: 1}}, "path_prefix"},
		{"missing trailing slash", []PublicPath{{PathPrefix: "/mcp/media", MaxBodyBytes: 1, MaxDurationSeconds: 1}}, "path_prefix"},
		{"root prefix", []PublicPath{{PathPrefix: "/", MaxBodyBytes: 1, MaxDurationSeconds: 1}}, "path_prefix"},
		{"dot segment", []PublicPath{{PathPrefix: "/mcp/../media/", MaxBodyBytes: 1, MaxDurationSeconds: 1}}, "path_prefix"},
		{"double slash", []PublicPath{{PathPrefix: "/mcp//media/", MaxBodyBytes: 1, MaxDurationSeconds: 1}}, "path_prefix"},
		{"query character", []PublicPath{{PathPrefix: "/mcp/media/?x=1/", MaxBodyBytes: 1, MaxDurationSeconds: 1}}, "path_prefix"},
		{"percent escape", []PublicPath{{PathPrefix: "/mcp/%2e%2e/", MaxBodyBytes: 1, MaxDurationSeconds: 1}}, "path_prefix"},
		{"zero body limit", []PublicPath{{PathPrefix: "/mcp/media/u/", MaxBodyBytes: 0, MaxDurationSeconds: 60}}, "max_body_bytes"},
		{"body limit too large", []PublicPath{{PathPrefix: "/mcp/media/u/", MaxBodyBytes: MaxPublicPathBodyBytes + 1, MaxDurationSeconds: 60}}, "max_body_bytes"},
		{"zero duration", []PublicPath{{PathPrefix: "/mcp/media/u/", MaxBodyBytes: 1, MaxDurationSeconds: 0}}, "max_duration_seconds"},
		{"duration too long", []PublicPath{{PathPrefix: "/mcp/media/u/", MaxBodyBytes: 1, MaxDurationSeconds: MaxPublicPathDurationSeconds + 1}}, "max_duration_seconds"},
		{"duplicate", []PublicPath{goodPublicPath(), goodPublicPath()}, "duplicate"},
		{"overlap", []PublicPath{goodPublicPath(), {PathPrefix: "/mcp/media/u/x/", MaxBodyBytes: 1, MaxDurationSeconds: 1}}, "overlap"},
		{"too many", tooMany, "at most"},
	} {
		doc := publicPathDoc(tc.paths...)
		if err := Stamp(doc, ""); err != nil {
			t.Fatalf("%s: Stamp() error = %v", tc.name, err)
		}
		err := Validate(doc)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Validate() error = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestValidateRejectsPublicPathsWithoutOAuth(t *testing.T) {
	doc := &Document{
		Server: Server{Name: "demo"},
		Auth:   &Auth{Mode: "header", Headers: []string{"X-Api-Key"}, PublicPaths: []PublicPath{goodPublicPath()}},
	}
	if err := Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if err := Validate(doc); err == nil {
		t.Fatal("Validate() accepted public paths on a header-mode server")
	}
}

func TestMatchPublicPath(t *testing.T) {
	doc := publicPathDoc(goodPublicPath())
	for _, tc := range []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"matching upload", http.MethodPost, "/mcp/media/u/3f2a9c", true},
		{"nested token", http.MethodPost, "/mcp/media/u/abc/def", true},
		{"prefix itself has no token", http.MethodPost, "/mcp/media/u/", false},
		{"sibling of the prefix", http.MethodPost, "/mcp/media/other", false},
		{"the MCP endpoint", http.MethodPost, "/mcp", false},
		{"wrong method GET", http.MethodGet, "/mcp/media/u/3f2a9c", false},
		{"wrong method PUT", http.MethodPut, "/mcp/media/u/3f2a9c", false},
		{"dot-dot escape", http.MethodPost, "/mcp/media/u/../../mcp", false},
		{"encoded-looking dot-dot", http.MethodPost, "/mcp/media/u/..", false},
		{"double slash", http.MethodPost, "/mcp/media//u/abc", false},
		{"prefix without slash boundary", http.MethodPost, "/mcp/media/uabc", false},
		{"case differs", http.MethodPost, "/mcp/media/U/abc", false},
		{"empty path", http.MethodPost, "", false},
	} {
		_, ok := MatchPublicPath(doc, tc.method, tc.path)
		if ok != tc.want {
			t.Errorf("%s: MatchPublicPath(%s %q) = %v, want %v", tc.name, tc.method, tc.path, ok, tc.want)
		}
	}
	if got, ok := MatchPublicPath(doc, http.MethodPost, "/mcp/media/u/x"); !ok || got.MaxBodyBytes != 2<<30 || got.MaxDurationSeconds != 1800 {
		t.Errorf("MatchPublicPath returned %+v, %v", got, ok)
	}
	if _, ok := MatchPublicPath(nil, http.MethodPost, "/mcp/media/u/x"); ok {
		t.Error("nil document matched")
	}
	if _, ok := MatchPublicPath(&Document{}, http.MethodPost, "/mcp/media/u/x"); ok {
		t.Error("document without auth matched")
	}
}

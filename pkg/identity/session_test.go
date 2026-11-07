package identity

import "testing"

func TestSessionSPIFFEID(t *testing.T) {
	t.Parallel()
	got, err := SessionSPIFFEID("example.org", "team-a", "session-1")
	if err != nil {
		t.Fatalf("SessionSPIFFEID() error = %v", err)
	}
	want := "spiffe://example.org/ns/team-a/session/session-1"
	if got != want {
		t.Fatalf("SessionSPIFFEID() = %q, want %q", got, want)
	}
	// Round-trips back to the original components.
	ns, sess, ok := ParseSessionSPIFFE(got, "example.org")
	if !ok || ns != "team-a" || sess != "session-1" {
		t.Fatalf("round-trip = (%q, %q, %v), want (team-a, session-1, true)", ns, sess, ok)
	}
}

func TestSessionSPIFFEIDTrimsWhitespace(t *testing.T) {
	t.Parallel()
	got, err := SessionSPIFFEID(" example.org ", " team-a ", " session-1 ")
	if err != nil {
		t.Fatalf("SessionSPIFFEID() error = %v", err)
	}
	if want := "spiffe://example.org/ns/team-a/session/session-1"; got != want {
		t.Fatalf("SessionSPIFFEID() = %q, want %q", got, want)
	}
}

// SPIFFE path segments allow only letters, digits, '.', '-' and '_'. Identity
// components outside that set are refused instead of being percent-escaped, so
// every ID we mint is one a standard SPIFFE parser accepts.
func TestSessionSPIFFEIDRejectsInvalidComponents(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, trust, namespace, session string
	}{
		{"empty trust domain", "", "team-a", "session-1"},
		{"uppercase trust domain", "Example.org", "team-a", "session-1"},
		{"trust domain with path", "example.org/evil", "team-a", "session-1"},
		{"empty namespace", "example.org", "", "session-1"},
		{"empty session", "example.org", "team-a", ""},
		{"slash in namespace", "example.org", "team/a", "session-1"},
		{"hash in session", "example.org", "team-a", "session#1"},
		{"percent in session", "example.org", "team-a", "session%2F1"},
		{"dot segment namespace", "example.org", "..", "session-1"},
		{"dot segment session", "example.org", "team-a", "."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, err := SessionSPIFFEID(tc.trust, tc.namespace, tc.session); err == nil {
				t.Fatalf("SessionSPIFFEID(%q, %q, %q) = %q, want error", tc.trust, tc.namespace, tc.session, got)
			}
		})
	}
}

func TestParseSessionSPIFFE(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		raw      string
		trust    string
		wantNS   string
		wantSess string
		wantOK   bool
	}{
		{
			name:     "valid",
			raw:      "spiffe://example.org/ns/team-a/session/session-1",
			trust:    "example.org",
			wantNS:   "team-a",
			wantSess: "session-1",
			wantOK:   true,
		},
		{
			name:     "valid with dots and underscores",
			raw:      "spiffe://example.org/ns/team_a.v2/session/s_1.x",
			trust:    "example.org",
			wantNS:   "team_a.v2",
			wantSess: "s_1.x",
			wantOK:   true,
		},
		{
			name:     "trust domain argument is trimmed",
			raw:      "spiffe://example.org/ns/team-a/session/session-1",
			trust:    " example.org ",
			wantNS:   "team-a",
			wantSess: "session-1",
			wantOK:   true,
		},
		{name: "percent escapes rejected", raw: "spiffe://example.org/ns/team%2Fa/session/session%231", trust: "example.org"},
		{name: "rejected with query", raw: "spiffe://example.org/ns/team-a/session/session-1?foo=bar", trust: "example.org"},
		{name: "rejected with fragment", raw: "spiffe://example.org/ns/team-a/session/session-1#baz", trust: "example.org"},
		{name: "rejected with trailing slash", raw: "spiffe://example.org/ns/team-a/session/session-1/", trust: "example.org"},
		{name: "rejected with empty segment", raw: "spiffe://example.org/ns//session/session-1", trust: "example.org"},
		{name: "rejected with dot segment", raw: "spiffe://example.org/ns/../session/session-1", trust: "example.org"},
		{name: "rejected with port", raw: "spiffe://example.org:8443/ns/team-a/session/session-1", trust: "example.org"},
		{name: "rejected with userinfo", raw: "spiffe://user@example.org/ns/team-a/session/session-1", trust: "example.org"},
		{name: "wrong scheme", raw: "https://example.org/ns/team-a/session/session-1", trust: "example.org"},
		{name: "wrong trust domain", raw: "spiffe://attacker.org/ns/team-a/session/session-1", trust: "example.org"},
		{name: "trust domain prefix is not a match", raw: "spiffe://example.org.evil.io/ns/team-a/session/session-1", trust: "example.org"},
		{name: "uppercase trust domain", raw: "spiffe://Example.org/ns/team-a/session/session-1", trust: "example.org"},
		{name: "empty configured trust domain", raw: "spiffe://example.org/ns/team-a/session/session-1", trust: ""},
		{name: "wrong path shape", raw: "spiffe://example.org/user/alice/agent/codex", trust: "example.org"},
		{name: "too many segments", raw: "spiffe://example.org/ns/team-a/session/session-1/extra", trust: "example.org"},
		{name: "empty", raw: "", trust: "example.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ns, sess, ok := ParseSessionSPIFFE(tc.raw, tc.trust)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ns != tc.wantNS || sess != tc.wantSess {
				t.Fatalf("ParseSessionSPIFFE() = (%q, %q), want (%q, %q)", ns, sess, tc.wantNS, tc.wantSess)
			}
		})
	}
}

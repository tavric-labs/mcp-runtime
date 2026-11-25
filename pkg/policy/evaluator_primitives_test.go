package policy

import (
	"net/http"
	"testing"
	"time"
)

func primitivePolicy(grants ...Grant) *Document {
	return &Document{
		Policy: &Config{Mode: "allow-list", DefaultDecision: "deny", PolicyVersion: "v"},
		Grants: grants,
	}
}

var primitiveNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func primitiveRequest(method, target string) Request {
	return Request{Identity: Identity{AgentID: "agent-a"}, RPCMethod: method, Target: target}
}

func TestAuthorizePrimitiveMethods(t *testing.T) {
	t.Parallel()
	open := Grant{Name: "open", AgentID: "agent-a"}
	scoped := Grant{Name: "scoped", AgentID: "agent-a", PrimitiveRules: []PrimitiveRule{
		{Kind: PrimitiveResource, Pattern: "file:///docs/*", Decision: "allow"},
		{Kind: PrimitiveResource, Pattern: "file:///docs/secret.md", Decision: "deny"},
		{Kind: PrimitivePrompt, Pattern: "review", Decision: "allow"},
	}}
	tests := []struct {
		name       string
		grants     []Grant
		method     string
		target     string
		wantAllow  bool
		wantReason string
	}{
		{"no grant denies resource read", nil, "resources/read", "file:///a", false, "no_matching_grant"},
		{"no grant denies prompt get", nil, "prompts/get", "p", false, "no_matching_grant"},
		{"no grant denies skill get", nil, "skills/get", "pdf", false, "no_matching_grant"},
		{"no grant denies listing", nil, "resources/list", "", false, "no_matching_grant"},
		{"grant without rules allows reads", []Grant{open}, "resources/read", "file:///a", true, "allowed"},
		{"grant without rules allows listing", []Grant{open}, "prompts/list", "", true, "allowed"},
		{"prefix rule allows", []Grant{scoped}, "resources/read", "file:///docs/a.md", true, "allowed"},
		{"outside the allow list is denied", []Grant{scoped}, "resources/read", "file:///etc/passwd", false, "primitive_not_granted"},
		{"deny rule wins over prefix allow", []Grant{scoped}, "resources/read", "file:///docs/secret.md", false, "primitive_denied"},
		{"rules for one kind leave others open", []Grant{scoped}, "skills/get", "skill://pdf/SKILL.md", true, "allowed"},
		{"prompt rule allows named prompt", []Grant{scoped}, "prompts/get", "review", true, "allowed"},
		{"prompt rule rejects other prompt", []Grant{scoped}, "prompts/get", "deploy", false, "primitive_not_granted"},
		{"subscribe follows resource rules", []Grant{scoped}, "resources/subscribe", "file:///etc/x", false, "primitive_not_granted"},
		{"directory read follows resource rules", []Grant{scoped}, "resources/directory/read", "file:///etc/", false, "primitive_not_granted"},
		{"missing target fails closed", []Grant{open}, "resources/read", "", false, "primitive_target_missing"},
		{"disabled grant does not count", []Grant{{Name: "off", AgentID: "agent-a", Disabled: true}}, "resources/read", "file:///a", false, "primitive_not_granted"},
		{"expired grant is reported", []Grant{{Name: "old", AgentID: "agent-a", ExpiresAt: "2026-01-01T00:00:00Z"}}, "resources/read", "file:///a", false, "grant_expired"},
		{"tasks need a grant", nil, "tasks/get", "", false, "no_matching_grant"},
		{"tasks allowed with a grant", []Grant{open}, "tasks/update", "", true, "allowed"},
		{"tasks ignore resource rules", []Grant{scoped}, "tasks/cancel", "", true, "allowed"},
		{"events/list needs a grant", nil, "events/list", "", false, "no_matching_grant"},
		{"events/poll allowed with a grant", []Grant{open}, "events/poll", "", true, "allowed"},
		{"events/stream allowed with a grant", []Grant{open}, "events/stream", "", true, "allowed"},
		{"events/subscribe needs a grant", nil, "events/subscribe", "", false, "no_matching_grant"},
		{"events ignore resource rules", []Grant{scoped}, "events/poll", "", true, "allowed"},
		{"other agent's grant does not apply", []Grant{{Name: "other", AgentID: "agent-b"}}, "resources/read", "file:///a", false, "no_matching_grant"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Authorize(primitivePolicy(tc.grants...), primitiveRequest(tc.method, tc.target), primitiveNow)
			if got.Allowed != tc.wantAllow || got.Reason != tc.wantReason {
				t.Fatalf("decision=%+v, want allow=%v reason=%q", got, tc.wantAllow, tc.wantReason)
			}
			if !tc.wantAllow && got.Status != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", got.Status)
			}
		})
	}
}

func TestAuthorizePrimitiveRequiresIdentityAndSession(t *testing.T) {
	t.Parallel()
	p := primitivePolicy(Grant{Name: "open", AgentID: "agent-a"})
	if got := Authorize(p, Request{RPCMethod: "resources/read", Target: "x"}, primitiveNow); got.Allowed || got.Reason != "missing_identity" {
		t.Fatalf("anonymous read: %+v", got)
	}
	p.Session = &Session{Required: true}
	if got := Authorize(p, primitiveRequest("resources/read", "x"), primitiveNow); got.Allowed || got.Reason != "missing_session" {
		t.Fatalf("sessionless read: %+v", got)
	}
	req := primitiveRequest("resources/read", "x")
	req.Identity.SessionID = "s1"
	p.Sessions = []Binding{{Name: "s1", AgentID: "agent-a", Revoked: true}}
	if got := Authorize(p, req, primitiveNow); got.Allowed || got.Reason != "session_revoked" {
		t.Fatalf("revoked session: %+v", got)
	}
	p.Sessions = []Binding{{Name: "s1", AgentID: "agent-a", ExpiresAt: "2026-01-01T00:00:00Z"}}
	if got := Authorize(p, req, primitiveNow); got.Allowed || got.Reason != "session_expired" {
		t.Fatalf("expired session: %+v", got)
	}
	p.Sessions = []Binding{{Name: "s1", AgentID: "agent-a"}}
	if got := Authorize(p, req, primitiveNow); !got.Allowed {
		t.Fatalf("live session: %+v", got)
	}
}

func TestAuthorizePrimitiveObserveAndDefaultAllow(t *testing.T) {
	t.Parallel()
	observe := &Document{Policy: &Config{Mode: "observe", DefaultDecision: "deny"}}
	if got := Authorize(observe, Request{RPCMethod: "resources/read", Target: "x"}, primitiveNow); !got.Allowed {
		t.Fatalf("observe mode must allow: %+v", got)
	}
	open := &Document{Policy: &Config{Mode: "allow-list", DefaultDecision: "allow"}}
	if got := Authorize(open, primitiveRequest("resources/read", "x"), primitiveNow); !got.Allowed {
		t.Fatalf("default-allow policy must allow: %+v", got)
	}
}

func TestAuthorizePrimitiveReportsMatchedGrant(t *testing.T) {
	t.Parallel()
	g := Grant{Name: "scoped", Namespace: "team-a", AgentID: "agent-a", PrimitiveRules: []PrimitiveRule{{Kind: PrimitiveResource, Pattern: "x", Decision: "deny"}}}
	got := Authorize(primitivePolicy(g), primitiveRequest("resources/read", "x"), primitiveNow)
	if got.MatchedGrant != "scoped" || got.MatchedGrantNamespace != "team-a" {
		t.Fatalf("denial lost its grant attribution: %+v", got)
	}
}

func TestPrimitiveRulesRequireNewSchemaVersion(t *testing.T) {
	t.Parallel()
	doc := primitivePolicy(Grant{Name: "g", AgentID: "a", PrimitiveRules: []PrimitiveRule{{Kind: PrimitivePrompt, Pattern: "p", Decision: "allow"}}})
	if got := RequiredSchemaVersion(doc); got != SchemaVersionPrimitiveRules {
		t.Fatalf("schema=%q, want %q", got, SchemaVersionPrimitiveRules)
	}
	if got := RequiredSchemaVersion(primitivePolicy(Grant{Name: "g", AgentID: "a"})); got == SchemaVersionPrimitiveRules {
		t.Fatal("documents without primitive rules must stay readable by older gateways")
	}
}

func TestValidatePrimitiveRules(t *testing.T) {
	t.Parallel()
	for name, rules := range map[string][]PrimitiveRule{
		"bad kind":          {{Kind: "tool", Pattern: "x", Decision: "allow"}},
		"empty pattern":     {{Kind: PrimitiveResource, Pattern: " ", Decision: "allow"}},
		"bad decision":      {{Kind: PrimitiveResource, Pattern: "x", Decision: "maybe"}},
		"duplicate":         {{Kind: PrimitiveResource, Pattern: "x", Decision: "allow"}, {Kind: PrimitiveResource, Pattern: "x", Decision: "deny"}},
		"star not trailing": {{Kind: PrimitiveResource, Pattern: "*", Decision: "allow"}, {Kind: PrimitiveResource, Pattern: "a*b", Decision: "allow"}},
	} {
		if err := validateGrants([]Grant{{Name: "g", AgentID: "a", PrimitiveRules: rules}}); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
	ok := []PrimitiveRule{{Kind: PrimitiveResource, Pattern: "file:///docs/*", Decision: "allow"}, {Kind: PrimitivePrompt, Pattern: "p", Decision: "deny"}}
	if err := validateGrants([]Grant{{Name: "g", AgentID: "a", PrimitiveRules: ok}}); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizeSubscriptionsListenTargets(t *testing.T) {
	t.Parallel()
	scoped := Grant{Name: "scoped", AgentID: "agent-a", PrimitiveRules: []PrimitiveRule{
		{Kind: PrimitiveResource, Pattern: "db://orders/*", Decision: "allow"},
		{Kind: PrimitiveResource, Pattern: "db://orders/private", Decision: "deny"},
	}}
	req := func(targets ...string) Request {
		return Request{Identity: Identity{AgentID: "agent-a"}, RPCMethod: "subscriptions/listen", Targets: targets}
	}
	p := primitivePolicy(scoped)
	for _, tc := range []struct {
		name    string
		targets []string
		allow   bool
		reason  string
	}{
		{"no uris", nil, true, "allowed"},
		{"allowed uri", []string{"db://orders/1"}, true, "allowed"},
		{"one bad uri fails the whole listen", []string{"db://orders/1", "db://hr/1"}, false, "primitive_not_granted"},
		{"deny rule wins", []string{"db://orders/private"}, false, "primitive_denied"},
	} {
		got := Authorize(p, req(tc.targets...), primitiveNow)
		if got.Allowed != tc.allow || got.Reason != tc.reason {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
	if got := Authorize(primitivePolicy(), req("db://orders/1"), primitiveNow); got.Allowed || got.Reason != "no_matching_grant" {
		t.Errorf("no grant: %+v", got)
	}
}

func TestTaskKindIsNotARuleKind(t *testing.T) {
	t.Parallel()
	err := validateGrants([]Grant{{Name: "g", AgentID: "a", PrimitiveRules: []PrimitiveRule{{Kind: "task", Pattern: "x", Decision: "allow"}}}})
	if err == nil {
		t.Fatal("tasks have no per-item rules; kind \"task\" must be rejected")
	}
}

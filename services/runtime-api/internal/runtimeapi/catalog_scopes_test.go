package runtimeapi

import (
	"context"
	"mcp-runtime/pkg/topology"
	"strings"
	"testing"
)

func TestScopeRoutingMapsOrgToItsCatalogInEveryMode(t *testing.T) {
	for _, mode := range []string{"org", "public"} {
		t.Setenv("PLATFORM_MODE", mode)
		for _, role := range []string{roleAdmin, roleUser} {
			p := principal{Role: role, Subject: "publisher", AuthType: "user_api_key"}
			ctx := withPrincipal(context.Background(), p)
			server := &RuntimeServer{}
			wantNS, _ := topology.ScopeCatalogNamespace(topology.ScopeOrg)
			gotNS, err := server.scopedNamespaceForServerApply(ctx, "", topology.ScopeOrg)
			if err != nil || gotNS != wantNS {
				t.Fatalf("mode=%s role=%s deploy namespace=%q err=%v, want %q", mode, role, gotNS, err, wantNS)
			}
			pushNS, _, err := registryPushAuthContext("registry.example.com/org/demo:v1", topology.ScopeOrg, p)
			if err != nil || pushNS != wantNS {
				t.Fatalf("mode=%s role=%s push namespace=%q err=%v, want %q", mode, role, pushNS, err, wantNS)
			}
			if _, err := server.scopedNamespaceForServerApply(ctx, "mcp-servers-public", topology.ScopeOrg); err == nil {
				t.Fatalf("mode=%s role=%s accepted mismatched namespace", mode, role)
			}
			if alias := defaultImageRepositoryScope(wantNS, ""); alias != "org" {
				t.Fatalf("namespace=%s image prefix=%q, want org", wantNS, alias)
			}
		}
	}
}

func TestPublicScopeIsRejectedInEveryMode(t *testing.T) {
	for _, mode := range []string{"org", "public"} {
		t.Setenv("PLATFORM_MODE", mode)
		err := publishScopeEnabledError(topology.Scope("public"))
		if err == nil || !strings.Contains(err.Error(), "enabled scopes: tenant, org") {
			t.Fatalf("mode=%s public scope error = %v, want rejection listing tenant and org", mode, err)
		}
		p := principal{Role: roleAdmin, Subject: "admin"}
		if _, _, err := registryPushAuthContext("registry.example.com/public/demo:v1", topology.Scope("public"), p); err == nil {
			t.Fatalf("mode=%s registry push accepted the removed public scope", mode)
		}
		server := &RuntimeServer{}
		if _, err := server.scopedNamespaceForServerApply(withPrincipal(context.Background(), p), "", topology.Scope("public")); err == nil {
			t.Fatalf("mode=%s server apply accepted the removed public scope", mode)
		}
	}
}

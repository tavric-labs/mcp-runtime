package server

import (
	"testing"

	"mcp-runtime/pkg/blueprint"
)

// server deploy must send only the auth values the user wrote in .mcp
// metadata; the operator derives an unset audience and issuer at reconcile.
func TestMergeDeployMetadataDoesNotDeriveOAuthAudience(t *testing.T) {
	spec := buildDeployServerSpec("buddy", "registry.example.com/acme/buddy", "v1", 1, 8088, 80)
	mergeDeployMetadata(&spec, &blueprint.ServerMetadata{
		Name: "buddy",
		Auth: &blueprint.AuthConfig{},
	})
	if spec.Auth == nil {
		t.Fatal("auth config was not preserved")
	}
	if spec.Auth.Audience != "" || spec.Auth.IssuerURL != "" {
		t.Fatalf("auth = %+v, want audience and issuer unset for reconcile-time derivation", spec.Auth)
	}

	explicit := buildDeployServerSpec("buddy", "registry.example.com/acme/buddy", "v1", 1, 8088, 80)
	mergeDeployMetadata(&explicit, &blueprint.ServerMetadata{
		Name: "buddy",
		Auth: &blueprint.AuthConfig{Audience: "https://mcp.example.com/custom/mcp"},
	})
	if explicit.Auth.Audience != "https://mcp.example.com/custom/mcp" {
		t.Fatalf("explicit audience = %q, want kept", explicit.Auth.Audience)
	}
}

package access

import (
	"testing"
	"time"
)

func TestGrantApplyValidatesPrimitiveRules(t *testing.T) {
	base := func(rules ...PrimitiveRule) GrantApplyRequest {
		return GrantApplyRequest{Name: "g", ServerRef: ServerReference{Name: "docs"}, Subject: SubjectRef{AgentID: "a"}, MaxTrust: "low", AllowedSideEffects: []ToolSideEffect{"read"}, PrimitiveRules: rules}
	}
	now := time.Now()
	if err := ValidateGrantApplyRequest(&[]GrantApplyRequest{base(PrimitiveRule{Kind: " resource ", Pattern: " file:///a/* ", Decision: "allow"})}[0], now); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	for name, rule := range map[string]PrimitiveRule{
		"kind":     {Kind: "tool", Pattern: "x", Decision: "allow"},
		"pattern":  {Kind: "prompt", Pattern: "", Decision: "allow"},
		"star":     {Kind: "prompt", Pattern: "a*b", Decision: "allow"},
		"decision": {Kind: "skill", Pattern: "x", Decision: "maybe"},
	} {
		req := base(rule)
		if err := ValidateGrantApplyRequest(&req, now); err == nil {
			t.Errorf("%s: invalid rule accepted", name)
		}
	}
	dup := base(PrimitiveRule{Kind: "prompt", Pattern: "x", Decision: "allow"}, PrimitiveRule{Kind: "prompt", Pattern: "x", Decision: "deny"})
	if err := ValidateGrantApplyRequest(&dup, now); err == nil {
		t.Error("duplicate rule accepted")
	}
}

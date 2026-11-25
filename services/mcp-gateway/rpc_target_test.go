package main

import "testing"

func TestRPCInspectionExtractsPrimitiveTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, target string
	}{
		{"resource read", `{"id":1,"method":"resources/read","params":{"uri":"file:///docs/a.md"}}`, "file:///docs/a.md"},
		{"resource subscribe", `{"id":1,"method":"resources/subscribe","params":{"uri":"db://orders"}}`, "db://orders"},
		{"resource unsubscribe", `{"id":1,"method":"resources/unsubscribe","params":{"uri":"db://orders"}}`, "db://orders"},
		{"directory read", `{"id":1,"method":"resources/directory/read","params":{"uri":"skill://pdf/"}}`, "skill://pdf/"},
		{"prompt get", `{"id":1,"method":"prompts/get","params":{"name":"review"}}`, "review"},
		{"skill get", `{"id":1,"method":"skills/get","params":{"name":"pdf"}}`, "pdf"},
		{"resource uri wrong type", `{"id":1,"method":"resources/read","params":{"uri":7}}`, ""},
		{"resource without params", `{"id":1,"method":"resources/read"}`, ""},
		{"list has no target", `{"id":1,"method":"resources/list","params":{"cursor":"x"}}`, ""},
		{"tool call uses tool name", `{"id":1,"method":"tools/call","params":{"name":"echo"}}`, ""},
		{"uri on unrelated method ignored", `{"id":1,"method":"ping","params":{"uri":"x"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := inspectRPCBody([]byte(tc.body))
			if got.Target() != tc.target {
				t.Fatalf("target=%q want %q (%+v)", got.Target(), tc.target, got)
			}
		})
	}
	if got := inspectRPCBody([]byte(`{"id":1,"method":"prompts/get","params":{"name":"review"}}`)); got.ToolName() != "review" {
		t.Fatalf("ToolName behavior changed for non-tool methods: %q", got.ToolName())
	}
}

func TestMetricRPCMethodCoversPrimitivesAndExtensions(t *testing.T) {
	t.Parallel()
	for _, m := range []string{"resources/templates/list", "resources/directory/read", "skills/list", "skills/get", "notifications/cancelled", "notifications/progress"} {
		if got := metricRPCMethod(m); got != m {
			t.Errorf("metricRPCMethod(%q)=%q, want it labelled", m, got)
		}
	}
	if got := metricRPCMethod("x/" + "attacker-controlled"); got != "other" {
		t.Fatalf("unknown method must stay bounded, got %q", got)
	}
}

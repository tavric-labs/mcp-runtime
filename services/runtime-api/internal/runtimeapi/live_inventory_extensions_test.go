package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcp-runtime/pkg/controlplane"
)

func probeWithInitializeCapabilities(t *testing.T, capabilities map[string]any) *liveInventory {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("content-type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": liveInventoryProtocolVersion, "capabilities": capabilities,
			}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		}
	}))
	t.Cleanup(fake.Close)
	prober := &mcpLiveInventoryProber{client: fake.Client(), baseURLForServer: func(controlplane.ServerInfo) string { return fake.URL }}
	got, err := prober.probe(context.Background(), controlplane.ServerInfo{Name: "demo", Namespace: "mcp-servers-org"})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLiveInventoryRecordsAdvertisedExtensions(t *testing.T) {
	got := probeWithInitializeCapabilities(t, map[string]any{
		"resources": map[string]any{},
		"extensions": map[string]any{
			"io.modelcontextprotocol/skills": map[string]any{"directoryRead": true},
			"io.modelcontextprotocol/ui":     map[string]any{},
		},
	})
	if len(got.Extensions) != 2 {
		t.Fatalf("extensions = %v", got.Extensions)
	}
	if string(got.Extensions["io.modelcontextprotocol/skills"]) != `{"directoryRead":true}` {
		t.Fatalf("settings not preserved: %s", got.Extensions["io.modelcontextprotocol/skills"])
	}
	encoded, _ := json.Marshal(got)
	var round struct {
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(encoded, &round); err != nil || len(round.Extensions) != 2 {
		t.Fatalf("extensions missing from the API shape: %s (%v)", encoded, err)
	}
}

func TestLiveInventoryOmitsExtensionsWhenNoneAdvertised(t *testing.T) {
	got := probeWithInitializeCapabilities(t, map[string]any{"tools": map[string]any{}})
	if len(got.Extensions) != 0 {
		t.Fatalf("extensions = %v, want none", got.Extensions)
	}
	if encoded, _ := json.Marshal(got); json.Valid(encoded) && contains(string(encoded), `"extensions"`) {
		t.Fatalf("empty extensions must be omitted: %s", encoded)
	}
}

func TestLiveInventoryIgnoresMalformedExtensions(t *testing.T) {
	// A server that advertises a non-object must not break the probe.
	got := probeWithInitializeCapabilities(t, map[string]any{"extensions": "nope"})
	if len(got.Extensions) != 0 {
		t.Fatalf("extensions = %v", got.Extensions)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

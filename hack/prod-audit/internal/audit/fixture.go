package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"strings"
	"time"
)

type FixtureConfig struct {
	Endpoint         string
	Write            bool
	ExpectDeniedTool string
	PublicEndpoint   string
}

func CheckFixture(ctx context.Context, cfg FixtureConfig, out io.Writer) (err error) {
	endpoint, write := cfg.Endpoint, cfg.Write
	c := mcp.NewClient(&mcp.Implementation{Name: "prod-audit-check", Version: "0.1.0"}, nil)
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer cs.Close()
	if cs.InitializeResult().ServerInfo.Name != "prod-audit" {
		return fmt.Errorf("refusing test: endpoint is not prod-audit")
	}
	call := func(name string, args any, wantError bool) (map[string]any, error) {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if r.IsError != wantError {
			return nil, fmt.Errorf("%s: unexpected tool error state", name)
		}
		if len(r.Content) != 1 {
			return nil, fmt.Errorf("%s: missing response", name)
		}
		text, ok := r.Content[0].(*mcp.TextContent)
		if !ok {
			return nil, fmt.Errorf("%s: unexpected response type", name)
		}
		if wantError {
			return map[string]any{"tool_error": text.Text}, nil
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(text.Text), &result); err != nil {
			return nil, err
		}
		return result, nil
	}
	if _, err := cs.ListTools(ctx, nil); err != nil {
		return err
	}
	fmt.Fprintln(out, "PASS tools/list")
	for _, name := range []string{"audit_inspect", "audit_echo", "audit_wait", "audit_fail"} {
		args := map[string]any{}
		if name == "audit_echo" {
			args["message"] = "synthetic-prod-audit-probe"
		}
		if name == "audit_wait" {
			if cfg.ExpectDeniedTool == name {
				continue
			}
			args["milliseconds"] = 10
		}
		result, err := call(name, args, name == "audit_fail")
		if err != nil {
			return err
		}
		switch name {
		case "audit_echo":
			if result["message"] != "synthetic-prod-audit-probe" {
				return fmt.Errorf("echo result did not match the synthetic marker")
			}
		case "audit_inspect":
			if result["server"] != "prod-audit" {
				return fmt.Errorf("fixture inspection returned a different server")
			}
		case "audit_wait":
			if result["completed"] != true {
				return fmt.Errorf("wait did not complete")
			}
		case "audit_fail":
			if !strings.Contains(fmt.Sprint(result["tool_error"]), "deliberate test failure") {
				return fmt.Errorf("deliberate failure returned a different error")
			}
		}
		fmt.Fprintln(out, "PASS", name)
	}
	if cfg.ExpectDeniedTool != "" {
		if cfg.ExpectDeniedTool != "audit_wait" {
			return fmt.Errorf("only audit_wait is supported as the deliberate ungranted tool")
		}
		if err := CheckDeniedTool(ctx, endpoint, cfg.ExpectDeniedTool); err != nil {
			return err
		}
		fmt.Fprintln(out, "PASS ungranted tool policy denial")
	}
	if cfg.PublicEndpoint != "" {
		if err := CheckAnonymousMCP(ctx, cfg.PublicEndpoint); err != nil {
			return err
		}
		fmt.Fprintln(out, "PASS anonymous MCP denial")
	}
	if !write {
		fmt.Fprintln(out, "SKIP temporary writes (enable with --write)")
		return nil
	}
	opened, err := call("audit_run_open", map[string]any{"label": "synthetic-smoke", "ttl_seconds": 60}, false)
	if err != nil {
		return err
	}
	id, ok := opened["run_id"].(string)
	if !ok || id == "" {
		return fmt.Errorf("run identifier missing")
	}
	fmt.Fprintln(out, "PASS audit_run_open")
	closed := false
	defer func() {
		if closed {
			return
		}
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		r, cleanupErr := cs.CallTool(cleanup, &mcp.CallToolParams{Name: "audit_run_close", Arguments: map[string]any{"run_id": id}})
		if cleanupErr != nil || r.IsError {
			fmt.Fprintln(out, "cleanup incomplete; run expires within 60 seconds")
			err = errors.Join(err, fmt.Errorf("fixture run cleanup failed"))
		}
	}()
	for _, step := range []struct {
		name      string
		args      map[string]any
		wantError bool
	}{
		{"audit_record_put", map[string]any{"run_id": id, "key": "probe", "value": "synthetic", "expected_revision": 0}, false},
		{"audit_record_get", map[string]any{"run_id": id, "key": "probe"}, false},
		{"audit_record_put", map[string]any{"run_id": id, "key": "probe", "value": "must-not-overwrite", "expected_revision": 0}, true},
		{"audit_record_delete", map[string]any{"run_id": id, "key": "probe"}, false},
		{"audit_record_get", map[string]any{"run_id": id, "key": "probe"}, true},
		{"audit_run_close", map[string]any{"run_id": id}, false},
	} {
		result, e := call(step.name, step.args, step.wantError)
		if e != nil {
			return e
		}
		if step.wantError {
			want := "not found"
			if step.name == "audit_record_put" {
				want = "revision conflict"
			}
			if !strings.Contains(fmt.Sprint(result["tool_error"]), want) {
				return fmt.Errorf("%s did not return the expected %s error", step.name, want)
			}
		} else {
			switch step.name {
			case "audit_record_put", "audit_record_get":
				if result["value"] != "synthetic" || result["key"] != "probe" || result["revision"] != float64(1) {
					return fmt.Errorf("%s returned incorrect synthetic record data", step.name)
				}
			case "audit_record_delete":
				if result["deleted"] != true {
					return fmt.Errorf("synthetic record deletion did not happen")
				}
			case "audit_run_close":
				if result["closed"] != true {
					return fmt.Errorf("synthetic run cleanup did not happen")
				}
			}
		}
		fmt.Fprintln(out, "PASS", step.name)
	}
	closed = true
	fmt.Fprintln(out, "PASS temporary data cleaned up")
	return nil
}

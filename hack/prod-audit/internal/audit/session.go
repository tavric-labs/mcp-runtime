package audit

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

type cappedLog struct {
	mu   sync.Mutex
	data []byte
}

func (b *cappedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 65536 {
		b.data = b.data[len(b.data)-65536:]
	}
	return len(p), nil
}
func (b *cappedLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}

var agentPattern = regexp.MustCompile(`agt_[a-z0-9]{26}`)
var teamPattern = regexp.MustCompile(`(?m)^Team: .* \(([a-zA-Z0-9-]+)\)$`)
var sessionPattern = regexp.MustCompile(`/session/([a-zA-Z0-9-]+)`)

// grantName names every grant, agent and session this runner creates
// prod-audit-YYYYMMDD-<suffix> (UTC date), so a reader can tell what made it and when.
func grantName(now time.Time, entropy [8]byte) string {
	return fmt.Sprintf("prod-audit-%s-%x", now.UTC().Format("20060102"), entropy)
}

func (r *postRunner) exercise(ctx context.Context, dir string) (err error) {
	var entropy [8]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return err
	}
	name := grantName(time.Now(), entropy)
	agent := ""
	grantCreated := false
	var stopProxy context.CancelFunc
	var proxyDone chan error
	var proxyLog cappedLog
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if stopProxy != nil {
			stopProxy()
			if proxyDone != nil {
				select {
				case <-proxyDone:
				case <-cleanup.Done():
					err = errors.Join(err, fmt.Errorf("adapter cleanup timed out"))
				}
			}
		}
		if match := sessionPattern.FindStringSubmatch(proxyLog.String()); len(match) == 2 {
			if _, e := r.cli(cleanup, "access", "session", "delete", match[1], "--namespace", "mcp-servers-org"); e != nil {
				err = errors.Join(err, fmt.Errorf("temporary session cleanup: %w", e))
			}
		}
		if grantCreated {
			if _, e := r.cli(cleanup, "access", "grant", "delete", name, "--namespace", "mcp-servers-org"); e != nil {
				err = errors.Join(err, fmt.Errorf("temporary grant cleanup: %w", e))
			}
		}
		if agent != "" {
			if _, e := r.cli(cleanup, "agent", "deactivate", agent); e != nil {
				err = errors.Join(err, fmt.Errorf("temporary agent cleanup: %w", e))
			}
		}
	}()
	raw, err := r.cli(ctx, "agent", "create", r.cfg.Team, "--name", name)
	if err != nil {
		return err
	}
	agent = agentPattern.FindString(string(raw))
	if agent == "" {
		return fmt.Errorf("agent create did not return an immutable agent ID")
	}
	raw, err = r.cli(ctx, "agent", "get", agent)
	if err != nil {
		return err
	}
	team := teamPattern.FindStringSubmatch(string(raw))
	if len(team) != 2 {
		return fmt.Errorf("agent team ID is unavailable")
	}
	grantFile := filepath.Join(dir, "grant.yaml")
	args := []string{"access", "grant", "init", name, "--server", "prod-audit", "--namespace", "mcp-servers-org", "--agent-id", agent, "--team-id", team[1], "--expires-in", "30m", "--trust", "high", "--side-effect", "read", "--side-effect", "write", "--side-effect", "destructive", "--output", grantFile}
	for _, rule := range []string{"audit_inspect:allow:low", "audit_echo:allow:low", "audit_record_get:allow:low", "audit_fail:allow:low", "audit_run_open:allow:medium", "audit_record_put:allow:medium", "audit_record_delete:allow:high", "audit_run_close:allow:high"} {
		args = append(args, "--tool-rule", rule)
	}
	if _, err = r.cli(ctx, args...); err != nil {
		return err
	}
	grantCreated = true // A rejected response may follow a partially persisted apply.
	if _, err = r.cli(ctx, "access", "grant", "apply", "--file", grantFile); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := listener.Addr().String()
	_ = listener.Close()
	proxyCtx, cancel := context.WithCancel(ctx)
	stopProxy = cancel
	proxy := exec.CommandContext(proxyCtx, r.cfg.RuntimeCLI, "adapter", "proxy", "--server", "prod-audit", "--namespace", "mcp-servers-org", "--agent", agent, "--requested-trust", "high", "--runtime-url", r.cfg.PublicMCP, "--platform-url", r.cfg.PlatformURL, "--listen", address)
	proxy.Env = r.env
	proxy.Stdout = &proxyLog
	proxy.Stderr = &proxyLog
	if err = proxy.Start(); err != nil {
		return err
	}
	proxyDone = make(chan error, 1)
	go func() { proxyDone <- proxy.Wait() }()
	endpoint := "http://" + address
	if err = poll(ctx, 90*time.Second, func() (bool, error) {
		select {
		case <-proxyDone:
			proxyDone <- fmt.Errorf("adapter exited")
			return false, fmt.Errorf("audit adapter exited before becoming ready")
		default:
		}
		status, _, e := request(ctx, http.MethodGet, endpoint+"/readyz", nil)
		return e == nil && status == http.StatusNoContent, nil
	}); err != nil {
		return err
	}
	// Proxy readiness proves its listener and certificate enrollment, not that
	// the operator-rendered grant has reached the gateway's policy snapshot.
	if err = waitForGrantedRead(ctx, endpoint); err != nil {
		return err
	}
	if err = CheckFixture(ctx, FixtureConfig{Endpoint: endpoint, Write: true, ExpectDeniedTool: "audit_wait", PublicEndpoint: r.cfg.PublicMCP}, r.out); err != nil {
		return err
	}
	if _, err = r.cli(ctx, "access", "grant", "revoke-sessions", name, "--namespace", "mcp-servers-org"); err != nil {
		return err
	}
	if err = poll(ctx, 20*time.Second, func() (bool, error) {
		status, _, e := request(ctx, http.MethodPost, endpoint, []byte(`{"jsonrpc":"2.0","id":89,"method":"tools/call","params":{"name":"audit_echo","arguments":{"message":"synthetic-revocation-probe"}}}`))
		return e == nil && (status == 401 || status == 403), nil
	}); err != nil {
		return fmt.Errorf("revoked session still allowed calls: %w", err)
	}
	fmt.Fprintln(r.out, "PASS session revocation denial")
	return nil
}

func poll(ctx context.Context, limit time.Duration, fn func() (bool, error)) error {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	for {
		ok, err := fn()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return fmt.Errorf("audit check timed out")
		case <-timer.C:
		}
	}
}

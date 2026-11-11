package audit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type PostDeployConfig struct {
	RuntimeCLI, Kubeconfig, KubeContext, ExpectedClusterUID                 string
	ReleaseTag, Manifest, PlatformURL, RegistryURL, Issuer, PublicMCP, Team string
	MCPAuthVersion                                                          string
	ClusterLogin                                                            bool
}

type CheckResult struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Milliseconds int64  `json:"milliseconds"`
	Error        string `json:"error,omitempty"`
}

type Report struct {
	Status   string        `json:"status"`
	Started  time.Time     `json:"started_at"`
	Finished time.Time     `json:"finished_at"`
	Release  string        `json:"release"`
	Target   string        `json:"mcp_target"`
	Checks   []CheckResult `json:"checks"`
}

func (c PostDeployConfig) Validate() error {
	if c.RuntimeCLI == "" || c.Kubeconfig == "" || c.KubeContext == "" || c.ExpectedClusterUID == "" || c.Manifest == "" {
		return fmt.Errorf("runtime CLI, explicit kubeconfig/context/cluster UID and release manifest are required")
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(c.ReleaseTag) {
		return fmt.Errorf("release tag must be a stable vMAJOR.MINOR.PATCH")
	}
	for _, raw := range []string{c.PlatformURL, c.RegistryURL, c.Issuer, c.PublicMCP} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("audit service URLs must be explicit HTTPS URLs without credentials, queries or fragments")
		}
	}
	u, _ := url.Parse(c.PublicMCP)
	if u.Path != "/prod-audit/mcp" {
		return fmt.Errorf("the production MCP test target must be /prod-audit/mcp")
	}
	if c.ClusterLogin && (c.PlatformURL != "https://platform.mcpruntime.org" || c.PublicMCP != "https://mcp.mcpruntime.org/prod-audit/mcp") {
		return fmt.Errorf("cluster-login credentials are restricted to the known Tavric production platform and fixture origins")
	}
	if c.Team == "" {
		return fmt.Errorf("an existing dedicated audit team is required")
	}
	return nil
}

type postRunner struct {
	cfg    PostDeployConfig
	env    []string
	report *Report
	out    io.Writer
}

func (r *postRunner) record(name string, fn func() error) error {
	start := time.Now()
	err := fn()
	result := CheckResult{Name: name, Status: "passed", Milliseconds: time.Since(start).Milliseconds()}
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
	}
	r.report.Checks = append(r.report.Checks, result)
	fmt.Fprintf(r.out, "%s %s\n", strings.ToUpper(result.Status), name)
	return err
}

func (r *postRunner) command(ctx context.Context, program string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, program, args...)
	command.Env = r.env
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	// Output may contain credential JSON. Never put stdout, stderr or command
	// arguments into reports or logs, even when a child exits unsuccessfully.
	if err != nil {
		return nil, fmt.Errorf("%s command failed: %w", filepath.Base(program), err)
	}
	return raw, nil
}

func (r *postRunner) kube(ctx context.Context, args ...string) ([]byte, error) {
	base := []string{"--kubeconfig", r.cfg.Kubeconfig, "--context", r.cfg.KubeContext}
	return r.command(ctx, "kubectl", append(base, args...)...)
}

func (r *postRunner) cli(ctx context.Context, args ...string) ([]byte, error) {
	return r.command(ctx, r.cfg.RuntimeCLI, args...)
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, v := range env {
		if !strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	return append(out, prefix+value)
}

// RunPostDeploy performs the shared post-deployment audit. Credentials remain
// in this runner and its CLI children; the fixture never receives admin keys.
func RunPostDeploy(ctx context.Context, cfg PostDeployConfig, out io.Writer) (report Report, err error) {
	report = Report{Started: time.Now().UTC(), Release: cfg.ReleaseTag, Target: "mcp-servers-org/prod-audit"}
	defer func() {
		report.Finished = time.Now().UTC()
		report.Status = "passed"
		if err != nil {
			report.Status = "failed"
		}
	}()
	if err = cfg.Validate(); err != nil {
		return report, err
	}
	dir, e := os.MkdirTemp("", "prod-audit-postdeploy-")
	if e != nil {
		return report, e
	}
	defer os.RemoveAll(dir)
	env := setEnv(os.Environ(), "KUBECONFIG", cfg.Kubeconfig)
	env = setEnv(env, "MCP_RUNTIME_CONFIG_DIR", filepath.Join(dir, "profile"))
	env = setEnv(env, "MCP_PLATFORM_API_URL", cfg.PlatformURL)
	r := &postRunner{cfg: cfg, env: env, report: &report, out: out}
	if err = r.record("production cluster identity", func() error {
		raw, e := r.kube(ctx, "get", "namespace", "kube-system", "-o", "json")
		if e != nil {
			return e
		}
		var d struct{ Metadata struct{ UID string } }
		if json.Unmarshal(raw, &d) != nil || d.Metadata.UID != cfg.ExpectedClusterUID {
			return fmt.Errorf("selected cluster identity does not match expected UID")
		}
		current, e := r.kube(ctx, "config", "current-context")
		if e != nil {
			return e
		}
		if strings.TrimSpace(string(current)) != cfg.KubeContext {
			return fmt.Errorf("the dedicated kubeconfig's current context must match --context so released CLI checks target the same cluster")
		}
		return nil
	}); err != nil {
		return report, err
	}
	if err = r.record("audit CLI explicit trust support", func() error {
		raw, e := r.cli(ctx, "adapter", "proxy", "--help")
		if e != nil {
			return e
		}
		if !bytes.Contains(raw, []byte("--requested-trust")) {
			return fmt.Errorf("audit write tests require a Runtime CLI with --requested-trust; use the adapter trust fix revision")
		}
		return nil
	}); err != nil {
		return report, err
	}
	if err = r.record("anonymous registry denial", func() error { return CheckRegistry(ctx, strings.TrimRight(cfg.RegistryURL, "/")+"/v2/") }); err != nil {
		return report, err
	}
	var inventory []byte
	if err = r.record("deployment readiness and release images", func() error {
		inventory, e = r.kube(ctx, "get", "deployments", "-A", "-o", "json")
		if e != nil {
			return e
		}
		return checkProductionInventory(inventory, cfg.Manifest, cfg.ReleaseTag, cfg.RegistryURL, cfg.MCPAuthVersion)
	}); err != nil {
		return report, err
	}
	if err = r.record("authorization server metadata", func() error {
		return poll(ctx, 60*time.Second, func() (bool, error) { return CheckAuthEndpoint(ctx, cfg.Issuer) == nil, nil })
	}); err != nil {
		return report, err
	}
	if cfg.ClusterLogin {
		if err = r.record("configured-user login", func() error { return r.login(ctx) }); err != nil {
			return report, err
		}
	} else if os.Getenv("MCP_PLATFORM_API_TOKEN") == "" {
		return report, fmt.Errorf("set a real user MCP_PLATFORM_API_TOKEN or explicitly select --cluster-login; operator keys cannot enroll personal sessions")
	}
	if err = r.record("Runtime cluster doctor", func() error { _, e := r.cli(ctx, "cluster", "doctor"); return e }); err != nil {
		return report, err
	}
	err = r.record("prod-audit managed session, tools, policy and cleanup", func() error { return r.exercise(ctx, dir) })
	return report, err
}

func (r *postRunner) login(ctx context.Context) error {
	raw, err := r.kube(ctx, "-n", "mcp-platform", "get", "secret", "mcp-platform-api-credentials", "-o", "json")
	if err != nil {
		return err
	}
	var secret struct{ Data map[string]string }
	if json.Unmarshal(raw, &secret) != nil {
		return fmt.Errorf("configured login credential structure is invalid")
	}
	email, e := base64.StdEncoding.DecodeString(secret.Data["PLATFORM_ADMIN_EMAIL"])
	if e != nil {
		return fmt.Errorf("configured login email is unavailable")
	}
	password, e := base64.StdEncoding.DecodeString(secret.Data["PLATFORM_ADMIN_PASSWORD"])
	if e != nil {
		return fmt.Errorf("configured login password is unavailable")
	}
	if len(email) == 0 || len(password) == 0 {
		return fmt.Errorf("configured user credentials are empty")
	}
	body, _ := json.Marshal(map[string]string{"email": string(email), "password": string(password)})
	status, response, err := request(ctx, http.MethodPost, strings.TrimRight(r.cfg.PlatformURL, "/")+"/api/v1/auth/login", body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("configured-user login returned HTTP %d", status)
	}
	var login struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(response, &login) != nil || login.AccessToken == "" {
		return fmt.Errorf("login did not return an access token")
	}
	// The credential goes only into the child environment, never argv/files/logs.
	r.env = setEnv(r.env, "MCP_PLATFORM_API_TOKEN", login.AccessToken)
	return nil
}

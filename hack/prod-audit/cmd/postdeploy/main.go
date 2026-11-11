package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"mcp-runtime/hack/prod-audit/internal/audit"
)

func main() {
	var c audit.PostDeployConfig
	flag.StringVar(&c.RuntimeCLI, "runtime-cli", "mcp-runtime", "reviewed release CLI executable")
	flag.StringVar(&c.Kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "explicit production kubeconfig file")
	flag.StringVar(&c.KubeContext, "context", "prod-mcp-runtime", "context within the explicit kubeconfig")
	flag.StringVar(&c.ExpectedClusterUID, "expected-cluster-uid", os.Getenv("MCP_EXPECTED_CLUSTER_UID"), "required production cluster UID")
	flag.StringVar(&c.ReleaseTag, "release", os.Getenv("RELEASE_TAG"), "expected Runtime release")
	flag.StringVar(&c.Manifest, "release-manifest", "", "reviewed release component manifest")
	flag.StringVar(&c.PlatformURL, "platform-url", "https://platform.mcpruntime.org", "platform API base URL")
	flag.StringVar(&c.RegistryURL, "registry-url", "https://registry.mcpruntime.org", "registry base URL")
	flag.StringVar(&c.Issuer, "issuer", "https://auth.mcpruntime.org/mcp-auth", "authorization server issuer")
	flag.StringVar(&c.PublicMCP, "mcp-url", "https://mcp.mcpruntime.org/prod-audit/mcp", "dedicated fixture URL")
	flag.StringVar(&c.Team, "team", "qa-audit", "existing dedicated audit team")
	flag.StringVar(&c.MCPAuthVersion, "mcp-auth-version", "", "optional separately selected MCP Auth version")
	flag.BoolVar(&c.ClusterLogin, "cluster-login", false, "explicitly use the configured platform admin user's credentials from the selected cluster, in memory only")
	reportPath := flag.String("report", "", "write structured audit evidence to this file (0600)")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report, err := audit.RunPostDeploy(ctx, c, os.Stdout)
	if *reportPath != "" {
		raw, _ := json.MarshalIndent(report, "", "  ")
		if e := os.WriteFile(*reportPath, raw, 0600); e != nil {
			fmt.Fprintln(os.Stderr, "cannot write audit report")
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "post-deployment audit failed:", err)
		os.Exit(1)
	}
}

package main

import (
	"context"
	"flag"
	"fmt"
	"mcp-runtime/hack/prod-audit/internal/audit"
	"os"
	"time"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:8099", "prod-audit endpoint, normally the authenticated Runtime adapter on loopback")
	write := flag.Bool("write", false, "exercise temporary synthetic records and always close the run")
	denied := flag.String("expect-denied-tool", "", "expect an explicit policy denial for audit_wait instead of calling it as an allowed tool")
	public := flag.String("public-endpoint", "", "public prod-audit URL for anonymous-denial verification")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := audit.CheckFixture(ctx, audit.FixtureConfig{Endpoint: *endpoint, Write: *write, ExpectDeniedTool: *denied, PublicEndpoint: *public}, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "audit failed:", err)
		os.Exit(1)
	}
}

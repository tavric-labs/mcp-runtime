package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
	_ "go.uber.org/automaxprocs"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi"
	clickhousepkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/envcfg"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/svcboot"
)

type server struct {
	runtime     *runtimeapi.RuntimeServer
	runtimeInit string
	platform    *platformapi.Client
	authentic   platformauth.Authenticator
}

func main() {
	port := envcfg.EnvOr("PORT", "8084")
	metricsPort := envcfg.EnvOr("METRICS_PORT", "9094")
	clickhouseAddr := envcfg.EnvOr("CLICKHOUSE_ADDR", "clickhouse:9000")
	dbName := envcfg.EnvOr("CLICKHOUSE_DB", "mcp")
	if err := clickhousepkg.ValidateDBName(dbName); err != nil {
		log.Fatalf("invalid CLICKHOUSE_DB: %v", err)
	}

	jwtSecret, err := jwtSecretFromEnv()
	if err != nil {
		log.Fatal(err.Error())
	}

	options, err := clickhousepkg.Options(clickhousepkg.Config{
		Addr:        clickhouseAddr,
		Database:    dbName,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("configure ClickHouse transport: %v", err)
	}
	conn, err := chdriver.Open(options)
	if err != nil {
		log.Fatalf("failed to connect to clickhouse: %v", err)
	}

	platformAPIURL := strings.TrimSpace(os.Getenv("PLATFORM_API_URL"))
	if platformAPIURL == "" {
		platformAPIURL = "http://mcp-platform-api.mcp-platform.svc.cluster.local:8080"
	}
	internalToken := strings.TrimSpace(os.Getenv("INTERNAL_AUTH_TOKEN"))
	platformClient := &platformapi.Client{
		BaseURL: platformAPIURL,
		Token:   internalToken,
	}

	apiKeys := serviceAPIKeysFromEnv()
	runtimeServer, initErr := runtimeapi.NewRuntimeServer(conn, dbName, apiKeys, platformClient)
	if initErr != nil {
		log.Fatalf("runtime server init failed: %v", initErr)
	}
	runtimeServer.SetAuditWriter(platformClient)

	srv := &server{
		runtime:  runtimeServer,
		platform: platformClient,
		authentic: platformauth.NewAudienceAuthenticator(platformauth.AudienceConfig{
			Audience:       platformauth.AudienceRuntime,
			Secret:         jwtSecret,
			PlatformAPIURL: platformAPIURL,
			InternalToken:  internalToken,
			ServiceAPIKeys: apiKeys,
			AdminAPIKeys:   adminAPIKeysFromEnv(),
			PublicFallback: runtimeapi.PublicCatalogFallback,
		}),
	}
	if !runtimeServer.KubernetesAvailable() {
		srv.runtimeInit = "kubernetes client not available"
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	if err := svcboot.Run(svcboot.Config{
		ServiceName: "mcp-runtime-api",
		Port:        port,
		MetricsPort: metricsPort,
		Handler:     mux,
		OnShutdown: func(context.Context) error {
			return conn.Close()
		},
	}); err != nil {
		log.Fatal(err)
	}
}

func jwtSecretFromEnv() ([]byte, error) {
	secret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if secret == "" {
		return nil, errors.New("JWT_SECRET is required")
	}
	return []byte(secret), nil
}

func serviceAPIKeysFromEnv() map[string]struct{} {
	out := map[string]struct{}{}
	for _, key := range strings.Split(envcfg.EnvOr("API_KEYS", ""), ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}

func adminAPIKeysFromEnv() map[string]struct{} {
	out := map[string]struct{}{}
	for _, key := range strings.Split(envcfg.EnvOr("ADMIN_API_KEYS", ""), ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}

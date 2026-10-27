package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
	_ "go.uber.org/automaxprocs"

	"mcp-analytics-api/internal/analytics"
	"mcp-analytics-api/internal/identity"
	"mcp-analytics-api/internal/usage"
	clickhousepkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/envcfg"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/svcboot"
)

type server struct {
	usage     *usage.Service
	events    *analytics.Handler
	authentic platformauth.Authenticator
}

func main() {
	port := envcfg.EnvOr("PORT", "8085")
	metricsPort := envcfg.EnvOr("METRICS_PORT", "9095")
	clickhouseAddr := envcfg.EnvOr("CLICKHOUSE_ADDR", "clickhouse:9000")
	dbName := envcfg.EnvOr("CLICKHOUSE_DB", "mcp")
	if err := clickhousepkg.ValidateDBName(dbName); err != nil {
		log.Fatalf("invalid CLICKHOUSE_DB: %v", err)
	}

	jwtSecret, err := platformauth.JWTSecretFromEnv()
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

	srv := &server{
		usage: &usage.Service{
			DB:     conn,
			DBName: dbName,
			Resolver: &identity.HTTPResolver{
				BaseURL: platformAPIURL,
				Token:   internalToken,
			},
		},
		events: analytics.NewHandler(&clickhousepkg.Client{Conn: conn, DBName: dbName}),
		authentic: platformauth.NewAudienceAuthenticator(platformauth.AudienceConfig{
			Audience:       platformauth.AudienceAnalytics,
			Secret:         jwtSecret,
			PlatformAPIURL: platformAPIURL,
			InternalToken:  internalToken,
			ServiceAPIKeys: svcboot.APIKeysFromEnv("API_KEYS"),
			AdminAPIKeys:   svcboot.APIKeysFromEnv("ADMIN_API_KEYS"),
		}),
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	if err := svcboot.Run(svcboot.Config{
		ServiceName: "mcp-analytics-api",
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

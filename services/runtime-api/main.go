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

	"mcp-runtime-api/internal/accessintentobserver"
	"mcp-runtime-api/internal/accessintentworker"
	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi"
	clickhousepkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/envcfg"
	"mcp-runtime/pkg/passport"
	"mcp-runtime/pkg/svcboot"
)

type server struct {
	runtime     *runtimeapi.RuntimeServer
	runtimeInit string
	platform    *platformapi.Client
	authentic   passport.Authenticator
}

func main() {
	port := envcfg.EnvOr("PORT", "8084")
	metricsPort := envcfg.EnvOr("METRICS_PORT", "9094")
	clickhouseAddr := envcfg.EnvOr("CLICKHOUSE_ADDR", "clickhouse:9000")
	dbName := envcfg.EnvOr("CLICKHOUSE_DB", "mcp")
	if err := clickhousepkg.ValidateDBName(dbName); err != nil {
		log.Fatalf("invalid CLICKHOUSE_DB: %v", err)
	}

	jwtVerifier, err := passport.VerifierFromEnv()
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

	apiKeys := svcboot.APIKeysFromEnv("API_KEYS")
	runtimeServer, initErr := runtimeapi.NewRuntimeServer(conn, dbName, apiKeys, platformClient)
	if initErr != nil {
		log.Fatalf("runtime server init failed: %v", initErr)
	}
	runtimeServer.SetAuditWriter(platformClient)

	srv := &server{
		runtime:  runtimeServer,
		platform: platformClient,
		authentic: passport.NewAudienceAuthenticator(passport.AudienceConfig{
			Audience:       passport.AudienceRuntime,
			Verifier:       jwtVerifier,
			PlatformAPIURL: platformAPIURL,
			InternalToken:  internalToken,
			ServiceAPIKeys: apiKeys,
			AdminAPIKeys:   svcboot.APIKeysFromEnv("ADMIN_API_KEYS"),
		}),
	}
	if !runtimeServer.KubernetesAvailable() {
		srv.runtimeInit = "kubernetes client not available"
	}

	var accessWorker *accessintentworker.Runner
	if enabled, ok := envcfg.BoolEnv("RUNTIME_ACCESS_INTENT_WORKER"); ok && enabled {
		dsn := strings.TrimSpace(os.Getenv("RUNTIME_ACCESS_INTENT_POSTGRES_DSN"))
		if dsn == "" {
			dsn = strings.TrimSpace(os.Getenv("PLATFORM_DB_DSN"))
		}
		if dsn == "" {
			dsn = strings.TrimSpace(os.Getenv("DATABASE_URL"))
		}
		k8s := runtimeServer.K8sClients()
		if k8s == nil || k8s.Dynamic == nil {
			log.Fatalf("RUNTIME_ACCESS_INTENT_WORKER requires Kubernetes client access")
		}
		accessWorker, err = accessintentworker.Start(context.Background(), accessintentworker.RunnerConfig{
			PostgresDSN:    dsn,
			PlatformClient: platformClient,
			DynamicClient:  k8s.Dynamic,
			ClusterIssuer:  strings.TrimSpace(os.Getenv("MCP_MTLS_CLUSTER_ISSUER")),
			WorkerID:       envcfg.EnvOr("RUNTIME_ACCESS_INTENT_WORKER_ID", envcfg.EnvOr("HOSTNAME", "runtime-api-access-worker")),
		})
		if err != nil {
			log.Fatalf("access intent worker startup failed: %v", err)
		}
	}

	var cancelObserver context.CancelFunc
	if enabled, ok := envcfg.BoolEnv("RUNTIME_ACCESS_INTENT_OBSERVER"); ok && enabled {
		k8s := runtimeServer.K8sClients()
		if k8s == nil || k8s.Dynamic == nil || k8s.Clientset == nil {
			log.Fatalf("RUNTIME_ACCESS_INTENT_OBSERVER requires Kubernetes client access")
		}
		obsCtx, cancel := context.WithCancel(context.Background())
		cancelObserver = cancel
		observer := accessintentobserver.New(
			platformClient,
			&accessintentobserver.K8sGrantReader{Dynamic: k8s.Dynamic},
			&accessintentobserver.K8sGatewayStatusProber{Clientset: k8s.Clientset},
			accessintentobserver.Config{
				ClusterKey: envcfg.EnvOr("RUNTIME_ACCESS_INTENT_CLUSTER_KEY", "default"),
				ObserverID: envcfg.EnvOr("RUNTIME_ACCESS_INTENT_OBSERVER_ID", envcfg.EnvOr("HOSTNAME", "runtime-api-access-observer")),
				Interval:   envcfg.EnvDuration("RUNTIME_ACCESS_INTENT_OBSERVER_INTERVAL", 10*time.Second),
			},
		)
		go observer.Run(obsCtx)
	}

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	if err := svcboot.Run(svcboot.Config{
		ServiceName: "mcp-runtime-api",
		Port:        port,
		MetricsPort: metricsPort,
		Handler:     mux,
		OnShutdown: func(ctx context.Context) error {
			if cancelObserver != nil {
				cancelObserver()
			}
			if accessWorker != nil {
				if stopErr := accessWorker.Stop(ctx); stopErr != nil {
					log.Printf("access intent worker shutdown error: %v", stopErr)
				}
			}
			return conn.Close()
		},
	}); err != nil {
		log.Fatal(err)
	}
}

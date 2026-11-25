package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
	_ "go.uber.org/automaxprocs"

	"mcp-runtime-api/internal/accessintentobserver"
	"mcp-runtime-api/internal/accessintentworker"
	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi"
	"mcp-runtime-api/internal/workloadintentobserver"
	"mcp-runtime-api/internal/workloadintentworker"
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
		k8s := runtimeServer.KubernetesClients()
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
		k8s := runtimeServer.KubernetesClients()
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

	workerEnabled, err := parseOptInBool("RUNTIME_WORKLOAD_INTENT_WORKER")
	if err != nil {
		log.Fatal(err.Error())
	}
	observerEnabled, err := parseOptInBool("RUNTIME_WORKLOAD_INTENT_OBSERVER")
	if err != nil {
		log.Fatal(err.Error())
	}

	var workloadRunner *workloadintentworker.Runner
	var observerCancel context.CancelFunc
	k8sClients := runtimeServer.KubernetesClients()

	if workerEnabled && k8sClients != nil && platformClient.Configured() {
		dsn := strings.TrimSpace(os.Getenv("PLATFORM_WORKLOAD_INTENT_DB_DSN"))
		if dsn == "" {
			dsn = strings.TrimSpace(os.Getenv("PLATFORM_DB_DSN"))
		}
		if dsn == "" {
			log.Fatal("RUNTIME_WORKLOAD_INTENT_WORKER=true requires PLATFORM_WORKLOAD_INTENT_DB_DSN or PLATFORM_DB_DSN")
		}
		workerID := strings.TrimSpace(os.Getenv("POD_NAME"))
		if workerID == "" {
			workerID, _ = os.Hostname()
		}
		if workerID == "" {
			workerID = "runtime-workload-worker"
		}
		cfg := workloadintentworker.DefaultRunnerConfig()
		cfg.DSN = dsn
		cfg.WorkerID = workerID

		deps := workloadintentworker.Dependencies{
			Server: workloadintentworker.NewServerWorker(
				platformClient,
				workloadintentworker.NewKubeServerExecutor(k8sClients.Dynamic, k8sClients.Clientset, runtimeServer.AccessManager(), platformClient),
				workerID,
			),
			Deployment: workloadintentworker.NewDeploymentWorker(
				platformClient,
				workloadintentworker.NewKubeDeploymentExecutor(k8sClients.Clientset),
				workerID,
			),
			Publication: workloadintentworker.NewImagePublicationWorker(
				platformClient,
				workloadintentworker.NewKubePublicationHelperRunner(
					k8sClients.Clientset,
					strings.TrimSpace(os.Getenv("MCP_REGISTRY_PUSH_HELPER_NAMESPACE")),
					strings.TrimSpace(os.Getenv("MCP_REGISTRY_PUSH_SKOPEO_IMAGE")),
					strings.TrimSpace(os.Getenv("MCP_REGISTRY_STAGING_DIR")),
				),
				workerID,
			),
			Restart: workloadintentworker.NewRestartWorker(
				platformClient,
				workloadintentworker.NewKubeRestartPatcher(k8sClients.Clientset),
				workerID,
			),
		}
		workloadRunner, err = workloadintentworker.NewRunner(context.Background(), cfg, deps)
		if err != nil {
			log.Fatalf("initialize workload intent worker: %v", err)
		}
		if err := workloadRunner.Start(context.Background()); err != nil {
			log.Fatalf("start workload intent worker: %v", err)
		}
	}

	if observerEnabled && k8sClients != nil && platformClient.Configured() {
		observerID := strings.TrimSpace(os.Getenv("POD_NAME"))
		if observerID == "" {
			observerID, _ = os.Hostname()
		}
		if observerID == "" {
			observerID = "runtime-workload-observer"
		}
		obs := workloadintentobserver.New(
			platformClient,
			workloadintentobserver.NewKubeClusterReader(k8sClients.Dynamic, k8sClients.Clientset),
			workloadintentobserver.Config{
				ClusterKey: strings.TrimSpace(os.Getenv("MCP_WORKLOAD_OBSERVER_CLUSTER_KEY")),
				ObserverID: observerID,
			},
		)
		obsCtx, cancel := context.WithCancel(context.Background())
		observerCancel = cancel
		go obs.Run(obsCtx)
	}

	if err := svcboot.Run(svcboot.Config{
		ServiceName: "mcp-runtime-api",
		Port:        port,
		MetricsPort: metricsPort,
		Handler:     mux,
		OnShutdown: func(ctx context.Context) error {
			if cancelObserver != nil {
				cancelObserver()
			}
			if observerCancel != nil {
				observerCancel()
			}
			var shutdownErr error
			if accessWorker != nil {
				shutdownErr = errors.Join(shutdownErr, accessWorker.Stop(ctx))
			}
			if workloadRunner != nil {
				shutdownErr = errors.Join(shutdownErr, workloadRunner.Stop(ctx))
			}
			return errors.Join(shutdownErr, conn.Close())
		},
	}); err != nil {
		log.Fatal(err)
	}
}

func parseOptInBool(key string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New(key + " must be a boolean")
	}
	return v, nil
}

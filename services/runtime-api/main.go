package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
	_ "go.uber.org/automaxprocs"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi"
	"mcp-runtime-api/internal/teamintentobserver"
	"mcp-runtime-api/internal/teamintentworker"
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

	workerRunner, workerCancel, err := startOptInTeamIntentWorker(runtimeServer, platformClient)
	if err != nil {
		log.Fatalf("team intent worker init failed: %v", err)
	}
	observerCancel, observerDone, err := startOptInTeamIntentObserver(runtimeServer, platformClient)
	if err != nil {
		if workerCancel != nil {
			workerCancel()
		}
		if workerRunner != nil {
			_ = workerRunner.Stop(context.Background())
		}
		log.Fatalf("team intent observer init failed: %v", err)
	}

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
	var workloadObserverCancel context.CancelFunc
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
		workloadObserverCancel = cancel
		go obs.Run(obsCtx)
	}

	if err := svcboot.Run(svcboot.Config{
		ServiceName: "mcp-runtime-api",
		Port:        port,
		MetricsPort: metricsPort,
		Handler:     mux,
		OnShutdown: func(ctx context.Context) error {
			var errs []error
			if observerCancel != nil {
				observerCancel()
				select {
				case <-observerDone:
				case <-ctx.Done():
					errs = append(errs, ctx.Err())
				}
			}
			if workloadObserverCancel != nil {
				workloadObserverCancel()
			}
			if workerCancel != nil {
				workerCancel()
			}
			if workerRunner != nil {
				if err := workerRunner.Stop(ctx); err != nil {
					errs = append(errs, err)
				}
			}
			if workloadRunner != nil {
				if err := workloadRunner.Stop(ctx); err != nil {
					errs = append(errs, err)
				}
			}
			if err := conn.Close(); err != nil {
				errs = append(errs, err)
			}
			return errors.Join(errs...)
		},
	}); err != nil {
		log.Fatal(err)
	}
}

func startOptInTeamIntentWorker(runtimeServer *runtimeapi.RuntimeServer, platformClient *platformapi.Client) (*teamintentworker.Runner, context.CancelFunc, error) {
	if strings.TrimSpace(os.Getenv("RUNTIME_TEAM_INTENT_WORKER")) != "true" {
		return nil, nil, nil
	}
	dsn := strings.TrimSpace(os.Getenv("RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL"))
	if dsn == "" {
		return nil, nil, errors.New("RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL is required when RUNTIME_TEAM_INTENT_WORKER=true")
	}
	if !platformClient.Configured() {
		return nil, nil, errors.New("PLATFORM_API_URL and INTERNAL_AUTH_TOKEN are required when RUNTIME_TEAM_INTENT_WORKER=true")
	}
	provCfg, ok := runtimeServer.Deployments().TeamIntentProvisionerConfig()
	if !ok {
		return nil, nil, errors.New("kubernetes client is required when RUNTIME_TEAM_INTENT_WORKER=true")
	}
	workerID := envcfg.EnvOr("RUNTIME_TEAM_INTENT_WORKER_ID", defaultInstanceID("runtime-worker"))
	initCtx, initCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer initCancel()

	runner, err := teamintentworker.NewRunner(initCtx, teamintentworker.RunnerConfig{
		DatabaseURL: dsn,
		WorkerID:    workerID,
		Platform:    platformClient,
		Reconciler: &teamintentworker.K8sNamespaceReconciler{
			Client:                   provCfg.Client,
			EnsureRegistryPullSecret: provCfg.EnsureRegistryPullSecret,
			Traefik:                  provCfg.Traefik,
			IngressFromNamespaces:    provCfg.IngressFromNamespaces,
		},
	})
	if err != nil {
		return nil, nil, err
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	if err := runner.Start(runCtx); err != nil {
		runCancel()
		_ = runner.Stop(context.Background())
		return nil, nil, err
	}
	log.Printf("started opt-in team intent worker %s", workerID)
	return runner, runCancel, nil
}

func startOptInTeamIntentObserver(runtimeServer *runtimeapi.RuntimeServer, platformClient *platformapi.Client) (context.CancelFunc, <-chan struct{}, error) {
	if strings.TrimSpace(os.Getenv("RUNTIME_TEAM_INTENT_OBSERVER")) != "true" {
		return nil, nil, nil
	}
	if !platformClient.Configured() {
		return nil, nil, errors.New("PLATFORM_API_URL and INTERNAL_AUTH_TOKEN are required when RUNTIME_TEAM_INTENT_OBSERVER=true")
	}
	provCfg, ok := runtimeServer.Deployments().TeamIntentProvisionerConfig()
	if !ok {
		return nil, nil, errors.New("kubernetes client is required when RUNTIME_TEAM_INTENT_OBSERVER=true")
	}
	observerID := envcfg.EnvOr("RUNTIME_TEAM_INTENT_OBSERVER_ID", defaultInstanceID("runtime-observer"))
	clusterKey := envcfg.EnvOr("RUNTIME_TEAM_INTENT_CLUSTER_KEY", "default")

	observer, err := teamintentobserver.New(teamintentobserver.Config{
		ClusterKey: clusterKey,
		ObserverID: observerID,
		Platform:   platformClient,
		K8sClient:  provCfg.Client,
		Traefik:    provCfg.Traefik,
	})
	if err != nil {
		return nil, nil, err
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := observer.Run(runCtx); err != nil {
			if errors.Is(err, teamintentobserver.ErrObserverLeaseLost) {
				log.Printf("team intent observer %s stepped down after lease superseded", observerID)
				return
			}
			log.Printf("team intent observer %s stopped with error: %v", observerID, err)
		}
	}()
	log.Printf("started opt-in team intent observer %s (cluster_key=%s)", observerID, clusterKey)
	return runCancel, done, nil
}

func defaultInstanceID(prefix string) string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s-%s-%d", prefix, strings.TrimSpace(host), os.Getpid())
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

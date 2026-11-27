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

	"mcp-runtime-api/internal/accessintentobserver"
	"mcp-runtime-api/internal/accessintentworker"
	"mcp-runtime-api/internal/k8sstore"
	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi"
	"mcp-runtime-api/internal/teamintentobserver"
	"mcp-runtime-api/internal/teamintentworker"
	"mcp-runtime-api/internal/workloadintentobserver"
	"mcp-runtime-api/internal/workloadintentworker"
	clickhousepkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/envcfg"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/passport"
	"mcp-runtime/pkg/svcboot"
)

const defaultIntentStartupTimeout = 60 * time.Second

var (
	intentStartupAttemptTimeout = 15 * time.Second
	intentStartupRetryInterval  = 2 * time.Second
)

type permanentStartupError struct {
	err error
}

func (e permanentStartupError) Error() string { return e.err.Error() }
func (e permanentStartupError) Unwrap() error { return e.err }

type teamObserverRunner interface {
	Run(context.Context) error
}

type workloadObserverRunner interface {
	Run(context.Context)
}

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

	accessWorker, accessCancel, err := startOptInAccessIntentWorker(runtimeServer, platformClient)
	if err != nil {
		log.Fatalf("access intent worker startup failed: %v", err)
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

	workloadRunner, workloadCancel, err := startOptInWorkloadIntentWorker(runtimeServer, platformClient)
	if err != nil {
		log.Fatalf("workload intent worker init failed: %v", err)
	}
	workloadObserverCancel, workloadObserverDone, err := startOptInWorkloadIntentObserver(runtimeServer, platformClient)
	if err != nil {
		if workloadCancel != nil {
			workloadCancel()
		}
		if workloadRunner != nil {
			_ = workloadRunner.Stop(context.Background())
		}
		log.Fatalf("workload intent observer init failed: %v", err)
	}

	if err := svcboot.Run(svcboot.Config{
		ServiceName: "mcp-runtime-api",
		Port:        port,
		MetricsPort: metricsPort,
		Handler:     mux,
		OnShutdown: func(ctx context.Context) error {
			var errs []error
			if cancelObserver != nil {
				cancelObserver()
			}
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
				select {
				case <-workloadObserverDone:
				case <-ctx.Done():
					errs = append(errs, ctx.Err())
				}
			}
			if workerCancel != nil {
				workerCancel()
			}
			if workerRunner != nil {
				if err := workerRunner.Stop(ctx); err != nil {
					errs = append(errs, err)
				}
			}
			if accessCancel != nil {
				accessCancel()
			}
			if accessWorker != nil {
				if err := accessWorker.Stop(ctx); err != nil {
					errs = append(errs, err)
				}
			}
			if workloadCancel != nil {
				workloadCancel()
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

func intentStartupTimeout() time.Duration {
	timeout := envcfg.EnvDuration("RUNTIME_INTENT_STARTUP_TIMEOUT", defaultIntentStartupTimeout)
	if timeout <= 0 {
		return defaultIntentStartupTimeout
	}
	return timeout
}

func retryIntentStartup(ctx context.Context, timeout, interval time.Duration, label string, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = defaultIntentStartupTimeout
	}
	if interval <= 0 {
		interval = intentStartupRetryInterval
	}
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	retryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for attempt := 1; ; attempt++ {
		attemptCtx, attemptCancel := context.WithTimeout(retryCtx, intentStartupAttemptTimeout)
		err := fn(attemptCtx)
		attemptCancel()
		if err == nil {
			if attempt > 1 {
				log.Printf("%s initialized after %d attempt(s)", label, attempt)
			}
			return nil
		}
		var perm permanentStartupError
		if errors.As(err, &perm) {
			return perm.err
		}
		lastErr = err
		if retryCtx.Err() != nil {
			return lastErr
		}
		log.Printf("%s not ready (attempt %d): %v", label, attempt, err)
		timer := time.NewTimer(interval)
		select {
		case <-retryCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return lastErr
		case <-timer.C:
		}
	}
}

// startIntentWorker bounds initialization without attaching the startup deadline
// to the worker's lifetime after initialization succeeds.
func startIntentWorker(ctx context.Context, start func(context.Context) error) (context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	stopStartupCancel := context.AfterFunc(ctx, runCancel)
	err := start(runCtx)
	if !stopStartupCancel() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		runCancel()
		return nil, err
	}
	return runCancel, nil
}

func defaultStartTeamIntentWorker(ctx context.Context, cfg teamintentworker.RunnerConfig) (*teamintentworker.Runner, context.CancelFunc, error) {
	runner, err := teamintentworker.NewRunner(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	runCancel, err := startIntentWorker(ctx, runner.Start)
	if err != nil {
		_ = runner.Stop(context.Background())
		return nil, nil, err
	}
	return runner, runCancel, nil
}

func startOptInTeamIntentWorker(runtimeServer *runtimeapi.RuntimeServer, platformClient *platformapi.Client) (*teamintentworker.Runner, context.CancelFunc, error) {
	provCfg, ok := runtimeServer.Deployments().TeamIntentProvisionerConfig()
	return startOptInTeamIntentWorkerWith(provCfg, ok, platformClient, defaultStartTeamIntentWorker)
}

func startOptInTeamIntentWorkerWith(
	provCfg runtimeapi.TeamIntentProvisionerConfig,
	provOK bool,
	platformClient *platformapi.Client,
	startFn func(context.Context, teamintentworker.RunnerConfig) (*teamintentworker.Runner, context.CancelFunc, error),
) (*teamintentworker.Runner, context.CancelFunc, error) {
	enabled, err := parseOptInBool("RUNTIME_TEAM_INTENT_WORKER")
	if err != nil {
		return nil, nil, err
	}
	if !enabled {
		return nil, nil, nil
	}
	dsn := firstNonEmptyEnv("RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL", "PLATFORM_DB_DSN", "DATABASE_URL")
	if dsn == "" {
		return nil, nil, errors.New("RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL, PLATFORM_DB_DSN, or DATABASE_URL is required when RUNTIME_TEAM_INTENT_WORKER=true")
	}
	if !platformClient.Configured() {
		return nil, nil, errors.New("PLATFORM_API_URL and INTERNAL_AUTH_TOKEN are required when RUNTIME_TEAM_INTENT_WORKER=true")
	}
	if !provOK || provCfg.Client == nil {
		return nil, nil, errors.New("kubernetes client is required when RUNTIME_TEAM_INTENT_WORKER=true")
	}
	if startFn == nil {
		startFn = defaultStartTeamIntentWorker
	}
	workerID := envcfg.EnvOr("RUNTIME_TEAM_INTENT_WORKER_ID", defaultInstanceID("runtime-worker"))
	cfg := teamintentworker.RunnerConfig{
		DatabaseURL: dsn,
		WorkerID:    workerID,
		Platform:    platformClient,
		Reconciler: &teamintentworker.K8sNamespaceReconciler{
			Client:                   provCfg.Client,
			EnsureRegistryPullSecret: provCfg.EnsureRegistryPullSecret,
			Traefik:                  provCfg.Traefik,
			IngressFromNamespaces:    provCfg.IngressFromNamespaces,
		},
	}

	var runner *teamintentworker.Runner
	var runCancel context.CancelFunc
	if err := retryIntentStartup(context.Background(), intentStartupTimeout(), intentStartupRetryInterval, "team intent worker", func(attemptCtx context.Context) error {
		r, cancel, err := startFn(attemptCtx, cfg)
		if err != nil {
			return err
		}
		runner = r
		runCancel = cancel
		return nil
	}); err != nil {
		return nil, nil, err
	}
	log.Printf("started opt-in team intent worker %s", workerID)
	return runner, runCancel, nil
}

func defaultNewTeamIntentObserver(cfg teamintentobserver.Config) (teamObserverRunner, error) {
	return teamintentobserver.New(cfg)
}

func startOptInTeamIntentObserver(runtimeServer *runtimeapi.RuntimeServer, platformClient *platformapi.Client) (context.CancelFunc, <-chan struct{}, error) {
	provCfg, ok := runtimeServer.Deployments().TeamIntentProvisionerConfig()
	return startOptInTeamIntentObserverWith(provCfg, ok, platformClient, defaultNewTeamIntentObserver)
}

func startOptInTeamIntentObserverWith(
	provCfg runtimeapi.TeamIntentProvisionerConfig,
	provOK bool,
	platformClient *platformapi.Client,
	newObserver func(teamintentobserver.Config) (teamObserverRunner, error),
) (context.CancelFunc, <-chan struct{}, error) {
	enabled, err := parseOptInBool("RUNTIME_TEAM_INTENT_OBSERVER")
	if err != nil {
		return nil, nil, err
	}
	if !enabled {
		return nil, nil, nil
	}
	if !platformClient.Configured() {
		return nil, nil, errors.New("PLATFORM_API_URL and INTERNAL_AUTH_TOKEN are required when RUNTIME_TEAM_INTENT_OBSERVER=true")
	}
	if !provOK || provCfg.Client == nil {
		return nil, nil, errors.New("kubernetes client is required when RUNTIME_TEAM_INTENT_OBSERVER=true")
	}
	if newObserver == nil {
		newObserver = defaultNewTeamIntentObserver
	}
	observerID := envcfg.EnvOr("RUNTIME_TEAM_INTENT_OBSERVER_ID", defaultInstanceID("runtime-observer"))
	clusterKey := envcfg.EnvOr("RUNTIME_TEAM_INTENT_CLUSTER_KEY", "default")

	observer, err := newObserver(teamintentobserver.Config{
		ClusterKey:     clusterKey,
		ObserverID:     observerID,
		Platform:       platformClient,
		K8sClient:      provCfg.Client,
		Traefik:        provCfg.Traefik,
		VerifyInterval: positiveEnvDuration("RUNTIME_TEAM_INTENT_OBSERVER_INTERVAL"),
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

func defaultStartAccessIntentWorker(ctx context.Context, cfg accessintentworker.RunnerConfig) (*accessintentworker.Runner, context.CancelFunc, error) {
	var runner *accessintentworker.Runner
	runCancel, err := startIntentWorker(ctx, func(runCtx context.Context) error {
		var startErr error
		runner, startErr = accessintentworker.Start(runCtx, cfg)
		return startErr
	})
	if err != nil {
		if runner != nil {
			_ = runner.Stop(context.Background())
		}
		return nil, nil, err
	}
	return runner, runCancel, nil
}

func startOptInAccessIntentWorker(runtimeServer *runtimeapi.RuntimeServer, platformClient *platformapi.Client) (*accessintentworker.Runner, context.CancelFunc, error) {
	return startOptInAccessIntentWorkerWith(runtimeServer.KubernetesClients(), platformClient, defaultStartAccessIntentWorker)
}

func startOptInAccessIntentWorkerWith(
	k8sClients *k8sclient.Clients,
	platformClient *platformapi.Client,
	startFn func(context.Context, accessintentworker.RunnerConfig) (*accessintentworker.Runner, context.CancelFunc, error),
) (*accessintentworker.Runner, context.CancelFunc, error) {
	enabled, err := parseOptInBool("RUNTIME_ACCESS_INTENT_WORKER")
	if err != nil {
		return nil, nil, err
	}
	if !enabled {
		return nil, nil, nil
	}
	dsn := firstNonEmptyEnv("RUNTIME_ACCESS_INTENT_POSTGRES_DSN", "PLATFORM_DB_DSN", "DATABASE_URL")
	if dsn == "" {
		return nil, nil, errors.New("RUNTIME_ACCESS_INTENT_POSTGRES_DSN, PLATFORM_DB_DSN, or DATABASE_URL is required when RUNTIME_ACCESS_INTENT_WORKER=true")
	}
	if !platformClient.Configured() {
		return nil, nil, errors.New("PLATFORM_API_URL and INTERNAL_AUTH_TOKEN are required when RUNTIME_ACCESS_INTENT_WORKER=true")
	}
	if k8sClients == nil || k8sClients.Dynamic == nil {
		return nil, nil, errors.New("RUNTIME_ACCESS_INTENT_WORKER requires Kubernetes client access")
	}
	if startFn == nil {
		startFn = defaultStartAccessIntentWorker
	}
	workerID := envcfg.EnvOr("RUNTIME_ACCESS_INTENT_WORKER_ID", envcfg.EnvOr("HOSTNAME", "runtime-api-access-worker"))
	cfg := accessintentworker.RunnerConfig{
		PostgresDSN:    dsn,
		PlatformClient: platformClient,
		DynamicClient:  k8sClients.Dynamic,
		ClusterIssuer:  strings.TrimSpace(os.Getenv("MCP_MTLS_CLUSTER_ISSUER")),
		WorkerID:       workerID,
	}

	var runner *accessintentworker.Runner
	var runCancel context.CancelFunc
	if err := retryIntentStartup(context.Background(), intentStartupTimeout(), intentStartupRetryInterval, "access intent worker", func(attemptCtx context.Context) error {
		r, cancel, err := startFn(attemptCtx, cfg)
		if err != nil {
			return err
		}
		runner = r
		runCancel = cancel
		return nil
	}); err != nil {
		return nil, nil, err
	}
	log.Printf("started opt-in access intent worker %s", workerID)
	return runner, runCancel, nil
}

func defaultStartWorkloadIntentWorker(ctx context.Context, cfg workloadintentworker.RunnerConfig, deps workloadintentworker.Dependencies) (*workloadintentworker.Runner, context.CancelFunc, error) {
	runner, err := workloadintentworker.NewRunner(ctx, cfg, deps)
	if err != nil {
		return nil, nil, err
	}
	runCancel, err := startIntentWorker(ctx, runner.Start)
	if err != nil {
		_ = runner.Stop(context.Background())
		return nil, nil, err
	}
	return runner, runCancel, nil
}

func startOptInWorkloadIntentWorker(runtimeServer *runtimeapi.RuntimeServer, platformClient *platformapi.Client) (*workloadintentworker.Runner, context.CancelFunc, error) {
	return startOptInWorkloadIntentWorkerWith(runtimeServer.KubernetesClients(), runtimeServer.AccessManager(), platformClient, defaultStartWorkloadIntentWorker)
}

func startOptInWorkloadIntentWorkerWith(
	k8sClients *k8sclient.Clients,
	accessMgr *k8sstore.Manager,
	platformClient *platformapi.Client,
	startFn func(context.Context, workloadintentworker.RunnerConfig, workloadintentworker.Dependencies) (*workloadintentworker.Runner, context.CancelFunc, error),
) (*workloadintentworker.Runner, context.CancelFunc, error) {
	enabled, err := parseOptInBool("RUNTIME_WORKLOAD_INTENT_WORKER")
	if err != nil {
		return nil, nil, err
	}
	if !enabled {
		return nil, nil, nil
	}
	dsn := firstNonEmptyEnv("PLATFORM_WORKLOAD_INTENT_DB_DSN", "PLATFORM_DB_DSN", "DATABASE_URL")
	if dsn == "" {
		return nil, nil, errors.New("PLATFORM_WORKLOAD_INTENT_DB_DSN, PLATFORM_DB_DSN, or DATABASE_URL is required when RUNTIME_WORKLOAD_INTENT_WORKER=true")
	}
	if !platformClient.Configured() {
		return nil, nil, errors.New("PLATFORM_API_URL and INTERNAL_AUTH_TOKEN are required when RUNTIME_WORKLOAD_INTENT_WORKER=true")
	}
	if k8sClients == nil || k8sClients.Dynamic == nil || k8sClients.Clientset == nil {
		return nil, nil, errors.New("kubernetes client is required when RUNTIME_WORKLOAD_INTENT_WORKER=true")
	}
	if startFn == nil {
		startFn = defaultStartWorkloadIntentWorker
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
			workloadintentworker.NewKubeServerExecutor(k8sClients.Dynamic, k8sClients.Clientset, accessMgr, platformClient),
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

	var runner *workloadintentworker.Runner
	var runCancel context.CancelFunc
	if err := retryIntentStartup(context.Background(), intentStartupTimeout(), intentStartupRetryInterval, "workload intent worker", func(attemptCtx context.Context) error {
		r, cancel, err := startFn(attemptCtx, cfg, deps)
		if err != nil {
			return err
		}
		runner = r
		runCancel = cancel
		return nil
	}); err != nil {
		return nil, nil, err
	}
	log.Printf("started opt-in workload intent worker %s", workerID)
	return runner, runCancel, nil
}

func defaultNewWorkloadIntentObserver(
	platform workloadintentobserver.PlatformClient,
	cluster workloadintentobserver.ClusterReader,
	cfg workloadintentobserver.Config,
) workloadObserverRunner {
	return workloadintentobserver.New(platform, cluster, cfg)
}

func startOptInWorkloadIntentObserver(runtimeServer *runtimeapi.RuntimeServer, platformClient *platformapi.Client) (context.CancelFunc, <-chan struct{}, error) {
	return startOptInWorkloadIntentObserverWith(runtimeServer.KubernetesClients(), platformClient, defaultNewWorkloadIntentObserver)
}

func startOptInWorkloadIntentObserverWith(
	k8sClients *k8sclient.Clients,
	platformClient *platformapi.Client,
	newObserver func(workloadintentobserver.PlatformClient, workloadintentobserver.ClusterReader, workloadintentobserver.Config) workloadObserverRunner,
) (context.CancelFunc, <-chan struct{}, error) {
	enabled, err := parseOptInBool("RUNTIME_WORKLOAD_INTENT_OBSERVER")
	if err != nil {
		return nil, nil, err
	}
	if !enabled {
		return nil, nil, nil
	}
	if !platformClient.Configured() {
		return nil, nil, errors.New("PLATFORM_API_URL and INTERNAL_AUTH_TOKEN are required when RUNTIME_WORKLOAD_INTENT_OBSERVER=true")
	}
	if k8sClients == nil || k8sClients.Dynamic == nil || k8sClients.Clientset == nil {
		return nil, nil, errors.New("kubernetes client is required when RUNTIME_WORKLOAD_INTENT_OBSERVER=true")
	}
	if newObserver == nil {
		newObserver = defaultNewWorkloadIntentObserver
	}
	observerID := strings.TrimSpace(os.Getenv("POD_NAME"))
	if observerID == "" {
		observerID, _ = os.Hostname()
	}
	if observerID == "" {
		observerID = "runtime-workload-observer"
	}
	clusterKey := strings.TrimSpace(os.Getenv("MCP_WORKLOAD_OBSERVER_CLUSTER_KEY"))
	obs := newObserver(
		platformClient,
		workloadintentobserver.NewKubeClusterReader(k8sClients.Dynamic, k8sClients.Clientset),
		workloadintentobserver.Config{
			ClusterKey:   clusterKey,
			ObserverID:   observerID,
			PollInterval: positiveEnvDuration("MCP_WORKLOAD_OBSERVER_INTERVAL", "RUNTIME_WORKLOAD_INTENT_OBSERVER_INTERVAL"),
		},
	)
	obsCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		obs.Run(obsCtx)
	}()
	log.Printf("started opt-in workload intent observer %s", observerID)
	return cancel, done, nil
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func positiveEnvDuration(keys ...string) time.Duration {
	for _, key := range keys {
		if d := envcfg.EnvDuration(key, 0); d > 0 {
			return d
		}
	}
	return 0
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

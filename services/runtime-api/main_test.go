package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"mcp-runtime-api/internal/accessintentworker"
	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime-api/internal/runtimeapi"
	"mcp-runtime-api/internal/teamintentobserver"
	"mcp-runtime-api/internal/teamintentworker"
	"mcp-runtime-api/internal/workloadintentobserver"
	"mcp-runtime-api/internal/workloadintentworker"
	"mcp-runtime/pkg/k8sclient"
)

type fakeTeamObserver struct{}

func (fakeTeamObserver) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

type fakeWorkloadObserver struct{}

func (fakeWorkloadObserver) Run(ctx context.Context) {
	<-ctx.Done()
}

func testK8sClients() *k8sclient.Clients {
	return &k8sclient.Clients{
		Clientset: kubernetesfake.NewSimpleClientset(),
		Dynamic:   dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}
}

func configuredPlatformClient() *platformapi.Client {
	return &platformapi.Client{
		BaseURL: "http://platform-api.example",
		Token:   "internal-token",
	}
}

func withFastIntentStartupRetry(t *testing.T, interval, attemptTimeout time.Duration) {
	t.Helper()
	origInterval := intentStartupRetryInterval
	origAttemptTimeout := intentStartupAttemptTimeout
	intentStartupRetryInterval = interval
	intentStartupAttemptTimeout = attemptTimeout
	t.Cleanup(func() {
		intentStartupRetryInterval = origInterval
		intentStartupAttemptTimeout = origAttemptTimeout
	})
}

func TestOptInTeamIntentWorkerAndObserverStayOffByDefault(t *testing.T) {
	t.Setenv("RUNTIME_TEAM_INTENT_WORKER", "")
	t.Setenv("RUNTIME_TEAM_INTENT_OBSERVER", "")

	runner, cancel, err := startOptInTeamIntentWorker(nil, &platformapi.Client{})
	if err != nil || runner != nil || cancel != nil {
		t.Fatalf("expected worker disabled by default, got runner=%v cancel=%v err=%v", runner, cancel, err)
	}

	obsCancel, obsDone, err := startOptInTeamIntentObserver(nil, &platformapi.Client{})
	if err != nil || obsCancel != nil || obsDone != nil {
		t.Fatalf("expected observer disabled by default, got cancel=%v done=%v err=%v", obsCancel, obsDone, err)
	}
}

func TestOptInTeamIntentWorkerRequiresQueueDatabaseURL(t *testing.T) {
	t.Setenv("RUNTIME_TEAM_INTENT_WORKER", "true")
	t.Setenv("RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL", "")
	t.Setenv("PLATFORM_DB_DSN", "")
	t.Setenv("DATABASE_URL", "")

	if _, _, err := startOptInTeamIntentWorker(nil, configuredPlatformClient()); err == nil {
		t.Fatal("expected error when all team intent database URL env vars are unset")
	}
}

func TestOptInTeamIntentWorkerAndObserverRejectInvalidBooleans(t *testing.T) {
	t.Setenv("RUNTIME_TEAM_INTENT_WORKER", "yes")
	if _, _, err := startOptInTeamIntentWorker(nil, configuredPlatformClient()); err == nil || !strings.Contains(err.Error(), "RUNTIME_TEAM_INTENT_WORKER") {
		t.Fatalf("expected explicit boolean error for RUNTIME_TEAM_INTENT_WORKER=yes, got %v", err)
	}

	t.Setenv("RUNTIME_TEAM_INTENT_OBSERVER", "yes")
	if _, _, err := startOptInTeamIntentObserver(nil, configuredPlatformClient()); err == nil || !strings.Contains(err.Error(), "RUNTIME_TEAM_INTENT_OBSERVER") {
		t.Fatalf("expected explicit boolean error for RUNTIME_TEAM_INTENT_OBSERVER=yes, got %v", err)
	}
}

func TestOptInTeamIntentWorkerDSNFallbackParity(t *testing.T) {
	withFastIntentStartupRetry(t, time.Millisecond, time.Second)
	provCfg := runtimeapi.TeamIntentProvisionerConfig{Client: kubernetesfake.NewSimpleClientset()}

	cases := []struct {
		name     string
		queueURL string
		platform string
		dbURL    string
		wantDSN  string
	}{
		{
			name:     "prefers RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL",
			queueURL: "postgres://queue-dsn",
			platform: "postgres://platform-dsn",
			dbURL:    "postgres://database-url",
			wantDSN:  "postgres://queue-dsn",
		},
		{
			name:     "falls back to PLATFORM_DB_DSN",
			queueURL: "",
			platform: "postgres://platform-dsn",
			dbURL:    "postgres://database-url",
			wantDSN:  "postgres://platform-dsn",
		},
		{
			name:     "falls back to DATABASE_URL",
			queueURL: "",
			platform: "",
			dbURL:    "postgres://database-url",
			wantDSN:  "postgres://database-url",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RUNTIME_TEAM_INTENT_WORKER", "true")
			t.Setenv("RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL", tc.queueURL)
			t.Setenv("PLATFORM_DB_DSN", tc.platform)
			t.Setenv("DATABASE_URL", tc.dbURL)

			var gotDSN string
			runner, cancel, err := startOptInTeamIntentWorkerWith(
				provCfg,
				true,
				configuredPlatformClient(),
				func(_ context.Context, cfg teamintentworker.RunnerConfig) (*teamintentworker.Runner, context.CancelFunc, error) {
					gotDSN = cfg.DatabaseURL
					return &teamintentworker.Runner{}, func() {}, nil
				},
			)
			if err != nil {
				t.Fatalf("startOptInTeamIntentWorkerWith() error = %v", err)
			}
			if runner == nil || cancel == nil {
				t.Fatalf("expected runner and cancel func, got runner=%v cancel=%v", runner, cancel)
			}
			if gotDSN != tc.wantDSN {
				t.Fatalf("DatabaseURL = %q, want %q", gotDSN, tc.wantDSN)
			}
		})
	}
}

func TestRetryIntentStartupSucceedsAfterTransientErrors(t *testing.T) {
	withFastIntentStartupRetry(t, time.Millisecond, time.Second)

	attempts := 0
	err := retryIntentStartup(context.Background(), 200*time.Millisecond, time.Millisecond, "test worker", func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("relation river_job does not exist")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryIntentStartup() error = %v, want nil", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestRetryIntentStartupReturnsLastErrorOnTimeout(t *testing.T) {
	withFastIntentStartupRetry(t, 2*time.Millisecond, time.Second)

	attempts := 0
	lastTransient := errors.New("postgres still starting")
	err := retryIntentStartup(context.Background(), 15*time.Millisecond, 2*time.Millisecond, "test worker", func(context.Context) error {
		attempts++
		return lastTransient
	})
	if !errors.Is(err, lastTransient) {
		t.Fatalf("retryIntentStartup() error = %v, want %v", err, lastTransient)
	}
	if attempts < 2 {
		t.Fatalf("expected multiple attempts before timeout, got %d", attempts)
	}
}

func TestRetryIntentStartupFailsFastOnPermanentError(t *testing.T) {
	withFastIntentStartupRetry(t, time.Millisecond, time.Second)

	attempts := 0
	configErr := errors.New("missing DSN")
	err := retryIntentStartup(context.Background(), 200*time.Millisecond, time.Millisecond, "test worker", func(context.Context) error {
		attempts++
		return permanentStartupError{err: configErr}
	})
	if !errors.Is(err, configErr) {
		t.Fatalf("retryIntentStartup() error = %v, want %v", err, configErr)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestOptInIntentWorkersRetryTransientStartupAndFailFastOnConfigErrors(t *testing.T) {
	withFastIntentStartupRetry(t, time.Millisecond, time.Second)

	t.Run("team worker retries transient errors and succeeds", func(t *testing.T) {
		t.Setenv("RUNTIME_TEAM_INTENT_WORKER", "true")
		t.Setenv("RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL", "postgres://example")
		t.Setenv("RUNTIME_INTENT_STARTUP_TIMEOUT", "200ms")

		provCfg := runtimeapi.TeamIntentProvisionerConfig{Client: kubernetesfake.NewSimpleClientset()}
		attempts := 0
		runner, cancel, err := startOptInTeamIntentWorkerWith(
			provCfg,
			true,
			configuredPlatformClient(),
			func(_ context.Context, _ teamintentworker.RunnerConfig) (*teamintentworker.Runner, context.CancelFunc, error) {
				attempts++
				if attempts < 3 {
					return nil, nil, errors.New("database not ready")
				}
				return &teamintentworker.Runner{}, func() {}, nil
			},
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if runner == nil || cancel == nil {
			t.Fatalf("expected runner and cancel, got runner=%v cancel=%v", runner, cancel)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})

	t.Run("team worker fails fast on config error without invoking starter", func(t *testing.T) {
		t.Setenv("RUNTIME_TEAM_INTENT_WORKER", "true")
		t.Setenv("RUNTIME_TEAM_INTENT_QUEUE_DATABASE_URL", "postgres://example")
		t.Setenv("RUNTIME_INTENT_STARTUP_TIMEOUT", "200ms")

		attempts := 0
		_, _, err := startOptInTeamIntentWorkerWith(
			runtimeapi.TeamIntentProvisionerConfig{},
			false,
			configuredPlatformClient(),
			func(_ context.Context, _ teamintentworker.RunnerConfig) (*teamintentworker.Runner, context.CancelFunc, error) {
				attempts++
				return &teamintentworker.Runner{}, func() {}, nil
			},
		)
		if err == nil {
			t.Fatal("expected error when kubernetes client is missing")
		}
		if attempts != 0 {
			t.Fatalf("expected 0 starter attempts on config error, got %d", attempts)
		}
	})

	t.Run("access worker retries transient errors and succeeds", func(t *testing.T) {
		t.Setenv("RUNTIME_ACCESS_INTENT_WORKER", "true")
		t.Setenv("RUNTIME_ACCESS_INTENT_POSTGRES_DSN", "postgres://access-dsn")
		t.Setenv("RUNTIME_INTENT_STARTUP_TIMEOUT", "200ms")

		attempts := 0
		runner, cancel, err := startOptInAccessIntentWorkerWith(
			testK8sClients(),
			configuredPlatformClient(),
			func(_ context.Context, _ accessintentworker.RunnerConfig) (*accessintentworker.Runner, context.CancelFunc, error) {
				attempts++
				if attempts < 3 {
					return nil, nil, errors.New("access_intent_queue not migrated yet")
				}
				return &accessintentworker.Runner{}, func() {}, nil
			},
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if runner == nil || cancel == nil {
			t.Fatalf("expected runner and cancel, got runner=%v cancel=%v", runner, cancel)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})

	t.Run("access worker fails fast on missing DSN without retrying", func(t *testing.T) {
		t.Setenv("RUNTIME_ACCESS_INTENT_WORKER", "true")
		t.Setenv("RUNTIME_ACCESS_INTENT_POSTGRES_DSN", "")
		t.Setenv("PLATFORM_DB_DSN", "")
		t.Setenv("DATABASE_URL", "")
		t.Setenv("RUNTIME_INTENT_STARTUP_TIMEOUT", "200ms")

		attempts := 0
		_, _, err := startOptInAccessIntentWorkerWith(
			testK8sClients(),
			configuredPlatformClient(),
			func(_ context.Context, _ accessintentworker.RunnerConfig) (*accessintentworker.Runner, context.CancelFunc, error) {
				attempts++
				return &accessintentworker.Runner{}, func() {}, nil
			},
		)
		if err == nil {
			t.Fatal("expected error when access intent DSN is missing")
		}
		if attempts != 0 {
			t.Fatalf("expected 0 starter attempts on missing DSN, got %d", attempts)
		}
	})

	t.Run("access worker fails fast on missing platform client without retrying", func(t *testing.T) {
		t.Setenv("RUNTIME_ACCESS_INTENT_WORKER", "true")
		t.Setenv("RUNTIME_ACCESS_INTENT_POSTGRES_DSN", "postgres://access-dsn")
		t.Setenv("RUNTIME_INTENT_STARTUP_TIMEOUT", "200ms")

		attempts := 0
		_, _, err := startOptInAccessIntentWorkerWith(
			testK8sClients(),
			&platformapi.Client{},
			func(_ context.Context, _ accessintentworker.RunnerConfig) (*accessintentworker.Runner, context.CancelFunc, error) {
				attempts++
				return &accessintentworker.Runner{}, func() {}, nil
			},
		)
		if err == nil {
			t.Fatal("expected error when platform client is unconfigured")
		}
		if attempts != 0 {
			t.Fatalf("expected 0 starter attempts on missing platform client, got %d", attempts)
		}
	})

	t.Run("workload worker retries transient errors and returns timeout error", func(t *testing.T) {
		t.Setenv("RUNTIME_WORKLOAD_INTENT_WORKER", "true")
		t.Setenv("PLATFORM_WORKLOAD_INTENT_DB_DSN", "postgres://workload-dsn")
		t.Setenv("RUNTIME_INTENT_STARTUP_TIMEOUT", "200ms")

		attempts := 0
		runner, cancel, err := startOptInWorkloadIntentWorkerWith(
			testK8sClients(),
			nil,
			configuredPlatformClient(),
			func(_ context.Context, _ workloadintentworker.RunnerConfig, _ workloadintentworker.Dependencies) (*workloadintentworker.Runner, context.CancelFunc, error) {
				attempts++
				if attempts < 3 {
					return nil, nil, errors.New("workload_intent_queue not ready")
				}
				return &workloadintentworker.Runner{}, func() {}, nil
			},
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if runner == nil || cancel == nil {
			t.Fatalf("expected runner and cancel, got runner=%v cancel=%v", runner, cancel)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}

		t.Setenv("RUNTIME_INTENT_STARTUP_TIMEOUT", "15ms")
		timeoutErr := errors.New("still migrating")
		_, _, err = startOptInWorkloadIntentWorkerWith(
			testK8sClients(),
			nil,
			configuredPlatformClient(),
			func(_ context.Context, _ workloadintentworker.RunnerConfig, _ workloadintentworker.Dependencies) (*workloadintentworker.Runner, context.CancelFunc, error) {
				return nil, nil, timeoutErr
			},
		)
		if !errors.Is(err, timeoutErr) {
			t.Fatalf("expected timeout error %v, got %v", timeoutErr, err)
		}
	})
}

func TestOptInWorkloadIntentWorkerAndObserverStayOffByDefault(t *testing.T) {
	t.Setenv("RUNTIME_WORKLOAD_INTENT_WORKER", "")
	t.Setenv("RUNTIME_WORKLOAD_INTENT_OBSERVER", "")

	runner, cancel, err := startOptInWorkloadIntentWorker(nil, &platformapi.Client{})
	if err != nil || runner != nil || cancel != nil {
		t.Fatalf("expected workload worker disabled by default, got runner=%v cancel=%v err=%v", runner, cancel, err)
	}

	obsCancel, obsDone, err := startOptInWorkloadIntentObserver(nil, &platformapi.Client{})
	if err != nil || obsCancel != nil || obsDone != nil {
		t.Fatalf("expected workload observer disabled by default, got cancel=%v done=%v err=%v", obsCancel, obsDone, err)
	}
}

func TestOptInWorkloadIntentWorkerFailsFastWithoutPrerequisites(t *testing.T) {
	withFastIntentStartupRetry(t, time.Millisecond, time.Second)

	t.Run("invalid boolean", func(t *testing.T) {
		t.Setenv("RUNTIME_WORKLOAD_INTENT_WORKER", "yes")
		if _, _, err := startOptInWorkloadIntentWorker(nil, configuredPlatformClient()); err == nil {
			t.Fatal("expected boolean validation error")
		}
	})

	t.Run("missing DSN", func(t *testing.T) {
		t.Setenv("RUNTIME_WORKLOAD_INTENT_WORKER", "true")
		t.Setenv("PLATFORM_WORKLOAD_INTENT_DB_DSN", "")
		t.Setenv("PLATFORM_DB_DSN", "")
		t.Setenv("DATABASE_URL", "")
		if _, _, err := startOptInWorkloadIntentWorkerWith(testK8sClients(), nil, configuredPlatformClient(), nil); err == nil {
			t.Fatal("expected error when workload worker DSN is missing")
		}
	})

	t.Run("missing platform client", func(t *testing.T) {
		t.Setenv("RUNTIME_WORKLOAD_INTENT_WORKER", "true")
		t.Setenv("PLATFORM_WORKLOAD_INTENT_DB_DSN", "postgres://workload-dsn")
		if _, _, err := startOptInWorkloadIntentWorkerWith(testK8sClients(), nil, &platformapi.Client{}, nil); err == nil {
			t.Fatal("expected error when platform client is unconfigured")
		}
	})

	t.Run("missing kubernetes client", func(t *testing.T) {
		t.Setenv("RUNTIME_WORKLOAD_INTENT_WORKER", "true")
		t.Setenv("PLATFORM_WORKLOAD_INTENT_DB_DSN", "postgres://workload-dsn")
		if _, _, err := startOptInWorkloadIntentWorker(nil, configuredPlatformClient()); err == nil {
			t.Fatal("expected error when kubernetes client is missing")
		}
		if _, _, err := startOptInWorkloadIntentWorkerWith(&k8sclient.Clients{Clientset: kubernetesfake.NewSimpleClientset()}, nil, configuredPlatformClient(), nil); err == nil {
			t.Fatal("expected error when kubernetes dynamic client is missing")
		}
		if _, _, err := startOptInWorkloadIntentWorkerWith(&k8sclient.Clients{Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}, nil, configuredPlatformClient(), nil); err == nil {
			t.Fatal("expected error when kubernetes clientset is missing")
		}
	})

	t.Run("DSN fallback chain", func(t *testing.T) {
		cases := []struct {
			name     string
			workload string
			platform string
			dbURL    string
			wantDSN  string
		}{
			{
				name:     "prefers PLATFORM_WORKLOAD_INTENT_DB_DSN",
				workload: "postgres://workload-dsn",
				platform: "postgres://platform-dsn",
				dbURL:    "postgres://database-url",
				wantDSN:  "postgres://workload-dsn",
			},
			{
				name:     "falls back to PLATFORM_DB_DSN",
				workload: "",
				platform: "postgres://platform-dsn",
				dbURL:    "postgres://database-url",
				wantDSN:  "postgres://platform-dsn",
			},
			{
				name:     "falls back to DATABASE_URL",
				workload: "",
				platform: "",
				dbURL:    "postgres://database-url",
				wantDSN:  "postgres://database-url",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("RUNTIME_WORKLOAD_INTENT_WORKER", "true")
				t.Setenv("PLATFORM_WORKLOAD_INTENT_DB_DSN", tc.workload)
				t.Setenv("PLATFORM_DB_DSN", tc.platform)
				t.Setenv("DATABASE_URL", tc.dbURL)

				var gotDSN string
				_, _, err := startOptInWorkloadIntentWorkerWith(
					testK8sClients(),
					nil,
					configuredPlatformClient(),
					func(_ context.Context, cfg workloadintentworker.RunnerConfig, _ workloadintentworker.Dependencies) (*workloadintentworker.Runner, context.CancelFunc, error) {
						gotDSN = cfg.DSN
						return &workloadintentworker.Runner{}, func() {}, nil
					},
				)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if gotDSN != tc.wantDSN {
					t.Fatalf("DSN = %q, want %q", gotDSN, tc.wantDSN)
				}
			})
		}
	})
}

func TestOptInWorkloadIntentObserverFailsFastWithoutPrerequisites(t *testing.T) {
	t.Run("invalid boolean", func(t *testing.T) {
		t.Setenv("RUNTIME_WORKLOAD_INTENT_OBSERVER", "yes")
		if _, _, err := startOptInWorkloadIntentObserver(nil, configuredPlatformClient()); err == nil {
			t.Fatal("expected boolean validation error")
		}
	})

	t.Run("missing platform client", func(t *testing.T) {
		t.Setenv("RUNTIME_WORKLOAD_INTENT_OBSERVER", "true")
		if _, _, err := startOptInWorkloadIntentObserverWith(testK8sClients(), &platformapi.Client{}, nil); err == nil {
			t.Fatal("expected error when platform client is unconfigured")
		}
	})

	t.Run("missing kubernetes client", func(t *testing.T) {
		t.Setenv("RUNTIME_WORKLOAD_INTENT_OBSERVER", "true")
		if _, _, err := startOptInWorkloadIntentObserver(nil, configuredPlatformClient()); err == nil {
			t.Fatal("expected error when kubernetes client is missing")
		}
		if _, _, err := startOptInWorkloadIntentObserverWith(&k8sclient.Clients{Clientset: kubernetesfake.NewSimpleClientset()}, configuredPlatformClient(), nil); err == nil {
			t.Fatal("expected error when kubernetes dynamic client is missing")
		}
		if _, _, err := startOptInWorkloadIntentObserverWith(&k8sclient.Clients{Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}, configuredPlatformClient(), nil); err == nil {
			t.Fatal("expected error when kubernetes clientset is missing")
		}
	})
}

func TestOptInTeamIntentObserverHonorsIntervalEnv(t *testing.T) {
	provCfg := runtimeapi.TeamIntentProvisionerConfig{Client: kubernetesfake.NewSimpleClientset()}

	cases := []struct {
		name         string
		rawInterval  string
		wantInterval time.Duration
	}{
		{name: "valid positive duration", rawInterval: "7s", wantInterval: 7 * time.Second},
		{name: "unset falls back to default", rawInterval: "", wantInterval: 0},
		{name: "invalid falls back to default", rawInterval: "not-a-duration", wantInterval: 0},
		{name: "zero falls back to default", rawInterval: "0s", wantInterval: 0},
		{name: "negative falls back to default", rawInterval: "-5s", wantInterval: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RUNTIME_TEAM_INTENT_OBSERVER", "true")
			t.Setenv("RUNTIME_TEAM_INTENT_OBSERVER_INTERVAL", tc.rawInterval)

			var gotCfg teamintentobserver.Config
			cancel, done, err := startOptInTeamIntentObserverWith(
				provCfg,
				true,
				configuredPlatformClient(),
				func(cfg teamintentobserver.Config) (teamObserverRunner, error) {
					gotCfg = cfg
					if _, err := teamintentobserver.New(cfg); err != nil {
						return nil, err
					}
					return fakeTeamObserver{}, nil
				},
			)
			if err != nil {
				t.Fatalf("startOptInTeamIntentObserverWith() error = %v", err)
			}
			cancel()
			<-done

			if gotCfg.VerifyInterval != tc.wantInterval {
				t.Fatalf("VerifyInterval = %v, want %v", gotCfg.VerifyInterval, tc.wantInterval)
			}
		})
	}
}

func TestOptInWorkloadIntentObserverHonorsIntervalEnv(t *testing.T) {
	cases := []struct {
		name             string
		primaryInterval  string
		fallbackInterval string
		wantInterval     time.Duration
	}{
		{name: "honors MCP_WORKLOAD_OBSERVER_INTERVAL", primaryInterval: "4s", fallbackInterval: "", wantInterval: 4 * time.Second},
		{name: "honors RUNTIME_WORKLOAD_INTENT_OBSERVER_INTERVAL fallback", primaryInterval: "", fallbackInterval: "6s", wantInterval: 6 * time.Second},
		{name: "prefers MCP_WORKLOAD_OBSERVER_INTERVAL over fallback", primaryInterval: "4s", fallbackInterval: "6s", wantInterval: 4 * time.Second},
		{name: "unset falls back safely", primaryInterval: "", fallbackInterval: "", wantInterval: 0},
		{name: "invalid falls back safely", primaryInterval: "bogus", fallbackInterval: "also-bogus", wantInterval: 0},
		{name: "non-positive falls back safely", primaryInterval: "-2s", fallbackInterval: "0s", wantInterval: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RUNTIME_WORKLOAD_INTENT_OBSERVER", "true")
			t.Setenv("MCP_WORKLOAD_OBSERVER_INTERVAL", tc.primaryInterval)
			t.Setenv("RUNTIME_WORKLOAD_INTENT_OBSERVER_INTERVAL", tc.fallbackInterval)

			var gotCfg workloadintentobserver.Config
			cancel, done, err := startOptInWorkloadIntentObserverWith(
				testK8sClients(),
				configuredPlatformClient(),
				func(p workloadintentobserver.PlatformClient, c workloadintentobserver.ClusterReader, cfg workloadintentobserver.Config) workloadObserverRunner {
					gotCfg = cfg
					_ = workloadintentobserver.New(p, c, cfg)
					return fakeWorkloadObserver{}
				},
			)
			if err != nil {
				t.Fatalf("startOptInWorkloadIntentObserverWith() error = %v", err)
			}
			cancel()
			<-done

			if gotCfg.PollInterval != tc.wantInterval {
				t.Fatalf("PollInterval = %v, want %v", gotCfg.PollInterval, tc.wantInterval)
			}
		})
	}
}

package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"mcp-runtime-api/internal/accessintentworker"
)

func TestAccessIntentWorkerStartupHonorsDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mu sync.Mutex
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	var closeOnce sync.Once
	closeConnections := func() {
		closeOnce.Do(func() {
			listener.Close()
			<-done
			mu.Lock()
			defer mu.Unlock()
			for _, conn := range conns {
				conn.Close()
			}
		})
	}
	release := time.AfterFunc(800*time.Millisecond, closeConnections)
	defer func() { release.Stop(); closeConnections() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	runner, stop, err := defaultStartAccessIntentWorker(ctx, accessintentworker.RunnerConfig{
		PostgresDSN:    "postgres://review:fixture@" + listener.Addr().String() + "/review?sslmode=disable&connect_timeout=1",
		PlatformClient: configuredPlatformClient(),
		DynamicClient:  testK8sClients().Dynamic,
	})
	if stop != nil {
		stop()
	}
	if runner != nil {
		_ = runner.Stop(context.Background())
	}
	if err == nil {
		t.Fatal("expected unavailable PostgreSQL to fail startup")
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("startup ignored its 100ms deadline: returned after %s: %v", elapsed, err)
	}
}

func TestIntentWorkerStartupContextLifecycle(t *testing.T) {
	t.Run("startup cancellation interrupts initialization", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		stop, err := startIntentWorker(ctx, func(runCtx context.Context) error {
			cancel()
			<-runCtx.Done()
			return runCtx.Err()
		})
		if stop != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("startup result: stop=%v err=%v, want cancellation", stop, err)
		}
	})
	t.Run("successful startup survives startup context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var workerCtx context.Context
		stop, err := startIntentWorker(ctx, func(runCtx context.Context) error {
			workerCtx = runCtx
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		cancel()
		if workerCtx.Err() != nil {
			t.Fatal("startup context stopped initialized worker")
		}
		stop()
		if !errors.Is(workerCtx.Err(), context.Canceled) {
			t.Fatal("worker cancellation did not stop worker")
		}
	})
	t.Run("failed initialization cancels its worker context", func(t *testing.T) {
		failure := errors.New("database unavailable")
		var workerCtx context.Context
		stop, err := startIntentWorker(context.Background(), func(runCtx context.Context) error {
			workerCtx = runCtx
			return failure
		})
		if stop != nil || !errors.Is(err, failure) || workerCtx.Err() == nil {
			t.Fatalf("failed startup result: stop=%v err=%v worker err=%v", stop, err, workerCtx.Err())
		}
	})
	t.Run("cancellation at completion cannot leave a running worker", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var workerCtx context.Context
		stop, err := startIntentWorker(ctx, func(runCtx context.Context) error {
			workerCtx = runCtx
			cancel()
			return nil
		})
		if stop != nil || !errors.Is(err, context.Canceled) || workerCtx.Err() == nil {
			t.Fatalf("canceled startup result: stop=%v err=%v worker err=%v", stop, err, workerCtx.Err())
		}
	})
}

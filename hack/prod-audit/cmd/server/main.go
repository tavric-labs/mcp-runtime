package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"mcp-runtime/hack/prod-audit/internal/fixture"
	"mcp-runtime/hack/prod-audit/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	if _, err := server.ConfiguredMCPPath(os.Getenv("MCP_PATH")); err != nil {
		logger.Error("invalid MCP path", "error", err)
		os.Exit(1)
	}
	jsonResponse := false
	if value := os.Getenv("PROD_AUDIT_JSON_RESPONSE"); value != "" {
		var err error
		jsonResponse, err = strconv.ParseBool(value)
		if err != nil {
			logger.Error("PROD_AUDIT_JSON_RESPONSE must be true or false")
			os.Exit(1)
		}
	}
	addr := os.Getenv("PROD_AUDIT_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	store := fixture.NewStore(time.Now)
	service := &server.Service{Store: store, Logger: logger, JSONResponse: jsonResponse}
	httpServer := &http.Server{Addr: addr, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				store.Inspect()
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = httpServer.Shutdown(shutdown)
				return
			}
		}
	}()
	logger.Info("prod-audit starting", "listen", addr, "version", server.Version, "json_response", jsonResponse)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

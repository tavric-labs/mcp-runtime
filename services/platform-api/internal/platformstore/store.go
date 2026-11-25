package platformstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"mcp-platform-api/internal/workloadintent"
	"mcp-runtime/pkg/passport"
	"mcp-runtime/pkg/telemetry/sqltrace"
)

// Open connects to Postgres, applies schema migrations, and returns a Store.
func Open(ctx context.Context, dsn string, signer passport.Signer, verifier passport.Verifier) (*Store, error) {
	var journalWorkloadIntent bool
	if value := strings.TrimSpace(os.Getenv("PLATFORM_WORKLOAD_INTENT_JOURNAL")); value != "" {
		var err error
		journalWorkloadIntent, err = strconv.ParseBool(value)
		if err != nil {
			return nil, errors.New("PLATFORM_WORKLOAD_INTENT_JOURNAL must be a boolean")
		}
	}
	db, err := sqltrace.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(intEnvOrDefault("PLATFORM_DB_MAX_CONNS", defaultDBMaxConns))
	db.SetMaxIdleConns(intEnvOrDefault("PLATFORM_DB_MAX_IDLE_CONNS", defaultDBMaxIdle))
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db, signer: signer, verifier: verifier}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if journalWorkloadIntent {
		s.workloadIntents, err = workloadintent.Open(ctx, db, dsn)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize workload intent journal: %w", err)
		}
	}
	if err := s.initializeSessions(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// NewForTest returns an in-memory Store shell for tests that stub persistence.
func NewForTest(signer passport.Signer, verifier passport.Verifier) *Store {
	return &Store{signer: signer, verifier: verifier}
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, platformSchemaSQL+credentialSchemaSQL)
	return err
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ping checks Postgres connectivity for readiness probes.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("platform store not configured")
	}
	return s.db.PingContext(ctx)
}

func intEnvOrDefault(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

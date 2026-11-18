// Package fixture owns bounded, expiring synthetic test data. It has no access
// to platform credentials, other servers, the filesystem, or external services.
package fixture

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

const (
	MaxRuns       = 128
	MaxRecords    = 64
	MaxValueBytes = 4096
	DefaultTTL    = 15 * time.Minute
	MaxTTL        = time.Hour
)

var (
	ErrNotFound = errors.New("run or record not found (it may have expired)")
	ErrConflict = errors.New("revision conflict")
	ErrCapacity = errors.New("fixture capacity reached; close a run or remove a record")
	validID     = regexp.MustCompile(`^[a-f0-9]{32}$`)
	validKey    = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

type Record struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Revision uint64 `json:"revision"`
}

type RunInfo struct {
	ID        string    `json:"run_id"`
	Label     string    `json:"label"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Stats struct {
	Runs    int `json:"active_runs"`
	Records int `json:"records"`
}

type run struct {
	RunInfo
	records map[string]Record
	// A monotonic revision survives record deletion, preventing ABA updates.
	revision uint64
}

type Store struct {
	mu   sync.Mutex
	now  func() time.Time
	runs map[string]*run
}

func NewStore(now func() time.Time) *Store {
	return &Store{now: now, runs: make(map[string]*run)}
}

func (s *Store) prune() {
	now := s.now()
	for id, r := range s.runs {
		if !now.Before(r.ExpiresAt) {
			delete(s.runs, id)
		}
	}
}

func (s *Store) Open(label string, ttl time.Duration) (RunInfo, error) {
	if len(label) > 80 {
		return RunInfo{}, fmt.Errorf("label exceeds 80 bytes")
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < time.Second || ttl > MaxTTL {
		return RunInfo{}, fmt.Errorf("ttl_seconds must be between 1 and 3600")
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return RunInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	if len(s.runs) >= MaxRuns {
		return RunInfo{}, ErrCapacity
	}
	info := RunInfo{ID: hex.EncodeToString(entropy[:]), Label: label, ExpiresAt: s.now().UTC().Add(ttl)}
	s.runs[info.ID] = &run{RunInfo: info, records: make(map[string]Record)}
	return info, nil
}

func (s *Store) lookup(id string) (*run, error) {
	if !validID.MatchString(id) {
		return nil, fmt.Errorf("invalid run_id")
	}
	s.prune()
	r, ok := s.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return r, nil
}

func (s *Store) Put(id, key, value string, expected *uint64) (Record, error) {
	if !validKey.MatchString(key) {
		return Record{}, fmt.Errorf("key must be 1 to 64 ASCII letters, digits, underscores or hyphens")
	}
	if len(value) > MaxValueBytes {
		return Record{}, fmt.Errorf("value exceeds %d bytes", MaxValueBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.lookup(id)
	if err != nil {
		return Record{}, err
	}
	old, exists := r.records[key]
	if expected != nil && *expected != old.Revision {
		return Record{}, ErrConflict
	}
	if exists && old.Value == value {
		return old, nil
	}
	if !exists && len(r.records) >= MaxRecords {
		return Record{}, ErrCapacity
	}
	r.revision++
	next := Record{Key: key, Value: value, Revision: r.revision}
	r.records[key] = next
	return next, nil
}

func (s *Store) Get(id, key string) (Record, error) {
	if !validKey.MatchString(key) {
		return Record{}, fmt.Errorf("invalid key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.lookup(id)
	if err != nil {
		return Record{}, err
	}
	record, ok := r.records[key]
	if !ok {
		return Record{}, ErrNotFound
	}
	return record, nil
}

func (s *Store) Delete(id, key string, expected *uint64) (bool, error) {
	if !validKey.MatchString(key) {
		return false, fmt.Errorf("invalid key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.lookup(id)
	if err != nil {
		return false, err
	}
	old, exists := r.records[key]
	if expected != nil && *expected != old.Revision {
		return false, ErrConflict
	}
	delete(r.records, key)
	return exists, nil
}

func (s *Store) Close(id string) (bool, error) {
	if !validID.MatchString(id) {
		return false, fmt.Errorf("invalid run_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	_, exists := s.runs[id]
	delete(s.runs, id)
	return exists, nil
}

func (s *Store) Inspect() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	stats := Stats{Runs: len(s.runs)}
	for _, r := range s.runs {
		stats.Records += len(r.records)
	}
	return stats
}

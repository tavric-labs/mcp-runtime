package fixture

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunIsolationExpiryAndClose(t *testing.T) {
	now := time.Unix(1000, 0)
	s := NewStore(func() time.Time { return now })
	a, err := s.Open("case-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Open("case-b", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || len(a.ID) != 32 {
		t.Fatal("run identifiers must be unique and unguessable")
	}
	if _, err := s.Put(a.ID, "probe", "synthetic", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(b.ID, "probe"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-run read: %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := s.Get(a.ID, "probe"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired read: %v", err)
	}
	if _, err := s.Put(a.ID, "probe", "revive", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired write: %v", err)
	}
	if stats := s.Inspect(); stats.Runs != 1 || stats.Records != 0 {
		t.Fatalf("expired data retained: %+v", stats)
	}
	if removed, err := s.Close(b.ID); err != nil || !removed {
		t.Fatalf("close: %v %v", removed, err)
	}
	if removed, err := s.Close(b.ID); err != nil || removed {
		t.Fatalf("idempotent close: %v %v", removed, err)
	}
	if stats := s.Inspect(); stats.Runs != 0 {
		t.Fatalf("not cleaned up: %+v", stats)
	}
}

func TestCompareAndSwapAndIdempotency(t *testing.T) {
	s := NewStore(time.Now)
	run, _ := s.Open("cas", time.Minute)
	zero := uint64(0)
	first, err := s.Put(run.ID, "key", "one", &zero)
	if err != nil || first.Revision != 1 {
		t.Fatalf("create: %+v %v", first, err)
	}
	if _, err := s.Put(run.ID, "key", "two", &zero); !errors.Is(err, ErrConflict) {
		t.Fatalf("create overwrote: %v", err)
	}
	same, err := s.Put(run.ID, "key", "one", nil)
	if err != nil || same.Revision != first.Revision {
		t.Fatalf("retry changed revision: %+v %v", same, err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Put(run.ID, "key", "two", &first.Revision); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("CAS: %v", err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("CAS winners: %d", successes.Load())
	}
	if _, err := s.Delete(run.ID, "key", &first.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if removed, err := s.Delete(run.ID, "key", nil); err != nil || !removed {
		t.Fatalf("delete: %v %v", removed, err)
	}
	if removed, err := s.Delete(run.ID, "key", nil); err != nil || removed {
		t.Fatalf("repeat delete: %v %v", removed, err)
	}
}

func TestLimitsBoundMemoryAndRejectInvalidInputs(t *testing.T) {
	s := NewStore(time.Now)
	if _, err := s.Open("bad", time.Hour+time.Second); err == nil {
		t.Fatal("accepted unbounded TTL")
	}
	run, _ := s.Open("bounded", time.Minute)
	if _, err := s.Put(run.ID, "../escape", "x", nil); err == nil {
		t.Fatal("accepted invalid key")
	}
	if _, err := s.Put(run.ID, "key", strings.Repeat("x", MaxValueBytes+1), nil); err == nil {
		t.Fatal("accepted oversize value")
	}
	for i := 0; i < MaxRecords; i++ {
		if _, err := s.Put(run.ID, strings.Repeat("a", i+1), "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(run.ID, "overflow", "x", nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("record capacity: %v", err)
	}
	if _, err := s.Put(run.ID, "a", "updated", nil); err != nil {
		t.Fatalf("update at capacity: %v", err)
	}
	for i := 1; i < MaxRuns; i++ {
		if _, err := s.Open("bounded", time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Open("overflow", time.Minute); !errors.Is(err, ErrCapacity) {
		t.Fatalf("run capacity: %v", err)
	}
}

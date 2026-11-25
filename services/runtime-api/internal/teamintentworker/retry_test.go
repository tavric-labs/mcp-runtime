package teamintentworker

import (
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"
)

func TestRetryPolicyExponentialBackoffCapAndJitter(t *testing.T) {
	fixedNow := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	t.Run("exponential growth capped at MaxDelay without jitter", func(t *testing.T) {
		policy := &RetryPolicy{
			BaseDelay:      1 * time.Second,
			MaxDelay:       30 * time.Second,
			JitterFraction: 0.2,
			Now:            func() time.Time { return fixedNow },
			RandFloat64:    func() float64 { return 0.0 },
		}
		wants := []struct {
			attempt int
			delay   time.Duration
		}{
			{attempt: 0, delay: 1 * time.Second},
			{attempt: 1, delay: 1 * time.Second},
			{attempt: 2, delay: 2 * time.Second},
			{attempt: 3, delay: 4 * time.Second},
			{attempt: 4, delay: 8 * time.Second},
			{attempt: 5, delay: 16 * time.Second},
			{attempt: 6, delay: 30 * time.Second},
			{attempt: 50, delay: 30 * time.Second},
		}
		for _, tc := range wants {
			got := policy.NextRetry(&rivertype.JobRow{Attempt: tc.attempt}).Sub(fixedNow)
			if got != tc.delay {
				t.Fatalf("attempt %d delay = %v, want %v", tc.attempt, got, tc.delay)
			}
		}
	})

	t.Run("applies bounded jitter", func(t *testing.T) {
		policy := &RetryPolicy{
			BaseDelay:      1 * time.Second,
			MaxDelay:       30 * time.Second,
			JitterFraction: 0.2,
			Now:            func() time.Time { return fixedNow },
			RandFloat64:    func() float64 { return 1.0 },
		}
		got := policy.NextRetry(&rivertype.JobRow{Attempt: 6}).Sub(fixedNow)
		if got != 24*time.Second {
			t.Fatalf("attempt 6 max-jitter delay = %v, want 24s", got)
		}
	})
}

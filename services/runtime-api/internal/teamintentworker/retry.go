package teamintentworker

import (
	"math/rand/v2"
	"time"

	"github.com/riverqueue/river/rivertype"
)

const (
	defaultRetryBaseDelay      = 1 * time.Second
	defaultRetryMaxDelay       = 30 * time.Second
	defaultRetryJitterFraction = 0.2
)

// RetryPolicy implements river.ClientRetryPolicy with capped exponential
// backoff and bounded jitter so transient Kubernetes outages back off cleanly
// without thundering herds or unbounded delays.
type RetryPolicy struct {
	BaseDelay      time.Duration
	MaxDelay       time.Duration
	JitterFraction float64
	Now            func() time.Time
	RandFloat64    func() float64
}

// NewRetryPolicy returns a capped exponential backoff retry policy with jitter.
func NewRetryPolicy() *RetryPolicy {
	return &RetryPolicy{
		BaseDelay:      defaultRetryBaseDelay,
		MaxDelay:       defaultRetryMaxDelay,
		JitterFraction: defaultRetryJitterFraction,
	}
}

// NextRetry computes the next retry timestamp for job using capped exponential
// backoff with downward jitter in [(1-JitterFraction)*delay, delay].
func (p *RetryPolicy) NextRetry(job *rivertype.JobRow) time.Time {
	nowFn := time.Now
	if p != nil && p.Now != nil {
		nowFn = p.Now
	}
	base := defaultRetryBaseDelay
	maxDelay := defaultRetryMaxDelay
	jitter := defaultRetryJitterFraction
	randFn := rand.Float64
	if p != nil {
		if p.BaseDelay > 0 {
			base = p.BaseDelay
		}
		if p.MaxDelay > 0 {
			maxDelay = p.MaxDelay
		}
		if p.JitterFraction >= 0 && p.JitterFraction <= 1 {
			jitter = p.JitterFraction
		}
		if p.RandFloat64 != nil {
			randFn = p.RandFloat64
		}
	}
	if maxDelay < base {
		maxDelay = base
	}

	attempt := 1
	if job != nil && job.Attempt > 1 {
		attempt = job.Attempt
	}

	delay := base
	for i := 1; i < attempt; i++ {
		if delay >= maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}

	if jitter > 0 {
		r := randFn()
		if r < 0 {
			r = 0
		} else if r > 1 {
			r = 1
		}
		delay = time.Duration(float64(delay) * (1.0 - jitter*r))
		if delay < time.Millisecond {
			delay = time.Millisecond
		}
	}
	return nowFn().UTC().Add(delay)
}

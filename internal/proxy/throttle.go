package proxy

import (
	"context"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
)

// rateLimiter implements a token-bucket algorithm for bandwidth throttling.
// It is safe for concurrent use; multiple SPDY streams share one limiter.
type rateLimiter struct {
	bytesPerSec int64
	mu          sync.Mutex
	tokens      float64
	lastRefill  time.Time
}

// newRateLimiter creates a rate limiter that allows bytesPerSec throughput
// with a burst capacity of one second.
func newRateLimiter(bytesPerSec int64) *rateLimiter {
	return &rateLimiter{
		bytesPerSec: bytesPerSec,
		tokens:      float64(bytesPerSec), // start with 1 second of burst
		lastRefill:  time.Now(),
	}
}

// Wait consumes n tokens from the bucket, sleeping if necessary until enough
// tokens are available. It returns ctx.Err() if the context is cancelled
// before the wait completes.
func (r *rateLimiter) Wait(ctx context.Context, n int) error {
	r.mu.Lock()

	// Refill tokens based on elapsed time.
	now := time.Now()
	elapsed := now.Sub(r.lastRefill).Seconds()
	r.tokens += elapsed * float64(r.bytesPerSec)
	r.lastRefill = now

	// Cap tokens at 1 second of burst.
	cap := float64(r.bytesPerSec)
	if r.tokens > cap {
		r.tokens = cap
	}

	// Consume tokens.
	r.tokens -= float64(n)

	// If we have enough tokens, no sleep needed.
	if r.tokens >= 0 {
		r.mu.Unlock()
		return nil
	}

	// Calculate how long to sleep to replenish the deficit.
	deficit := -r.tokens
	sleepDur := time.Duration(deficit / float64(r.bytesPerSec) * float64(time.Second))
	r.mu.Unlock()

	return sleepWithContext(ctx, sleepDur)
}

// throttledStream wraps an httpstream.Stream to inject latency/jitter and
// apply bandwidth throttling.
type throttledStream struct {
	stream  httpstream.Stream
	ctx     context.Context
	latency time.Duration
	jitter  time.Duration
	limiter *rateLimiter // nil means no bandwidth cap; shared across streams in a forward
}

func (s *throttledStream) Write(p []byte) (int, error) {
	// Inject latency with jitter on writes.
	if s.latency > 0 {
		delay := s.latency + randJitter(s.jitter)
		if delay < 0 {
			delay = 0
		}
		if delay > 0 {
			if err := sleepWithContext(s.ctx, delay); err != nil {
				return 0, err
			}
		}
	}

	// Apply bandwidth throttling.
	if s.limiter != nil {
		if err := s.limiter.Wait(s.ctx, len(p)); err != nil {
			return 0, err
		}
	}

	return s.stream.Write(p)
}

func (s *throttledStream) Read(p []byte) (int, error) {
	n, err := s.stream.Read(p)

	// Rate-limit ingress after read. Preserve the original error (e.g. io.EOF)
	// when the limiter wait also fails (e.g. context cancelled during shutdown).
	if s.limiter != nil && n > 0 {
		if waitErr := s.limiter.Wait(s.ctx, n); waitErr != nil && err == nil {
			err = waitErr
		}
	}

	return n, err
}

func (s *throttledStream) Close() error         { return s.stream.Close() }
func (s *throttledStream) Reset() error         { return s.stream.Reset() }
func (s *throttledStream) Headers() http.Header { return s.stream.Headers() }
func (s *throttledStream) Identifier() uint32   { return s.stream.Identifier() }

// randJitter returns a uniform random duration in [-jitter, +jitter].
func randJitter(jitter time.Duration) time.Duration {
	if jitter == 0 {
		return 0
	}
	// rand.Int64N returns [0, n). We map it to [-jitter, +jitter].
	return time.Duration(rand.Int64N(int64(2*jitter+1)) - int64(jitter)) // #nosec G404 -- math/rand is fine for jitter
}

// sleepWithContext sleeps for d but returns early with ctx.Err() if the
// context is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

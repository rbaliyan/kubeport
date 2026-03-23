package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// mockStream implements httpstream.Stream for testing.
type mockStream struct {
	readData  []byte
	readPos   int
	writeData []byte
}

func (s *mockStream) Read(p []byte) (int, error) {
	if s.readPos >= len(s.readData) {
		return 0, nil
	}
	n := copy(p, s.readData[s.readPos:])
	s.readPos += n
	return n, nil
}

func (s *mockStream) Write(p []byte) (int, error) {
	s.writeData = append(s.writeData, p...)
	return len(p), nil
}

func (s *mockStream) Close() error         { return nil }
func (s *mockStream) Reset() error         { return nil }
func (s *mockStream) Headers() http.Header { return http.Header{} }
func (s *mockStream) Identifier() uint32   { return 0 }

func TestRateLimiter_Wait(t *testing.T) {
	// 10KB/s limiter; write 10KB should take ~1 second.
	limiter := newRateLimiter(10_000)
	// Drain the initial burst tokens.
	ctx := context.Background()
	if err := limiter.Wait(ctx, 10_000); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := limiter.Wait(ctx, 10_000); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if elapsed < 800*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("expected ~1s, got %v", elapsed)
	}
}

func TestRateLimiter_ContextCancellation(t *testing.T) {
	limiter := newRateLimiter(100)
	ctx := context.Background()
	// Drain burst.
	_ = limiter.Wait(ctx, 100)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := limiter.Wait(ctx, 10_000)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestThrottledStream_LatencyInjection(t *testing.T) {
	ms := &mockStream{}
	ts := &throttledStream{
		stream:  ms,
		ctx:     context.Background(),
		latency: 50 * time.Millisecond,
		jitter:  0,
	}

	data := []byte("hello")
	start := time.Now()
	n, err := ts.Write(data)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if n != len(data) {
		t.Fatalf("expected %d bytes, got %d", len(data), n)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected at least 40ms latency, got %v", elapsed)
	}
}

func TestThrottledStream_JitterVariance(t *testing.T) {
	// Run multiple writes and verify we see variance.
	var durations []time.Duration
	for i := 0; i < 20; i++ {
		ms := &mockStream{}
		ts := &throttledStream{
			stream:  ms,
			ctx:     context.Background(),
			latency: 50 * time.Millisecond,
			jitter:  20 * time.Millisecond,
		}
		start := time.Now()
		_, _ = ts.Write([]byte("x"))
		durations = append(durations, time.Since(start))
	}

	// Check we have some variance (min and max should differ by > 5ms).
	minD, maxD := durations[0], durations[0]
	for _, d := range durations[1:] {
		if d < minD {
			minD = d
		}
		if d > maxD {
			maxD = d
		}
	}
	if maxD-minD < 5*time.Millisecond {
		t.Fatalf("expected jitter variance, but range was only %v", maxD-minD)
	}
}

func TestThrottledStream_NoLatency_NoDelay(t *testing.T) {
	ms := &mockStream{}
	ts := &throttledStream{
		stream:  ms,
		ctx:     context.Background(),
		latency: 0,
		jitter:  0,
		limiter: nil,
	}

	start := time.Now()
	_, err := ts.Write([]byte("fast"))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 5*time.Millisecond {
		t.Fatalf("expected no delay, got %v", elapsed)
	}
}

func TestThrottledStream_ReadWithBandwidthCap(t *testing.T) {
	data := make([]byte, 5000)
	ms := &mockStream{readData: data}
	limiter := newRateLimiter(5000)
	// Drain the initial burst.
	_ = limiter.Wait(context.Background(), 5000)

	ts := &throttledStream{
		stream:  ms,
		ctx:     context.Background(),
		limiter: limiter,
	}

	buf := make([]byte, 5000)
	start := time.Now()
	_, err := ts.Read(buf)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 800*time.Millisecond {
		t.Fatalf("expected ~1s rate limiting on read, got %v", elapsed)
	}
}

func TestRandJitter_Zero(t *testing.T) {
	if j := randJitter(0); j != 0 {
		t.Fatalf("expected 0, got %v", j)
	}
}

func TestRandJitter_Range(t *testing.T) {
	jitter := 10 * time.Millisecond
	for i := 0; i < 100; i++ {
		j := randJitter(jitter)
		if j < -jitter || j > jitter {
			t.Fatalf("jitter %v out of range [-%v, %v]", j, jitter, jitter)
		}
	}
}

func TestSleepWithContext_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := sleepWithContext(ctx, 5*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("expected fast return, got %v", elapsed)
	}
}

func TestSleepWithContext_ZeroDuration(t *testing.T) {
	if err := sleepWithContext(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}

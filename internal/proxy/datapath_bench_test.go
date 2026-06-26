package proxy

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"k8s.io/streaming/pkg/httpstream"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// benchStream is a minimal httpstream.Stream for data-plane benchmarks. Write
// reports the full length consumed and Read fills the buffer, so the wrapper
// accounting (counting/chaos/throttle) is exercised without a live SPDY tunnel.
// It is named distinctly from the test-only fakeStream (whose Read returns 0) to
// avoid a redeclaration in package proxy and to provide a non-zero Read.
type benchStream struct{}

func (benchStream) Read(p []byte) (int, error)  { return len(p), nil }
func (benchStream) Write(p []byte) (int, error) { return len(p), nil }
func (benchStream) Close() error                { return nil }
func (benchStream) Reset() error                { return nil }
func (benchStream) Headers() http.Header        { return nil }
func (benchStream) Identifier() uint32          { return 0 }

var _ httpstream.Stream = benchStream{}

// BenchmarkRateLimiterWait measures the no-sleep fast path of the token-bucket
// limiter. The limiter is constructed with an enormous bytesPerSec so the bucket
// never runs dry within the benchmark, isolating the lock + refill arithmetic
// paid on every relayed write. The sleeping (throttled) path is intentionally
// not benchmarked.
func BenchmarkRateLimiterWait(b *testing.B) {
	const huge = int64(1) << 40 // ~1 TiB/s: never sleeps for benchmark write sizes
	ctx := context.Background()
	rl := newRateLimiter(huge)

	b.ReportAllocs()
	for b.Loop() {
		if err := rl.Wait(ctx, 4096); err != nil {
			b.Fatalf("Wait: %v", err)
		}
	}
}

// BenchmarkRateLimiterWaitParallel measures contention on the shared limiter
// mutex when many streams call Wait concurrently (the multi-stream case).
func BenchmarkRateLimiterWaitParallel(b *testing.B) {
	const huge = int64(1) << 40
	ctx := context.Background()
	rl := newRateLimiter(huge)

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := rl.Wait(ctx, 4096); err != nil {
				b.Fatalf("Wait: %v", err)
			}
		}
	})
}

// BenchmarkChaosStreamWrite measures the per-write overhead of the chaos wrapper.
// The "disabled" row is the key number: it is the atomic.Pointer.Load + branch
// cost paid on every relayed write when chaos is switched off. The
// "enabled-no-injection" row pays the additional rate-comparison branches with
// zero probabilities so no error or spike is actually injected.
func BenchmarkChaosStreamWrite(b *testing.B) {
	payload := make([]byte, 4096)

	rows := []struct {
		name string
		cfg  *config.ParsedChaosConfig
	}{
		{"disabled", nil},
		{"enabled-no-injection", &config.ParsedChaosConfig{Enabled: true}},
	}

	for _, row := range rows {
		var ptr atomic.Pointer[config.ParsedChaosConfig]
		if row.cfg != nil {
			ptr.Store(row.cfg)
		}
		s := &chaosStream{
			stream:   benchStream{},
			ctx:      context.Background(),
			cfgPtr:   &ptr,
			counters: &chaosCounters{},
		}
		b.Run(row.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				if _, err := s.Write(payload); err != nil {
					b.Fatalf("Write: %v", err)
				}
			}
		})
	}
}

// BenchmarkCountingStream measures the atomic byte-accounting overhead the
// counting wrapper adds to each Read and Write over a no-op stream.
func BenchmarkCountingStream(b *testing.B) {
	for _, size := range []int{1 << 10, 64 << 10} {
		payload := make([]byte, size)
		s := &countingStream{stream: benchStream{}, counter: &byteCounter{}}

		b.Run(fmt.Sprintf("Write/size=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				if _, err := s.Write(payload); err != nil {
					b.Fatalf("Write: %v", err)
				}
			}
		})
		b.Run(fmt.Sprintf("Read/size=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				if _, err := s.Read(payload); err != nil {
					b.Fatalf("Read: %v", err)
				}
			}
		})
	}
}

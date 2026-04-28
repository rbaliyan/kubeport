package proxy

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"sync/atomic"

	"k8s.io/streaming/pkg/httpstream"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// errChaosInjected is returned when chaos engineering injects a connection error.
var errChaosInjected = errors.New("chaos: injected connection error")

// chaosCounters tracks chaos injection statistics.
type chaosCounters struct {
	errorsInjected atomic.Int64
	spikesInjected atomic.Int64
}

// chaosStream wraps an httpstream.Stream to inject random errors and latency
// spikes on writes. It reads the live config from cfgPtr on every Write so
// that runtime mutations (via UpdateChaos) take effect immediately without
// reconnecting the tunnel.
type chaosStream struct {
	stream   httpstream.Stream
	ctx      context.Context
	cfgPtr   *atomic.Pointer[config.ParsedChaosConfig]
	counters *chaosCounters
}

func (s *chaosStream) Write(p []byte) (int, error) {
	cfg := s.cfgPtr.Load()
	if cfg == nil || !cfg.Enabled {
		return s.stream.Write(p)
	}

	// Check for error injection first.
	if cfg.ErrorRate > 0 && rand.Float64() < cfg.ErrorRate { // #nosec G404 -- math/rand is fine for chaos testing
		s.counters.errorsInjected.Add(1)
		return 0, errChaosInjected
	}

	// Check for latency spike injection.
	if cfg.LatencySpikeProbability > 0 && rand.Float64() < cfg.LatencySpikeProbability { // #nosec G404 -- math/rand is fine for chaos testing
		s.counters.spikesInjected.Add(1)
		if err := sleepWithContext(s.ctx, cfg.LatencySpikeDuration); err != nil {
			return 0, err
		}
	}

	return s.stream.Write(p)
}

func (s *chaosStream) Read(p []byte) (int, error)  { return s.stream.Read(p) }
func (s *chaosStream) Close() error                 { return s.stream.Close() }
func (s *chaosStream) Reset() error                 { return s.stream.Reset() }
func (s *chaosStream) Headers() http.Header         { return s.stream.Headers() }
func (s *chaosStream) Identifier() uint32           { return s.stream.Identifier() }

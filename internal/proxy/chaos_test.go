package proxy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// newChaosPtr creates an atomic pointer pre-loaded with cfg.
func newChaosPtr(cfg config.ParsedChaosConfig) *atomic.Pointer[config.ParsedChaosConfig] {
	p := &atomic.Pointer[config.ParsedChaosConfig]{}
	p.Store(&cfg)
	return p
}

func TestChaosStream_ErrorInjection_Always(t *testing.T) {
	counters := &chaosCounters{}
	s := &chaosStream{
		stream:   &mockStream{},
		ctx:      context.Background(),
		cfgPtr:   newChaosPtr(config.ParsedChaosConfig{Enabled: true, ErrorRate: 1.0}),
		counters: counters,
	}

	_, err := s.Write([]byte("hello"))
	if !errors.Is(err, errChaosInjected) {
		t.Fatalf("expected errChaosInjected, got %v", err)
	}
	if counters.errorsInjected.Load() != 1 {
		t.Fatalf("expected 1 error injected, got %d", counters.errorsInjected.Load())
	}
}

func TestChaosStream_ErrorInjection_Never(t *testing.T) {
	counters := &chaosCounters{}
	ms := &mockStream{}
	s := &chaosStream{
		stream:   ms,
		ctx:      context.Background(),
		cfgPtr:   newChaosPtr(config.ParsedChaosConfig{Enabled: true, ErrorRate: 0.0}),
		counters: counters,
	}

	for i := 0; i < 100; i++ {
		_, err := s.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("unexpected error on write %d: %v", i, err)
		}
	}
	if counters.errorsInjected.Load() != 0 {
		t.Fatalf("expected 0 errors injected, got %d", counters.errorsInjected.Load())
	}
}

func TestChaosStream_LatencySpike(t *testing.T) {
	counters := &chaosCounters{}
	ms := &mockStream{}
	s := &chaosStream{
		stream: ms,
		ctx:    context.Background(),
		cfgPtr: newChaosPtr(config.ParsedChaosConfig{
			Enabled:                 true,
			LatencySpikeProbability: 1.0,
			LatencySpikeDuration:    50 * time.Millisecond,
		}),
		counters: counters,
	}

	start := time.Now()
	_, err := s.Write([]byte("hello"))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected at least 40ms delay, got %v", elapsed)
	}
	if counters.spikesInjected.Load() != 1 {
		t.Fatalf("expected 1 spike injected, got %d", counters.spikesInjected.Load())
	}
}

func TestChaosStream_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	counters := &chaosCounters{}
	s := &chaosStream{
		stream: &mockStream{},
		ctx:    ctx,
		cfgPtr: newChaosPtr(config.ParsedChaosConfig{
			Enabled:                 true,
			LatencySpikeProbability: 1.0,
			LatencySpikeDuration:    10 * time.Second,
		}),
		counters: counters,
	}

	_, err := s.Write([]byte("hello"))
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestChaosStream_ReadPassthrough(t *testing.T) {
	ms := &mockStream{readData: []byte("hello")}
	counters := &chaosCounters{}
	s := &chaosStream{
		stream:   ms,
		ctx:      context.Background(),
		cfgPtr:   newChaosPtr(config.ParsedChaosConfig{Enabled: true, ErrorRate: 1.0}),
		counters: counters,
	}

	buf := make([]byte, 10)
	n, err := s.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(buf[:n]))
	}
}

func TestChaosStream_ErrorPrecedesSpike(t *testing.T) {
	counters := &chaosCounters{}
	s := &chaosStream{
		stream: &mockStream{},
		ctx:    context.Background(),
		cfgPtr: newChaosPtr(config.ParsedChaosConfig{
			Enabled:                 true,
			ErrorRate:               1.0,
			LatencySpikeProbability: 1.0,
			LatencySpikeDuration:    10 * time.Second,
		}),
		counters: counters,
	}

	start := time.Now()
	_, err := s.Write([]byte("hello"))
	elapsed := time.Since(start)

	if !errors.Is(err, errChaosInjected) {
		t.Fatalf("expected errChaosInjected, got %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("expected immediate error, but took %v", elapsed)
	}
	if counters.errorsInjected.Load() != 1 {
		t.Fatalf("expected 1 error, got %d", counters.errorsInjected.Load())
	}
	if counters.spikesInjected.Load() != 0 {
		t.Fatalf("expected 0 spikes, got %d", counters.spikesInjected.Load())
	}
}

func TestChaosStream_LiveUpdate(t *testing.T) {
	counters := &chaosCounters{}
	cfgPtr := newChaosPtr(config.ParsedChaosConfig{Enabled: false})
	s := &chaosStream{
		stream:   &mockStream{},
		ctx:      context.Background(),
		cfgPtr:   cfgPtr,
		counters: counters,
	}

	// Disabled — writes should pass through.
	if _, err := s.Write([]byte("hello")); err != nil {
		t.Fatalf("unexpected error when disabled: %v", err)
	}
	if counters.errorsInjected.Load() != 0 {
		t.Fatal("should not have injected error while disabled")
	}

	// Enable chaos at runtime.
	enabled := config.ParsedChaosConfig{Enabled: true, ErrorRate: 1.0}
	cfgPtr.Store(&enabled)

	_, err := s.Write([]byte("hello"))
	if !errors.Is(err, errChaosInjected) {
		t.Fatalf("expected errChaosInjected after live enable, got %v", err)
	}
	if counters.errorsInjected.Load() != 1 {
		t.Fatalf("expected 1 error after live enable, got %d", counters.errorsInjected.Load())
	}
}

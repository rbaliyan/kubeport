package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rbaliyan/kubeport/pkg/config"
)

func TestChaosStream_ErrorInjection_Always(t *testing.T) {
	counters := &chaosCounters{}
	s := &chaosStream{
		stream: &mockStream{},
		ctx:    context.Background(),
		cfg: config.ParsedChaosConfig{
			Enabled:   true,
			ErrorRate: 1.0, // always fail
		},
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
		stream: ms,
		ctx:    context.Background(),
		cfg: config.ParsedChaosConfig{
			Enabled:   true,
			ErrorRate: 0.0, // never fail
		},
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
		cfg: config.ParsedChaosConfig{
			Enabled:                 true,
			LatencySpikeProbability: 1.0, // always spike
			LatencySpikeDuration:    50 * time.Millisecond,
		},
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
	cancel() // cancel immediately

	counters := &chaosCounters{}
	s := &chaosStream{
		stream: &mockStream{},
		ctx:    ctx,
		cfg: config.ParsedChaosConfig{
			Enabled:                 true,
			LatencySpikeProbability: 1.0,
			LatencySpikeDuration:    10 * time.Second,
		},
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
		stream: ms,
		ctx:    context.Background(),
		cfg: config.ParsedChaosConfig{
			Enabled:   true,
			ErrorRate: 1.0, // errors only on write, not read
		},
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
	// When both error_rate=1.0 and spike probability=1.0,
	// the error check fires first and the spike is never reached.
	counters := &chaosCounters{}
	s := &chaosStream{
		stream: &mockStream{},
		ctx:    context.Background(),
		cfg: config.ParsedChaosConfig{
			Enabled:                 true,
			ErrorRate:               1.0,
			LatencySpikeProbability: 1.0,
			LatencySpikeDuration:    10 * time.Second,
		},
		counters: counters,
	}

	start := time.Now()
	_, err := s.Write([]byte("hello"))
	elapsed := time.Since(start)

	if !errors.Is(err, errChaosInjected) {
		t.Fatalf("expected errChaosInjected, got %v", err)
	}
	// Should return immediately — no spike delay.
	if elapsed > 100*time.Millisecond {
		t.Fatalf("expected immediate error, but took %v (spike should not fire)", elapsed)
	}
	if counters.errorsInjected.Load() != 1 {
		t.Fatalf("expected 1 error, got %d", counters.errorsInjected.Load())
	}
	if counters.spikesInjected.Load() != 0 {
		t.Fatalf("expected 0 spikes, got %d", counters.spikesInjected.Load())
	}
}

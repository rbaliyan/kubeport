package proxy

import (
	"net/http"
	"testing"

	"k8s.io/streaming/pkg/httpstream"
)

// fakeStream is a minimal httpstream.Stream that records delegated calls and
// returns deterministic values, so the wrapper types can be asserted in
// isolation without a live SPDY tunnel.
type fakeStream struct {
	writeN     int
	closeCalls int
	resetCalls int
	headers    http.Header
	id         uint32
}

func (f *fakeStream) Read(p []byte) (int, error) { return 0, nil }
func (f *fakeStream) Write(p []byte) (int, error) {
	if f.writeN > 0 {
		return f.writeN, nil
	}
	return len(p), nil
}
func (f *fakeStream) Close() error { f.closeCalls++; return nil }
func (f *fakeStream) Reset() error { f.resetCalls++; return nil }
func (f *fakeStream) Headers() http.Header {
	if f.headers == nil {
		f.headers = http.Header{"X-Test": {"v"}}
	}
	return f.headers
}
func (f *fakeStream) Identifier() uint32 { return f.id }

var _ httpstream.Stream = (*fakeStream)(nil)

func TestChaosStream_DelegatesPassthrough(t *testing.T) {
	fake := &fakeStream{id: 7}
	s := &chaosStream{stream: fake}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCalls != 1 {
		t.Errorf("Close not delegated: calls = %d", fake.closeCalls)
	}
	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if fake.resetCalls != 1 {
		t.Errorf("Reset not delegated: calls = %d", fake.resetCalls)
	}
	if s.Headers().Get("X-Test") != "v" {
		t.Errorf("Headers not delegated: %v", s.Headers())
	}
	if s.Identifier() != 7 {
		t.Errorf("Identifier = %d, want 7", s.Identifier())
	}
}

func TestThrottledStream_DelegatesPassthrough(t *testing.T) {
	fake := &fakeStream{id: 11}
	s := &throttledStream{stream: fake}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCalls != 1 {
		t.Errorf("Close not delegated: calls = %d", fake.closeCalls)
	}
	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if fake.resetCalls != 1 {
		t.Errorf("Reset not delegated: calls = %d", fake.resetCalls)
	}
	if s.Headers().Get("X-Test") != "v" {
		t.Errorf("Headers not delegated: %v", s.Headers())
	}
	if s.Identifier() != 11 {
		t.Errorf("Identifier = %d, want 11", s.Identifier())
	}
}

func TestCountingStream_DelegatesPassthrough(t *testing.T) {
	fake := &fakeStream{id: 99}
	s := &countingStream{stream: fake, counter: &byteCounter{}}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCalls != 1 {
		t.Errorf("Close not delegated: calls = %d", fake.closeCalls)
	}
	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if fake.resetCalls != 1 {
		t.Errorf("Reset not delegated: calls = %d", fake.resetCalls)
	}
	if s.Headers().Get("X-Test") != "v" {
		t.Errorf("Headers not delegated: %v", s.Headers())
	}
	if s.Identifier() != 99 {
		t.Errorf("Identifier = %d, want 99", s.Identifier())
	}
}

func TestCountingStream_WriteIncrementsCounters(t *testing.T) {
	fake := &fakeStream{writeN: 4}
	counter := &byteCounter{}
	s := &countingStream{stream: fake, counter: counter}

	n, err := s.Write([]byte("data"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 4 {
		t.Fatalf("Write returned n = %d, want 4", n)
	}
	if got := counter.bytesOut.Load(); got != 4 {
		t.Errorf("bytesOut = %d, want 4", got)
	}
	if got := counter.connBytesOut.Load(); got != 4 {
		t.Errorf("connBytesOut = %d, want 4", got)
	}

	// A second write accumulates.
	if _, err := s.Write([]byte("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := counter.bytesOut.Load(); got != 8 {
		t.Errorf("bytesOut after second write = %d, want 8", got)
	}
	if got := counter.connBytesOut.Load(); got != 8 {
		t.Errorf("connBytesOut after second write = %d, want 8", got)
	}
}

package proxy

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/streaming/pkg/httpstream"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockDialer struct {
	conn httpstream.Connection
	err  error
}

func (d *mockDialer) Dial(_ ...string) (httpstream.Connection, string, error) {
	return d.conn, "portforward.k8s.io", d.err
}

// mockConnWithStreams extends mockHTTPStreamConn to control CreateStream output.
type mockConnWithStreams struct {
	mockHTTPStreamConn
	streamToReturn httpstream.Stream
	createErr      error
}

func (m *mockConnWithStreams) CreateStream(_ http.Header) (httpstream.Stream, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.streamToReturn, nil
}

// ---------------------------------------------------------------------------
// countingDialer
// ---------------------------------------------------------------------------

func TestCountingDialer_Dial_ResetsConnCounters(t *testing.T) {
	conn := newMockHTTPStreamConn()
	d := &countingDialer{
		dialer:   &mockDialer{conn: conn},
		counter:  &byteCounter{},
		chaosPtr: newNilChaosPtr(),
	}
	d.counter.streamErrors.Store(5)
	d.counter.connBytesIn.Store(1000)
	d.counter.connBytesOut.Store(2000)

	_, _, err := d.Dial("portforward.k8s.io")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.counter.streamErrors.Load() != 0 {
		t.Errorf("streamErrors: want 0 after reset, got %d", d.counter.streamErrors.Load())
	}
	if d.counter.connBytesIn.Load() != 0 {
		t.Errorf("connBytesIn: want 0 after reset, got %d", d.counter.connBytesIn.Load())
	}
	if d.counter.connBytesOut.Load() != 0 {
		t.Errorf("connBytesOut: want 0 after reset, got %d", d.counter.connBytesOut.Load())
	}
}

func TestCountingDialer_Dial_SkipConnReset(t *testing.T) {
	conn := newMockHTTPStreamConn()
	d := &countingDialer{
		dialer:        &mockDialer{conn: conn},
		counter:       &byteCounter{},
		chaosPtr:      newNilChaosPtr(),
		skipConnReset: true,
	}
	d.counter.streamErrors.Store(7)
	d.counter.connBytesIn.Store(999)

	_, _, err := d.Dial("portforward.k8s.io")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.counter.streamErrors.Load() != 7 {
		t.Errorf("streamErrors: want 7 (not reset), got %d", d.counter.streamErrors.Load())
	}
	if d.counter.connBytesIn.Load() != 999 {
		t.Errorf("connBytesIn: want 999 (not reset), got %d", d.counter.connBytesIn.Load())
	}
}

func TestCountingDialer_Dial_PropagatesError(t *testing.T) {
	dialErr := errors.New("connection refused")
	d := &countingDialer{
		dialer:   &mockDialer{err: dialErr},
		counter:  &byteCounter{},
		chaosPtr: newNilChaosPtr(),
	}
	_, _, err := d.Dial("portforward.k8s.io")
	if !errors.Is(err, dialErr) {
		t.Fatalf("want dial error propagated, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// countingConnection.CreateStream
// ---------------------------------------------------------------------------

func TestCountingConnection_CreateStream_IncrementsStreamErrorsOnFailure(t *testing.T) {
	streamErr := errors.New("stream limit reached")
	inner := &mockConnWithStreams{
		mockHTTPStreamConn: *newMockHTTPStreamConn(),
		createErr:          streamErr,
	}
	counter := &byteCounter{}
	cc := &countingConnection{
		conn:     inner,
		counter:  counter,
		chaosPtr: newNilChaosPtr(),
	}

	_, err := cc.CreateStream(http.Header{})
	if err == nil {
		t.Fatal("expected error from CreateStream")
	}
	if counter.streamErrors.Load() != 1 {
		t.Errorf("streamErrors: want 1, got %d", counter.streamErrors.Load())
	}
}

func TestCountingConnection_CreateStream_ReturnsCountingStreamOnSuccess(t *testing.T) {
	inner := &mockConnWithStreams{
		mockHTTPStreamConn: *newMockHTTPStreamConn(),
		streamToReturn:     &mockStream{},
	}
	counter := &byteCounter{}
	cc := &countingConnection{
		conn:     inner,
		counter:  counter,
		chaosPtr: newNilChaosPtr(),
	}

	stream, err := cc.CreateStream(http.Header{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream == nil {
		t.Fatal("expected non-nil stream")
	}
	if counter.streamErrors.Load() != 0 {
		t.Errorf("streamErrors should remain 0 on success, got %d", counter.streamErrors.Load())
	}
}

func TestCountingConnection_CreateStream_AccumulatesErrors(t *testing.T) {
	inner := &mockConnWithStreams{
		mockHTTPStreamConn: *newMockHTTPStreamConn(),
		createErr:          errors.New("error"),
	}
	counter := &byteCounter{}
	cc := &countingConnection{conn: inner, counter: counter, chaosPtr: newNilChaosPtr()}

	for range 3 {
		_, _ = cc.CreateStream(http.Header{})
	}
	if counter.streamErrors.Load() != 3 {
		t.Errorf("streamErrors: want 3 after 3 failures, got %d", counter.streamErrors.Load())
	}
}

// ---------------------------------------------------------------------------
// countingStream.Read / Write
// ---------------------------------------------------------------------------

func TestCountingStream_Read_CountsBytesIn(t *testing.T) {
	inner := &mockStream{readData: []byte("hello world")}
	counter := &byteCounter{}
	cs := &countingStream{stream: inner, counter: counter}

	buf := make([]byte, 11)
	n, err := cs.Read(buf)
	if err != nil {
		t.Fatalf("unexpected read error: %v", err)
	}
	if n != 11 {
		t.Fatalf("read %d bytes, want 11", n)
	}
	if counter.bytesIn.Load() != 11 {
		t.Errorf("bytesIn: want 11, got %d", counter.bytesIn.Load())
	}
	if counter.connBytesIn.Load() != 11 {
		t.Errorf("connBytesIn: want 11, got %d", counter.connBytesIn.Load())
	}
}

func TestCountingStream_Write_CountsBytesOut(t *testing.T) {
	inner := &mockStream{}
	counter := &byteCounter{}
	cs := &countingStream{stream: inner, counter: counter}

	payload := []byte("outbound data")
	n, err := cs.Write(payload)
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("wrote %d bytes, want %d", n, len(payload))
	}
	if counter.bytesOut.Load() != int64(len(payload)) {
		t.Errorf("bytesOut: want %d, got %d", len(payload), counter.bytesOut.Load())
	}
	if counter.connBytesOut.Load() != int64(len(payload)) {
		t.Errorf("connBytesOut: want %d, got %d", len(payload), counter.connBytesOut.Load())
	}
}

func TestCountingStream_Read_AccumulatesAcrossMultipleCalls(t *testing.T) {
	counter := &byteCounter{}
	var total int64
	for i := range 3 {
		payload := make([]byte, (i+1)*5)
		inner := &mockStream{readData: payload}
		cs := &countingStream{stream: inner, counter: counter}
		buf := make([]byte, len(payload))
		cs.Read(buf)
		total += int64(len(payload))
	}
	if counter.bytesIn.Load() != total {
		t.Errorf("bytesIn: want %d, got %d", total, counter.bytesIn.Load())
	}
}

// ---------------------------------------------------------------------------
// countingConnection passthrough methods
// ---------------------------------------------------------------------------

func TestCountingConnection_Passthrough(t *testing.T) {
	inner := newMockHTTPStreamConn()
	counter := &byteCounter{}
	cc := &countingConnection{conn: inner, counter: counter, chaosPtr: newNilChaosPtr()}

	if err := cc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-cc.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("CloseChan not signalled after Close")
	}

	cc.SetIdleTimeout(5 * time.Second)
	cc.RemoveStreams()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newNilChaosPtr() *atomic.Pointer[config.ParsedChaosConfig] {
	p := &atomic.Pointer[config.ParsedChaosConfig]{}
	p.Store(&config.ParsedChaosConfig{})
	return p
}

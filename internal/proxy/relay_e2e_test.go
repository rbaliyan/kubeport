package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/streaming/pkg/httpstream"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// eventually polls fn until it returns true or the timeout elapses. It fails the
// test if the condition is never met, avoiding fixed sleeps for synchronization.
func eventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

// echoBackend is an in-process TCP server that echoes every byte it receives
// back to the sender, standing in for the pod end of a port-forward tunnel.
type echoBackend struct {
	ln    net.Listener
	conns sync.WaitGroup
}

func newEchoBackend(t *testing.T) *echoBackend {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo backend: %v", err)
	}
	b := &echoBackend{ln: ln}
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			b.conns.Add(1)
			go func() {
				defer b.conns.Done()
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return b
}

func (b *echoBackend) addr() string { return b.ln.Addr().String() }
func (b *echoBackend) close()       { _ = b.ln.Close() }

// echoStream is an httpstream.Stream backed by a real TCP connection to an echo
// backend. spdyForwardConn io.Copy's the client connection into the data stream
// (Write) and copies the data stream back to the client (Read), so wiring this
// stream to the echo backend produces a full round-trip relay.
//
// It is distinct from the package's existing fakeStream (whose Read returns
// 0,nil) because the relay path requires real byte movement.
type echoStream struct {
	conn       net.Conn
	headers    http.Header
	id         uint32
	closeCalls atomic.Int32
}

func (s *echoStream) Read(p []byte) (int, error)  { return s.conn.Read(p) }
func (s *echoStream) Write(p []byte) (int, error) { return s.conn.Write(p) }
func (s *echoStream) Close() error {
	s.closeCalls.Add(1)
	return s.conn.Close()
}
func (s *echoStream) Reset() error { return s.conn.Close() }
func (s *echoStream) Headers() http.Header {
	if s.headers == nil {
		s.headers = http.Header{}
	}
	return s.headers
}
func (s *echoStream) Identifier() uint32 { return s.id }

// fakeSPDYConn is an httpstream.Connection whose data stream is wired to an echo
// backend. The first CreateStream call returns the error stream (which
// spdyForwardConn closes immediately and only reads from); the second returns
// the data stream connected to the backend.
type fakeSPDYConn struct {
	backendAddr string

	mu           sync.Mutex
	streamCount  int
	dataStream   *echoStream // captured for assertions
	errReadBlock chan struct{}

	closeCh chan bool
	closed  bool
}

func newFakeSPDYConn(backendAddr string) *fakeSPDYConn {
	return &fakeSPDYConn{
		backendAddr:  backendAddr,
		errReadBlock: make(chan struct{}),
		closeCh:      make(chan bool),
	}
}

func (c *fakeSPDYConn) CreateStream(headers http.Header) (httpstream.Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamCount++
	streamType := headers.Get(corev1.StreamType)
	if streamType == corev1.StreamTypeError {
		// Error stream: spdyForwardConn closes it for writing and only reads.
		// Block reads until the connection tears down so no spurious remote
		// error is surfaced during a healthy relay.
		return &blockingErrStream{block: c.errReadBlock, headers: headers}, nil
	}
	// Data stream: dial the echo backend so bytes round-trip.
	conn, err := net.Dial("tcp", c.backendAddr)
	if err != nil {
		return nil, err
	}
	s := &echoStream{conn: conn, headers: headers, id: uint32(c.streamCount)} // #nosec G115
	c.dataStream = s
	return s, nil
}

func (c *fakeSPDYConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.errReadBlock)
		close(c.closeCh)
	}
	return nil
}
func (c *fakeSPDYConn) CloseChan() <-chan bool               { return c.closeCh }
func (c *fakeSPDYConn) SetIdleTimeout(_ time.Duration)       {}
func (c *fakeSPDYConn) RemoveStreams(_ ...httpstream.Stream) {}

// blockingErrStream models the k8s port-forward error stream: the client closes
// it for writing and reads until EOF. Read blocks until block is closed, then
// returns EOF with no payload (no remote error).
type blockingErrStream struct {
	block   chan struct{}
	headers http.Header
}

func (s *blockingErrStream) Read(_ []byte) (int, error) {
	<-s.block
	return 0, io.EOF
}
func (s *blockingErrStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *blockingErrStream) Close() error                { return nil }
func (s *blockingErrStream) Reset() error                { return nil }
func (s *blockingErrStream) Headers() http.Header        { return s.headers }
func (s *blockingErrStream) Identifier() uint32          { return 0 }

var _ httpstream.Connection = (*fakeSPDYConn)(nil)
var _ httpstream.Stream = (*echoStream)(nil)
var _ httpstream.Stream = (*blockingErrStream)(nil)

// relayClient binds a local listener, drives spdyForwardConn over an accepted
// client connection against the supplied SPDY connection, and returns the
// connected client end for the test to read/write.
func relayClient(t *testing.T, spdyConn httpstream.Connection) (clientConn net.Conn, done <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind local listener: %v", err)
	}

	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		defer func() { _ = ln.Close() }()
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		spdyForwardConn(context.Background(), conn, spdyConn, 80, 1, discardLogger(), "relay-test")
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial local listener: %v", err)
	}
	return client, relayDone
}

func TestSpdyForwardConn_EchoRoundTrip(t *testing.T) {
	backend := newEchoBackend(t)
	defer backend.close()

	spdyConn := newFakeSPDYConn(backend.addr())
	client, relayDone := relayClient(t, spdyConn)
	defer func() { _ = client.Close() }()

	payload := []byte("hello kubeport relay")
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}

	got := make([]byte, len(payload))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo mismatch: got %q, want %q", got, payload)
	}

	// Both error and data streams must have been created.
	spdyConn.mu.Lock()
	streamCount := spdyConn.streamCount
	spdyConn.mu.Unlock()
	if streamCount != 2 {
		t.Fatalf("expected 2 streams created (error + data), got %d", streamCount)
	}

	// Closing the client end tears the relay down cleanly.
	_ = client.Close()
	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not tear down after client close")
	}
	_ = spdyConn.Close()
}

func TestSpdyForwardConn_CountingStreamTracksBytes(t *testing.T) {
	// Drive the relay through a countingConnection so the byte counters that
	// power Status() are exercised end-to-end (they wrap the data stream).
	backend := newEchoBackend(t)
	defer backend.close()

	counter := &byteCounter{}
	var chaosPtr atomic.Pointer[config.ParsedChaosConfig]
	wrapped := &countingConnection{
		conn:     newFakeSPDYConn(backend.addr()),
		counter:  counter,
		chaosPtr: &chaosPtr,
		ctx:      context.Background(),
	}

	client, relayDone := relayClient(t, wrapped)
	defer func() { _ = client.Close() }()

	payload := []byte("count these bytes")
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}

	got := make([]byte, len(payload))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}

	// bytesOut is incremented as the client payload is written into the data
	// stream; bytesIn as the echoed reply is read back.
	eventually(t, 2*time.Second, func() bool {
		return counter.bytesOut.Load() >= int64(len(payload)) &&
			counter.bytesIn.Load() >= int64(len(payload))
	})

	_ = client.Close()
	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not tear down")
	}
}

func TestSpdyForwardConn_LatencyDelaysRoundTrip(t *testing.T) {
	// Configure injected latency on the data path and assert the echo round-trip
	// is delayed by at least the configured latency. Latency is injected on the
	// throttledStream Write (client->remote) so a single round-trip pays it once.
	const latency = 150 * time.Millisecond

	backend := newEchoBackend(t)
	defer backend.close()

	counter := &byteCounter{}
	var chaosPtr atomic.Pointer[config.ParsedChaosConfig]
	wrapped := &countingConnection{
		conn:       newFakeSPDYConn(backend.addr()),
		counter:    counter,
		chaosPtr:   &chaosPtr,
		networkCfg: config.ParsedNetworkConfig{Latency: latency},
		ctx:        context.Background(),
	}

	client, relayDone := relayClient(t, wrapped)
	defer func() { _ = client.Close() }()

	payload := []byte("delayed payload")
	start := time.Now()
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
	got := make([]byte, len(payload))
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	elapsed := time.Since(start)

	// Tolerant lower bound: the write must have slept ~latency before reaching
	// the backend. Allow scheduler slack (90% of the configured latency).
	if elapsed < latency*9/10 {
		t.Fatalf("round-trip took %s, expected at least ~%s of injected latency", elapsed, latency)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo mismatch: got %q want %q", got, payload)
	}

	_ = client.Close()
	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not tear down")
	}
}

func TestSpdyForwardConn_BandwidthThrottleDelaysTransfer(t *testing.T) {
	// Configure a tight bandwidth cap and send a payload larger than one second's
	// worth of tokens; the rate limiter must stall the transfer by a measurable,
	// computable lower bound. burst = bytesPerSec, so the excess (len-burst) bytes
	// each incur 1/bytesPerSec of wait.
	const bytesPerSec = 4096
	payload := make([]byte, 8192) // 2x the per-second budget
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	// Excess over the initial burst that must wait: (8192 - 4096) / 4096 = 1s.
	minDelay := time.Duration(float64(len(payload)-bytesPerSec)/float64(bytesPerSec)) * time.Second

	backend := newEchoBackend(t)
	defer backend.close()

	counter := &byteCounter{}
	var chaosPtr atomic.Pointer[config.ParsedChaosConfig]
	wrapped := &countingConnection{
		conn:       newFakeSPDYConn(backend.addr()),
		counter:    counter,
		chaosPtr:   &chaosPtr,
		networkCfg: config.ParsedNetworkConfig{BytesPerSec: bytesPerSec},
		limiter:    newRateLimiter(bytesPerSec),
		ctx:        context.Background(),
	}

	client, relayDone := relayClient(t, wrapped)
	defer func() { _ = client.Close() }()

	start := time.Now()
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
	got := make([]byte, len(payload))
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < minDelay*9/10 {
		t.Fatalf("throttled transfer took %s, expected at least ~%s", elapsed, minDelay)
	}

	_ = client.Close()
	select {
	case <-relayDone:
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not tear down")
	}
}

func TestSpdyForwardConn_BackendFailureTearsDownCleanly(t *testing.T) {
	// P1: mid-stream backend failure. Close the echo backend after the relay is
	// established and assert the relay tears down without leaking goroutines.
	backend := newEchoBackend(t)
	spdyConn := newFakeSPDYConn(backend.addr())

	client, relayDone := relayClient(t, spdyConn)
	defer func() { _ = client.Close() }()

	// Establish the data path with a first round-trip.
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	got := make([]byte, 4)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}

	// Wait until the data stream exists, then close the backend connection
	// mid-transfer to simulate the pod dropping the tunnel.
	eventually(t, 2*time.Second, func() bool {
		spdyConn.mu.Lock()
		defer spdyConn.mu.Unlock()
		return spdyConn.dataStream != nil
	})
	spdyConn.mu.Lock()
	dataStream := spdyConn.dataStream
	spdyConn.mu.Unlock()
	_ = dataStream.conn.Close() // break the backend side
	backend.close()

	// The relay must observe the remote EOF and return (closing the client conn),
	// rather than hanging — bounded wait, no fixed sleep.
	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not tear down after backend failure")
	}

	// Client reads should now fail (relay closed the client conn).
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := client.Read(buf); err == nil {
		t.Fatal("expected client read error after relay teardown")
	}
}

func TestSpdyForwardConn_MuxRequestIDsDoNotCollide(t *testing.T) {
	// In mux mode a single SPDY connection is shared across clients, each with a
	// distinct requestID. Drive two relays over one fakeSPDYConn with different
	// IDs and confirm both round-trip independently (no stream-ID collision).
	backend := newEchoBackend(t)
	defer backend.close()

	spdyConn := newFakeSPDYConn(backend.addr())

	relay := func(id int, payload string) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Errorf("bind: %v", err)
			return
		}
		defer func() { _ = ln.Close() }()
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			spdyForwardConn(context.Background(), conn, spdyConn, 80, id, discardLogger(), "mux-test")
		}()
		client, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		defer func() { _ = client.Close() }()
		if _, err := client.Write([]byte(payload)); err != nil {
			t.Errorf("write: %v", err)
			return
		}
		got := make([]byte, len(payload))
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.ReadFull(client, got); err != nil {
			t.Errorf("read: %v", err)
			return
		}
		if string(got) != payload {
			t.Errorf("echo mismatch id=%d: got %q want %q", id, got, payload)
		}
		_ = client.Close()
		<-done
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); relay(1, "client-one") }()
	go func() { defer wg.Done(); relay(2, "client-two") }()
	wg.Wait()
	_ = spdyConn.Close()
}

func TestSpdyForwardConn_ChaosErrorReachesClient(t *testing.T) {
	// P2: chaos error rate 1.0 must surface as a write failure on the data
	// stream, tearing the relay down. The chaosStream wraps the data stream and
	// returns errChaosInjected on every Write.
	backend := newEchoBackend(t)
	defer backend.close()

	counter := &byteCounter{}
	var chaosPtr atomic.Pointer[config.ParsedChaosConfig]
	chaosPtr.Store(&config.ParsedChaosConfig{Enabled: true, ErrorRate: 1.0})

	wrapped := &countingConnection{
		conn:     newFakeSPDYConn(backend.addr()),
		counter:  counter,
		chaosPtr: &chaosPtr,
		ctx:      context.Background(),
	}

	client, relayDone := relayClient(t, wrapped)
	defer func() { _ = client.Close() }()

	if _, err := client.Write([]byte("this should be dropped by chaos")); err != nil {
		t.Fatalf("client write: %v", err)
	}

	// The injected write error closes the data stream, which io.Copy surfaces;
	// the relay tears down and the chaos counter records the injection.
	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not tear down after chaos error injection")
	}
	if got := counter.chaos.errorsInjected.Load(); got == 0 {
		t.Fatal("expected at least one chaos error injected")
	}
}

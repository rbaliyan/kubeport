package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"k8s.io/streaming/pkg/httpstream"

	"github.com/rbaliyan/kubeport/internal/netutil"
	"github.com/rbaliyan/kubeport/pkg/config"
)

// isPortBound returns true if the port is already in use (cannot be re-bound).
// Uses Listen rather than Dial so it does not create an inbound connection.
func isPortBound(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return true // bind failed ⇒ something already holds this port
	}
	ln.Close()
	return false
}

// mockHTTPStreamConn is a minimal httpstream.Connection for testing isConnClosed.
type mockHTTPStreamConn struct {
	closeCh chan bool
	closed  bool
}

func newMockHTTPStreamConn() *mockHTTPStreamConn {
	return &mockHTTPStreamConn{closeCh: make(chan bool)}
}

func (m *mockHTTPStreamConn) CreateStream(_ http.Header) (httpstream.Stream, error) { return nil, nil }
func (m *mockHTTPStreamConn) Close() error {
	if !m.closed {
		m.closed = true
		close(m.closeCh)
	}
	return nil
}
func (m *mockHTTPStreamConn) CloseChan() <-chan bool          { return m.closeCh }
func (m *mockHTTPStreamConn) SetIdleTimeout(_ time.Duration) {}
func (m *mockHTTPStreamConn) RemoveStreams(_ ...httpstream.Stream) {}

func TestIsConnClosed_Open(t *testing.T) {
	conn := newMockHTTPStreamConn()
	if isConnClosed(conn) {
		t.Fatal("isConnClosed() = true for open connection, want false")
	}
}

func TestIsConnClosed_Closed(t *testing.T) {
	conn := newMockHTTPStreamConn()
	_ = conn.Close()
	if !isConnClosed(conn) {
		t.Fatal("isConnClosed() = false for closed connection, want true")
	}
}

func TestRunLazyPortForward_BindsPortAndWaits(t *testing.T) {
	m := &Manager{
		cfg:    &config.Config{},
		output: io.Discard,
		logger: discardLogger(),
	}
	pf := &portForward{
		svc: config.ServiceConfig{
			Name:       "lazy-test",
			Service:    "svc",
			LocalPort:  0, // OS-assigned
			RemotePort: 80,
			Lazy:       true,
		},
		namespace: "default",
		preemptCh: make(chan string, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- m.runLazyPortForward(ctx, pf)
	}()

	// Wait for the listener to bind.
	var port int
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pf.mu.Lock()
		port = pf.actualPort
		pf.mu.Unlock()
		if port != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pf.mu.Lock()
	state := pf.state
	tunnelOpen := pf.tunnelOpen
	pf.mu.Unlock()

	if state != StateWaiting {
		t.Fatalf("state = %v, want StateWaiting", state)
	}
	if port == 0 {
		t.Fatal("actualPort should be set once lazy forward starts")
	}
	if !isPortBound(port) {
		t.Fatalf("port %d should be bound while waiting for connections", port)
	}
	if tunnelOpen {
		t.Fatal("tunnelOpen should be false before any connection arrives")
	}

	cancel()
	select {
	case err := <-doneCh:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runLazyPortForward did not return after context cancel")
	}
}

func TestRunLazyPortForward_FixedPort(t *testing.T) {
	// Pick a free port and release it so the lazy forwarder can bind it.
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	fixedPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	m := &Manager{
		cfg:    &config.Config{},
		output: io.Discard,
		logger: discardLogger(),
	}
	pf := &portForward{
		svc: config.ServiceConfig{
			Name:       "lazy-fixed",
			Service:    "svc",
			LocalPort:  fixedPort,
			RemotePort: 80,
			Lazy:       true,
		},
		namespace: "default",
		preemptCh: make(chan string, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- m.runLazyPortForward(ctx, pf)
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pf.mu.Lock()
		port := pf.actualPort
		pf.mu.Unlock()
		if port != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pf.mu.Lock()
	got := pf.actualPort
	pf.mu.Unlock()

	if got != fixedPort {
		t.Fatalf("actualPort = %d, want %d", got, fixedPort)
	}

	cancel()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runLazyPortForward did not return after context cancel")
	}
}

func TestRunLazyPortForward_PortReleasedAfterCancel(t *testing.T) {
	m := &Manager{
		cfg:    &config.Config{},
		output: io.Discard,
		logger: discardLogger(),
	}
	pf := &portForward{
		svc: config.ServiceConfig{
			Name:       "lazy-release",
			Service:    "svc",
			LocalPort:  0,
			RemotePort: 80,
		},
		namespace: "default",
		preemptCh: make(chan string, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- m.runLazyPortForward(ctx, pf)
	}()

	// Wait for port to bind.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pf.mu.Lock()
		p := pf.actualPort
		pf.mu.Unlock()
		if p != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pf.mu.Lock()
	port := pf.actualPort
	pf.mu.Unlock()

	cancel()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runLazyPortForward did not return after context cancel")
	}

	// Port should no longer be bound.
	if port != 0 && netutil.IsPortOpen(port) {
		t.Fatalf("port %d should be released after the lazy forwarder stops", port)
	}
}

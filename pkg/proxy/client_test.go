package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// fakeDaemon is a minimal in-process DaemonServiceServer for testing.
type fakeDaemon struct {
	kubeportv1.UnimplementedDaemonServiceServer
	mu        sync.Mutex
	addrs     map[string]string
	callCount atomic.Int32
	fail      bool // when true, Mappings returns an error
}

func (s *fakeDaemon) setAddrs(a map[string]string) {
	s.mu.Lock()
	s.addrs = a
	s.mu.Unlock()
}

func (s *fakeDaemon) Mappings(_ context.Context, _ *kubeportv1.MappingsRequest) (*kubeportv1.MappingsResponse, error) {
	s.callCount.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return nil, status.Error(codes.Internal, "injected failure")
	}
	return &kubeportv1.MappingsResponse{Addrs: s.addrs}, nil
}

// startFakeDaemon starts a gRPC server on a random TCP port and returns its address.
func startFakeDaemon(t *testing.T, srv *fakeDaemon) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	kubeportv1.RegisterDaemonServiceServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

// makeClient creates a *client connected to addr with the given initial addrs.
// The underlying gRPC connection is closed via t.Cleanup.
func makeClient(t *testing.T, addr string, initAddrs map[string]string) *client {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	c := &client{
		conn:         conn,
		grpcClient:   kubeportv1.NewDaemonServiceClient(conn),
		addrs:        initAddrs,
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	t.Cleanup(func() { conn.Close() })
	return c
}

// unusedAddr returns a TCP address that is not currently listening.
func unusedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── noopProxy ─────────────────────────────────────────────────────────────────

func TestNoopGRPCTarget(t *testing.T) {
	p := Noop()
	if got := p.GRPCTarget("svc.ns.svc.cluster.local:8080"); got != "svc.ns.svc.cluster.local:8080" {
		t.Errorf("GRPCTarget = %q, want original addr", got)
	}
}

func TestNoopAddrs(t *testing.T) {
	if got := Noop().Addrs(); got != nil {
		t.Errorf("Addrs = %v, want nil", got)
	}
}

func TestNoopRefresh(t *testing.T) {
	if err := Noop().Refresh(context.Background()); err != nil {
		t.Errorf("Refresh unexpected error: %v", err)
	}
}

func TestNoopClose(t *testing.T) {
	if err := Noop().Close(context.Background()); err != nil {
		t.Errorf("Close unexpected error: %v", err)
	}
}

func TestNoopDialFunc(t *testing.T) {
	if fn := Noop().DialFunc(); fn == nil {
		t.Fatal("DialFunc returned nil")
	}
}

// ── translateAddr ─────────────────────────────────────────────────────────────

func TestTranslateAddr(t *testing.T) {
	c := &client{
		addrs: map[string]string{
			// host-only key → translated host (no port)
			"redis.ns.svc.cluster.local": "localhost",
			// full addr key → translated full addr
			"api.ns.svc.cluster.local:443": "localhost:8443",
		},
	}

	tests := []struct {
		input string
		want  string
	}{
		// Exact full-address match takes priority
		{"api.ns.svc.cluster.local:443", "localhost:8443"},
		// Host-only key match: translated host + original port
		{"redis.ns.svc.cluster.local:6379", "localhost:6379"},
		// Host-only key exact match (no port in input)
		{"redis.ns.svc.cluster.local", "localhost"},
		// No match → passthrough unchanged
		{"unknown.host:1234", "unknown.host:1234"},
		// No match, no port
		{"nope", "nope"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := c.translateAddr(tt.input)
			if got != tt.want {
				t.Errorf("translateAddr(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTranslateAddr_EmptyAddrs(t *testing.T) {
	c := &client{addrs: nil}
	if got := c.translateAddr("svc:9090"); got != "svc:9090" {
		t.Errorf("expected passthrough, got %q", got)
	}
}

// ── client.Addrs ──────────────────────────────────────────────────────────────

func TestClientAddrs_ReturnsCopy(t *testing.T) {
	c := &client{
		addrs:        map[string]string{"key": "original"},
		shutdownChan: make(chan struct{}),
	}

	got := c.Addrs()
	if got["key"] != "original" {
		t.Fatalf("Addrs() = %v, want {key: original}", got)
	}

	// Mutating the returned map must not affect internal state.
	got["key"] = "mutated"
	got["extra"] = "added"

	internal := c.Addrs()
	if internal["key"] != "original" {
		t.Errorf("internal addrs were mutated to %q", internal["key"])
	}
	if _, ok := internal["extra"]; ok {
		t.Error("extra key leaked into internal addrs")
	}
}

func TestClientAddrs_NilAddrs(t *testing.T) {
	c := &client{addrs: nil, shutdownChan: make(chan struct{})}
	if got := c.Addrs(); got != nil {
		t.Errorf("Addrs() with nil internal map = %v, want nil", got)
	}
}

// ── client.GRPCTarget ─────────────────────────────────────────────────────────

func TestClientGRPCTarget_EmptyAddrs(t *testing.T) {
	c := &client{addrs: map[string]string{}}
	// No mappings → return the address as-is (no resolver).
	if got := c.GRPCTarget("svc:8080"); got != "svc:8080" {
		t.Errorf("GRPCTarget with empty addrs = %q, want passthrough", got)
	}
}

func TestClientGRPCTarget_WithAddrs(t *testing.T) {
	c := &client{addrs: map[string]string{"svc.ns.svc.cluster.local": "localhost"}}
	got := c.GRPCTarget("svc.ns.svc.cluster.local:9090")
	want := resolverScheme + ":///" + "svc.ns.svc.cluster.local:9090"
	if got != want {
		t.Errorf("GRPCTarget = %q, want %q", got, want)
	}
}

// ── client.Refresh ────────────────────────────────────────────────────────────

func TestClientRefresh_UpdatesAddrs(t *testing.T) {
	srv := &fakeDaemon{addrs: map[string]string{"new": "127.0.0.1:2"}}
	addr := startFakeDaemon(t, srv)
	c := makeClient(t, addr, map[string]string{"old": "127.0.0.1:1"})

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	addrs := c.Addrs()
	if addrs["new"] != "127.0.0.1:2" {
		t.Errorf("new addr missing after Refresh, got %v", addrs)
	}
	if _, ok := addrs["old"]; ok {
		t.Error("old addr should be gone after Refresh")
	}
}

func TestClientRefresh_ErrorPreservesAddrs(t *testing.T) {
	srv := &fakeDaemon{fail: true}
	addr := startFakeDaemon(t, srv)
	c := makeClient(t, addr, map[string]string{"preserved": "127.0.0.1:1"})

	err := c.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected Refresh to return an error")
	}

	// Addrs must be unchanged when Refresh fails.
	addrs := c.Addrs()
	if addrs["preserved"] != "127.0.0.1:1" {
		t.Errorf("addrs should be preserved on error, got %v", addrs)
	}
}

func TestClientRefresh_CancelledContext(t *testing.T) {
	srv := &fakeDaemon{addrs: map[string]string{}}
	addr := startFakeDaemon(t, srv)
	c := makeClient(t, addr, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	if err := c.Refresh(ctx); err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

// ── client.Close ──────────────────────────────────────────────────────────────

func TestClientClose_ClosesShutdownChan(t *testing.T) {
	srv := &fakeDaemon{addrs: map[string]string{}}
	addr := startFakeDaemon(t, srv)
	c := makeClient(t, addr, nil)

	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-c.shutdownChan:
		// expected: channel is closed
	default:
		t.Error("shutdownChan not closed after Close")
	}
}

// ── client.DialContext ────────────────────────────────────────────────────────

func TestClientDialContext_TranslatesAddr(t *testing.T) {
	// Start a simple TCP listener to connect to.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	lisHost, lisPort, _ := net.SplitHostPort(lis.Addr().String())

	c := &client{
		// Map "my-svc" → real listener host so DialContext reaches it.
		addrs:        map[string]string{"my-svc": lisHost},
		shutdownChan: make(chan struct{}),
		logger:       discardLogger(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := c.DialContext(ctx, "tcp", "my-svc:"+lisPort)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()
}

func TestClientDialContext_NoTranslation(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	c := &client{addrs: map[string]string{}, shutdownChan: make(chan struct{}), logger: discardLogger()}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// No mapping → dials the real address directly.
	conn, err := c.DialContext(ctx, "tcp", lis.Addr().String())
	if err != nil {
		t.Fatalf("DialContext without mapping: %v", err)
	}
	conn.Close()
}

// ── New() options ─────────────────────────────────────────────────────────────

func TestNew_WithEnabledFalse(t *testing.T) {
	p, err := New(WithEnabled(false))
	if err != nil {
		t.Fatalf("New(WithEnabled(false)): %v", err)
	}
	// Must be a noop proxy.
	if p.Addrs() != nil {
		t.Error("expected nil Addrs from noop proxy")
	}
	if p.GRPCTarget("foo:8080") != "foo:8080" {
		t.Error("expected passthrough GRPCTarget from noop proxy")
	}
}

func TestNew_WithEnabledTrue_FallsBackToNoop(t *testing.T) {
	// Point at an address that is definitely not listening.
	addr := unusedAddr(t)
	p, err := New(
		WithTCP(addr, ""),
		WithEnabled(true),
		WithLogger(discardLogger()),
	)
	if err != nil {
		t.Fatalf("New with WithEnabled(true) must not return error, got: %v", err)
	}
	// Should silently fall back to noop.
	if p.Addrs() != nil {
		t.Error("expected noop fallback with nil Addrs")
	}
}

func TestNew_WithServer(t *testing.T) {
	srv := &fakeDaemon{addrs: map[string]string{
		"svc.ns.svc.cluster.local": "localhost",
	}}
	addr := startFakeDaemon(t, srv)

	p, err := New(
		WithTCP(addr, ""),
		WithRefreshInterval(0), // disable background refresh
		WithLogger(discardLogger()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close(context.Background())

	addrs := p.Addrs()
	if addrs["svc.ns.svc.cluster.local"] != "localhost" {
		t.Errorf("Addrs = %v, want {svc.ns.svc.cluster.local: localhost}", addrs)
	}

	// GRPCTarget should use the kubeport resolver scheme.
	target := p.GRPCTarget("svc.ns.svc.cluster.local:9090")
	wantPrefix := resolverScheme + ":///"
	if len(target) < len(wantPrefix) || target[:len(wantPrefix)] != wantPrefix {
		t.Errorf("GRPCTarget = %q, want prefix %q", target, wantPrefix)
	}
}

// ── auto-refresh ──────────────────────────────────────────────────────────────

func TestAutoRefresh_UpdatesAddrs(t *testing.T) {
	srv := &fakeDaemon{addrs: map[string]string{"initial": "127.0.0.1:1"}}
	addr := startFakeDaemon(t, srv)

	p, err := New(
		WithTCP(addr, ""),
		WithRefreshInterval(30*time.Millisecond),
		WithLogger(discardLogger()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close(context.Background())

	// Update server-side mappings.
	srv.setAddrs(map[string]string{"updated": "127.0.0.1:2"})

	// Wait up to 500 ms for the auto-refresh to propagate.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := p.Addrs()["updated"]; ok {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("auto-refresh did not update addrs within 500ms")
}

func TestAutoRefresh_StopsOnClose(t *testing.T) {
	srv := &fakeDaemon{addrs: map[string]string{}}
	addr := startFakeDaemon(t, srv)

	p, err := New(
		WithTCP(addr, ""),
		WithRefreshInterval(20*time.Millisecond),
		WithLogger(discardLogger()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Let a few refreshes happen.
	time.Sleep(80 * time.Millisecond)
	countBefore := srv.callCount.Load()

	p.Close(context.Background())

	// After close, no more refreshes should occur.
	time.Sleep(100 * time.Millisecond)
	countAfter := srv.callCount.Load()

	// Allow at most one in-flight call after close.
	if countAfter > countBefore+1 {
		t.Errorf("refresh kept firing after Close: %d calls before, %d after", countBefore, countAfter)
	}
}

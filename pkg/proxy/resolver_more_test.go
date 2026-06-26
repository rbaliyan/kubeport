package proxy

import (
	"io"
	"log/slog"
	"net/url"
	"testing"

	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
)

func parseURL(t *testing.T, raw string) url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return *u
}

// fakeClientConn is a minimal resolver.ClientConn capturing the last state.
type fakeClientConn struct {
	state resolver.State
}

func (f *fakeClientConn) UpdateState(s resolver.State) error { f.state = s; return nil }
func (f *fakeClientConn) ReportError(error)                  {}
func (f *fakeClientConn) NewAddress(addrs []resolver.Address) {
	f.state = resolver.State{Addresses: addrs}
}
func (f *fakeClientConn) ParseServiceConfig(string) *serviceconfig.ParseResult { return nil }

func newTestClient(addrs map[string]string) *client {
	return &client{
		addrs:        addrs,
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestResolverBuilder_Scheme(t *testing.T) {
	b := &resolverBuilder{client: newTestClient(nil)}
	if b.Scheme() != resolverScheme {
		t.Errorf("Scheme = %q, want %q", b.Scheme(), resolverScheme)
	}
}

func TestResolverBuilder_BuildTranslatesAddr(t *testing.T) {
	c := newTestClient(map[string]string{"my-svc:80": "localhost:8080"})
	b := &resolverBuilder{client: c}
	cc := &fakeClientConn{}

	target := resolver.Target{URL: parseURL(t, "kubeport:///my-svc:80")}
	r, err := b.Build(target, cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(cc.state.Addresses) != 1 {
		t.Fatalf("addresses = %d, want 1", len(cc.state.Addresses))
	}
	if got := cc.state.Addresses[0].Addr; got != "localhost:8080" {
		t.Errorf("translated addr = %q, want localhost:8080", got)
	}

	// ResolveNow and Close must be safe no-ops.
	r.ResolveNow(resolver.ResolveNowOptions{})
	r.Close()
}

func TestGlobalResolverBuilder_BuildResolveClose(t *testing.T) {
	// Use the package global registry; seed a mapping for a stable result.
	globalRegistry.merge(map[string]string{"global-svc:80": "localhost:7070"})
	t.Cleanup(func() {
		globalRegistry.remove(map[string]string{"global-svc:80": "localhost:7070"})
	})

	b := &globalResolverBuilder{}
	if b.Scheme() != resolverScheme {
		t.Errorf("Scheme = %q, want %q", b.Scheme(), resolverScheme)
	}

	cc := &fakeClientConn{}
	target := resolver.Target{URL: parseURL(t, "kubeport:///global-svc:80")}
	r, err := b.Build(target, cc, resolver.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(cc.state.Addresses) != 1 || cc.state.Addresses[0].Addr != "localhost:7070" {
		t.Fatalf("addresses = %v, want [localhost:7070]", cc.state.Addresses)
	}

	// ResolveNow re-translates; Close unregisters without panicking.
	r.ResolveNow(resolver.ResolveNowOptions{})
	r.Close()
}

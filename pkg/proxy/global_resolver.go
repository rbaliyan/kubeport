package proxy

import (
	"strings"
	"sync"

	"google.golang.org/grpc/resolver"
)

// globalRegistry holds address mappings contributed by all active proxy clients.
// Protected by mu; only initialized after RegisterGlobalResolver is called.
var globalRegistry = &registry{
	addrs:     make(map[string]string),
	resolvers: make(map[*globalResolver]struct{}),
}

// registry is a thread-safe address map that multiple proxy clients contribute to.
type registry struct {
	mu        sync.RWMutex
	addrs     map[string]string
	resolvers map[*globalResolver]struct{} // active resolvers to notify on update
	registered bool
}

// RegisterGlobalResolver registers a global gRPC resolver with the "kubeport"
// scheme. After calling this, any gRPC client can dial "kubeport:///addr" and
// have addresses translated using mappings from all active proxy instances.
//
// Call this once at application startup before creating gRPC connections.
// This is safe to call multiple times; subsequent calls are no-ops.
//
// Scheme precedence: per-connection resolvers registered via a client's
// GRPCDialOption() take priority over this global resolver for that connection.
// If a client that registered via GRPCDialOption() is closed, its per-connection
// resolver is unregistered and the gRPC connection will fall back to this global
// resolver (if registered), which may have different or stale mappings. Avoid
// mixing RegisterGlobalResolver and per-client GRPCDialOption on connections
// that outlive the proxy client.
func RegisterGlobalResolver() {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	if globalRegistry.registered {
		return
	}
	globalRegistry.registered = true
	resolver.Register(&globalResolverBuilder{})
}

// merge adds or replaces address mappings from a client and notifies active resolvers.
func (r *registry) merge(addrs map[string]string) {
	r.mu.Lock()
	for k, v := range addrs {
		r.addrs[k] = v
	}
	// Snapshot resolvers under lock, notify outside to avoid deadlock.
	resolvers := make([]*globalResolver, 0, len(r.resolvers))
	for gr := range r.resolvers {
		resolvers = append(resolvers, gr)
	}
	r.mu.Unlock()

	for _, gr := range resolvers {
		gr.resolve()
	}
}

// remove deletes address mappings owned by a client and notifies active resolvers.
func (r *registry) remove(addrs map[string]string) {
	r.mu.Lock()
	for k, v := range addrs {
		// Only delete if the value still matches (another client may have overwritten).
		if r.addrs[k] == v {
			delete(r.addrs, k)
		}
	}
	resolvers := make([]*globalResolver, 0, len(r.resolvers))
	for gr := range r.resolvers {
		resolvers = append(resolvers, gr)
	}
	r.mu.Unlock()

	for _, gr := range resolvers {
		gr.resolve()
	}
}

// translate looks up an address in the global registry using the same
// fallback logic as client.translateAddr.
func (r *registry) translate(addr string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return resolveAddr(r.addrs, addr, true)
}

// globalResolverBuilder implements resolver.Builder for the global registry.
type globalResolverBuilder struct{}

func (b *globalResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	addr := target.Endpoint()
	if addr == "" {
		addr = strings.TrimPrefix(target.URL.Path, "/")
	}

	gr := &globalResolver{cc: cc, addr: addr}

	globalRegistry.mu.Lock()
	globalRegistry.resolvers[gr] = struct{}{}
	globalRegistry.mu.Unlock()

	gr.resolve()
	return gr, nil
}

func (b *globalResolverBuilder) Scheme() string {
	return resolverScheme
}

// globalResolver is a resolver instance that re-translates on ResolveNow.
type globalResolver struct {
	cc   resolver.ClientConn
	addr string // original untranslated address
}

func (r *globalResolver) resolve() {
	translated := globalRegistry.translate(r.addr)
	_ = r.cc.UpdateState(resolver.State{
		Addresses: []resolver.Address{{Addr: translated}},
	})
}

func (r *globalResolver) ResolveNow(_ resolver.ResolveNowOptions) {
	r.resolve()
}

func (r *globalResolver) Close() {
	globalRegistry.mu.Lock()
	delete(globalRegistry.resolvers, r)
	globalRegistry.mu.Unlock()
}

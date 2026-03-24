// Package proxy provides address translation for applications that connect to
// Kubernetes services through kubeport port-forwards.
//
// When running locally with kubeport, services like Redis Sentinel return internal
// Kubernetes DNS names (e.g., "redis-0.redis.ns.svc.cluster.local:6379") that are
// not resolvable from localhost. This package queries the running kubeport daemon
// for its address mappings and provides a Dialer that translates those internal
// addresses to the correct localhost ports automatically.
//
// Basic usage:
//
//	p, err := proxy.New(proxy.WithSocketPath("/path/to/.kubeport.sock"))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer p.Close(context.Background())
//
//	// Use as a custom dialer for Redis, gRPC, HTTP, etc.
//	redisClient := redis.NewClient(&redis.Options{
//	    Dialer: p.DialFunc(),
//	})
//
// When kubeport is not running, use Noop() to get a passthrough proxy:
//
//	p, err := proxy.New()
//	if err != nil {
//	    p = proxy.Noop() // Fall back to direct connections
//	}
//	defer p.Close(context.Background())
//
//	// DialFunc and GRPCTarget work transparently with no translation
//	redisClient := redis.NewClient(&redis.Options{
//	    Dialer: p.DialFunc(),
//	})
package proxy

import (
	"context"
	"net"

	"google.golang.org/grpc"
)

// Proxy provides address translation for Kubernetes service connections.
// Use New() to create a proxy connected to kubeport, or Noop() for a
// passthrough that dials addresses without translation.
type Proxy interface {
	// DialContext establishes a connection with address translation.
	// Compatible with net.Dialer.DialContext signature.
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)

	// DialFunc returns a dialer function suitable for passing to Redis clients,
	// HTTP transports, or any library that accepts a custom dialer.
	DialFunc() func(ctx context.Context, network, addr string) (net.Conn, error)

	// GRPCTarget returns the gRPC dial target for the given address.
	// For an active proxy, this returns "kubeport:///address" which requires
	// the dial option from GRPCDialOption. For a noop proxy, returns the address as-is.
	GRPCTarget(addr string) string

	// GRPCDialOption returns a grpc.DialOption that registers the proxy's
	// address resolver for use with GRPCTarget. Pass this when creating
	// gRPC client connections. Returns nil for a noop proxy.
	GRPCDialOption() grpc.DialOption

	// Addrs returns the current address mapping table.
	// Returns nil for a noop proxy.
	Addrs() map[string]string

	// Refresh re-fetches address mappings from the kubeport daemon.
	// No-op for a noop proxy.
	Refresh(context.Context) error

	// Close releases resources. Must be called when done.
	Close(context.Context) error
}

// noopProxy is a passthrough proxy that dials addresses without translation.
type noopProxy struct{}

// Noop returns a Proxy that passes all connections through without translation.
// Use this as a fallback when kubeport is not running.
func Noop() Proxy {
	return noopProxy{}
}

func (noopProxy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func (n noopProxy) DialFunc() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.DialContext
}

func (noopProxy) GRPCTarget(addr string) string      { return addr }
func (noopProxy) GRPCDialOption() grpc.DialOption    { return nil }
func (noopProxy) Addrs() map[string]string           { return nil }
func (noopProxy) Refresh(_ context.Context) error { return nil }
func (noopProxy) Close(_ context.Context) error   { return nil }

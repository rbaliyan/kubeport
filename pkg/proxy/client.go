package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	pkgconfig "github.com/rbaliyan/kubeport/pkg/config"
	"github.com/rbaliyan/kubeport/pkg/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Compile-time check that client implements Proxy.
var _ Proxy = (*client)(nil)

// client is the real Proxy implementation backed by a kubeport daemon connection.
type client struct {
	conn           *grpc.ClientConn
	grpcClient     kubeportv1.DaemonServiceClient
	addrs          map[string]string
	disableFuzzy   bool // when true, skip headless FQDN fuzzy matching
	mu             sync.Mutex
	closeOnce      sync.Once
	shutdownChan   chan struct{}
	logger         *slog.Logger
}

// Option configures the Proxy.
type Option func(*options)

type options struct {
	enabled         *bool // nil = always connect; true = connect, noop on error; false = noop
	socketPath      string
	host            string
	apiKey          string
	tlsCertFile     string // path to server's TLS cert for pinning (TCP mode only)
	clusterDomain   string
	fuzzyMatch      *bool // nil = default (true); explicit true/false overrides
	refreshInterval time.Duration
	logger          *slog.Logger
}

// WithRefreshInterval sets the referesh interval for auto refresh
// If not set defaults to 10 seconds
func WithRefreshInterval(t time.Duration) Option {
	return func(o *options) { o.refreshInterval = t }
}

// WithSocketPath sets the Unix socket path to connect to the kubeport daemon.
// If not set, the proxy attempts auto-discovery via standard kubeport config paths.
func WithSocketPath(path string) Option {
	return func(o *options) { o.socketPath = path }
}

// WithTCP connects to a remote kubeport daemon over TCP with API key auth.
func WithTCP(host, apiKey string) Option {
	return func(o *options) {
		o.host = host
		o.apiKey = apiKey
	}
}

// WithClusterDomain sets the Kubernetes cluster domain (default: "cluster.local").
func WithClusterDomain(domain string) Option {
	return func(o *options) { o.clusterDomain = domain }
}

// WithEnabled controls whether the proxy is active. When set to true, New()
// returns a Noop proxy on connection errors instead of failing. When set to
// false, New() always returns a Noop proxy without attempting to connect.
// When not set (default), New() returns an error if it cannot connect.
func WithEnabled(enabled bool) Option {
	return func(o *options) { o.enabled = &enabled }
}

// WithTLSCertFile sets the path to the daemon's TLS certificate for TCP
// connections. The certificate is pinned in a custom root CA pool, avoiding
// the need for InsecureSkipVerify. When not set, the system CA pool is used.
func WithTLSCertFile(path string) Option {
	return func(o *options) { o.tlsCertFile = path }
}

// WithFuzzyMatch controls whether the proxy uses fuzzy address matching for
// headless service FQDNs. When enabled (default), addresses like
// "pod-0.headless-svc.ns.svc.cluster.local:port" are resolved by extracting
// the pod name and namespace. Disable for strict exact-match-only resolution.
func WithFuzzyMatch(enabled bool) Option {
	return func(o *options) { o.fuzzyMatch = &enabled }
}

// WithLogger sets a structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// New creates a Proxy that connects to the kubeport daemon, fetches current
// address mappings, and registers a gRPC resolver. Close must be called when done.
//
// If WithEnabled(false) is set, New returns a Noop proxy without connecting.
// If WithEnabled(true) is set, New returns a Noop proxy on connection errors
// instead of failing. If WithEnabled is not set, New returns an error on failure.
func New(opts ...Option) (Proxy, error) {
	o := &options{
		logger:          slog.Default(),
		refreshInterval: time.Second * 10,
	}
	for _, opt := range opts {
		opt(o)
	}

	// WithEnabled(false) → skip connection entirely
	if o.enabled != nil && !*o.enabled {
		return Noop(), nil
	}

	p, err := newClient(o)
	if err != nil {
		// WithEnabled(true) → graceful fallback to noop
		if o.enabled != nil && *o.enabled {
			o.logger.Warn("kubeport proxy disabled: falling back to direct connections", slog.String("error", err.Error()))
			return Noop(), nil
		}
		return nil, err
	}
	return p, nil
}

func newClient(o *options) (Proxy, error) {
	var (
		conn *grpc.ClientConn
		err  error
	)

	switch {
	case o.host != "":
		// Explicit TCP target — use pinned cert if provided, system CA otherwise.
		tcpCreds, cerr := tlsCredsFromCertFile(o.tlsCertFile)
		if cerr != nil {
			return nil, cerr
		}
		conn, err = grpc.NewClient(
			o.host,
			grpc.WithTransportCredentials(tcpCreds),
			grpc.WithUnaryInterceptor(grpcauth.ClientInterceptor(o.apiKey)),
		)
	case o.socketPath != "":
		// Explicit socket path
		conn, err = grpc.NewClient(
			"unix://"+o.socketPath,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	default:
		// Auto-discover from kubeport config file or standard socket locations
		target, discoverErr := discoverTarget()
		if discoverErr != nil {
			return nil, discoverErr
		}
		if target.mode == pkgconfig.ListenTCP {
			tcpCreds, cerr := tlsCredsFromCertFile(target.certPath)
			if cerr != nil {
				return nil, cerr
			}
			conn, err = grpc.NewClient(
				target.address,
				grpc.WithTransportCredentials(tcpCreds),
				grpc.WithUnaryInterceptor(grpcauth.ClientInterceptor(target.apiKey)),
			)
		} else {
			conn, err = grpc.NewClient(
				"unix://"+target.address,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("connect to kubeport daemon: %w", err)
	}

	grpcClient := kubeportv1.NewDaemonServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := grpcClient.Mappings(ctx, &kubeportv1.MappingsRequest{
		ClusterDomain: o.clusterDomain,
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("fetch mappings from kubeport: %w", err)
	}

	c := &client{
		conn:         conn,
		grpcClient:   grpcClient,
		addrs:        resp.Addrs,
		disableFuzzy: o.fuzzyMatch != nil && !*o.fuzzyMatch,
		shutdownChan: make(chan struct{}),
		logger:       o.logger.With(slog.String("component", "kubeport-proxy")),
	}

	// Contribute mappings to the global resolver (if registered).
	globalRegistry.merge(resp.Addrs)

	// auto refresh entries
	if o.refreshInterval > 0 {
		go func() {
			t := time.NewTicker(o.refreshInterval)
			defer t.Stop()
			for {
				select {
				case <-c.shutdownChan:
					return
				case <-t.C:
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					_ = c.Refresh(ctx)
					cancel()
				}
			}
		}()
	}
	return c, nil
}

func (c *client) Close(_ context.Context) error {
	var err error
	c.closeOnce.Do(func() {
		close(c.shutdownChan)

		// Remove this client's mappings from the global resolver.
		c.mu.Lock()
		addrs := c.addrs
		c.mu.Unlock()
		globalRegistry.remove(addrs)

		err = c.conn.Close()
	})
	return err
}

func (c *client) Refresh(ctx context.Context) error {
	resp, err := c.grpcClient.Mappings(ctx, &kubeportv1.MappingsRequest{})
	if err != nil {
		return fmt.Errorf("refresh mappings: %w", err)
	}
	c.mu.Lock()
	old := c.addrs
	c.addrs = resp.Addrs
	c.mu.Unlock()

	// Atomically replace old entries with new ones in the global resolver.
	globalRegistry.swap(old, resp.Addrs)
	return nil
}

func (c *client) Addrs() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.addrs)
}

func (c *client) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	translated := c.translateAddr(addr)
	return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, translated)
}

func (c *client) DialFunc() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return c.DialContext
}

func (c *client) GRPCDialOption() grpc.DialOption {
	return c.resolverDialOption()
}

func (c *client) GRPCTarget(addr string) string {
	c.mu.Lock()
	empty := len(c.addrs) == 0
	c.mu.Unlock()
	if empty {
		return addr
	}
	return resolverScheme + ":///" + addr
}

func (c *client) translateAddr(addr string) string {
	// Safe: Refresh replaces c.addrs atomically (never mutates in place),
	// so holding the pointer after releasing the lock is safe to read.
	c.mu.Lock()
	addrs := c.addrs
	c.mu.Unlock()
	return resolveAddr(addrs, addr, !c.disableFuzzy)
}

// resolveAddr translates an address using the given mapping table.
// Lookup order:
//  1. Exact match on full addr (host:port)
//  2. Host-only match (addr has port, map key is host without port)
//  3. (fuzzy only) Namespace-qualified pod match: extract pod name + namespace
//     from headless FQDN, try "pod.ns:port"
//  4. (fuzzy only) Short pod name match: extract first DNS label, try "pod:port"
func resolveAddr(addrs map[string]string, addr string, fuzzy bool) string {
	if translated, ok := addrs[addr]; ok {
		return translated
	}

	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if translated, ok := addrs[host]; ok {
			return net.JoinHostPort(translated, port)
		}
	}

	// Fuzzy match for headless-service FQDNs like
	// "pod-name.svc-name.ns.svc.cluster.local:port".
	if fuzzy && err == nil {
		if short, rest, ok := strings.Cut(host, "."); ok {
			// Try namespace-qualified match first (pod.ns:port) to
			// disambiguate pods with the same name across namespaces.
			if ns := extractNamespace(rest); ns != "" {
				nsAddr := net.JoinHostPort(short+"."+ns, port)
				if translated, ok := addrs[nsAddr]; ok {
					return translated
				}
			}

			// Fall back to short pod name match.
			shortAddr := net.JoinHostPort(short, port)
			if translated, ok := addrs[shortAddr]; ok {
				return translated
			}
		}
	}

	return addr
}

// tlsCredsFromCertFile builds gRPC TLS credentials that trust only the
// certificate at certFile (cert pinning). If certFile is empty or cannot be
// read, the system CA pool is used instead. InsecureSkipVerify is never set.
func tlsCredsFromCertFile(certFile string) (credentials.TransportCredentials, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if certFile != "" {
		pem, err := os.ReadFile(certFile)
		if err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pem) {
				cfg.RootCAs = pool
			}
		}
		// If the cert file can't be read (daemon not yet started, wrong path),
		// fall through to system CA pool rather than failing the connection.
	}
	return credentials.NewTLS(cfg), nil
}

// extractNamespace extracts the namespace from the remainder of a headless
// service FQDN after the pod name has been removed.
// Input: "redis-headless.dev.svc.cluster.local" → "dev"
// Input: "redis-headless.dev" → "dev"
// Input: "redis-headless" → "" (no namespace)
func extractNamespace(rest string) string {
	// rest is everything after "<pod>." — e.g., "svc-name.ns.svc.cluster.local"
	// Skip the headless service name (first label).
	_, afterSvc, ok := strings.Cut(rest, ".")
	if !ok {
		return ""
	}
	// afterSvc is "ns.svc.cluster.local" or "ns" — the namespace is the first label.
	ns, _, _ := strings.Cut(afterSvc, ".")
	return ns
}

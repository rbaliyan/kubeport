package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"sync"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/config"
	"github.com/rbaliyan/kubeport/internal/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Compile-time check that client implements Proxy.
var _ Proxy = (*client)(nil)

// client is the real Proxy implementation backed by a kubeport daemon connection.
type client struct {
	conn       *grpc.ClientConn
	grpcClient kubeportv1.DaemonServiceClient
	addrs      map[string]string
	mu sync.Mutex
	shutdownChan chan struct{}
	logger     *slog.Logger
}

// Option configures the Proxy.
type Option func(*options)

type options struct {
	enabled       *bool // nil = always connect; true = connect, noop on error; false = noop
	socketPath    string
	host          string
	apiKey        string
	clusterDomain string
	refreshInterval time.Duration
	logger        *slog.Logger
}

// WithRefreshInterval sets the referesh interval for auto refresh
// If not set defaults to 10 seconds
func WithRefreshInterval(t time.Duration) Option{
	return func(o *options){ o.refreshInterval = t}
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
		logger: slog.Default(),
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
		// Explicit TCP target
		conn, err = grpc.NewClient(
			o.host,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithUnaryInterceptor(daemon.APIKeyClientInterceptor(o.apiKey)),
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
		if target.mode == config.ListenTCP {
			conn, err = grpc.NewClient(
				target.address,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithUnaryInterceptor(daemon.APIKeyClientInterceptor(target.apiKey)),
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
		conn:       conn,
		grpcClient: grpcClient,
		addrs:      resp.Addrs,
		shutdownChan: make(chan struct{}),
		logger:     o.logger.With(slog.String("component", "kubeport-proxy")),
	}

	// Register gRPC resolver
	c.registerResolver()
	
	// auto refresh entries
	if o.refreshInterval > 0{
		go func(){
			t := time.NewTicker(o.refreshInterval)
			for {
				select {
				case <-c.shutdownChan:
					return
				case <-t.C:
					ctx , cancel := context.WithTimeout(context.Background(), time.Second)
					_ = c.Refresh(ctx)
					cancel()
				}
			}
		}()
	}
	return c, nil
}

func (c *client) Close(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		close(c.shutdownChan)
		return c.conn.Close()
	}
	return nil
}

func (c *client) Refresh(ctx context.Context) error {
	resp, err := c.grpcClient.Mappings(ctx, &kubeportv1.MappingsRequest{})
	if err != nil {
		return fmt.Errorf("refresh mappings: %w", err)
	}
    c.mu.Lock()
	defer c.mu.Unlock()
	c.addrs = resp.Addrs
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

func (c *client) GRPCTarget(addr string) string {
	if len(c.addrs) == 0 {
		return addr
	}
	return resolverScheme + ":///" + addr
}

func (c *client) translateAddr(addr string) string {
	if translated, ok := c.addrs[addr]; ok {
		return translated
	}

	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if translated, ok := c.addrs[host]; ok {
			return net.JoinHostPort(translated, port)
		}
	}

	return addr
}

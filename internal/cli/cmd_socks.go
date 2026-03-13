package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/rbaliyan/kubeport/pkg/config"
	"github.com/rbaliyan/kubeport/pkg/proxy"
)

// proxyFlags holds flags shared by socks and http-proxy commands.
type proxyFlags struct {
	listenAddr    string
	clusterDomain string
	username      string
	password      string
}

func parseProxyFlags(args []string) proxyFlags {
	var f proxyFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--listen", "-l":
			if i+1 < len(args) {
				i++
				f.listenAddr = args[i]
			}
		case "--cluster-domain":
			if i+1 < len(args) {
				i++
				f.clusterDomain = args[i]
			}
		case "--username", "-u":
			if i+1 < len(args) {
				i++
				f.username = args[i]
			}
		case "--password", "-p":
			if i+1 < len(args) {
				i++
				f.password = args[i]
			}
		default:
			if len(args[i]) > 0 && args[i][0] == '-' {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				os.Exit(1)
			}
		}
	}
	return f
}

// applyConfigDefaults fills empty flag values from a config section.
func (f *proxyFlags) applyConfigDefaults(cfg config.ProxyServerConfig) {
	if f.listenAddr == "" && cfg.Listen != "" {
		f.listenAddr = cfg.Listen
	}
	if f.username == "" && cfg.Username != "" {
		f.username = cfg.Username
	}
	if f.password == "" && cfg.Password != "" {
		f.password = cfg.Password
	}
}

// connectDaemon builds proxy options and creates a Proxy connected to the daemon.
func (a *app) connectDaemon(f proxyFlags, cfgSection config.ProxyServerConfig) (proxy.Proxy, error) {
	var proxyOpts []proxy.Option
	proxyOpts = append(proxyOpts, proxy.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))))

	if cfgSection.FuzzyMatch != nil {
		proxyOpts = append(proxyOpts, proxy.WithFuzzyMatch(*cfgSection.FuzzyMatch))
	}
	if f.clusterDomain != "" {
		proxyOpts = append(proxyOpts, proxy.WithClusterDomain(f.clusterDomain))
	}

	if a.cfg != nil {
		lc := a.cfg.ListenAddress()
		switch lc.Mode {
		case config.ListenTCP:
			proxyOpts = append(proxyOpts, proxy.WithTCP(lc.Address, a.cfg.APIKey))
		default:
			proxyOpts = append(proxyOpts, proxy.WithSocketPath(a.cfg.SocketFile()))
		}
	}

	return proxy.New(proxyOpts...)
}

func printMappings(p proxy.Proxy) {
	addrs := p.Addrs()
	if len(addrs) > 0 {
		fmt.Fprintf(os.Stderr, "Active mappings (%d):\n", len(addrs))
		for k, v := range addrs {
			fmt.Fprintf(os.Stderr, "  %s → %s\n", k, v)
		}
		fmt.Fprintln(os.Stderr)
	}
}

// runProxyServer starts a proxy server, waits for signals, and shuts down.
func runProxyServer(p proxy.Proxy, serve func(context.Context) error, close func() error, name string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\nShutting down %s proxy...\n", name)
		cancel()
		_ = close()
	}()

	if err := serve(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s proxy error: %v\n", name, err)
	}

	_ = p.Close(context.Background())
}

func (a *app) cmdSocks(args []string) {
	f := parseProxyFlags(args)
	var cfgSection config.ProxyServerConfig
	if a.cfg != nil {
		cfgSection = a.cfg.SOCKS
	}
	f.applyConfigDefaults(cfgSection)

	if f.listenAddr == "" {
		f.listenAddr = "127.0.0.1:1080"
	}

	p, err := a.connectDaemon(f, cfgSection)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError connecting to daemon: %v%s\n", colorRed, err, colorReset)
		fmt.Fprintf(os.Stderr, "Make sure kubeport is running (kubeport start)\n")
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var socksOpts []proxy.SOCKSOption
	socksOpts = append(socksOpts, proxy.WithSOCKSLogger(logger))
	if f.username != "" || f.password != "" {
		socksOpts = append(socksOpts, proxy.WithSOCKSAuth(f.username, f.password))
	}

	srv, err := proxy.NewSOCKSServer(p, f.listenAddr, socksOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError starting SOCKS server: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "SOCKS5 proxy listening on %s\n", srv.Addr())
	if f.username != "" {
		fmt.Fprintf(os.Stderr, "Authentication: enabled (username/password)\n")
		fmt.Fprintf(os.Stderr, "Usage: curl --proxy socks5h://%s:%s@%s http://my-service:8080/\n\n", f.username, f.password, srv.Addr())
	} else {
		fmt.Fprintf(os.Stderr, "Usage: curl --proxy socks5h://%s http://my-service:8080/\n\n", srv.Addr())
	}

	printMappings(p)
	runProxyServer(p, srv.Serve, srv.Close, "SOCKS")
}

func (a *app) cmdHTTPProxy(args []string) {
	f := parseProxyFlags(args)
	var cfgSection config.ProxyServerConfig
	if a.cfg != nil {
		cfgSection = a.cfg.HTTPProxy
	}
	f.applyConfigDefaults(cfgSection)

	if f.listenAddr == "" {
		f.listenAddr = "127.0.0.1:3128"
	}

	p, err := a.connectDaemon(f, cfgSection)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError connecting to daemon: %v%s\n", colorRed, err, colorReset)
		fmt.Fprintf(os.Stderr, "Make sure kubeport is running (kubeport start)\n")
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var httpOpts []proxy.HTTPProxyOption
	httpOpts = append(httpOpts, proxy.WithHTTPProxyLogger(logger))
	if f.username != "" || f.password != "" {
		httpOpts = append(httpOpts, proxy.WithHTTPProxyAuth(f.username, f.password))
	}

	srv, err := proxy.NewHTTPProxyServer(p, f.listenAddr, httpOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError starting HTTP proxy: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "HTTP proxy listening on %s\n", srv.Addr())
	if f.username != "" {
		fmt.Fprintf(os.Stderr, "Authentication: enabled (basic auth)\n")
		fmt.Fprintf(os.Stderr, "Usage: curl --proxy http://%s:%s@%s http://my-service:8080/\n\n", f.username, f.password, srv.Addr())
	} else {
		fmt.Fprintf(os.Stderr, "Usage: curl --proxy http://%s http://my-service:8080/\n\n", srv.Addr())
	}
	fmt.Fprintf(os.Stderr, "Env:   export http_proxy=http://%s https_proxy=http://%s\n\n", srv.Addr(), srv.Addr())

	printMappings(p)
	runProxyServer(p, srv.Serve, srv.Close, "HTTP")
}

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

func (a *app) cmdSocks(args []string) {
	var (
		listenAddr    string
		clusterDomain string
		username      string
		password      string
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--listen", "-l":
			if i+1 < len(args) {
				i++
				listenAddr = args[i]
			}
		case "--cluster-domain":
			if i+1 < len(args) {
				i++
				clusterDomain = args[i]
			}
		case "--username", "-u":
			if i+1 < len(args) {
				i++
				username = args[i]
			}
		case "--password", "-p":
			if i+1 < len(args) {
				i++
				password = args[i]
			}
		default:
			if len(args[i]) > 0 && args[i][0] == '-' {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				os.Exit(1)
			}
		}
	}

	// Apply config file defaults for SOCKS settings.
	if a.cfg != nil {
		if listenAddr == "" && a.cfg.SOCKS.Listen != "" {
			listenAddr = a.cfg.SOCKS.Listen
		}
		if username == "" && a.cfg.SOCKS.Username != "" {
			username = a.cfg.SOCKS.Username
		}
		if password == "" && a.cfg.SOCKS.Password != "" {
			password = a.cfg.SOCKS.Password
		}
	}

	if listenAddr == "" {
		listenAddr = "127.0.0.1:1080"
	}

	// Connect to the kubeport daemon.
	var proxyOpts []proxy.Option
	proxyOpts = append(proxyOpts, proxy.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))))

	// Apply fuzzy_match from config (default: enabled).
	if a.cfg != nil && a.cfg.SOCKS.FuzzyMatch != nil {
		proxyOpts = append(proxyOpts, proxy.WithFuzzyMatch(*a.cfg.SOCKS.FuzzyMatch))
	}

	if clusterDomain != "" {
		proxyOpts = append(proxyOpts, proxy.WithClusterDomain(clusterDomain))
	}

	// Use config-based connection if available.
	if a.cfg != nil {
		lc := a.cfg.ListenAddress()
		switch lc.Mode {
		case config.ListenTCP:
			proxyOpts = append(proxyOpts, proxy.WithTCP(lc.Address, a.cfg.APIKey))
		default:
			proxyOpts = append(proxyOpts, proxy.WithSocketPath(a.cfg.SocketFile()))
		}
	}

	p, err := proxy.New(proxyOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError connecting to daemon: %v%s\n", colorRed, err, colorReset)
		fmt.Fprintf(os.Stderr, "Make sure kubeport is running (kubeport start)\n")
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var socksOpts []proxy.SOCKSOption
	socksOpts = append(socksOpts, proxy.WithSOCKSLogger(logger))
	if username != "" || password != "" {
		socksOpts = append(socksOpts, proxy.WithSOCKSAuth(username, password))
	}

	srv, err := proxy.NewSOCKSServer(p, listenAddr, socksOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError starting SOCKS server: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "SOCKS5 proxy listening on %s\n", srv.Addr())
	if username != "" {
		fmt.Fprintf(os.Stderr, "Authentication: enabled (username/password)\n")
		fmt.Fprintf(os.Stderr, "Usage: curl --proxy socks5h://%s:%s@%s http://my-service:8080/\n\n", username, password, srv.Addr())
	} else {
		fmt.Fprintf(os.Stderr, "Usage: curl --proxy socks5h://%s http://my-service:8080/\n\n", srv.Addr())
	}

	// Show current mappings.
	addrs := p.Addrs()
	if len(addrs) > 0 {
		fmt.Fprintf(os.Stderr, "Active mappings (%d):\n", len(addrs))
		for k, v := range addrs {
			fmt.Fprintf(os.Stderr, "  %s → %s\n", k, v)
		}
		fmt.Fprintln(os.Stderr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nShutting down SOCKS proxy...")
		cancel()
		_ = srv.Close()
	}()

	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "SOCKS server error: %v\n", err)
	}

	_ = p.Close(context.Background())
}

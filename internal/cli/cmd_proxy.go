package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	version "github.com/rbaliyan/go-version"
	"github.com/rbaliyan/kubeport/internal/daemon"
	"github.com/rbaliyan/kubeport/internal/hook"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"github.com/rbaliyan/kubeport/internal/registry"
	"github.com/rbaliyan/kubeport/pkg/config"
	pkgproxy "github.com/rbaliyan/kubeport/pkg/proxy"
)

func (a *app) cmdForeground(ctx context.Context) {
	if a.cfg == nil {
		fmt.Fprintf(os.Stderr, "%sNo valid config loaded%s\n", colorRed, colorReset)
		os.Exit(1)
	}

	a.runProxy(ctx, os.Stdout)
}

func (a *app) cmdDaemon(ctx context.Context, args []string) {
	// Parse daemon-specific flags (forwarded from start command)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			if i+1 < len(args) {
				i++
				a.configFile = args[i]
			}
		case "--context", "--kube-context":
			if i+1 < len(args) {
				i++
				a.cliContext = args[i]
			}
		case "--namespace", "-n":
			if i+1 < len(args) {
				i++
				a.cliNamespace = args[i]
			}
		case "--svc":
			if i+1 < len(args) {
				i++
				a.cliServices = append(a.cliServices, args[i])
			}
		case "--disable-svc":
			if i+1 < len(args) {
				i++
				a.disableServices = append(a.disableServices, args[i])
			}
		case "--no-config":
			a.noConfig = true
		}
	}

	// Reload config if we got new args from daemon invocation
	if a.cfg == nil {
		if err := a.loadConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "daemon config error: %v\n", err)
			os.Exit(1)
		}
	}

	a.runProxy(ctx, os.Stdout)
}

// watchConfigFile polls the config file for modifications and triggers a reload
// when changes are detected. It uses a debounce to avoid rapid successive reloads
// (e.g., editors that write atomically via rename).
func (a *app) watchConfigFile(ctx context.Context, logger *slog.Logger, reload func(string)) {
	if a.configFile == "" {
		return
	}

	const pollInterval = 2 * time.Second
	const debounce = 500 * time.Millisecond

	info, err := os.Stat(a.configFile)
	if err != nil {
		logger.Warn("config file watch: cannot stat file", "error", err)
		return
	}
	lastModTime := info.ModTime()
	lastSize := info.Size()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(a.configFile)
			if err != nil {
				continue
			}
			modTime := info.ModTime()
			size := info.Size()
			if modTime != lastModTime || size != lastSize {
				lastModTime = modTime
				lastSize = size
				// Debounce: wait briefly in case the editor is still writing
				time.Sleep(debounce)
				reload("file change detected")
			}
		}
	}
}

func (a *app) runProxy(ctx context.Context, output io.Writer) {
	_, _ = fmt.Fprintf(output, "kubeport %s starting\n", version.Get().Raw)
	_, _ = fmt.Fprintf(output, "Context:   %s\n", a.cfg.Context)
	_, _ = fmt.Fprintf(output, "Namespace: %s\n", a.cfg.Namespace)
	_, _ = fmt.Fprintf(output, "Services:  %d\n\n", len(a.cfg.Services))

	// Migrate stale old-style files (.kubeport.pid, .kubeport.sock) left in the
	// config directory by versions before the central runtime dir was introduced.
	migrateOldStyleFiles(a.cfg, output)

	// Write PID file so the daemon can be located by CLI commands.
	pidFile := a.cfg.PIDFile()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0600); err != nil {
		_, _ = fmt.Fprintf(output, "Warning: failed to write PID file: %v\n", err)
	}

	reg, err := a.openRegistry()
	if err != nil {
		_, _ = fmt.Fprintf(output, "Warning: failed to open instance registry: %v\n", err)
	} else {
		listenCfg := a.cfg.ListenAddress()
		entry := registry.Entry{
			PID:        os.Getpid(),
			ConfigFile: a.cfg.FilePath(),
			PIDFile:    a.cfg.PIDFile(),
			LogFile:    a.cfg.LogFile(),
			HasAPIKey:  a.cfg.APIKey != "",
			APIKeyHash: registry.HashKey(a.cfg.APIKey),
			Version:    version.Get().Raw,
			StartedAt:  time.Now(),
		}
		if listenCfg.Mode == config.ListenTCP {
			entry.TCPAddress = listenCfg.Address
		} else {
			entry.Socket = listenCfg.Address
		}
		if err := reg.Register(entry); err != nil {
			_, _ = fmt.Fprintf(output, "Warning: failed to register instance: %v\n", err)
		}
	}

	logger := slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dispatcher := hook.NewDispatcher(logger)
	for _, hc := range a.cfg.Hooks {
		hookReg, err := hook.BuildFromConfig(hc)
		if err != nil {
			_, _ = fmt.Fprintf(output, "Warning: skip hook %q: %v\n", hc.Name, err)
			continue
		}
		dispatcher.Register(hookReg)
		_, _ = fmt.Fprintf(output, "Hook registered: %s (%s)\n", hc.Name, hc.Type)
	}

	// Fire manager starting gate (e.g., VPN startup)
	if err := dispatcher.Fire(ctx, hook.Event{
		Type: hook.EventManagerStarting,
		Time: time.Now(),
	}); err != nil {
		_, _ = fmt.Fprintf(output, "Hook blocked startup: %v\n", err)
		os.Exit(1)
	}

	mgr, err := proxy.NewManager(a.cfg, output,
		proxy.WithHooks(dispatcher),
		proxy.WithLogger(logger),
	)
	if err != nil {
		_, _ = fmt.Fprintf(output, "Error: %v\n", err)
		os.Exit(1)
	}

	// Start gRPC daemon server
	daemonSrv := daemon.NewServer(mgr, a.cfg)
	go func() {
		if err := daemonSrv.Start(); err != nil {
			_, _ = fmt.Fprintf(output, "gRPC server error: %v\n", err)
		}
	}()
	defer func() {
		daemonSrv.Shutdown()
		if err := os.Remove(a.cfg.PIDFile()); err != nil && !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(output, "Warning: failed to remove PID file: %v\n", err)
		}
		if reg != nil {
			if err := reg.Deregister(os.Getpid()); err != nil {
				_, _ = fmt.Fprintf(output, "Warning: failed to deregister instance: %v\n", err)
			}
		}
		_ = dispatcher.Fire(context.Background(), hook.Event{
			Type: hook.EventManagerStopped,
			Time: time.Now(),
		})
		dispatcher.Wait()
	}()
	listenCfg := a.cfg.ListenAddress()
	switch listenCfg.Mode {
	case config.ListenTCP:
		_, _ = fmt.Fprintf(output, "gRPC server listening on tcp://%s\n", listenCfg.Address)
	default:
		_, _ = fmt.Fprintf(output, "gRPC server listening on %s\n", listenCfg.Address)
	}

	// Auto-start SOCKS proxy if enabled
	if a.cfg.SOCKS.Enabled {
		go a.autoStartSOCKS(ctx, logger, output)
	}

	// Auto-start HTTP proxy if enabled
	if a.cfg.HTTPProxy.Enabled {
		go a.autoStartHTTPProxy(ctx, logger, output)
	}

	// Verify namespace access
	if err := mgr.CheckNamespace(ctx); err != nil {
		_, _ = fmt.Fprintf(output, "Warning: %v\n", err)
		_, _ = fmt.Fprintf(output, "Continuing anyway (namespace checks may fail for some services)\n\n")
	}

	// Config reload helper shared by SIGHUP and file watcher
	reloadConfig := func(reason string) {
		logger.Info("reloading config", "trigger", reason)
		newCfg, err := config.Load(a.configFile)
		if err != nil {
			logger.Error("reload config failed", "error", err)
			return
		}
		if err := newCfg.Validate(); err != nil {
			logger.Error("reload config validation failed", "error", err)
			return
		}
		added, removed, err := mgr.Reload(newCfg)
		if err != nil {
			logger.Error("reload failed", "error", err)
			return
		}
		logger.Info("config reloaded", "trigger", reason, "added", added, "removed", removed)
	}

	// SIGHUP triggers config reload
	if a.configFile != "" {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGHUP)
		go func() {
			for range sigCh {
				reloadConfig("SIGHUP")
			}
		}()

		// Watch config file for changes (poll-based)
		go a.watchConfigFile(ctx, logger, reloadConfig)
	}

	_, _ = fmt.Fprintf(output, "Starting port forwards...\n\n")
	mgr.Start(ctx)
}

// autoStartSOCKS starts an embedded SOCKS5 proxy server when socks.enabled is true.
func (a *app) autoStartSOCKS(ctx context.Context, logger *slog.Logger, output io.Writer) {
	listenAddr := a.cfg.SOCKS.Listen
	if listenAddr == "" {
		listenAddr = "127.0.0.1:1080"
	}

	// Brief delay to let the daemon gRPC socket become ready.
	time.Sleep(500 * time.Millisecond)

	f := proxyFlags{
		listenAddr: listenAddr,
		username:   a.cfg.SOCKS.Username,
		password:   a.cfg.SOCKS.Password,
	}

	p, err := a.connectDaemon(f, a.cfg.SOCKS)
	if err != nil {
		_, _ = fmt.Fprintf(output, "Warning: SOCKS auto-start failed to connect to daemon: %v\n", err)
		return
	}

	var socksOpts []pkgproxy.SOCKSOption
	socksOpts = append(socksOpts, pkgproxy.WithSOCKSLogger(logger))
	if f.username != "" || f.password != "" {
		socksOpts = append(socksOpts, pkgproxy.WithSOCKSAuth(f.username, f.password))
	}

	srv, err := pkgproxy.NewSOCKSServer(p, f.listenAddr, socksOpts...)
	if err != nil {
		_, _ = fmt.Fprintf(output, "Warning: SOCKS auto-start failed: %v\n", err)
		return
	}

	_, _ = fmt.Fprintf(output, "SOCKS5 proxy listening on %s\n", srv.Addr())

	go func() { // #nosec G118 -- cleanup needs a non-cancelled context
		<-ctx.Done()
		_ = srv.Close()
		_ = p.Close(context.Background())
	}()

	if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintf(output, "SOCKS proxy error: %v\n", err)
	}
}

// autoStartHTTPProxy starts an embedded HTTP proxy server when http_proxy.enabled is true.
func (a *app) autoStartHTTPProxy(ctx context.Context, logger *slog.Logger, output io.Writer) {
	listenAddr := a.cfg.HTTPProxy.Listen
	if listenAddr == "" {
		listenAddr = "127.0.0.1:3128"
	}

	// Brief delay to let the daemon gRPC socket become ready.
	time.Sleep(500 * time.Millisecond)

	f := proxyFlags{
		listenAddr: listenAddr,
		username:   a.cfg.HTTPProxy.Username,
		password:   a.cfg.HTTPProxy.Password,
	}

	p, err := a.connectDaemon(f, a.cfg.HTTPProxy)
	if err != nil {
		_, _ = fmt.Fprintf(output, "Warning: HTTP proxy auto-start failed to connect to daemon: %v\n", err)
		return
	}

	var httpOpts []pkgproxy.HTTPProxyOption
	httpOpts = append(httpOpts, pkgproxy.WithHTTPProxyLogger(logger))
	if f.username != "" || f.password != "" {
		httpOpts = append(httpOpts, pkgproxy.WithHTTPProxyAuth(f.username, f.password))
	}

	srv, err := pkgproxy.NewHTTPProxyServer(p, f.listenAddr, httpOpts...)
	if err != nil {
		_, _ = fmt.Fprintf(output, "Warning: HTTP proxy auto-start failed: %v\n", err)
		return
	}

	_, _ = fmt.Fprintf(output, "HTTP proxy listening on %s\n", srv.Addr())

	go func() { // #nosec G118 -- cleanup needs a non-cancelled context
		<-ctx.Done()
		_ = srv.Close()
		_ = p.Close(context.Background())
	}()

	if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintf(output, "HTTP proxy error: %v\n", err)
	}
}

// migrateOldStyleFiles removes stale runtime files left in the config directory by
// older versions of kubeport that placed .pid, .sock, and .log files alongside the
// config file rather than in the central directory.
func migrateOldStyleFiles(cfg *config.Config, output io.Writer) {
	if cfg.FilePath() == "" {
		return
	}
	dir := filepath.Dir(cfg.FilePath())

	// Remove stale PID file.
	oldPID := filepath.Join(dir, ".kubeport.pid")
	if data, err := os.ReadFile(oldPID); err == nil { // #nosec G304
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 && syscall.Kill(pid, 0) != nil {
			if err := os.Remove(oldPID); err == nil {
				_, _ = fmt.Fprintf(output, "Removed stale legacy PID file: %s\n", oldPID)
			}
		}
	}

	// Remove stale socket file.
	oldSock := filepath.Join(dir, ".kubeport.sock")
	if _, err := os.Stat(oldSock); err == nil {
		conn, err := net.DialTimeout("unix", oldSock, 500*time.Millisecond)
		if err != nil {
			if err := os.Remove(oldSock); err == nil {
				_, _ = fmt.Fprintf(output, "Removed stale legacy socket: %s\n", oldSock)
			}
		} else {
			_ = conn.Close()
		}
	}
}

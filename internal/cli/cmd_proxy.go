package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	version "github.com/rbaliyan/go-version"
	"github.com/rbaliyan/kubeport/internal/config"
	"github.com/rbaliyan/kubeport/internal/daemon"
	"github.com/rbaliyan/kubeport/internal/hook"
	"github.com/rbaliyan/kubeport/internal/proxy"
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

func (a *app) runProxy(ctx context.Context, output io.Writer) {
	_, _ = fmt.Fprintf(output, "kubeport %s starting\n", version.Get().Raw)
	_, _ = fmt.Fprintf(output, "Context:   %s\n", a.cfg.Context)
	_, _ = fmt.Fprintf(output, "Namespace: %s\n", a.cfg.Namespace)
	_, _ = fmt.Fprintf(output, "Services:  %d\n\n", len(a.cfg.Services))

	// Write PID file so the daemon can be located by CLI commands.
	pidFile := a.cfg.PIDFile()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0600); err != nil {
		_, _ = fmt.Fprintf(output, "Warning: failed to write PID file: %v\n", err)
	}

	logger := slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Build hook dispatcher from config
	dispatcher := hook.NewDispatcher(logger)
	for _, hc := range a.cfg.Hooks {
		reg, err := hook.BuildFromConfig(hc)
		if err != nil {
			_, _ = fmt.Fprintf(output, "Warning: skip hook %q: %v\n", hc.Name, err)
			continue
		}
		dispatcher.Register(reg)
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
		_ = dispatcher.Fire(context.Background(), hook.Event{
			Type: hook.EventManagerStopped,
			Time: time.Now(),
		})
		dispatcher.Wait()
	}()
	_, _ = fmt.Fprintf(output, "gRPC server listening on %s\n", a.cfg.SocketFile())

	// Verify namespace access
	if err := mgr.CheckNamespace(ctx); err != nil {
		_, _ = fmt.Fprintf(output, "Warning: %v\n", err)
		_, _ = fmt.Fprintf(output, "Continuing anyway (namespace checks may fail for some services)\n\n")
	}

	// SIGHUP triggers config reload
	if a.configFile != "" {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGHUP)
		go func() {
			for range sigCh {
				logger.Info("SIGHUP received, reloading config")
				newCfg, err := config.Load(a.configFile)
				if err != nil {
					logger.Error("reload config failed", "error", err)
					continue
				}
				if err := newCfg.Validate(); err != nil {
					logger.Error("reload config validation failed", "error", err)
					continue
				}
				added, removed, err := mgr.Reload(newCfg)
				if err != nil {
					logger.Error("reload failed", "error", err)
					continue
				}
				logger.Info("config reloaded", "added", added, "removed", removed)
			}
		}()
	}

	_, _ = fmt.Fprintf(output, "Starting port forwards...\n\n")
	mgr.Start(ctx)
}

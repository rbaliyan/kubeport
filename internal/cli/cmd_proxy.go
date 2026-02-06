package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

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
	// Parse daemon-specific flags (config was passed down)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			if i+1 < len(args) {
				i++
				a.configFile = args[i]
			}
		}
	}

	// Reload config if we got a new path from daemon args
	if a.cfg == nil {
		if err := a.loadConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "daemon config error: %v\n", err)
			os.Exit(1)
		}
	}

	a.runProxy(ctx, os.Stdout)
}

func (a *app) runProxy(ctx context.Context, output io.Writer) {
	fmt.Fprintf(output, "kubeport %s starting\n", Version)
	fmt.Fprintf(output, "Context:   %s\n", a.cfg.Context)
	fmt.Fprintf(output, "Namespace: %s\n", a.cfg.Namespace)
	fmt.Fprintf(output, "Services:  %d\n\n", len(a.cfg.Services))

	logger := slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Build hook dispatcher from config
	dispatcher := hook.NewDispatcher(logger)
	for _, hc := range a.cfg.Hooks {
		h, events, fm, timeout, err := hook.BuildFromConfig(hc)
		if err != nil {
			fmt.Fprintf(output, "Warning: skip hook %q: %v\n", hc.Name, err)
			continue
		}
		dispatcher.Register(h, events, fm, timeout)
		fmt.Fprintf(output, "Hook registered: %s (%s)\n", hc.Name, hc.Type)
	}

	// Fire manager starting gate (e.g., VPN startup)
	if err := dispatcher.Fire(ctx, hook.Event{
		Type: hook.EventManagerStarting,
		Time: time.Now(),
	}); err != nil {
		fmt.Fprintf(output, "Hook blocked startup: %v\n", err)
		os.Exit(1)
	}

	mgr, err := proxy.NewManager(a.cfg, output,
		proxy.WithHooks(dispatcher),
		proxy.WithLogger(logger),
	)
	if err != nil {
		fmt.Fprintf(output, "Error: %v\n", err)
		os.Exit(1)
	}

	// Start gRPC daemon server
	daemonSrv := daemon.NewServer(mgr, a.cfg)
	go func() {
		if err := daemonSrv.Start(); err != nil {
			fmt.Fprintf(output, "gRPC server error: %v\n", err)
		}
	}()
	defer func() {
		daemonSrv.Shutdown()
		if err := os.Remove(a.cfg.PIDFile()); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(output, "Warning: failed to remove PID file: %v\n", err)
		}
		dispatcher.Fire(context.Background(), hook.Event{
			Type: hook.EventManagerStopped,
			Time: time.Now(),
		})
		// Allow async hooks a moment to complete
		time.Sleep(500 * time.Millisecond)
	}()
	fmt.Fprintf(output, "gRPC server listening on %s\n", a.cfg.SocketFile())

	// Verify namespace access
	if err := mgr.CheckNamespace(ctx); err != nil {
		fmt.Fprintf(output, "Warning: %v\n", err)
		fmt.Fprintf(output, "Continuing anyway (namespace checks may fail for some services)\n\n")
	}

	fmt.Fprintf(output, "Starting port forwards...\n\n")
	mgr.Start(ctx)
}

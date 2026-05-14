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
	"sync"
	"syscall"
	"time"

	version "github.com/rbaliyan/go-version"
	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/daemon"
	"github.com/rbaliyan/kubeport/internal/hook"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"github.com/rbaliyan/kubeport/internal/registry"
	"github.com/rbaliyan/kubeport/pkg/config"
	pkgproxy "github.com/rbaliyan/kubeport/pkg/proxy"
)

// detectExternalConflicts queries running instances and returns ExternalForward
// entries for any service in cfg that is already managed by another instance
// (matched by service name or static local port).
// Delegate instances and the current process are excluded from the search.
func (a *app) detectExternalConflicts(cfg *config.Config) []proxy.ExternalForward {
	if cfg == nil || len(cfg.Services) == 0 {
		return nil
	}
	reg, err := a.openRegistry()
	if err != nil {
		return nil
	}
	entries, err := reg.List()
	if err != nil {
		return nil
	}

	myPID := os.Getpid()
	var candidates []registry.Entry
	for _, e := range entries {
		if e.PID != myPID && !e.Delegate {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	live := a.queryLiveInstances(candidates)
	if len(live) == 0 {
		return nil
	}

	type owner struct {
		instance   string
		pid        int
		configFile string
		actualPort int
	}
	byName := make(map[string]owner)
	byPort := make(map[int]owner)

	for _, inst := range live {
		ep := inst.entry.Socket
		if ep == "" {
			ep = inst.entry.TCPAddress
		}
		for _, fw := range inst.forwards {
			o := owner{
				instance:   ep,
				pid:        inst.entry.PID,
				configFile: inst.entry.ConfigFile,
				actualPort: int(fw.ActualPort),
			}
			if name := fw.GetService().GetName(); name != "" {
				byName[name] = o
			}
			port := int(fw.ActualPort)
			if port == 0 {
				port = int(fw.GetService().GetLocalPort())
			}
			if port > 0 {
				byPort[port] = o
			}
		}
	}

	seen := make(map[string]bool)
	var externals []proxy.ExternalForward
	for _, svc := range cfg.Services {
		if seen[svc.Name] {
			continue
		}
		if o, ok := byName[svc.Name]; ok {
			seen[svc.Name] = true
			externals = append(externals, proxy.ExternalForward{
				Service: svc, Instance: o.instance, PID: o.pid,
				ConfigFile: o.configFile, ActualPort: o.actualPort,
			})
			continue
		}
		if svc.LocalPort != 0 {
			if o, ok := byPort[svc.LocalPort]; ok {
				seen[svc.Name] = true
				externals = append(externals, proxy.ExternalForward{
					Service: svc, Instance: o.instance, PID: o.pid,
					ConfigFile: o.configFile, ActualPort: o.actualPort,
				})
			}
		}
	}
	return externals
}

// isProxyPortInUse returns true when something is already listening on addr.
func isProxyPortInUse(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

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
		case "--delegate":
			a.delegate = true
		case "--primary-socket":
			if i+1 < len(args) {
				i++
				a.primarySocket = args[i]
			}
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

// noopSupervisor satisfies daemon.Supervisor for delegate instances that manage
// no local port-forwards of their own. cancel is called by Stop() so that the
// gRPC Stop RPC unblocks the runDelegateProxy select.
type noopSupervisor struct {
	cancel context.CancelFunc
}

func (n *noopSupervisor) Status() []proxy.ForwardStatus { return nil }
func (n *noopSupervisor) Stop() {
	if n.cancel != nil {
		n.cancel()
	}
}
func (n *noopSupervisor) AddService(_ config.ServiceConfig) error {
	return fmt.Errorf("delegate instance: cannot add services directly — use the primary")
}
func (n *noopSupervisor) RemoveService(_ string) error {
	return fmt.Errorf("delegate instance: service not found")
}
func (n *noopSupervisor) Reload(_ *config.Config) (int, int, error)        { return 0, 0, nil }
func (n *noopSupervisor) Apply(_ []config.ServiceConfig) (int, int, []string) { return 0, 0, nil }
func (n *noopSupervisor) Mappings(_ string) []proxy.AddressMapping            { return nil }
func (n *noopSupervisor) UpdateChaos(_ []string, _ config.ParsedChaosConfig) ([]string, []string) {
	return nil, nil
}
func (n *noopSupervisor) ResetChaos(_ []string) ([]string, []string) { return nil, nil }
func (n *noopSupervisor) ReleaseBySource(_ string) (int, error)      { return 0, nil }

func (a *app) runProxy(ctx context.Context, output io.Writer) {
	if a.delegate {
		a.runDelegateProxy(ctx, output)
		return
	}

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
			AuthEnabled: a.cfg.APIKey != "",
			KeyID:       a.cfg.ResolvedKeyID(),
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

	a.warnInvalidLogLevel(output)
	logger := a.newLogger(output)

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

	// Detect services already managed by another running instance.
	// SetExternalForwards is called before Start so those services are never started locally.
	if externals := a.detectExternalConflicts(a.cfg); len(externals) > 0 {
		mgr.SetExternalForwards(externals)
		for _, ef := range externals {
			_, _ = fmt.Fprintf(output, "Service %q: managed by instance PID %d (%s) — not starting locally\n",
				ef.Service.Name, ef.PID, ef.Instance)
		}
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

	// Auto-start SOCKS proxy if enabled. Fail loudly if the port is already in use
	// — two kubeport instances cannot share the same proxy address.
	if a.cfg.SOCKS.IsEnabled() {
		socksAddr := a.cfg.SOCKS.Listen
		if socksAddr == "" {
			socksAddr = "127.0.0.1:1080"
		}
		if isProxyPortInUse(socksAddr) {
			_, _ = fmt.Fprintf(output, "Error: SOCKS proxy cannot start — %s is already in use. Only one kubeport instance may run a proxy on the same address.\n", socksAddr)
		} else {
			go a.autoStartSOCKS(ctx, logger, output)
		}
	}

	// Auto-start HTTP proxy if enabled. Same single-instance constraint.
	if a.cfg.HTTPProxy.IsEnabled() {
		httpAddr := a.cfg.HTTPProxy.Listen
		if httpAddr == "" {
			httpAddr = "127.0.0.1:3128"
		}
		if isProxyPortInUse(httpAddr) {
			_, _ = fmt.Fprintf(output, "Error: HTTP proxy cannot start — %s is already in use. Only one kubeport instance may run a proxy on the same address.\n", httpAddr)
		} else {
			go a.autoStartHTTPProxy(ctx, logger, output)
		}
	}

	// Verify namespace access
	if err := mgr.CheckNamespace(ctx); err != nil {
		_, _ = fmt.Fprintf(output, "Warning: %v\n", err)
		_, _ = fmt.Fprintf(output, "Continuing anyway (namespace checks may fail for some services)\n\n")
	}

	// Config reload helper shared by SIGHUP and file watcher.
	// reloadMu prevents concurrent reloads from interleaving SetExternalForwards
	// calls (which are not idempotent with respect to the "newly external" delta).
	var reloadMu sync.Mutex
	reloadConfig := func(reason string) {
		reloadMu.Lock()
		defer reloadMu.Unlock()
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

		// Re-detect external conflicts with the refreshed config.
		// Services that became external (another instance took them) are stopped locally.
		// Services that became local (owner instance stopped) are reclaimed by Reload.
		newExternals := a.detectExternalConflicts(newCfg)
		toRemove := mgr.SetExternalForwards(newExternals)
		for _, name := range toRemove {
			if rmErr := mgr.RemoveService(name); rmErr != nil {
				logger.Warn("reload: failed to stop newly-external service", "service", name, "error", rmErr)
			} else {
				logger.Info("service is now managed by another instance — stopped locally", "service", name)
			}
		}

		added, removed, err := mgr.Reload(newCfg)
		if err != nil {
			logger.Error("reload failed", "error", err)
			return
		}
		logger.Info("config reloaded", "trigger", reason,
			"added", added, "removed", removed, "external", len(newExternals))
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

// runDelegateProxy runs a lightweight lease-holder daemon. It registers in the
// central registry and runs the gRPC server (so kubeport stop works), but
// manages no local port-forwards. On shutdown it calls ReleaseBySource on the
// primary to remove the services it contributed.
func (a *app) runDelegateProxy(ctx context.Context, output io.Writer) {
	_, _ = fmt.Fprintf(output, "kubeport %s starting (delegate mode)\n", version.Get().Raw)
	_, _ = fmt.Fprintf(output, "Config:        %s\n", a.cfg.FilePath())
	_, _ = fmt.Fprintf(output, "Primary:       %s\n\n", a.primarySocket)

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
			PID:           os.Getpid(),
			ConfigFile:    a.cfg.FilePath(),
			PIDFile:       a.cfg.PIDFile(),
			LogFile:       a.cfg.LogFile(),
			Version:       version.Get().Raw,
			StartedAt:     time.Now(),
			Delegate:      true,
			PrimarySocket: a.primarySocket,
		}
		if listenCfg.Mode == config.ListenTCP {
			entry.TCPAddress = listenCfg.Address
		} else {
			entry.Socket = listenCfg.Address
		}
		if err := reg.Register(entry); err != nil {
			_, _ = fmt.Fprintf(output, "Warning: failed to register delegate instance: %v\n", err)
		}
	}

	stopCtx, stopCancel := context.WithCancel(ctx)
	noop := &noopSupervisor{cancel: stopCancel}
	daemonSrv := daemon.NewServer(noop, a.cfg)
	go func() {
		if err := daemonSrv.Start(); err != nil {
			_, _ = fmt.Fprintf(output, "gRPC server error: %v\n", err)
		}
	}()

	// On shutdown: release contributed services from primary, then clean up.
	defer func() {
		if a.primarySocket != "" && a.cfg.FilePath() != "" {
			absPath, _ := filepath.Abs(a.cfg.FilePath())
			releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if dc, dialErr := dialDaemon(a.primarySocket); dialErr == nil && dc != nil {
				resp, rErr := dc.client.ReleaseBySource(releaseCtx, &kubeportv1.ReleaseBySourceRequest{
					SourceConfig: absPath,
				})
				if rErr == nil && resp != nil {
					_, _ = fmt.Fprintf(output, "Released %d service(s) from primary\n", resp.Released)
				}
				dc.Close()
			}
		}
		daemonSrv.Shutdown()
		if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(output, "Warning: failed to remove PID file: %v\n", err)
		}
		if reg != nil {
			if err := reg.Deregister(os.Getpid()); err != nil {
				_, _ = fmt.Fprintf(output, "Warning: failed to deregister delegate instance: %v\n", err)
			}
		}
	}()

	listenCfg := a.cfg.ListenAddress()
	switch listenCfg.Mode {
	case config.ListenTCP:
		_, _ = fmt.Fprintf(output, "gRPC server listening on tcp://%s\n", listenCfg.Address)
	default:
		_, _ = fmt.Fprintf(output, "gRPC server listening on %s\n", listenCfg.Address)
	}

	// Block until context cancelled (SIGTERM / gRPC Stop via noopSupervisor.Stop).
	<-stopCtx.Done()
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

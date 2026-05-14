package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/registry"
	"github.com/rbaliyan/kubeport/pkg/config"
)

const defaultWaitTimeout = 30 * time.Second

func (a *app) cmdStart(ctx context.Context) {
	if a.cfg == nil {
		fmt.Fprintf(os.Stderr, "%sNo valid config loaded%s\n", colorRed, colorReset)
		os.Exit(1)
	}

	// --offload: send services to an already-running instance instead of starting a new one.
	if a.offload {
		a.offloadServicesToInstance()
		return
	}

	// --delegate: hand off non-conflicting services to an existing instance.
	if a.delegate {
		a.cmdStartDelegate(ctx)
		return
	}

	// Check if already running (via PID file)
	if pid, running := a.isRunning(); running {
		// If --wait, check if all forwards are ready — if so, no-op
		if a.startWait {
			if a.allForwardsReady() {
				fmt.Printf("%sProxy already running and ready (PID: %d)%s\n", colorGreen, pid, colorReset)
				return
			}
			// Already running but not all ready — wait for readiness
			fmt.Printf("%sProxy running (PID: %d), waiting for all forwards...%s\n", colorYellow, pid, colorReset)
			if err := a.waitForReady(); err != nil {
				fmt.Fprintf(os.Stderr, "%s%v%s\n", colorRed, err, colorReset)
				os.Exit(1)
			}
			return
		}
		fmt.Printf("%sProxy is already running (PID: %d)%s\n", colorYellow, pid, colorReset)
		fmt.Println("Use 'status' to check or 'restart' to restart")
		return
	}

	if a.cfg.FilePath() != "" {
		if reg, err := a.openRegistry(); err == nil {
			if existing, err := reg.FindByConfig(a.cfg.FilePath()); err == nil && existing != nil {
				fmt.Printf("%sAn instance is already running this config (PID: %d)%s\n", colorYellow, existing.PID, colorReset)
				fmt.Printf("  Endpoint: %s\n", entryEndpoint(*existing))
				fmt.Printf("  Use --offload to add services to that instance instead\n")
				os.Exit(1)
			}
		}
	}

	a.launchDaemon(ctx)
}

// launchDaemon re-execs the binary as a background daemon process, polls for
// readiness, and prints the result. Shared by cmdStart and cmdStartNormal.
func (a *app) launchDaemon(_ context.Context) {
	fmt.Print("Starting proxy in background... ")

	exePath, err := os.Executable()
	if err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	daemonArgs := []string{"_daemon"}
	if a.noConfig {
		daemonArgs = append(daemonArgs, "--no-config")
	} else if a.configFile != "" {
		daemonArgs = append(daemonArgs, "--config", a.configFile)
	}
	if a.cliContext != "" {
		daemonArgs = append(daemonArgs, "--context", a.cliContext)
	}
	if a.cliNamespace != "" {
		daemonArgs = append(daemonArgs, "--namespace", a.cliNamespace)
	}
	for _, svc := range a.cliServices {
		daemonArgs = append(daemonArgs, "--svc", svc)
	}
	for _, name := range a.disableServices {
		daemonArgs = append(daemonArgs, "--disable-svc", name)
	}
	if a.verbose {
		daemonArgs = append(daemonArgs, "--verbose")
	}

	logFile, err := os.OpenFile(a.cfg.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error creating log file: %v\n", err)
		os.Exit(1)
	}

	cmd := exec.Command(exePath, daemonArgs...) // #nosec G204 -- launching self as daemon with known args
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	_ = logFile.Close()

	pid := cmd.Process.Pid

	if err := os.WriteFile(a.cfg.PIDFile(), []byte(strconv.Itoa(pid)), 0600); err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error writing PID file: %v\n", err)
		os.Exit(1)
	}

	_ = cmd.Process.Release()

	started := false
	deadline := time.After(5 * time.Second)
	for !started {
		select {
		case <-deadline:
		case <-time.After(200 * time.Millisecond):
			if _, running := a.isRunning(); running {
				if dc, _ := a.dialTarget(); dc != nil {
					dc.Close()
					started = true
				}
			}
			continue
		}
		break
	}

	if started {
		fmt.Printf("%sstarted%s (PID: %d)\n", colorGreen, colorReset, pid)

		if a.startWait {
			fmt.Print("Waiting for all forwards to be ready... ")
			if err := a.waitForReady(); err != nil {
				fmt.Fprintf(os.Stderr, "\n%s%v%s\n", colorRed, err, colorReset)
				os.Exit(1)
			}
		} else {
			fmt.Printf("\nLog file: %s\n", a.cfg.LogFile())
			fmt.Println("\nCommands:")
			fmt.Println("  status  - Check port status")
			fmt.Println("  logs    - View logs")
			fmt.Println("  stop    - Stop proxy")
		}
	} else if _, running := a.isRunning(); running {
		fmt.Printf("%sstarting%s (PID: %d)\n", colorYellow, colorReset, pid)
		if a.startWait {
			fmt.Print("Waiting for all forwards to be ready... ")
			if err := a.waitForReady(); err != nil {
				fmt.Fprintf(os.Stderr, "\n%s%v%s\n", colorRed, err, colorReset)
				os.Exit(1)
			}
		} else {
			fmt.Println("Daemon is still initializing. Check 'status' shortly.")
		}
	} else {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Println("\nCheck logs for errors:")
		a.tailLog(20)

		a.printConflictingInstances()

		os.Exit(1)
	}
}

// printConflictingInstances lists other running instances and, when possible,
// shows which specific ports conflict with the current config.
func (a *app) printConflictingInstances() {
	reg, err := a.openRegistry()
	if err != nil {
		return
	}
	entries, err := reg.List()
	if err != nil || len(entries) == 0 {
		return
	}

	// Try to build a detailed conflict report.
	if a.cfg != nil && len(a.cfg.Services) > 0 {
		liveInstances := a.queryLiveInstances(entries)
		conflicts, _ := buildConflictReport(a.cfg.Services, liveInstances)
		if len(conflicts) > 0 {
			fmt.Printf("\n%sPort conflicts with running instances:%s\n", colorYellow, colorReset)
			fmt.Printf("  %-6s  %-20s  %-20s  %-6s  %s\n", "PORT", "THIS CONFIG", "OWNED BY", "PID", "OWNER CONFIG")
			for _, c := range conflicts {
				owner := c.OwnerConfig
				if owner == "" {
					owner = "(unknown)"
				}
				fmt.Printf("  %-6d  %-20s  %-20s  %-6d  %s\n",
					c.Port, c.ServiceName, c.OwnedBy, c.OwnerPID, owner)
			}
			fmt.Printf("\nTip: use --delegate to hand off non-conflicting services to the running instance.\n")
			fmt.Println()
			return
		}
	}

	// Fall back to brief listing.
	fmt.Printf("\n%sOther running kubeport instances (possible port conflict):%s\n", colorYellow, colorReset)
	for i := range entries {
		printInstanceBrief(&entries[i])
	}
	fmt.Println()
}

// offloadServicesToInstance sends all services from the current config to an
// already-running kubeport instance. The target is either found via the registry
// (matching config file) or via --host / the socket from the loaded config.
func (a *app) offloadServicesToInstance() {
	if a.cfg == nil || len(a.cfg.Services) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no services to offload\n")
		os.Exit(1)
	}

	var target *registry.Entry
	if a.cfg.FilePath() != "" {
		if reg, err := a.openRegistry(); err == nil {
			target, _ = reg.FindByConfig(a.cfg.FilePath())
		}
	}

	// Fall back to direct dial (--host or socket).
	dc, err := a.dialTarget()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to running instance: %v\n", err)
		os.Exit(1)
	}
	if dc == nil {
		if target != nil {
			fmt.Fprintf(os.Stderr, "Error: instance found in registry (PID %d) but cannot connect\n", target.PID)
		} else {
			fmt.Fprintf(os.Stderr, "Error: no running kubeport instance found\n")
		}
		os.Exit(1)
	}
	if target != nil {
		fmt.Printf("Offloading to existing instance (PID: %d, endpoint: %s)\n", target.PID, entryEndpoint(*target))
	} else {
		fmt.Println("Offloading to existing instance...")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	ok, failed := 0, 0
	for _, svc := range a.cfg.Services {
		req := serviceConfigToProto(svc)
		resp, err := dc.client.AddService(ctx, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s✗%s %s: %v\n", colorRed, colorReset, svc.Name, err)
			failed++
			continue
		}
		if !resp.Success {
			fmt.Fprintf(os.Stderr, "  %s✗%s %s: %s\n", colorRed, colorReset, svc.Name, resp.Error)
			failed++
			continue
		}
		fmt.Printf("  %s✓%s %s", colorGreen, colorReset, svc.Name)
		if resp.ActualPort > 0 {
			fmt.Printf(" (local port: %d)", resp.ActualPort)
		}
		fmt.Println()
		ok++
	}
	cancel()
	dc.Close()

	if failed > 0 {
		fmt.Fprintf(os.Stderr, "\n%d service(s) failed to offload\n", failed)
		os.Exit(1)
	}
	fmt.Printf("\n%d service(s) offloaded successfully\n", ok)
}

// serviceConfigToProto converts a config.ServiceConfig to an AddServiceRequest.
func serviceConfigToProto(svc config.ServiceConfig) *kubeportv1.AddServiceRequest {
	req := &kubeportv1.AddServiceRequest{
		Service: &kubeportv1.ServiceInfo{
			Name:       svc.Name,
			Service:    svc.Service,
			Pod:        svc.Pod,
			LocalPort:  int32(svc.LocalPort),  // #nosec G115 -- port numbers fit int32
			RemotePort: int32(svc.RemotePort), // #nosec G115 -- port numbers fit int32
			Namespace:  svc.Namespace,
		},
	}
	if svc.Ports.IsSet() {
		ps := &kubeportv1.PortSpec{
			LocalPortOffset: int32(svc.LocalPortOffset), // #nosec G115 -- offset fits int32
		}
		if svc.Ports.All {
			ps.All = true
		} else {
			names := make([]string, 0, len(svc.Ports.Selectors))
			for _, sel := range svc.Ports.Selectors {
				if sel.Name != "" {
					names = append(names, sel.Name)
				}
			}
			ps.PortNames = names
		}
		req.Ports = ps
	}
	return req
}


// allForwardsReady checks if all forwards are in RUNNING state via gRPC.
func (a *app) allForwardsReady() bool {
	dc, err := a.dialTarget()
	if dc == nil || err != nil {
		return false
	}
	defer dc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := dc.client.Status(ctx, &kubeportv1.StatusRequest{})
	if err != nil {
		return false
	}

	if len(resp.Forwards) == 0 {
		return false
	}

	for _, fw := range resp.Forwards {
		switch fw.State {
		case kubeportv1.ForwardState_FORWARD_STATE_RUNNING,
			kubeportv1.ForwardState_FORWARD_STATE_WAITING,
			kubeportv1.ForwardState_FORWARD_STATE_EXTERNAL:
			// ready (WAITING = lazy with port bound; EXTERNAL = owned by another instance)
		default:
			return false
		}
	}
	return true
}

// waitForReady polls the daemon until all forwards are RUNNING or timeout expires.
func (a *app) waitForReady() error {
	timeout := a.startTimeout
	if timeout == 0 {
		timeout = defaultWaitTimeout
	}

	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			// Build a summary of what's not ready
			return fmt.Errorf("timeout after %s waiting for forwards to be ready\nRun 'kubeport status' to check current state", timeout)
		case <-ticker.C:
			dc, _ := a.dialTarget()
			if dc == nil {
				// Daemon not reachable yet, keep waiting
				if _, running := a.isRunning(); !running {
					return fmt.Errorf("daemon process died while waiting for readiness")
				}
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			resp, err := dc.client.Status(ctx, &kubeportv1.StatusRequest{})
			cancel()
			dc.Close()

			if err != nil {
				continue
			}

			if len(resp.Forwards) == 0 {
				continue
			}

			allReady := true
			for _, fw := range resp.Forwards {
				switch fw.State {
				case kubeportv1.ForwardState_FORWARD_STATE_RUNNING,
					kubeportv1.ForwardState_FORWARD_STATE_WAITING,
					kubeportv1.ForwardState_FORWARD_STATE_EXTERNAL:
					// ok (WAITING = lazy with port bound; EXTERNAL = owned by another instance)
				case kubeportv1.ForwardState_FORWARD_STATE_FAILED, kubeportv1.ForwardState_FORWARD_STATE_STOPPED:
					allReady = false
				default:
					allReady = false
				}
			}

			// Note: if some forwards are permanently failed, we continue waiting
			// because the supervisor may restart them. Only an overall timeout
			// (handled by the deadline case above) terminates the wait.

			if allReady {
				fmt.Printf("%sready%s (%d forwards connected)\n", colorGreen, colorReset, len(resp.Forwards))
				return nil
			}
		}
	}
}

// liveInstance holds a registry entry paired with the live status fetched from it.
type liveInstance struct {
	entry    registry.Entry
	forwards []*kubeportv1.ForwardStatusProto
}

// conflictEntry describes a port clash between the new config and a running instance.
type conflictEntry struct {
	Port        int
	ServiceName string // service name in the new config
	OwnedBy     string // service name on the existing instance
	OwnerPID    int
	OwnerConfig string
}

// queryLiveInstances dials all registry entries concurrently and fetches their
// Status. Entries that cannot be reached within 2 seconds are silently skipped.
func (a *app) queryLiveInstances(entries []registry.Entry) []liveInstance {
	var (
		mu   sync.Mutex
		live []liveInstance
		wg   sync.WaitGroup
	)
	for _, e := range entries {
		wg.Add(1)
		e := e
		go func() {
			defer wg.Done()
			dc, err := a.dialEntryWithTimeout(e, 2*time.Second)
			if err != nil || dc == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			resp, err := dc.client.Status(ctx, &kubeportv1.StatusRequest{})
			cancel()
			dc.Close()
			if err != nil {
				return
			}
			mu.Lock()
			live = append(live, liveInstance{entry: e, forwards: resp.Forwards})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return live
}

// dialEntryWithTimeout dials a registry entry using the appropriate transport
// (Unix socket or TCP+TLS). Returns (nil, nil) on timeout.
func (a *app) dialEntryWithTimeout(e registry.Entry, timeout time.Duration) (*daemonClient, error) {
	if e.TCPAddress != "" {
		apiKey := a.apiKey
		if apiKey == "" && e.ConfigFile != "" {
			if cfg, loadErr := config.Load(e.ConfigFile); loadErr == nil {
				apiKey = cfg.APIKey
			}
		}
		return dialWithTimeout(func() (*daemonClient, error) {
			return dialDaemonTCP(e.TCPAddress, apiKey, "")
		}, timeout)
	}
	if e.Socket != "" {
		return dialDaemonWithTimeout(e.Socket, timeout)
	}
	return nil, nil
}

// buildConflictReport compares new services against live forwards and partitions
// them into conflicting (port already bound) and clean (no conflict) lists.
// Only non-zero static local ports are checked; dynamic ports (0) cannot conflict.
func buildConflictReport(newServices []config.ServiceConfig, live []liveInstance) (conflicts []conflictEntry, clean []config.ServiceConfig) {
	// Build port ownership map: port → (ownerService, ownerPID, ownerConfig)
	type owner struct {
		serviceName string
		pid         int
		configFile  string
	}
	portOwner := make(map[int]owner)
	for _, inst := range live {
		for _, fw := range inst.forwards {
			port := int(fw.ActualPort)
			if port == 0 {
				port = int(fw.GetService().GetLocalPort())
			}
			if port == 0 {
				continue
			}
			portOwner[port] = owner{
				serviceName: fw.GetService().GetName(),
				pid:         inst.entry.PID,
				configFile:  inst.entry.ConfigFile,
			}
		}
	}

	for _, svc := range newServices {
		if svc.LocalPort == 0 || svc.IsMultiPort() {
			clean = append(clean, svc)
			continue
		}
		if o, clash := portOwner[svc.LocalPort]; clash {
			conflicts = append(conflicts, conflictEntry{
				Port:        svc.LocalPort,
				ServiceName: svc.Name,
				OwnedBy:     o.serviceName,
				OwnerPID:    o.pid,
				OwnerConfig: o.configFile,
			})
		} else {
			clean = append(clean, svc)
		}
	}
	return conflicts, clean
}

// cmdStartDelegate implements --delegate start mode: scans running instances for
// port conflicts, hands off non-conflicting services to the primary, then starts
// a lease-holder daemon.
func (a *app) cmdStartDelegate(ctx context.Context) {
	if len(a.cfg.Services) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no services in config\n")
		os.Exit(1)
	}

	fmt.Print("Scanning running instances for port conflicts... ")

	reg, err := a.openRegistry()
	if err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	entries, err := reg.List()
	if err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Filter out other delegate instances when looking for a primary.
	var primaryCandidates []registry.Entry
	for _, e := range entries {
		if !e.Delegate {
			primaryCandidates = append(primaryCandidates, e)
		}
	}

	if len(primaryCandidates) == 0 {
		fmt.Printf("%snone found%s\n", colorYellow, colorReset)
		fmt.Println("No primary instance found — starting normally.")
		a.delegate = false
		a.cmdStartNormal(ctx)
		return
	}

	live := a.queryLiveInstances(primaryCandidates)
	if len(live) == 0 {
		fmt.Printf("%snone reachable%s\n", colorYellow, colorReset)
		fmt.Println("No reachable primary found — starting normally.")
		a.delegate = false
		a.cmdStartNormal(ctx)
		return
	}
	fmt.Printf("%sdone%s\n", colorGreen, colorReset)

	primary := live[0]
	fmt.Printf("  Found primary: PID %d (%s)\n\n", primary.entry.PID, primary.entry.ConfigFile)

	conflicts, clean := buildConflictReport(a.cfg.Services, live)

	if len(conflicts) > 0 {
		fmt.Printf("%sPort conflicts detected:%s\n", colorYellow, colorReset)
		fmt.Printf("  %-6s  %-20s  %-20s  %-6s  %s\n", "PORT", "THIS CONFIG", "OWNED BY", "PID", "OWNER CONFIG")
		for _, c := range conflicts {
			owner := c.OwnerConfig
			if owner == "" {
				owner = "(unknown)"
			}
			fmt.Printf("  %-6d  %-20s  %-20s  %-6d  %s\n",
				c.Port, c.ServiceName, c.OwnedBy, c.OwnerPID, owner)
		}
		fmt.Println()
	}

	if len(clean) == 0 {
		fmt.Printf("%sAll services conflict — nothing to hand off.%s\n", colorRed, colorReset)
		fmt.Println("Use 'kubeport status' to see port ownership.")
		os.Exit(0)
	}

	// Determine the primary's socket address.
	primarySocket := primary.entry.Socket
	if primarySocket == "" {
		primarySocket = primary.entry.TCPAddress
	}
	a.primarySocket = primarySocket

	// Launch the delegate daemon with an empty service list.
	// Hand-off to the primary happens from here (CLI parent) after the daemon starts.
	fmt.Printf("Starting delegate daemon... ")

	exePath, err := os.Executable()
	if err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	daemonArgs := []string{"_daemon", "--delegate", "--primary-socket", primarySocket}
	if a.configFile != "" {
		daemonArgs = append(daemonArgs, "--config", a.configFile)
	}
	if a.cliContext != "" {
		daemonArgs = append(daemonArgs, "--context", a.cliContext)
	}
	if a.cliNamespace != "" {
		daemonArgs = append(daemonArgs, "--namespace", a.cliNamespace)
	}
	if a.verbose {
		daemonArgs = append(daemonArgs, "--verbose")
	}

	logFile, err := os.OpenFile(a.cfg.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error creating log file: %v\n", err)
		os.Exit(1)
	}

	cmd := exec.Command(exePath, daemonArgs...) // #nosec G204 -- launching self as daemon with known args
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	_ = logFile.Close()
	pid := cmd.Process.Pid
	if err := os.WriteFile(a.cfg.PIDFile(), []byte(strconv.Itoa(pid)), 0600); err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error writing PID file: %v\n", err)
		os.Exit(1)
	}
	_ = cmd.Process.Release()

	// Poll until the delegate daemon's socket appears.
	deadline := time.After(5 * time.Second)
polling:
	for {
		select {
		case <-deadline:
			break polling
		case <-time.After(200 * time.Millisecond):
			if dc, _ := dialDaemon(a.socketPath()); dc != nil {
				dc.Close()
				break polling
			}
		}
	}
	fmt.Printf("%sstarted%s (PID: %d)\n\n", colorGreen, colorReset, pid)

	// Hand off clean services to the primary from this CLI process.
	absConfig := a.configFile
	if ap, err2 := filepath.Abs(a.configFile); err2 == nil {
		absConfig = ap
	}

	dc, err := dialDaemonWithTimeout(primarySocket, 3*time.Second)
	if err != nil || dc == nil {
		fmt.Fprintf(os.Stderr, "%sWarning: could not connect to primary to hand off services: %v%s\n", colorYellow, err, colorReset)
		return
	}
	defer dc.Close()

	if len(conflicts) > 0 {
		fmt.Printf("Handing off %d non-conflicting service(s) to primary (PID: %d)...\n", len(clean), primary.entry.PID)
	} else {
		fmt.Printf("Handing off %d service(s) to primary (PID: %d)...\n", len(clean), primary.entry.PID)
	}

	handoffCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ok, failed := 0, 0
	for _, svc := range clean {
		req := serviceConfigToProto(svc)
		req.SourceConfig = absConfig
		resp, err := dc.client.AddService(handoffCtx, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s✗%s %s: %v\n", colorRed, colorReset, svc.Name, err)
			failed++
			continue
		}
		if !resp.Success {
			fmt.Fprintf(os.Stderr, "  %s✗%s %s: %s\n", colorRed, colorReset, svc.Name, resp.Error)
			failed++
			continue
		}
		fmt.Printf("  %s✓%s %s", colorGreen, colorReset, svc.Name)
		if resp.ActualPort > 0 {
			fmt.Printf(" (port %d)", resp.ActualPort)
		}
		fmt.Println()
		ok++
	}

	if len(conflicts) > 0 {
		fmt.Printf("\n%d handed off, %d skipped (port conflict), %d failed\n", ok, len(conflicts), failed)
	} else {
		fmt.Printf("\n%d service(s) handed off successfully\n", ok)
	}
	fmt.Printf("Stop with: kubeport stop --config %s\n", a.configFile)
}

// cmdStartNormal is used when --delegate falls back to a regular start because
// no primary instance was found or reachable.
func (a *app) cmdStartNormal(ctx context.Context) {
	if pid, running := a.isRunning(); running {
		if a.startWait {
			if a.allForwardsReady() {
				fmt.Printf("%sProxy already running and ready (PID: %d)%s\n", colorGreen, pid, colorReset)
				return
			}
			fmt.Printf("%sProxy running (PID: %d), waiting for all forwards...%s\n", colorYellow, pid, colorReset)
			if err := a.waitForReady(); err != nil {
				fmt.Fprintf(os.Stderr, "%s%v%s\n", colorRed, err, colorReset)
				os.Exit(1)
			}
			return
		}
		fmt.Printf("%sProxy is already running (PID: %d)%s\n", colorYellow, pid, colorReset)
		fmt.Println("Use 'status' to check or 'restart' to restart")
		return
	}
	a.launchDaemon(ctx)
}

// dialWithTimeout calls dialFn in a goroutine and returns its result, or
// (nil, nil) if timeout elapses first. If the dial completes after the timeout
// the returned connection is closed to prevent a leak.
func dialWithTimeout(dialFn func() (*daemonClient, error), timeout time.Duration) (*daemonClient, error) {
	type result struct {
		dc  *daemonClient
		err error
	}
	done := make(chan result, 1)
	go func() {
		dc, err := dialFn()
		done <- result{dc, err}
	}()
	select {
	case res := <-done:
		return res.dc, res.err
	case <-time.After(timeout):
		go func() {
			if res := <-done; res.dc != nil {
				res.dc.Close()
			}
		}()
		return nil, nil
	}
}

// dialDaemonWithTimeout attempts to dial a Unix-socket daemon with the given timeout.
func dialDaemonWithTimeout(socketPath string, timeout time.Duration) (*daemonClient, error) {
	return dialWithTimeout(func() (*daemonClient, error) {
		return dialDaemon(socketPath)
	}, timeout)
}

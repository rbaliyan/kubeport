package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/registry"
	"github.com/rbaliyan/kubeport/pkg/config"
)

const defaultWaitTimeout = 30 * time.Second

func (a *app) cmdStart(_ context.Context) {
	if a.cfg == nil {
		fmt.Fprintf(os.Stderr, "%sNo valid config loaded%s\n", colorRed, colorReset)
		os.Exit(1)
	}

	// --offload: send services to an already-running instance instead of starting a new one.
	if a.offload {
		a.offloadServicesToInstance()
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

	fmt.Print("Starting proxy in background... ")

	// Re-exec ourselves as a daemon
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

	// Capture PID before detaching (accessing cmd.Process after Release is undefined)
	pid := cmd.Process.Pid

	// Write PID file
	if err := os.WriteFile(a.cfg.PIDFile(), []byte(strconv.Itoa(pid)), 0600); err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error writing PID file: %v\n", err)
		os.Exit(1)
	}

	// Detach from child
	_ = cmd.Process.Release()

	// Poll for startup: check socket + PID with timeout
	started := false
	deadline := time.After(5 * time.Second)
	for !started {
		select {
		case <-deadline:
			// Timeout
		case <-time.After(200 * time.Millisecond):
			if _, running := a.isRunning(); running {
				// Try to connect to gRPC socket for definitive confirmation
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

		// If --wait, block until all forwards are ready
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
		// PID alive but gRPC not ready yet — slow startup
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

		// Show running instances from registry to help diagnose port conflicts.
		a.printConflictingInstances()

		os.Exit(1)
	}
}

// printConflictingInstances lists any other running kubeport instances from the registry.
func (a *app) printConflictingInstances() {
	reg, err := a.openRegistry()
	if err != nil {
		return
	}
	entries, err := reg.List()
	if err != nil || len(entries) == 0 {
		return
	}
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
	defer dc.Close()

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
		if fw.State != kubeportv1.ForwardState_FORWARD_STATE_RUNNING {
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
				case kubeportv1.ForwardState_FORWARD_STATE_RUNNING:
					// ok
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

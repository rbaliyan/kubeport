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
)

const defaultWaitTimeout = 30 * time.Second

func (a *app) cmdStart(ctx context.Context) {
	if a.cfg == nil {
		fmt.Fprintf(os.Stderr, "%sNo valid config loaded%s\n", colorRed, colorReset)
		os.Exit(1)
	}

	// Check if already running
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

	cmd := exec.Command(exePath, daemonArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	logFile.Close()

	// Write PID file
	if err := os.WriteFile(a.cfg.PIDFile(), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error writing PID file: %v\n", err)
		os.Exit(1)
	}

	// Detach from child
	cmd.Process.Release()

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
				if dc, _ := dialDaemon(a.socketPath()); dc != nil {
					dc.Close()
					started = true
				}
			}
			continue
		}
		break
	}

	if started {
		fmt.Printf("%sstarted%s (PID: %d)\n", colorGreen, colorReset, cmd.Process.Pid)

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
		fmt.Printf("%sstarting%s (PID: %d)\n", colorYellow, colorReset, cmd.Process.Pid)
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
		os.Exit(1)
	}
}

// allForwardsReady checks if all forwards are in RUNNING state via gRPC.
func (a *app) allForwardsReady() bool {
	dc, err := dialDaemon(a.socketPath())
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
			dc, _ := dialDaemon(a.socketPath())
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
			var failed []string
			for _, fw := range resp.Forwards {
				switch fw.State {
				case kubeportv1.ForwardState_FORWARD_STATE_RUNNING:
					// ok
				case kubeportv1.ForwardState_FORWARD_STATE_FAILED, kubeportv1.ForwardState_FORWARD_STATE_STOPPED:
					failed = append(failed, fw.Service.GetName())
					allReady = false
				default:
					allReady = false
				}
			}

			// If any forward is permanently failed (not just starting), report immediately
			if len(failed) > 0 {
				// Check if restarts are exhausted by checking if they stay in failed state
				// For now, continue waiting — the supervisor may restart them
			}

			if allReady {
				fmt.Printf("%sready%s (%d forwards connected)\n", colorGreen, colorReset, len(resp.Forwards))
				return nil
			}
		}
	}
}

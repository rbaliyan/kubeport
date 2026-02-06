package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func (a *app) cmdStart(ctx context.Context) {
	if a.cfg == nil {
		fmt.Fprintf(os.Stderr, "%sNo valid config loaded%s\n", colorRed, colorReset)
		os.Exit(1)
	}

	// Check if already running
	if pid, running := a.isRunning(); running {
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
	if a.configFile != "" {
		daemonArgs = append(daemonArgs, "--config", a.configFile)
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
		fmt.Printf("\nLog file: %s\n", a.cfg.LogFile())
		fmt.Println("\nCommands:")
		fmt.Println("  status  - Check port status")
		fmt.Println("  logs    - View logs")
		fmt.Println("  stop    - Stop proxy")
	} else if _, running := a.isRunning(); running {
		// PID alive but gRPC not ready yet — slow startup
		fmt.Printf("%sstarting%s (PID: %d)\n", colorYellow, colorReset, cmd.Process.Pid)
		fmt.Println("Daemon is still initializing. Check 'status' shortly.")
	} else {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Println("\nCheck logs for errors:")
		a.tailLog(20)
		os.Exit(1)
	}
}

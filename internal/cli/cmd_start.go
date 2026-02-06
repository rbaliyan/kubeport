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

	logFile, err := os.OpenFile(a.cfg.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
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
	if err := os.WriteFile(a.cfg.PIDFile(), []byte(strconv.Itoa(cmd.Process.Pid)), 0644); err != nil {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error writing PID file: %v\n", err)
		os.Exit(1)
	}

	// Detach from child
	cmd.Process.Release()

	// Wait briefly for startup
	time.Sleep(2 * time.Second)

	// Verify it's running
	if _, running := a.isRunning(); running {
		fmt.Printf("%sstarted%s (PID: %d)\n", colorGreen, colorReset, cmd.Process.Pid)
		fmt.Printf("\nLog file: %s\n", a.cfg.LogFile())
		fmt.Println("\nCommands:")
		fmt.Println("  status  - Check port status")
		fmt.Println("  logs    - View logs")
		fmt.Println("  stop    - Stop proxy")
	} else {
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Println("\nCheck logs for errors:")
		a.tailLog(20)
		os.Exit(1)
	}
}

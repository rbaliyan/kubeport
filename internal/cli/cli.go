// Package cli implements the command-line interface for kubeport.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/config"
	"github.com/rbaliyan/kubeport/internal/daemon"
	"github.com/rbaliyan/kubeport/internal/hook"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Build info set via ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Terminal colors
const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[0;33m"
	colorCyan   = "\033[0;36m"
	colorReset  = "\033[0m"
)

type app struct {
	configFile string
	cfg        *config.Config
}

// Execute runs the CLI with the given context.
func Execute(ctx context.Context) {
	a := &app{}
	args := os.Args[1:]

	// Parse flags and extract command
	var command string
	var remaining []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--config" || arg == "-c":
			if i+1 < len(args) {
				i++
				a.configFile = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "Error: %s requires a path argument\n", arg)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--config="):
			a.configFile = strings.TrimPrefix(arg, "--config=")
		default:
			if command == "" {
				if strings.HasPrefix(arg, "-") {
					fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", arg)
					os.Exit(1)
				}
				command = arg
			} else {
				// Once we have a command, pass everything else through
				remaining = append(remaining, arg)
			}
		}
	}

	// Config subcommands don't need a loaded/validated config
	if command == "config" {
		a.handleConfigCommand(remaining)
		return
	}

	// Version doesn't need config
	if command == "version" || command == "--version" {
		a.cmdVersion()
		return
	}

	// Load config (not needed for help)
	if command != "help" && command != "--help" && command != "-h" {
		if err := a.loadConfig(); err != nil {
			// Allow stop/status/logs without valid config
			if command != "stop" && command != "status" && command != "logs" {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	}

	switch command {
	case "", "help", "--help", "-h":
		a.cmdHelp()
	case "start":
		a.cmdStart(ctx)
	case "stop":
		a.cmdStop()
	case "status":
		a.cmdStatus()
	case "logs":
		a.cmdLogs()
	case "restart":
		a.cmdRestart(ctx)
	case "fg", "foreground":
		a.cmdForeground(ctx)
	case "_daemon":
		a.cmdDaemon(ctx, remaining)
	default:
		fmt.Fprintf(os.Stderr, "%sUnknown command: %s%s\n\n", colorRed, command, colorReset)
		a.cmdHelp()
		os.Exit(1)
	}
}

func (a *app) loadConfig() error {
	path := a.configFile
	if path == "" {
		var err error
		path, err = config.Discover()
		if err != nil {
			return err
		}
	}

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation: %w", err)
	}

	a.configFile = path
	a.cfg = cfg
	return nil
}

func (a *app) cmdHelp() {
	exe := filepath.Base(os.Args[0])
	fmt.Printf("Usage: %s [flags] <command>\n\n", exe)
	fmt.Println("Kubernetes port-forward supervisor with health checks and auto-restart.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start       Start proxy in background")
	fmt.Println("  stop        Stop running proxy")
	fmt.Println("  status      Check proxy status and port connectivity")
	fmt.Println("  logs        Show proxy logs (follow mode)")
	fmt.Println("  restart     Restart proxy")
	fmt.Println("  fg          Run in foreground (blocks terminal)")
	fmt.Println("  config      Manage configuration (init, show, set, add, remove, path)")
	fmt.Println("  version     Show version information")
	fmt.Println("  help        Show this help message")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --config, -c <path>         Config file path")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  K8S_CONTEXT     Override context from config")
	fmt.Println("  K8S_NAMESPACE   Override namespace from config")
	fmt.Println()
	fmt.Println("Config file search order:")
	fmt.Println("  1. --config flag")
	fmt.Println("  2. kubeport.{yaml,yml,toml} in current directory")
	fmt.Println("  3. ~/.config/kubeport/kubeport.{yaml,yml,toml}")
	fmt.Println("  4. ~/.kubeport/kubeport.{yaml,yml,toml}")
}

func (a *app) cmdVersion() {
	fmt.Printf("kubeport %s (commit: %s, built: %s)\n", Version, Commit, Date)
}

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

func (a *app) cmdStop() {
	// Try gRPC first
	dc, err := dialDaemon(a.socketPath())
	if dc != nil {
		defer dc.Close()
		a.cmdStopGRPC(dc)
		return
	}
	if err != nil {
		// Socket exists but dial failed — log it and fall back
		fmt.Fprintf(os.Stderr, "%sWarning: gRPC dial failed: %v%s\n", colorYellow, err, colorReset)
	}

	// Fall back to legacy PID-based stop
	a.cmdStopLegacy()
}

func (a *app) cmdStopGRPC(dc *daemonClient) {
	fmt.Print("Stopping proxy via gRPC... ")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := dc.client.Stop(ctx, &kubeportv1.StopRequest{})
	if err != nil {
		// If the daemon is shutting down, the connection may drop — that's success.
		if s, ok := status.FromError(err); ok && s.Code() == codes.Unavailable {
			fmt.Printf("%sstopped%s\n", colorGreen, colorReset)
			return
		}
		fmt.Printf("%sfailed%s\n", colorRed, colorReset)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Println("Trying legacy stop...")
		a.cmdStopLegacy()
		return
	}

	// Wait briefly for the daemon to actually exit
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if _, err := os.Stat(a.socketPath()); os.IsNotExist(err) {
			break
		}
	}

	fmt.Printf("%sstopped%s\n", colorGreen, colorReset)
}

func (a *app) cmdStopLegacy() {
	pid, running := a.isRunning()
	if !running {
		fmt.Printf("%sProxy is not running%s\n", colorYellow, colorReset)
		return
	}

	fmt.Printf("Stopping proxy (PID: %d)... ", pid)

	// Send SIGTERM to the process group
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "%sWarning: SIGTERM failed: %v%s\n", colorYellow, err, colorReset)
	}

	// Wait for process to stop
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if err := syscall.Kill(pid, 0); err != nil {
			break
		}
	}

	// Force kill if still running
	if err := syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}

	if a.cfg != nil {
		os.Remove(a.cfg.PIDFile())
	} else {
		// Try default PID file location
		os.Remove(".kubeport.pid")
	}

	fmt.Printf("%sstopped%s\n", colorGreen, colorReset)
}

func (a *app) cmdStatus() {
	// Try gRPC first
	dc, err := dialDaemon(a.socketPath())
	if dc != nil {
		defer dc.Close()
		a.cmdStatusGRPC(dc)
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sWarning: gRPC dial failed: %v%s\n", colorYellow, err, colorReset)
	}

	// Fall back to legacy PID + port-probe status
	a.cmdStatusLegacy()
}

func (a *app) cmdStatusGRPC(dc *daemonClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := dc.client.Status(ctx, &kubeportv1.StatusRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gRPC status failed: %v\n", err)
		fmt.Println("Falling back to legacy status...")
		a.cmdStatusLegacy()
		return
	}

	fmt.Printf("%sProxy Status%s\n\n", colorCyan, colorReset)
	fmt.Printf("Status: %sRunning%s (gRPC)\n", colorGreen, colorReset)
	fmt.Printf("\nContext:   %s\n", resp.Context)
	fmt.Printf("Namespace: %s\n", resp.Namespace)
	if a.configFile != "" {
		fmt.Printf("Config:    %s\n", a.configFile)
	}

	if len(resp.Forwards) > 0 {
		fmt.Println("\nForwards:")
		for _, fw := range resp.Forwards {
			printForwardStatus(fw)
		}
	}

	fmt.Println()
	fmt.Println("Use 'logs' to view logs")
}

func (a *app) cmdStatusLegacy() {
	fmt.Printf("%sProxy Status%s\n\n", colorCyan, colorReset)

	pid, running := a.isRunning()
	if running {
		fmt.Printf("Status: %sRunning%s (PID: %d)\n", colorGreen, colorReset, pid)
	} else {
		fmt.Printf("Status: %sStopped%s\n", colorRed, colorReset)
	}

	if a.cfg != nil {
		fmt.Printf("\nContext:   %s\n", a.cfg.Context)
		fmt.Printf("Namespace: %s\n", a.cfg.Namespace)
		fmt.Printf("Config:    %s\n", a.configFile)
		fmt.Println("\nPort Status:")
		for _, svc := range a.cfg.Services {
			a.printPortStatus(svc.LocalPort, svc.Name)
		}
	}

	fmt.Println()
	if running {
		fmt.Println("Use 'logs' to view logs")
	} else {
		fmt.Println("Use 'start' to start the proxy")
	}
}

func printForwardStatus(fw *kubeportv1.ForwardStatusProto) {
	var stateColor, stateText, indicator string

	switch fw.State {
	case kubeportv1.ForwardState_FORWARD_STATE_RUNNING:
		stateColor = colorGreen
		stateText = "running"
		indicator = "●"
	case kubeportv1.ForwardState_FORWARD_STATE_STARTING:
		stateColor = colorYellow
		stateText = "starting"
		indicator = "◌"
	case kubeportv1.ForwardState_FORWARD_STATE_FAILED:
		stateColor = colorRed
		stateText = "failed"
		indicator = "✗"
	case kubeportv1.ForwardState_FORWARD_STATE_STOPPED:
		stateColor = colorRed
		stateText = "stopped"
		indicator = "○"
	default:
		stateColor = colorYellow
		stateText = "unknown"
		indicator = "?"
	}

	name := fw.Service.GetName()
	port := fw.ActualPort
	remotePort := fw.Service.GetRemotePort()

	if port > 0 {
		fmt.Printf("  %s%s%s %s: localhost:%d -> :%d [%s%s%s]",
			stateColor, indicator, colorReset,
			name, port, remotePort,
			stateColor, stateText, colorReset)
	} else {
		fmt.Printf("  %s%s%s %s: :%d [%s%s%s]",
			stateColor, indicator, colorReset,
			name, remotePort,
			stateColor, stateText, colorReset)
	}

	if fw.Restarts > 0 {
		fmt.Printf(" (restarts: %d)", fw.Restarts)
	}
	if fw.Error != "" {
		fmt.Printf(" %serr: %s%s", colorRed, fw.Error, colorReset)
	}
	fmt.Println()
}

func (a *app) cmdRestart(ctx context.Context) {
	a.cmdStop()
	a.waitForExit()
	a.cmdStart(ctx)
}

func (a *app) cmdLogs() {
	logFile := ".kubeport.log"
	if a.cfg != nil {
		logFile = a.cfg.LogFile()
	}

	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		fmt.Printf("%sNo log file found: %s%s\n", colorYellow, logFile, colorReset)
		fmt.Println("Start the proxy first")
		os.Exit(1)
	}

	fmt.Printf("%sProxy Logs%s (Ctrl+C to exit)\n", colorCyan, colorReset)
	fmt.Printf("Log file: %s\n---\n", logFile)

	cmd := exec.Command("tail", "-f", logFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
}

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
		os.Remove(a.cfg.PIDFile()) // Clean up PID file on graceful exit
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

// Process management

func (a *app) socketPath() string {
	if a.cfg != nil {
		return a.cfg.SocketFile()
	}
	return ".kubeport.sock"
}

func (a *app) isRunning() (int, bool) {
	pidFile := ".kubeport.pid"
	if a.cfg != nil {
		pidFile = a.cfg.PIDFile()
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}

	// Check if process exists
	if err := syscall.Kill(pid, 0); err != nil {
		os.Remove(pidFile)
		return 0, false
	}

	return pid, true
}

// waitForExit polls until the daemon fully exits (PID gone + socket gone).
func (a *app) waitForExit() {
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			return
		case <-time.After(200 * time.Millisecond):
			_, running := a.isRunning()
			_, sockErr := os.Stat(a.socketPath())
			if !running && os.IsNotExist(sockErr) {
				return
			}
		}
	}
}

func (a *app) printPortStatus(port int, name string) {
	if port == 0 {
		fmt.Printf("  %s~%s %s: dynamic port (check logs)\n", colorYellow, colorReset, name)
		return
	}
	if isPortOpen(port) {
		fmt.Printf("  %s●%s %s: localhost:%d\n", colorGreen, colorReset, name, port)
	} else {
		fmt.Printf("  %s○%s %s: localhost:%d (not connected)\n", colorRed, colorReset, name, port)
	}
}

func (a *app) tailLog(lines int) {
	logFile := ".kubeport.log"
	if a.cfg != nil {
		logFile = a.cfg.LogFile()
	}

	cmd := exec.Command("tail", "-n", strconv.Itoa(lines), logFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func isPortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

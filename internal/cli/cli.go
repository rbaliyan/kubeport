// Package cli implements the command-line interface for kubeport.
package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rbaliyan/kubeport/internal/config"
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

// Process management helpers

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

func isPortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

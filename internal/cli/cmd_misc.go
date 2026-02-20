package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	version "github.com/rbaliyan/go-version"
)

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
	fmt.Println("  add         Add a service to running proxy")
	fmt.Println("  remove      Remove a service from running proxy")
	fmt.Println("  reload      Reload config file (add new, remove deleted services)")
	fmt.Println("  apply       Apply services from a YAML/TOML file to running proxy")
	fmt.Println("  fg          Run in foreground (blocks terminal)")
	fmt.Println("  config      Manage configuration (init, show, validate, set, add, remove, path)")
	fmt.Println("  version     Show version information")
	fmt.Println("  help        Show this help message")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --config, -c <path>         Config file path")
	fmt.Println("  --no-config                 Ignore config file, use only CLI flags")
	fmt.Println("  --context, --kube-context   Kubernetes context (overrides config)")
	fmt.Println("  --namespace, -n <namespace> Kubernetes namespace (overrides config)")
	fmt.Println("  --svc <spec>                Service spec (repeatable); merges with config file")
	fmt.Println("                              Format: name:svc/target:remoteport:localport[:namespace]")
	fmt.Println("                                      name:pod/target:remoteport:localport[:namespace]")
	fmt.Println("  --disable-svc <name>        Disable a service from config (repeatable)")
	fmt.Println("  --json                      Output status as JSON (for status command)")
	fmt.Println("  --wait                      Block until all forwards are connected (for start)")
	fmt.Println("  --timeout <duration>        Max wait time for --wait (default: 30s, implies --wait)")
	fmt.Println("  --help, -h                  Show this help message")
	fmt.Println("  --version, -v               Show version information")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  kubeport start                                    # Use config file")
	fmt.Println("  kubeport start --disable-svc Vault                # Skip Vault from config")
	fmt.Println("  kubeport start --svc \"extra:svc/foo:80:9090\"      # Add service to config")
	fmt.Println("  kubeport fg --no-config --context my-ctx -n dev \\")
	fmt.Println("    --svc \"web:svc/nginx:80:8080\" \\")
	fmt.Println("    --svc \"db:pod/postgres-0:5432:5432\"              # CLI-only, no config file")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  K8S_CONTEXT     Override context from config")
	fmt.Println("  K8S_NAMESPACE   Override namespace from config")
	fmt.Println()
	fmt.Println("Config file search order:")
	fmt.Println("  1. --config flag")
	fmt.Println("  2. kubeport.{yaml,yml,toml} in current directory")
	fmt.Println("  3. .kubeport.{yaml,yml,toml} in current directory")
	fmt.Println("  4. ~/.config/kubeport/kubeport.{yaml,yml,toml}")
	fmt.Println("  5. ~/.kubeport/kubeport.{yaml,yml,toml}")
}

func (a *app) cmdVersion() {
	v := version.Get()
	g := version.Git()
	b := version.Build()

	if v.Raw != "" {
		fmt.Printf("kubeport %s", v.Raw)
	} else {
		fmt.Printf("kubeport dev")
	}
	if g.Commit != "" {
		fmt.Printf(" (commit: %.7s", g.Commit)
		if !b.Timestamp.IsZero() {
			fmt.Printf(", built: %s", b.Timestamp.Format(time.RFC3339))
		}
		fmt.Print(")")
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

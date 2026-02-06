package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

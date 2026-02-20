// Package cli implements the command-line interface for kubeport.
package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	version "github.com/rbaliyan/go-version"
	"github.com/rbaliyan/kubeport/internal/config"
	"github.com/rbaliyan/kubeport/internal/netutil"
)

func init() {
	version.SetAppInfo("kubeport", "Kubernetes port-forward supervisor")
	// Fall back to git info when running via `go run`
	_ = version.LoadFromGit()
}

// Terminal colors
const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[0;33m"
	colorCyan   = "\033[0;36m"
	colorReset  = "\033[0m"
)

type app struct {
	configFile      string
	noConfig        bool
	cfg             *config.Config
	cliContext      string
	cliNamespace    string
	cliServices     []string
	disableServices []string
	startWait       bool
	startTimeout    time.Duration
	statusJSON      bool
}

// Execute runs the CLI with the given context.
func Execute(ctx context.Context) {
	a := &app{}
	command, remaining := a.parseArgs(os.Args[1:])
	a.dispatch(ctx, command, remaining)
}

// parseArgs parses global flags from args and returns the command and any remaining args.
func (a *app) parseArgs(args []string) (command string, remaining []string) {
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
		case arg == "--context" || arg == "--kube-context":
			if i+1 < len(args) {
				i++
				a.cliContext = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "Error: %s requires a value\n", arg)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--context="):
			a.cliContext = strings.TrimPrefix(arg, "--context=")
		case strings.HasPrefix(arg, "--kube-context="):
			a.cliContext = strings.TrimPrefix(arg, "--kube-context=")
		case arg == "--namespace" || arg == "-n":
			if i+1 < len(args) {
				i++
				a.cliNamespace = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "Error: %s requires a value\n", arg)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--namespace="):
			a.cliNamespace = strings.TrimPrefix(arg, "--namespace=")
		case arg == "--svc":
			if i+1 < len(args) {
				i++
				a.cliServices = append(a.cliServices, args[i])
			} else {
				fmt.Fprintf(os.Stderr, "Error: %s requires a service spec\n", arg)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--svc="):
			a.cliServices = append(a.cliServices, strings.TrimPrefix(arg, "--svc="))
		case arg == "--disable-svc":
			if i+1 < len(args) {
				i++
				a.disableServices = append(a.disableServices, args[i])
			} else {
				fmt.Fprintf(os.Stderr, "Error: %s requires a service name\n", arg)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--disable-svc="):
			a.disableServices = append(a.disableServices, strings.TrimPrefix(arg, "--disable-svc="))
		case arg == "--no-config":
			a.noConfig = true
		case arg == "--json":
			a.statusJSON = true
		case arg == "--wait":
			a.startWait = true
		case arg == "--timeout":
			if i+1 < len(args) {
				i++
				d, err := time.ParseDuration(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --timeout value %q: %v\n", args[i], err)
					os.Exit(1)
				}
				a.startTimeout = d
				a.startWait = true // --timeout implies --wait
			} else {
				fmt.Fprintf(os.Stderr, "Error: %s requires a duration (e.g., 30s)\n", arg)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--timeout="):
			d, err := time.ParseDuration(strings.TrimPrefix(arg, "--timeout="))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --timeout value: %v\n", err)
				os.Exit(1)
			}
			a.startTimeout = d
			a.startWait = true
		case arg == "--help" || arg == "-h":
			a.cmdHelp()
			os.Exit(0)
		case arg == "--version" || arg == "-v":
			a.cmdVersion()
			os.Exit(0)
		default:
			if command == "" {
				if strings.HasPrefix(arg, "-") {
					fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", arg)
					os.Exit(1)
				}
				command = arg
			} else {
				remaining = append(remaining, arg)
			}
		}
	}
	return command, remaining
}

// dispatch routes the parsed command to the appropriate handler.
func (a *app) dispatch(ctx context.Context, command string, remaining []string) {
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
			// Allow stop/status/logs/add/remove/reload without valid config
			if command != "stop" && command != "status" && command != "logs" &&
				command != "add" && command != "remove" && command != "reload" && command != "apply" {
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
	case "add":
		a.cmdAdd(remaining)
	case "remove":
		a.cmdRemove(remaining)
	case "reload":
		a.cmdReload()
	case "apply":
		a.cmdApply(remaining)
	case "_daemon":
		a.cmdDaemon(ctx, remaining)
	default:
		fmt.Fprintf(os.Stderr, "%sUnknown command: %s%s\n\n", colorRed, command, colorReset)
		a.cmdHelp()
		os.Exit(1)
	}
}

func (a *app) loadConfig() error {
	// Parse CLI service specs upfront (used by both paths)
	cliSvcs, err := a.parseCLIServices()
	if err != nil {
		return err
	}

	// --no-config or only --svc flags with no config file specified: pure CLI mode
	if a.noConfig || (len(cliSvcs) > 0 && a.configFile == "" && !a.hasDiscoverableConfig()) {
		return a.buildConfigFromCLI(cliSvcs)
	}

	// Load config file
	path := a.configFile
	if !a.noConfig && path == "" {
		path, err = config.Discover()
		if err != nil {
			// If --svc provided, fall back to CLI-only mode
			if len(cliSvcs) > 0 {
				return a.buildConfigFromCLI(cliSvcs)
			}
			return err
		}
	}

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	// CLI flags override config file values
	if a.cliContext != "" {
		cfg.Context = a.cliContext
	}
	if a.cliNamespace != "" {
		cfg.Namespace = a.cliNamespace
	}

	// Apply --disable-svc: remove matching services by name
	if len(a.disableServices) > 0 {
		disabled := make(map[string]bool, len(a.disableServices))
		for _, name := range a.disableServices {
			disabled[strings.ToLower(name)] = true
		}
		filtered := cfg.Services[:0]
		for _, svc := range cfg.Services {
			if !disabled[strings.ToLower(svc.Name)] {
				filtered = append(filtered, svc)
			}
		}
		cfg.Services = filtered
	}

	// Apply --svc: override matching service names, append new ones
	if len(cliSvcs) > 0 {
		for _, cs := range cliSvcs {
			found := false
			for i, existing := range cfg.Services {
				if strings.EqualFold(existing.Name, cs.Name) {
					cfg.Services[i] = cs
					found = true
					break
				}
			}
			if !found {
				cfg.Services = append(cfg.Services, cs)
			}
		}
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation: %w", err)
	}

	a.configFile = path
	a.cfg = cfg
	return nil
}

func (a *app) buildConfigFromCLI(services []config.ServiceConfig) error {
	if len(services) == 0 {
		return fmt.Errorf("no services specified (use --svc or a config file)")
	}

	namespace := a.cliNamespace
	if namespace == "" {
		namespace = "default"
	}

	cfg := config.NewInMemory(a.cliContext, namespace, services)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation: %w", err)
	}

	a.cfg = cfg
	return nil
}

func (a *app) parseCLIServices() ([]config.ServiceConfig, error) {
	if len(a.cliServices) == 0 {
		return nil, nil
	}
	services := make([]config.ServiceConfig, 0, len(a.cliServices))
	for _, raw := range a.cliServices {
		svc, err := parseSvcFlag(raw)
		if err != nil {
			return nil, err
		}
		services = append(services, svc)
	}
	return services, nil
}

func (a *app) hasDiscoverableConfig() bool {
	_, err := config.Discover()
	return err == nil
}

func parseSvcFlag(s string) (config.ServiceConfig, error) {
	// Format: name:type/target:remoteport:localport[:namespace]
	parts := strings.SplitN(s, ":", 5)
	if len(parts) < 4 {
		return config.ServiceConfig{}, fmt.Errorf(
			"invalid --svc format %q: expected name:type/target:remoteport:localport[:namespace]", s)
	}

	name := parts[0]
	typTarget := parts[1]

	remote, err := strconv.Atoi(parts[2])
	if err != nil {
		return config.ServiceConfig{}, fmt.Errorf("invalid remote port in --svc %q: %w", s, err)
	}

	local, err := strconv.Atoi(parts[3])
	if err != nil {
		return config.ServiceConfig{}, fmt.Errorf("invalid local port in --svc %q: %w", s, err)
	}

	slash := strings.SplitN(typTarget, "/", 2)
	if len(slash) != 2 || slash[1] == "" {
		return config.ServiceConfig{}, fmt.Errorf(
			"invalid target in --svc %q: expected svc/<name> or pod/<name>", s)
	}

	svc := config.ServiceConfig{
		Name:       name,
		LocalPort:  local,
		RemotePort: remote,
	}

	switch slash[0] {
	case "svc", "service":
		svc.Service = slash[1]
	case "pod":
		svc.Pod = slash[1]
	default:
		return config.ServiceConfig{}, fmt.Errorf(
			"invalid type %q in --svc %q: expected 'svc' or 'pod'", slash[0], s)
	}

	if len(parts) == 5 && parts[4] != "" {
		svc.Namespace = parts[4]
	}

	return svc, nil
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
		_ = os.Remove(pidFile)
		return 0, false
	}

	return pid, true
}

// waitForExit polls until the daemon fully exits (PID gone + socket gone).
func (a *app) waitForExit() {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()

	poll := time.NewTimer(200 * time.Millisecond)
	defer poll.Stop()

	for {
		select {
		case <-deadline.C:
			return
		case <-poll.C:
			_, running := a.isRunning()
			_, sockErr := os.Stat(a.socketPath())
			if !running && os.IsNotExist(sockErr) {
				return
			}
			poll.Reset(200 * time.Millisecond)
		}
	}
}

func (a *app) printPortStatus(port int, name string) {
	if port == 0 {
		fmt.Printf("  %s~%s %s: dynamic port (check logs)\n", colorYellow, colorReset, name)
		return
	}
	if netutil.IsPortOpen(port) {
		fmt.Printf("  %s●%s %s: localhost:%d\n", colorGreen, colorReset, name, port)
	} else {
		fmt.Printf("  %s○%s %s: localhost:%d (not connected)\n", colorRed, colorReset, name, port)
	}
}

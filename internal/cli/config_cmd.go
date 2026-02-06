package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/rbaliyan/kubeport/internal/config"
)

func (a *app) handleConfigCommand(args []string) {
	if len(args) == 0 {
		a.configHelp()
		return
	}

	switch args[0] {
	case "init":
		a.configInit(args[1:])
	case "show":
		a.configShow()
	case "set":
		a.configSet(args[1:])
	case "add":
		a.configAdd(args[1:])
	case "remove":
		a.configRemove(args[1:])
	case "path":
		a.configPath()
	case "help":
		a.configHelp()
	default:
		fmt.Fprintf(os.Stderr, "%sUnknown config subcommand: %s%s\n\n", colorRed, args[0], colorReset)
		a.configHelp()
		os.Exit(1)
	}
}

func (a *app) configHelp() {
	fmt.Println("Usage: kubeport config <subcommand> [options]")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  init                Create a new config file")
	fmt.Println("  show                Display current configuration")
	fmt.Println("  set <key> <value>   Set a config value (context, namespace)")
	fmt.Println("  add                 Add a service to the config")
	fmt.Println("  remove <name>       Remove a service by name")
	fmt.Println("  path                Print the config file path")
	fmt.Println("  help                Show this help message")
	fmt.Println()
	fmt.Println("Init options:")
	fmt.Println("  --format yaml|toml  Config format (default: yaml)")
	fmt.Println("  -o, --output <path> Output file path")
	fmt.Println()
	fmt.Println("Add options:")
	fmt.Println("  --name <name>           Service display name (required)")
	fmt.Println("  --service <name>        Kubernetes service name")
	fmt.Println("  --pod <name>            Kubernetes pod name")
	fmt.Println("  --local-port <port>     Local port (0 for dynamic)")
	fmt.Println("  --remote-port <port>    Remote port (required)")
	fmt.Println("  --namespace <namespace> Namespace override")
}

func (a *app) configInit(args []string) {
	format := config.FormatYAML
	var outputPath string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "Error: --format requires a value (yaml or toml)\n")
				os.Exit(1)
			}
			i++
			switch strings.ToLower(args[i]) {
			case "yaml", "yml":
				format = config.FormatYAML
			case "toml":
				format = config.FormatTOML
			default:
				fmt.Fprintf(os.Stderr, "Error: unsupported format %q (use yaml or toml)\n", args[i])
				os.Exit(1)
			}
		case "-o", "--output":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "Error: %s requires a path\n", args[i])
				os.Exit(1)
			}
			i++
			outputPath = args[i]
		}
	}

	if outputPath == "" {
		switch format {
		case config.FormatTOML:
			outputPath = "kubeport.toml"
		default:
			outputPath = "kubeport.yaml"
		}
	}

	abs, err := filepath.Abs(outputPath)
	if err != nil {
		abs = outputPath
	}

	cfg, err := config.Init(abs, format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	fmt.Printf("%sCreated %s%s\n", colorGreen, cfg.FilePath(), colorReset)
	fmt.Println("\nNext steps:")
	fmt.Println("  kubeport config set context <your-context>")
	fmt.Println("  kubeport config set namespace <your-namespace>")
	fmt.Println("  kubeport config add --name MyAPI --service my-api --local-port 8080 --remote-port 80")
	fmt.Println("  kubeport start")
}

func (a *app) configShow() {
	path := a.resolveConfigPath()
	if path == "" {
		fmt.Fprintf(os.Stderr, "%sNo config file found%s\n", colorRed, colorReset)
		fmt.Println("Run 'kubeport config init' to create one")
		os.Exit(1)
	}

	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	fmt.Printf("%sConfiguration%s (%s)\n\n", colorCyan, colorReset, path)
	fmt.Printf("Context:   %s\n", valueOrDefault(cfg.Context, "(not set)"))
	fmt.Printf("Namespace: %s\n", valueOrDefault(cfg.Namespace, "(not set)"))
	fmt.Printf("Format:    %s\n", cfg.FileFormat())

	if len(cfg.Services) == 0 {
		fmt.Println("\nNo services configured")
		return
	}

	fmt.Printf("\nServices (%d):\n", len(cfg.Services))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  NAME\tTYPE\tTARGET\tLOCAL\tREMOTE\tNAMESPACE")
	for _, svc := range cfg.Services {
		typ := "service"
		target := svc.Service
		if svc.IsPod() {
			typ = "pod"
			target = svc.Pod
		}
		localPort := strconv.Itoa(svc.LocalPort)
		if svc.LocalPort == 0 {
			localPort = "dynamic"
		}
		ns := svc.Namespace
		if ns == "" {
			ns = "-"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%d\t%s\n", svc.Name, typ, target, localPort, svc.RemotePort, ns)
	}
	w.Flush()
}

func (a *app) configSet(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: kubeport config set <key> <value>\n")
		fmt.Fprintf(os.Stderr, "Keys: context, namespace\n")
		os.Exit(1)
	}

	path := a.resolveConfigPath()
	if path == "" {
		fmt.Fprintf(os.Stderr, "%sNo config file found%s\n", colorRed, colorReset)
		fmt.Println("Run 'kubeport config init' to create one")
		os.Exit(1)
	}

	cfg, err := config.LoadForEdit(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	key := args[0]
	value := args[1]

	switch key {
	case "context":
		cfg.Context = value
	case "namespace":
		cfg.Namespace = value
	default:
		fmt.Fprintf(os.Stderr, "Unknown config key: %s (use 'context' or 'namespace')\n", key)
		os.Exit(1)
	}

	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "%sError saving config: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	fmt.Printf("Set %s = %s\n", key, value)
}

func (a *app) configAdd(args []string) {
	var svc config.ServiceConfig

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "Error: --name requires a value\n")
				os.Exit(1)
			}
			i++
			svc.Name = args[i]
		case "--service":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "Error: --service requires a value\n")
				os.Exit(1)
			}
			i++
			svc.Service = args[i]
		case "--pod":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "Error: --pod requires a value\n")
				os.Exit(1)
			}
			i++
			svc.Pod = args[i]
		case "--local-port":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "Error: --local-port requires a value\n")
				os.Exit(1)
			}
			i++
			port, err := strconv.Atoi(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid local port: %s\n", args[i])
				os.Exit(1)
			}
			svc.LocalPort = port
		case "--remote-port":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "Error: --remote-port requires a value\n")
				os.Exit(1)
			}
			i++
			port, err := strconv.Atoi(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid remote port: %s\n", args[i])
				os.Exit(1)
			}
			svc.RemotePort = port
		case "--namespace":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "Error: --namespace requires a value\n")
				os.Exit(1)
			}
			i++
			svc.Namespace = args[i]
		default:
			fmt.Fprintf(os.Stderr, "Unknown option: %s\n", args[i])
			os.Exit(1)
		}
	}

	// Validate required fields
	if svc.Name == "" {
		fmt.Fprintf(os.Stderr, "Error: --name is required\n")
		os.Exit(1)
	}
	if svc.Service == "" && svc.Pod == "" {
		fmt.Fprintf(os.Stderr, "Error: --service or --pod is required\n")
		os.Exit(1)
	}
	if svc.Service != "" && svc.Pod != "" {
		fmt.Fprintf(os.Stderr, "Error: use --service or --pod, not both\n")
		os.Exit(1)
	}
	if svc.RemotePort == 0 {
		fmt.Fprintf(os.Stderr, "Error: --remote-port is required\n")
		os.Exit(1)
	}

	path := a.resolveConfigPath()
	if path == "" {
		fmt.Fprintf(os.Stderr, "%sNo config file found%s\n", colorRed, colorReset)
		fmt.Println("Run 'kubeport config init' to create one")
		os.Exit(1)
	}

	cfg, err := config.LoadForEdit(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	if err := cfg.AddService(svc); err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "%sError saving config: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	localPort := strconv.Itoa(svc.LocalPort)
	if svc.LocalPort == 0 {
		localPort = "dynamic"
	}
	fmt.Printf("Added service %q (%s:%s -> %d)\n", svc.Name, svc.Target(), localPort, svc.RemotePort)
}

func (a *app) configRemove(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: kubeport config remove <name>\n")
		os.Exit(1)
	}
	name := args[0]

	path := a.resolveConfigPath()
	if path == "" {
		fmt.Fprintf(os.Stderr, "%sNo config file found%s\n", colorRed, colorReset)
		os.Exit(1)
	}

	cfg, err := config.LoadForEdit(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	if err := cfg.RemoveService(name); err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "%sError saving config: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	fmt.Printf("Removed service %q\n", name)
}

func (a *app) configPath() {
	path := a.resolveConfigPath()
	if path == "" {
		fmt.Fprintf(os.Stderr, "%sNo config file found%s\n", colorRed, colorReset)
		os.Exit(1)
	}
	fmt.Println(path)
}

// resolveConfigPath returns the config file path from --config flag or discovery.
func (a *app) resolveConfigPath() string {
	if a.configFile != "" {
		return a.configFile
	}
	path, err := config.Discover()
	if err != nil {
		return ""
	}
	return path
}

func valueOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

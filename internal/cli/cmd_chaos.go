package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
)

// handleChaosCommand dispatches chaos sub-commands.
func (a *app) handleChaosCommand(args []string) {
	if len(args) == 0 {
		a.printChaosHelp()
		os.Exit(0)
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "set":
		a.cmdChaosSet(rest)
	case "enable":
		a.cmdChaosEnable(rest)
	case "disable":
		a.cmdChaosDisable(rest)
	case "preset":
		a.cmdChaosPreset(rest)
	case "reset":
		a.cmdChaosReset(rest)
	case "help", "--help", "-h":
		a.printChaosHelp()
	default:
		fmt.Fprintf(os.Stderr, "%sUnknown chaos subcommand: %s%s\n\n", colorRed, sub, colorReset)
		a.printChaosHelp()
		os.Exit(1)
	}
}

// cmdChaosSet applies explicit chaos params to one or more services.
//
//	kubeport chaos set [<service>...] [--error-rate 0.05] [--latency 500ms] [--spike-prob 0.05] [--all]
func (a *app) cmdChaosSet(args []string) {
	var services []string
	var errorRate float64
	var spikeProb float64
	var latencyStr string
	all := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--error-rate" || arg == "-e":
			v := parseNextArg(args, &i, arg)
			if _, err := fmt.Sscanf(v, "%f", &errorRate); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --error-rate %q\n", v)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--error-rate="):
			v := strings.TrimPrefix(arg, "--error-rate=")
			if _, err := fmt.Sscanf(v, "%f", &errorRate); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --error-rate %q\n", v)
				os.Exit(1)
			}
		case arg == "--latency" || arg == "-l":
			latencyStr = parseNextArg(args, &i, arg)
		case strings.HasPrefix(arg, "--latency="):
			latencyStr = strings.TrimPrefix(arg, "--latency=")
		case arg == "--spike-prob" || arg == "-p":
			v := parseNextArg(args, &i, arg)
			if _, err := fmt.Sscanf(v, "%f", &spikeProb); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --spike-prob %q\n", v)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--spike-prob="):
			v := strings.TrimPrefix(arg, "--spike-prob=")
			if _, err := fmt.Sscanf(v, "%f", &spikeProb); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --spike-prob %q\n", v)
				os.Exit(1)
			}
		case arg == "--all":
			all = true
		default:
			if !strings.HasPrefix(arg, "-") {
				services = append(services, arg)
			} else {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", arg)
				os.Exit(1)
			}
		}
	}

	if !all && len(services) == 0 {
		fmt.Fprintln(os.Stderr, "Error: specify at least one service name or --all")
		os.Exit(1)
	}
	if all {
		services = nil // empty = all in the RPC
	}

	var spikeDurationMs int64
	if latencyStr != "" {
		d, err := time.ParseDuration(latencyStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --latency %q: %v\n", latencyStr, err)
			os.Exit(1)
		}
		spikeDurationMs = d.Milliseconds()
		if spikeProb == 0 {
			spikeProb = 1.0 // latency with no explicit prob → always spike
		}
	}

	req := &kubeportv1.UpdateChaosRequest{
		Services:         services,
		Enabled:          true,
		ErrorRate:        errorRate,
		SpikeProbability: spikeProb,
		SpikeDurationMs:  spikeDurationMs,
	}
	a.runUpdateChaos(req)
}

// cmdChaosEnable enables chaos for services using their current/config params.
func (a *app) cmdChaosEnable(args []string) {
	services, all := parseChaosTargets(args)
	if !all && len(services) == 0 {
		fmt.Fprintln(os.Stderr, "Error: specify at least one service name or --all")
		os.Exit(1)
	}
	if all {
		services = nil
	}
	req := &kubeportv1.UpdateChaosRequest{Services: services, Enabled: true}
	a.runUpdateChaos(req)
}

// cmdChaosDisable disables chaos for services without touching the config.
func (a *app) cmdChaosDisable(args []string) {
	services, all := parseChaosTargets(args)
	if !all && len(services) == 0 {
		fmt.Fprintln(os.Stderr, "Error: specify at least one service name or --all")
		os.Exit(1)
	}
	if all {
		services = nil
	}
	// Send enabled=false with no other params to disable.
	req := &kubeportv1.UpdateChaosRequest{Services: services, Enabled: false}
	a.runUpdateChaos(req)
}

// cmdChaosPreset applies a named chaos preset.
//
//	kubeport chaos preset <preset> [<service>...] [--all]
//
// Built-in presets: slow-network, unstable-cluster, packet-loss
func (a *app) cmdChaosPreset(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Error: preset name required")
		fmt.Fprintln(os.Stderr, "Available presets: slow-network, unstable-cluster, packet-loss")
		os.Exit(1)
	}
	presetName := args[0]
	services, all := parseChaosTargets(args[1:])
	if all {
		services = nil
	}

	var preset kubeportv1.ChaosPreset
	switch presetName {
	case "slow-network":
		preset = kubeportv1.ChaosPreset_CHAOS_PRESET_SLOW_NETWORK
	case "unstable-cluster":
		preset = kubeportv1.ChaosPreset_CHAOS_PRESET_UNSTABLE_CLUSTER
	case "packet-loss":
		preset = kubeportv1.ChaosPreset_CHAOS_PRESET_PACKET_LOSS
	default:
		fmt.Fprintf(os.Stderr, "Unknown preset %q. Available: slow-network, unstable-cluster, packet-loss\n", presetName)
		os.Exit(1)
	}
	req := &kubeportv1.UpdateChaosRequest{Services: services, Preset: preset}
	a.runUpdateChaos(req)
}

// cmdChaosReset reverts services back to their config-defined chaos settings.
func (a *app) cmdChaosReset(args []string) {
	services, all := parseChaosTargets(args)
	if all {
		services = nil
	}
	req := &kubeportv1.UpdateChaosRequest{Services: services, Reset_: true}
	a.runUpdateChaos(req)
}

// runUpdateChaos dials the daemon and calls UpdateChaos RPC.
func (a *app) runUpdateChaos(req *kubeportv1.UpdateChaosRequest) {
	dc, err := a.dialTarget()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
		os.Exit(1)
	}
	if dc == nil {
		fmt.Fprintf(os.Stderr, "Proxy is not running\n")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	resp, rpcErr := dc.client.UpdateChaos(ctx, req)
	cancel()
	dc.Close()

	if rpcErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", rpcErr)
		os.Exit(1)
	}

	if len(resp.Updated) > 0 {
		fmt.Printf("%sUpdated:%s %s\n", colorGreen, colorReset, strings.Join(resp.Updated, ", "))
	}
	if len(resp.NotFound) > 0 {
		fmt.Printf("%sNot found:%s %s\n", colorYellow, colorReset, strings.Join(resp.NotFound, ", "))
	}
}

// parseChaosTargets extracts service names and --all flag from args.
func parseChaosTargets(args []string) (services []string, all bool) {
	for _, arg := range args {
		if arg == "--all" {
			all = true
		} else if !strings.HasPrefix(arg, "-") {
			services = append(services, arg)
		}
	}
	return services, all
}

func (a *app) printChaosHelp() {
	fmt.Println("Manage live chaos engineering settings without restarting or reloading.")
	fmt.Println()
	fmt.Println("Usage: kubeport chaos <subcommand> [<service>...] [flags]")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  set      [<service>...] --error-rate 0.05 --latency 500ms --spike-prob 0.1 [--all]")
	fmt.Println("           Apply explicit chaos params to the named services")
	fmt.Println("  enable   [<service>...] [--all]")
	fmt.Println("           Enable chaos with current params for the named services")
	fmt.Println("  disable  [<service>...] [--all]")
	fmt.Println("           Disable chaos for the named services (does not change config)")
	fmt.Println("  preset   <name> [<service>...] [--all]")
	fmt.Println("           Apply a named preset to the named services")
	fmt.Println("  reset    [<service>...] [--all]")
	fmt.Println("           Revert to config-defined chaos for the named services")
	fmt.Println()
	fmt.Println("Built-in presets:")
	fmt.Println("  slow-network      200 ms latency spikes (10% probability)")
	fmt.Println("  unstable-cluster  5% errors + 5% 2s latency spikes")
	fmt.Println("  packet-loss       15% connection errors")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  kubeport chaos set postgres --error-rate 0.05")
	fmt.Println("  kubeport chaos set postgres --latency 200ms --spike-prob 0.1")
	fmt.Println("  kubeport chaos preset slow-network postgres redis")
	fmt.Println("  kubeport chaos enable postgres")
	fmt.Println("  kubeport chaos disable --all")
	fmt.Println("  kubeport chaos reset postgres")
}

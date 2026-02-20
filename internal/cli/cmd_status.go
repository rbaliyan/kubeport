package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	version "github.com/rbaliyan/go-version"
	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/config"
	"github.com/rbaliyan/kubeport/internal/netutil"
)

// JSON output types for --json flag.
type statusOutput struct {
	Running       bool                  `json:"running"`
	CLIVersion    string                `json:"cli_version,omitempty"`
	DaemonVersion string                `json:"daemon_version,omitempty"`
	Context       string                `json:"context,omitempty"`
	Namespace     string                `json:"namespace,omitempty"`
	Config        string                `json:"config,omitempty"`
	Forwards      []forwardStatusOutput `json:"forwards,omitempty"`
}

type forwardStatusOutput struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	Target     string `json:"target,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Restarts   int    `json:"restarts,omitempty"`
	Error      string `json:"error,omitempty"`
	NextRetry  string `json:"next_retry,omitempty"`
}

func (a *app) cmdStatus() {
	// Try gRPC first
	dc, err := dialDaemon(a.socketPath())
	if dc != nil {
		defer dc.Close()
		a.cmdStatusGRPC(dc)
		return
	}
	if err != nil && !a.statusJSON {
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
		if a.statusJSON {
			a.writeJSON(statusOutput{Running: false})
			return
		}
		fmt.Fprintf(os.Stderr, "gRPC status failed: %v\n", err)
		fmt.Println("Falling back to legacy status...")
		a.cmdStatusLegacy()
		return
	}

	cliVer := version.Get().Raw
	daemonVer := resp.Version

	if a.statusSort {
		slices.SortFunc(resp.Forwards, func(a, b *kubeportv1.ForwardStatusProto) int {
			return strings.Compare(a.Service.GetName(), b.Service.GetName())
		})
	}

	if a.statusJSON {
		out := statusOutput{
			Running:       true,
			CLIVersion:    cliVer,
			DaemonVersion: daemonVer,
			Context:       resp.Context,
			Namespace:     resp.Namespace,
			Config:        a.configFile,
		}
		for _, fw := range resp.Forwards {
			out.Forwards = append(out.Forwards, forwardFromProto(fw))
		}
		a.writeJSON(out)
		return
	}

	fmt.Printf("%sProxy Status%s\n\n", colorCyan, colorReset)
	fmt.Printf("Status: %sRunning%s (gRPC)\n", colorGreen, colorReset)
	if cliVer != "" || daemonVer != "" {
		fmt.Printf("\nCLI version:    %s\n", cliVer)
		fmt.Printf("Daemon version: %s\n", daemonVer)
		if cliVer != "" && daemonVer != "" && cliVer != daemonVer {
			fmt.Printf("%sWarning: CLI and daemon versions differ — consider restarting the daemon%s\n", colorYellow, colorReset)
		}
	}
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
	pid, running := a.isRunning()

	services := a.legacyServices()

	if a.statusJSON {
		out := statusOutput{Running: running}
		if a.cfg != nil {
			out.Context = a.cfg.Context
			out.Namespace = a.cfg.Namespace
			out.Config = a.configFile
			for _, svc := range services {
				state := "unknown"
				if running && netutil.IsPortOpen(svc.LocalPort) {
					state = "running"
				} else if !running {
					state = "stopped"
				}
				target := svc.Service
				if target == "" {
					target = svc.Pod
				}
				out.Forwards = append(out.Forwards, forwardStatusOutput{
					Name:       svc.Name,
					State:      state,
					LocalPort:  svc.LocalPort,
					RemotePort: svc.RemotePort,
					Target:     target,
					Namespace:  svc.Namespace,
				})
			}
		}
		a.writeJSON(out)
		return
	}

	fmt.Printf("%sProxy Status%s\n\n", colorCyan, colorReset)

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
		for _, svc := range services {
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

func forwardFromProto(fw *kubeportv1.ForwardStatusProto) forwardStatusOutput {
	svc := fw.GetService()
	target := svc.GetService()
	if target == "" {
		target = svc.GetPod()
	}
	out := forwardStatusOutput{
		Name:       svc.GetName(),
		State:      strings.TrimPrefix(strings.ToLower(fw.State.String()), "forward_state_"),
		LocalPort:  int(fw.ActualPort),
		RemotePort: int(svc.GetRemotePort()),
		Target:     target,
		Namespace:  svc.GetNamespace(),
		Restarts:   int(fw.Restarts),
		Error:      fw.Error,
	}
	if fw.NextRetry != nil && fw.NextRetry.IsValid() {
		out.NextRetry = fw.NextRetry.AsTime().Format(time.RFC3339)
	}
	return out
}

func (a *app) writeJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// legacyServices returns config services, optionally sorted by name.
func (a *app) legacyServices() []config.ServiceConfig {
	if a.cfg == nil {
		return nil
	}
	services := make([]config.ServiceConfig, len(a.cfg.Services))
	copy(services, a.cfg.Services)
	if a.statusSort {
		slices.SortFunc(services, func(a, b config.ServiceConfig) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	return services
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
	fmt.Println()

	if fw.Error != "" {
		fmt.Printf("         %sERROR: %s%s\n", colorRed, fw.Error, colorReset)
	}

	if fw.NextRetry != nil && fw.NextRetry.IsValid() {
		remaining := time.Until(fw.NextRetry.AsTime())
		if remaining > 0 {
			fmt.Printf("         Reconnecting in %s\n", remaining.Round(time.Second))
		} else {
			fmt.Printf("         Reconnecting now\n")
		}
	}
}

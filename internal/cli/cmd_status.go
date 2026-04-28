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
	"github.com/rbaliyan/kubeport/pkg/config"
	"github.com/rbaliyan/kubeport/internal/netutil"
)

// JSON output types for --json flag.
type proxyStatusOutput struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen,omitempty"`
}

type statusOutput struct {
	Running       bool                  `json:"running"`
	CLIVersion    string                `json:"cli_version,omitempty"`
	DaemonVersion string                `json:"daemon_version,omitempty"`
	Context       string                `json:"context,omitempty"`
	Namespace     string                `json:"namespace,omitempty"`
	Config        string                `json:"config,omitempty"`
	SOCKS         *proxyStatusOutput    `json:"socks,omitempty"`
	HTTPProxy     *proxyStatusOutput    `json:"http_proxy,omitempty"`
	Forwards      []forwardStatusOutput `json:"forwards,omitempty"`
}

type forwardStatusOutput struct {
	Name               string `json:"name"`
	State              string `json:"state"`
	LocalPort          int    `json:"local_port"`
	RemotePort         int    `json:"remote_port"`
	Target             string `json:"target,omitempty"`
	Namespace          string `json:"namespace,omitempty"`
	ParentName         string `json:"parent_name,omitempty"`
	PortName           string `json:"port_name,omitempty"`
	Restarts           int    `json:"restarts,omitempty"`
	Error              string `json:"error,omitempty"`
	NextRetry          string `json:"next_retry,omitempty"`
	BytesIn            int64  `json:"bytes_in"`
	BytesOut           int64  `json:"bytes_out"`
	EffectiveLatencyMs int64   `json:"effective_latency_ms,omitempty"`
	EffectiveJitterMs  int64   `json:"effective_jitter_ms,omitempty"`
	EffectiveBandwidth int64   `json:"effective_bandwidth,omitempty"`
	ChaosEnabled       bool    `json:"chaos_enabled,omitempty"`
	ChaosErrorRate     float64 `json:"chaos_error_rate,omitempty"`
	ChaosErrorsInjected int64  `json:"chaos_errors_injected,omitempty"`
	ChaosSpikesInjected int64  `json:"chaos_spikes_injected,omitempty"`
	Lazy               bool   `json:"lazy,omitempty"`
	TunnelOpen         bool   `json:"tunnel_open,omitempty"`
}

func (a *app) cmdStatus() {
	// Try gRPC first
	dc, err := a.dialTarget()
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

	socksStatus, httpProxyStatus := a.proxyStatusFromConfig()

	if a.statusJSON {
		out := statusOutput{
			Running:       true,
			CLIVersion:    cliVer,
			DaemonVersion: daemonVer,
			Context:       resp.Context,
			Namespace:     resp.Namespace,
			Config:        a.configFile,
			SOCKS:         socksStatus,
			HTTPProxy:     httpProxyStatus,
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

	if socksStatus != nil || httpProxyStatus != nil {
		fmt.Println("\nProxies:")
		printProxyStatus(socksStatus, httpProxyStatus)
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

	socksStatus, httpProxyStatus := a.proxyStatusFromConfig()

	if a.statusJSON {
		out := statusOutput{Running: running}
		if a.cfg != nil {
			out.Context = a.cfg.Context
			out.Namespace = a.cfg.Namespace
			out.Config = a.configFile
			out.SOCKS = socksStatus
			out.HTTPProxy = httpProxyStatus
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

		if socksStatus != nil || httpProxyStatus != nil {
			fmt.Println("\nProxies:")
			printProxyStatus(socksStatus, httpProxyStatus)
		}

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
		Name:               svc.GetName(),
		State:              strings.TrimPrefix(strings.ToLower(fw.State.String()), "forward_state_"),
		LocalPort:          int(fw.ActualPort),
		RemotePort:         int(svc.GetRemotePort()),
		Target:             target,
		Namespace:          svc.GetNamespace(),
		ParentName:         svc.GetParentName(),
		PortName:           svc.GetPortName(),
		Restarts:           int(fw.Restarts),
		Error:              fw.Error,
		BytesIn:            fw.BytesIn,
		BytesOut:           fw.BytesOut,
		EffectiveLatencyMs:  fw.EffectiveLatencyMs,
		EffectiveJitterMs:   fw.EffectiveJitterMs,
		EffectiveBandwidth:  fw.EffectiveBandwidth,
		ChaosEnabled:        fw.ChaosEnabled,
		ChaosErrorRate:      fw.ChaosErrorRate,
		ChaosErrorsInjected: fw.ChaosErrorsInjected,
		ChaosSpikesInjected: fw.ChaosSpikesInjected,
	}
	if fw.NextRetry != nil && fw.NextRetry.IsValid() {
		out.NextRetry = fw.NextRetry.AsTime().Format(time.RFC3339)
	}
	out.Lazy = fw.Lazy
	out.TunnelOpen = fw.TunnelOpen
	return out
}

// proxyStatusFromConfig returns proxy status structs from the loaded config.
// Returns nil for each proxy if not configured (no listen address and not enabled).
func (a *app) proxyStatusFromConfig() (socks, httpProxy *proxyStatusOutput) {
	if a.cfg == nil {
		return nil, nil
	}
	if a.cfg.SOCKS.IsEnabled() || a.cfg.SOCKS.Listen != "" {
		addr := a.cfg.SOCKS.Listen
		if addr == "" {
			addr = "127.0.0.1:1080"
		}
		socks = &proxyStatusOutput{Enabled: a.cfg.SOCKS.IsEnabled(), Listen: addr}
	}
	if a.cfg.HTTPProxy.IsEnabled() || a.cfg.HTTPProxy.Listen != "" {
		addr := a.cfg.HTTPProxy.Listen
		if addr == "" {
			addr = "127.0.0.1:3128"
		}
		httpProxy = &proxyStatusOutput{Enabled: a.cfg.HTTPProxy.IsEnabled(), Listen: addr}
	}
	return socks, httpProxy
}

// printProxyStatus prints SOCKS and HTTP proxy status lines.
func printProxyStatus(socks, httpProxy *proxyStatusOutput) {
	if socks != nil {
		state := "manual"
		stateColor := colorYellow
		if socks.Enabled {
			state = "auto"
			stateColor = colorGreen
		}
		fmt.Printf("  SOCKS5:     %s [%s%s%s]\n", socks.Listen, stateColor, state, colorReset)
	}
	if httpProxy != nil {
		state := "manual"
		stateColor := colorYellow
		if httpProxy.Enabled {
			state = "auto"
			stateColor = colorGreen
		}
		fmt.Printf("  HTTP proxy: %s [%s%s%s]\n", httpProxy.Listen, stateColor, state, colorReset)
	}
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
	writeForwardStatus(os.Stdout, fw)
	_, _ = fmt.Fprintln(os.Stdout)
}

// formatBandwidth formats a bytes-per-second value into a human-readable bandwidth string.
func formatBandwidth(bytesPerSec int64) string {
	bitsPerSec := float64(bytesPerSec) * 8
	switch {
	case bitsPerSec >= 1_000_000_000:
		return fmt.Sprintf("%.1f Gbps", bitsPerSec/1_000_000_000)
	case bitsPerSec >= 1_000_000:
		return fmt.Sprintf("%.1f Mbps", bitsPerSec/1_000_000)
	case bitsPerSec >= 1_000:
		return fmt.Sprintf("%.1f Kbps", bitsPerSec/1_000)
	default:
		return fmt.Sprintf("%d bps", int64(bitsPerSec))
	}
}

// formatBytes formats a byte count into a human-readable string.
func formatBytes(b int64) string {
	const (
		kB = 1024
		mB = 1024 * kB
		gB = 1024 * mB
	)
	switch {
	case b >= gB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gB))
	case b >= mB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mB))
	case b >= kB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

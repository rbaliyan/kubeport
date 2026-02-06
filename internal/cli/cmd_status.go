package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
)

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
	fmt.Println()

	if fw.Error != "" {
		fmt.Printf("         %sERROR: %s%s\n", colorRed, fw.Error, colorReset)
	}
}

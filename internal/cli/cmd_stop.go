package cli

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
		if err := os.Remove(a.cfg.PIDFile()); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "%sWarning: failed to remove PID file: %v%s\n", colorYellow, err, colorReset)
		}
	} else {
		// Try default PID file location
		os.Remove(".kubeport.pid")
	}

	fmt.Printf("%sstopped%s\n", colorGreen, colorReset)
}

package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
)

func (a *app) cmdReload() {
	dc, err := dialDaemon(a.socketPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
		os.Exit(1)
	}
	if dc == nil {
		fmt.Fprintf(os.Stderr, "Proxy is not running\n")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	resp, err := dc.client.Reload(ctx, &kubeportv1.ReloadRequest{})
	cancel()
	dc.Close()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	fmt.Printf("Config reloaded: %d added, %d removed\n", resp.Added, resp.Removed)
}

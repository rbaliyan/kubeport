package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
)

func (a *app) cmdRemove(args []string) {
	var (
		name    string
		persist bool
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--persist":
			persist = true
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				os.Exit(1)
			}
			if name == "" {
				name = args[i]
			}
		}
	}

	if name == "" {
		fmt.Fprintf(os.Stderr, "Usage: kubeport remove <name> [--persist]\n")
		os.Exit(1)
	}

	dc, err := dialDaemon(a.socketPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
		os.Exit(1)
	}
	if dc == nil {
		fmt.Fprintf(os.Stderr, "Proxy is not running\n")
		os.Exit(1)
	}
	defer dc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := dc.client.RemoveService(ctx, &kubeportv1.RemoveServiceRequest{
		Name:    name,
		Persist: persist,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	fmt.Printf("Service %q removed\n", name)
	if persist {
		fmt.Println("Config file updated")
	}
}

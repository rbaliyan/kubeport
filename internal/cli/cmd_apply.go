package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/config"
)

func (a *app) cmdApply(args []string) {
	var (
		filePath string
		merge    bool
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--file", "-f":
			if i+1 < len(args) {
				i++
				filePath = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "Error: %s requires a path argument\n", args[i])
				os.Exit(1)
			}
		case "--merge":
			merge = true
		default:
			switch {
			case strings.HasPrefix(args[i], "--file="):
				filePath = strings.TrimPrefix(args[i], "--file=")
			case strings.HasPrefix(args[i], "-f="):
				filePath = strings.TrimPrefix(args[i], "-f=")
			case strings.HasPrefix(args[i], "-"):
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				os.Exit(1)
			}
		}
	}

	if filePath == "" {
		fmt.Fprintf(os.Stderr, "Error: --file/-f is required\n")
		fmt.Fprintf(os.Stderr, "Usage: kubeport apply --file <path> [--merge]\n")
		os.Exit(1)
	}

	services, err := config.LoadServices(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading %s: %v\n", filePath, err)
		os.Exit(1)
	}

	if len(services) == 0 {
		fmt.Fprintf(os.Stderr, "No services found in %s\n", filePath)
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
	// Convert to proto ServiceInfo
	protoServices := make([]*kubeportv1.ServiceInfo, 0, len(services))
	for _, svc := range services {
		protoServices = append(protoServices, &kubeportv1.ServiceInfo{
			Name:       svc.Name,
			Service:    svc.Service,
			Pod:        svc.Pod,
			LocalPort:  int32(svc.LocalPort),
			RemotePort: int32(svc.RemotePort),
			Namespace:  svc.Namespace,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	resp, err := dc.client.Apply(ctx, &kubeportv1.ApplyRequest{
		Services: protoServices,
		Merge:    merge,
	})
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

	fmt.Printf("Applied %s: %d added, %d skipped\n", filePath, resp.Added, resp.Skipped)
	for _, w := range resp.Warnings {
		fmt.Printf("  %s!%s %s\n", colorYellow, colorReset, w)
	}
}

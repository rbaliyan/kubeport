package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
)

func (a *app) cmdAdd(args []string) {
	var (
		name      string
		service   string
		pod       string
		namespace string
		localPort int
		remotePort int
		persist   bool
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				i++
				name = args[i]
			}
		case "--service":
			if i+1 < len(args) {
				i++
				service = args[i]
			}
		case "--pod":
			if i+1 < len(args) {
				i++
				pod = args[i]
			}
		case "--namespace", "-n":
			if i+1 < len(args) {
				i++
				namespace = args[i]
			}
		case "--local-port":
			if i+1 < len(args) {
				i++
				p, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --local-port: %v\n", err)
					os.Exit(1)
				}
				localPort = p
			}
		case "--remote-port":
			if i+1 < len(args) {
				i++
				p, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --remote-port: %v\n", err)
					os.Exit(1)
				}
				remotePort = p
			}
		case "--persist":
			persist = true
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				os.Exit(1)
			}
		}
	}

	if name == "" {
		fmt.Fprintf(os.Stderr, "Error: --name is required\n")
		os.Exit(1)
	}
	if service == "" && pod == "" {
		fmt.Fprintf(os.Stderr, "Error: --service or --pod is required\n")
		os.Exit(1)
	}
	if service != "" && pod != "" {
		fmt.Fprintf(os.Stderr, "Error: specify --service or --pod, not both\n")
		os.Exit(1)
	}
	if remotePort == 0 {
		fmt.Fprintf(os.Stderr, "Error: --remote-port is required\n")
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

	resp, err := dc.client.AddService(ctx, &kubeportv1.AddServiceRequest{
		Service: &kubeportv1.ServiceInfo{
			Name:       name,
			Service:    service,
			Pod:        pod,
			LocalPort:  int32(localPort),
			RemotePort: int32(remotePort),
			Namespace:  namespace,
		},
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

	fmt.Printf("Service %q added", name)
	if resp.ActualPort > 0 {
		fmt.Printf(" (local port: %d)", resp.ActualPort)
	}
	fmt.Println()
	if persist {
		fmt.Println("Config file updated")
	}
}

package cli

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
)

func (a *app) cmdAdd(args []string) {
	var (
		name            string
		service         string
		pod             string
		namespace       string
		localPort       int
		remotePort      int
		persist         bool
		portsFlag       string
		excludePorts    string
		localPortOffset int
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
		case "--ports":
			if i+1 < len(args) {
				i++
				portsFlag = args[i]
			}
		case "--exclude-ports":
			if i+1 < len(args) {
				i++
				excludePorts = args[i]
			}
		case "--local-port-offset":
			if i+1 < len(args) {
				i++
				p, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --local-port-offset: %v\n", err)
					os.Exit(1)
				}
				localPortOffset = p
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

	isMultiPort := portsFlag != ""

	if isMultiPort {
		// Multi-port mode validation
		if remotePort != 0 || localPort != 0 {
			fmt.Fprintf(os.Stderr, "Error: --remote-port and --local-port cannot be used with --ports\n")
			os.Exit(1)
		}
	} else {
		// Legacy mode validation
		if remotePort == 0 {
			fmt.Fprintf(os.Stderr, "Error: --remote-port is required (or use --ports for multi-port mode)\n")
			os.Exit(1)
		}
		if localPort < 0 || localPort > math.MaxUint16 {
			fmt.Fprintf(os.Stderr, "Error: --local-port must be 0-%d\n", math.MaxUint16)
			os.Exit(1)
		}
		if remotePort < 0 || remotePort > math.MaxUint16 {
			fmt.Fprintf(os.Stderr, "Error: --remote-port must be 1-%d\n", math.MaxUint16)
			os.Exit(1)
		}
		if excludePorts != "" || localPortOffset != 0 {
			fmt.Fprintf(os.Stderr, "Error: --exclude-ports and --local-port-offset require --ports\n")
			os.Exit(1)
		}
	}

	dc, err := a.dialTarget()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
		os.Exit(1)
	}
	if dc == nil {
		fmt.Fprintf(os.Stderr, "Proxy is not running\n")
		os.Exit(1)
	}

	req := &kubeportv1.AddServiceRequest{
		Service: &kubeportv1.ServiceInfo{
			Name:       name,
			Service:    service,
			Pod:        pod,
			LocalPort:  int32(localPort),
			RemotePort: int32(remotePort),
			Namespace:  namespace,
		},
		Persist: persist,
	}

	if isMultiPort {
		ps := &kubeportv1.PortSpec{
			LocalPortOffset: int32(localPortOffset),
		}
		if portsFlag == "all" {
			ps.All = true
		} else {
			ps.PortNames = strings.Split(portsFlag, ",")
		}
		if excludePorts != "" {
			ps.ExcludePorts = strings.Split(excludePorts, ",")
		}
		req.Ports = ps
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	resp, err := dc.client.AddService(ctx, req)
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

	if isMultiPort {
		fmt.Printf("Service %q added (ports resolving, check 'kubeport status')\n", name)
	} else {
		fmt.Printf("Service %q added", name)
		if resp.ActualPort > 0 {
			fmt.Printf(" (local port: %d)", resp.ActualPort)
		}
		fmt.Println()
	}
	if persist {
		fmt.Println("Config file updated")
	}
}

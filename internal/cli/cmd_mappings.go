package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"gopkg.in/yaml.v3"
)

func (a *app) cmdMappings(args []string) {
	var (
		outputYAML    bool
		clusterDomain string
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--yaml":
			outputYAML = true
		case "--cluster-domain":
			if i+1 < len(args) {
				i++
				clusterDomain = args[i]
			}
		default:
			if len(args[i]) > 0 && args[i][0] == '-' {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				os.Exit(1)
			}
		}
	}

	dc, err := a.dialTarget()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
		os.Exit(1)
	}
	if dc == nil {
		fmt.Fprintf(os.Stderr, "%sProxy is not running%s\n", colorYellow, colorReset)
		os.Exit(1)
	}
	resp, err := a.fetchMappings(dc, clusterDomain)
	dc.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	switch {
	case a.statusJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(resp.Addrs)
	case outputYAML:
		data, _ := yaml.Marshal(resp.Addrs)
		_, _ = os.Stdout.Write(data)
	default:
		if len(resp.Mappings) == 0 {
			fmt.Println("No active mappings (no running forwards)")
			return
		}

		currentSvc := ""
		for _, m := range resp.Mappings {
			if m.ServiceName != currentSvc {
				if currentSvc != "" {
					fmt.Println()
				}
				fmt.Printf("%s# %s%s\n", colorCyan, m.ServiceName, colorReset)
				currentSvc = m.ServiceName
			}
			fmt.Printf("  %s → %s\n", m.InternalAddr, m.LocalAddr)
		}
	}
}

func (a *app) fetchMappings(dc *daemonClient, clusterDomain string) (*kubeportv1.MappingsResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return dc.client.Mappings(ctx, &kubeportv1.MappingsRequest{
		ClusterDomain: clusterDomain,
	})
}

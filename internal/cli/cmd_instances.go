package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/rbaliyan/kubeport/internal/registry"
)

// instanceJSON is the JSON representation of a running instance for --json output.
type instanceJSON struct {
	PID        int    `json:"pid"`
	ConfigFile string `json:"config_file,omitempty"`
	PIDFile    string `json:"pid_file,omitempty"`
	LogFile    string `json:"log_file,omitempty"`
	Endpoint   string `json:"endpoint"`
	HasAPIKey  bool   `json:"has_api_key"`
	APIKeyHash string `json:"api_key_hash,omitempty"` // first 12 hex chars only
	Version    string `json:"version,omitempty"`
	Uptime     string `json:"uptime"`
	StartedAt  string `json:"started_at"`
}

func (a *app) cmdInstances() {
	cfgPath := ""
	if a.cfg != nil {
		cfgPath = a.cfg.FilePath()
	}

	reg, err := registry.Open(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening instance registry: %v\n", err)
		os.Exit(1)
	}

	entries, err := reg.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading instance registry: %v\n", err)
		os.Exit(1)
	}

	if a.statusJSON {
		out := make([]instanceJSON, 0, len(entries))
		for _, e := range entries {
			out = append(out, entryToJSON(e))
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return
	}

	if len(entries) == 0 {
		fmt.Println("No running kubeport instances.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "PID\tUPTIME\tVERSION\tENDPOINT\tAPI KEY\tCONFIG")
	for _, e := range entries {
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
			e.PID,
			formatUptime(time.Since(e.StartedAt)),
			e.Version,
			entryEndpoint(e),
			entryAPIKeyLabel(e),
			entryConfigLabel(e),
		)
	}
	_ = w.Flush()

	// Print path details below the table.
	fmt.Println()
	for _, e := range entries {
		fmt.Printf("%sPID %d%s\n", colorCyan, e.PID, colorReset)
		if e.ConfigFile != "" {
			fmt.Printf("  Config:   %s\n", e.ConfigFile)
		} else {
			fmt.Printf("  Config:   (in-memory / CLI flags)\n")
		}
		if e.PIDFile != "" {
			fmt.Printf("  PID file: %s\n", e.PIDFile)
		}
		if e.LogFile != "" {
			fmt.Printf("  Log file: %s\n", e.LogFile)
		}
	}
}

// printInstanceBrief prints a compact summary line of a registry entry, used in
// conflict reports and offload prompts.
func printInstanceBrief(e *registry.Entry) {
	apiKeyNote := ""
	if e.HasAPIKey {
		hint := ""
		if len(e.APIKeyHash) >= 12 {
			hint = " hash:" + e.APIKeyHash[:12] + "…"
		}
		apiKeyNote = fmt.Sprintf(" [API key required%s]", hint)
	}
	fmt.Printf("  PID %-8d  endpoint: %-40s  config: %s%s\n",
		e.PID, entryEndpoint(*e), entryConfigLabel(*e), apiKeyNote)
}

func entryEndpoint(e registry.Entry) string {
	if e.TCPAddress != "" {
		return "tcp://" + e.TCPAddress
	}
	return e.Socket
}

func entryConfigLabel(e registry.Entry) string {
	if e.ConfigFile == "" {
		return "(in-memory)"
	}
	return e.ConfigFile
}

func entryAPIKeyLabel(e registry.Entry) string {
	if !e.HasAPIKey {
		return "none"
	}
	if len(e.APIKeyHash) >= 12 {
		return "yes (" + e.APIKeyHash[:12] + "…)"
	}
	return "yes"
}

func entryToJSON(e registry.Entry) instanceJSON {
	hashHint := ""
	if e.HasAPIKey && len(e.APIKeyHash) >= 12 {
		hashHint = e.APIKeyHash[:12]
	}
	return instanceJSON{
		PID:        e.PID,
		ConfigFile: e.ConfigFile,
		PIDFile:    e.PIDFile,
		LogFile:    e.LogFile,
		Endpoint:   entryEndpoint(e),
		HasAPIKey:  e.HasAPIKey,
		APIKeyHash: hashHint,
		Version:    e.Version,
		Uptime:     formatUptime(time.Since(e.StartedAt)),
		StartedAt:  e.StartedAt.Format(time.RFC3339),
	}
}

func formatUptime(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

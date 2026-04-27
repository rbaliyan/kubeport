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
// KeyID is the operator-visible identifier for the API key: either the user-provided
// key_id from config, or a short SHA-256 fingerprint derived from the key material.
type instanceJSON struct {
	PID         int    `json:"pid"`
	ConfigFile  string `json:"config_file,omitempty"`
	PIDFile     string `json:"pid_file,omitempty"`
	LogFile     string `json:"log_file,omitempty"`
	Endpoint    string `json:"endpoint"`
	AuthEnabled bool   `json:"auth_enabled"`
	KeyID       string `json:"key_id,omitempty"`
	Version     string `json:"version,omitempty"`
	Uptime      string `json:"uptime"`
	StartedAt   string `json:"started_at"`
}

func (a *app) cmdInstances() {
	reg, err := a.openRegistry()
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
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding instances: %v\n", err)
			os.Exit(1)
		}
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
			entryAuthLabel(e),
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
	authNote := ""
	if e.AuthEnabled {
		note := e.KeyID
		if note == "" {
			note = "yes"
		}
		authNote = fmt.Sprintf(" [auth: %s]", note)
	}
	fmt.Printf("  PID %-8d  endpoint: %-40s  config: %s%s\n",
		e.PID, entryEndpoint(*e), entryConfigLabel(*e), authNote)
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

func entryAuthLabel(e registry.Entry) string {
	if !e.AuthEnabled {
		return "none"
	}
	if e.KeyID != "" {
		return "yes (" + e.KeyID + ")"
	}
	return "yes"
}

func entryToJSON(e registry.Entry) instanceJSON {
	return instanceJSON{
		PID:         e.PID,
		ConfigFile:  e.ConfigFile,
		PIDFile:     e.PIDFile,
		LogFile:     e.LogFile,
		Endpoint:    entryEndpoint(e),
		AuthEnabled: e.AuthEnabled,
		KeyID:       e.KeyID,
		Version:     e.Version,
		Uptime:      formatUptime(time.Since(e.StartedAt)),
		StartedAt:   e.StartedAt.Format(time.RFC3339),
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

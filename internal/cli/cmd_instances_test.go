package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rbaliyan/kubeport/internal/registry"
)

// ---------------------------------------------------------------------------
// formatUptime
// ---------------------------------------------------------------------------

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{500 * time.Millisecond, "0s"}, // truncated to second
		{1 * time.Second, "1s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m0s"},
		{90 * time.Second, "1m30s"},
		{119 * time.Second, "1m59s"},
		{3600 * time.Second, "1h"},
		{3660 * time.Second, "1h1m"},
		{7200 * time.Second, "2h"},
		{7320 * time.Second, "2h2m"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatUptime(tt.d); got != tt.want {
				t.Errorf("formatUptime(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// entryEndpoint
// ---------------------------------------------------------------------------

func TestEntryEndpoint(t *testing.T) {
	t.Run("unix socket", func(t *testing.T) {
		e := registry.Entry{Socket: "/tmp/kube.sock"}
		if got := entryEndpoint(e); got != "/tmp/kube.sock" {
			t.Errorf("got %q, want %q", got, "/tmp/kube.sock")
		}
	})
	t.Run("tcp address", func(t *testing.T) {
		e := registry.Entry{TCPAddress: "127.0.0.1:9090"}
		if got := entryEndpoint(e); got != "tcp://127.0.0.1:9090" {
			t.Errorf("got %q, want %q", got, "tcp://127.0.0.1:9090")
		}
	})
	t.Run("tcp preferred over socket", func(t *testing.T) {
		e := registry.Entry{TCPAddress: "0.0.0.0:8080", Socket: "/tmp/kube.sock"}
		if got := entryEndpoint(e); !strings.HasPrefix(got, "tcp://") {
			t.Errorf("expected tcp:// prefix, got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// entryConfigLabel
// ---------------------------------------------------------------------------

func TestEntryConfigLabel(t *testing.T) {
	t.Run("with file", func(t *testing.T) {
		e := registry.Entry{ConfigFile: "/etc/kubeport.yaml"}
		if got := entryConfigLabel(e); got != "/etc/kubeport.yaml" {
			t.Errorf("got %q, want %q", got, "/etc/kubeport.yaml")
		}
	})
	t.Run("in-memory", func(t *testing.T) {
		e := registry.Entry{}
		if got := entryConfigLabel(e); got != "(in-memory)" {
			t.Errorf("got %q, want %q", got, "(in-memory)")
		}
	})
}

// ---------------------------------------------------------------------------
// entryAuthLabel
// ---------------------------------------------------------------------------

func TestEntryAuthLabel(t *testing.T) {
	t.Run("no auth", func(t *testing.T) {
		e := registry.Entry{AuthEnabled: false}
		if got := entryAuthLabel(e); got != "none" {
			t.Errorf("got %q, want %q", got, "none")
		}
	})
	t.Run("auth with key_id", func(t *testing.T) {
		e := registry.Entry{AuthEnabled: true, KeyID: "prod"}
		got := entryAuthLabel(e)
		if !strings.Contains(got, "prod") {
			t.Errorf("expected key_id in label, got %q", got)
		}
	})
	t.Run("auth without key_id", func(t *testing.T) {
		e := registry.Entry{AuthEnabled: true}
		got := entryAuthLabel(e)
		if got != "yes" {
			t.Errorf("got %q, want %q", got, "yes")
		}
	})
}

// ---------------------------------------------------------------------------
// entryToJSON
// ---------------------------------------------------------------------------

func TestEntryToJSON(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	e := registry.Entry{
		PID:         1234,
		ConfigFile:  "/tmp/kubeport.yaml",
		PIDFile:     "/tmp/kubeport.pid",
		LogFile:     "/tmp/kubeport.log",
		Socket:      "/tmp/kubeport.sock",
		AuthEnabled: true,
		KeyID:       "ci",
		Version:     "v1.2.3",
		StartedAt:   now,
	}
	j := entryToJSON(e)

	if j.PID != 1234 {
		t.Errorf("PID = %d, want 1234", j.PID)
	}
	if j.ConfigFile != "/tmp/kubeport.yaml" {
		t.Errorf("ConfigFile = %q", j.ConfigFile)
	}
	if j.PIDFile != "/tmp/kubeport.pid" {
		t.Errorf("PIDFile = %q", j.PIDFile)
	}
	if j.LogFile != "/tmp/kubeport.log" {
		t.Errorf("LogFile = %q", j.LogFile)
	}
	if j.Endpoint != "/tmp/kubeport.sock" {
		t.Errorf("Endpoint = %q", j.Endpoint)
	}
	if !j.AuthEnabled {
		t.Error("AuthEnabled should be true")
	}
	if j.KeyID != "ci" {
		t.Errorf("KeyID = %q, want %q", j.KeyID, "ci")
	}
	if j.Version != "v1.2.3" {
		t.Errorf("Version = %q", j.Version)
	}
	if j.StartedAt == "" {
		t.Error("StartedAt should be non-empty")
	}
	if j.Uptime == "" {
		t.Error("Uptime should be non-empty")
	}
}

func TestEntryToJSON_TCPEndpoint(t *testing.T) {
	e := registry.Entry{
		PID:        42,
		TCPAddress: "0.0.0.0:9090",
		StartedAt:  time.Now(),
	}
	j := entryToJSON(e)
	if j.Endpoint != "tcp://0.0.0.0:9090" {
		t.Errorf("Endpoint = %q, want %q", j.Endpoint, "tcp://0.0.0.0:9090")
	}
}

// ---------------------------------------------------------------------------
// printInstanceBrief — stdout capture
// ---------------------------------------------------------------------------

// captureStdout replaces os.Stdout with a pipe for the duration of fn,
// then returns what was written.
func captureStdout(fn func()) string {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestPrintInstanceBrief_NoAuth(t *testing.T) {
	e := &registry.Entry{
		PID:    42,
		Socket: "/tmp/kube.sock",
	}
	out := captureStdout(func() { printInstanceBrief(e) })
	if !strings.Contains(out, fmt.Sprintf("%d", 42)) {
		t.Errorf("expected PID in output, got: %q", out)
	}
	if strings.Contains(out, "auth") {
		t.Errorf("expected no auth note, got: %q", out)
	}
}

func TestPrintInstanceBrief_WithKeyID(t *testing.T) {
	e := &registry.Entry{
		PID:         99,
		TCPAddress:  "127.0.0.1:9090",
		AuthEnabled: true,
		KeyID:       "staging",
	}
	out := captureStdout(func() { printInstanceBrief(e) })
	if !strings.Contains(out, "staging") {
		t.Errorf("expected key_id in output, got: %q", out)
	}
	if !strings.Contains(out, "auth") {
		t.Errorf("expected auth label in output, got: %q", out)
	}
}

func TestPrintInstanceBrief_AuthNoKeyID(t *testing.T) {
	e := &registry.Entry{
		PID:         1,
		Socket:      "/tmp/k.sock",
		AuthEnabled: true,
	}
	out := captureStdout(func() { printInstanceBrief(e) })
	if !strings.Contains(out, "auth") {
		t.Errorf("expected auth label in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// parseArgs — offload and instances flags
// ---------------------------------------------------------------------------

func TestParseArgs_OffloadFlag(t *testing.T) {
	a := &app{}
	cmd, _ := a.parseArgs([]string{"--offload", "start"})
	if !a.offload {
		t.Error("expected offload = true")
	}
	if cmd != "start" {
		t.Errorf("command = %q, want %q", cmd, "start")
	}
}

func TestParseArgs_InstancesCommand(t *testing.T) {
	a := &app{}
	cmd, _ := a.parseArgs([]string{"instances"})
	if cmd != "instances" {
		t.Errorf("command = %q, want %q", cmd, "instances")
	}
}

func TestParseArgs_JSONFlag(t *testing.T) {
	a := &app{}
	a.parseArgs([]string{"--json", "status"})
	if !a.statusJSON {
		t.Error("expected statusJSON = true")
	}
}

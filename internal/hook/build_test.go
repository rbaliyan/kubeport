package hook

import (
	"testing"

	"github.com/rbaliyan/kubeport/internal/config"
)

func TestBuildFromConfig_Shell(t *testing.T) {
	hc := config.HookConfig{
		Name: "vpn",
		Type: "shell",
		Shell: map[string]string{
			"manager_starting":  "echo starting",
			"forward_connected": "echo connected",
		},
		FailMode: "closed",
		Timeout:  "30s",
	}

	h, events, fm, timeout, err := BuildFromConfig(hc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Name() != "vpn" {
		t.Fatalf("expected name 'vpn', got %s", h.Name())
	}
	if fm != FailClosed {
		t.Fatal("expected FailClosed")
	}
	if timeout.Seconds() != 30 {
		t.Fatalf("expected 30s timeout, got %v", timeout)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestBuildFromConfig_Shell_InferEvents(t *testing.T) {
	hc := config.HookConfig{
		Name: "notify",
		Type: "shell",
		Shell: map[string]string{
			"forward_connected": "echo ok",
		},
		// No explicit events — should be inferred from shell keys
	}

	_, events, _, _, err := BuildFromConfig(hc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 inferred event, got %d", len(events))
	}
	if events[0] != EventForwardConnected {
		t.Fatalf("expected EventForwardConnected, got %v", events[0])
	}
}

func TestBuildFromConfig_Shell_ExplicitEvents(t *testing.T) {
	hc := config.HookConfig{
		Name:   "notify",
		Type:   "shell",
		Events: []string{"forward_connected", "forward_disconnected"},
		Shell: map[string]string{
			"forward_connected":    "echo ok",
			"forward_disconnected": "echo bye",
		},
	}

	_, events, _, _, err := BuildFromConfig(hc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestBuildFromConfig_Webhook(t *testing.T) {
	hc := config.HookConfig{
		Name:   "alert",
		Type:   "webhook",
		Events: []string{"forward_failed"},
		Webhook: &config.WebhookConfig{
			URL:     "https://hooks.example.com/notify",
			Headers: map[string]string{"X-Token": "secret"},
		},
	}

	h, events, fm, timeout, err := BuildFromConfig(hc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Name() != "alert" {
		t.Fatalf("expected name 'alert', got %s", h.Name())
	}
	if fm != FailOpen {
		t.Fatal("expected FailOpen (default)")
	}
	if timeout.Seconds() != 10 {
		t.Fatalf("expected 10s default timeout, got %v", timeout)
	}
	if len(events) != 1 || events[0] != EventForwardFailed {
		t.Fatalf("expected EventForwardFailed, got %v", events)
	}
}

func TestBuildFromConfig_Exec(t *testing.T) {
	hc := config.HookConfig{
		Name:   "logger",
		Type:   "exec",
		Events: []string{"forward_connected"},
		Exec: &config.ExecConfig{
			Command: []string{"echo", "${SERVICE}", "${PORT}"},
		},
	}

	h, events, _, _, err := BuildFromConfig(hc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Name() != "logger" {
		t.Fatalf("expected name 'logger', got %s", h.Name())
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestBuildFromConfig_InvalidType(t *testing.T) {
	hc := config.HookConfig{
		Name: "bad",
		Type: "invalid",
	}

	_, _, _, _, err := BuildFromConfig(hc)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestBuildFromConfig_InvalidEvent(t *testing.T) {
	hc := config.HookConfig{
		Name:   "bad",
		Type:   "shell",
		Events: []string{"nonexistent"},
		Shell:  map[string]string{"forward_connected": "echo ok"},
	}

	_, _, _, _, err := BuildFromConfig(hc)
	if err == nil {
		t.Fatal("expected error for invalid event")
	}
}

func TestBuildFromConfig_InvalidTimeout(t *testing.T) {
	hc := config.HookConfig{
		Name:    "bad",
		Type:    "shell",
		Timeout: "not-a-duration",
		Shell:   map[string]string{"forward_connected": "echo ok"},
	}

	_, _, _, _, err := BuildFromConfig(hc)
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
}

func TestBuildFromConfig_WebhookMissingURL(t *testing.T) {
	hc := config.HookConfig{
		Name:    "bad",
		Type:    "webhook",
		Events:  []string{"forward_connected"},
		Webhook: &config.WebhookConfig{},
	}

	_, _, _, _, err := BuildFromConfig(hc)
	if err == nil {
		t.Fatal("expected error for missing webhook URL")
	}
}

func TestBuildFromConfig_ExecMissingCommand(t *testing.T) {
	hc := config.HookConfig{
		Name:   "bad",
		Type:   "exec",
		Events: []string{"forward_connected"},
		Exec:   &config.ExecConfig{},
	}

	_, _, _, _, err := BuildFromConfig(hc)
	if err == nil {
		t.Fatal("expected error for missing exec command")
	}
}

func TestBuildFromConfig_ShellMissingCommands(t *testing.T) {
	hc := config.HookConfig{
		Name: "bad",
		Type: "shell",
	}

	_, _, _, _, err := BuildFromConfig(hc)
	if err == nil {
		t.Fatal("expected error for missing shell commands")
	}
}

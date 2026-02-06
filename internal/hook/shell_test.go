package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewShellHook_ValidEvents(t *testing.T) {
	cmds := map[string]string{
		"forward_connected":    "echo connected",
		"forward_disconnected": "echo disconnected",
	}

	h, err := NewShellHook("test", cmds, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Name() != "test" {
		t.Fatalf("expected name 'test', got %s", h.Name())
	}
}

func TestNewShellHook_InvalidEvent(t *testing.T) {
	cmds := map[string]string{
		"nonexistent_event": "echo oops",
	}

	_, err := NewShellHook("bad", cmds, nil)
	if err == nil {
		t.Fatal("expected error for unknown event")
	}
}

func TestShellHook_OnEvent(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker.txt")

	cmds := map[string]string{
		"forward_connected": "echo $KUBEPORT_SERVICE > " + marker,
	}

	h, err := NewShellHook("test", cmds, nil)
	if err != nil {
		t.Fatal(err)
	}

	event := Event{
		Type:       EventForwardConnected,
		Time:       time.Now(),
		Service:    "my-svc",
		LocalPort:  8080,
		RemotePort: 80,
		PodName:    "my-pod-abc",
	}

	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "my-svc" {
		t.Fatalf("expected 'my-svc', got %q", got)
	}
}

func TestShellHook_ServiceFilter(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker.txt")

	cmds := map[string]string{
		"forward_connected": "touch " + marker,
	}

	h, err := NewShellHook("filtered", cmds, []string{"allowed-svc"})
	if err != nil {
		t.Fatal(err)
	}

	// Event for non-matching service should be skipped
	event := Event{
		Type:    EventForwardConnected,
		Time:    time.Now(),
		Service: "other-svc",
	}

	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker should not exist for filtered service")
	}

	// Event for matching service should run
	event.Service = "allowed-svc"
	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("marker should exist for allowed service")
	}
}

func TestShellHook_NoCommandForEvent(t *testing.T) {
	cmds := map[string]string{
		"forward_connected": "echo ok",
	}

	h, err := NewShellHook("test", cmds, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Fire an event that has no command
	event := Event{Type: EventForwardDisconnected, Time: time.Now()}
	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("unexpected error for missing command: %v", err)
	}
}

func TestShellHook_Gate(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "gate-marker.txt")

	cmds := map[string]string{
		"manager_starting": "touch " + marker,
	}

	h, err := NewShellHook("gate-test", cmds, nil)
	if err != nil {
		t.Fatal(err)
	}

	event := Event{Type: EventManagerStarting, Time: time.Now()}
	if err := h.Gate(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("gate marker should exist")
	}
}

func TestShellHook_ContextCancellation(t *testing.T) {
	cmds := map[string]string{
		"forward_connected": "sleep 10",
	}

	h, err := NewShellHook("cancel-test", cmds, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	event := Event{Type: EventForwardConnected, Time: time.Now()}
	err = h.OnEvent(ctx, event)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestEventEnv(t *testing.T) {
	event := Event{
		Type:       EventForwardConnected,
		Service:    "web",
		LocalPort:  8080,
		RemotePort: 80,
		PodName:    "web-abc",
		Restarts:   2,
	}

	env := eventEnv(event)
	expected := map[string]string{
		"KUBEPORT_EVENT":       "forward_connected",
		"KUBEPORT_SERVICE":     "web",
		"KUBEPORT_LOCAL_PORT":  "8080",
		"KUBEPORT_REMOTE_PORT": "80",
		"KUBEPORT_POD":         "web-abc",
		"KUBEPORT_RESTARTS":    "2",
	}

	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	for k, v := range expected {
		if got, ok := envMap[k]; !ok {
			t.Errorf("missing env var %s", k)
		} else if got != v {
			t.Errorf("env %s = %q, want %q", k, got, v)
		}
	}
}

func TestEventEnv_WithError(t *testing.T) {
	event := Event{
		Type:  EventForwardFailed,
		Error: context.DeadlineExceeded,
	}

	env := eventEnv(event)
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "KUBEPORT_ERROR=") {
			found = true
			if !strings.Contains(e, "deadline exceeded") {
				t.Errorf("expected deadline exceeded in error, got %s", e)
			}
		}
	}
	if !found {
		t.Error("expected KUBEPORT_ERROR env var")
	}
}

func TestSanitizeEnvValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal value", "normal value"},
		{"has\nnewline", "has newline"},
		{"has\rcarriage", "has carriage"},
		{"has\x00null", "hasnull"},
		{"multi\nline\nerror", "multi line error"},
		{"mixed\r\n\x00chars", "mixed  chars"},
		{"", ""},
	}
	for _, tt := range tests {
		got := sanitizeEnvValue(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeEnvValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestEventEnv_SanitizesError(t *testing.T) {
	event := Event{
		Type:    EventForwardFailed,
		Service: "normal-svc",
		Error:   fmt.Errorf("error with\nnewline injection"),
	}

	env := eventEnv(event)
	for _, e := range env {
		if strings.Contains(e, "\n") {
			t.Errorf("env var contains newline: %q", e)
		}
		if strings.Contains(e, "\r") {
			t.Errorf("env var contains carriage return: %q", e)
		}
	}
}

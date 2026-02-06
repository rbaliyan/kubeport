package hook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewExecHook_EmptyCommand(t *testing.T) {
	_, err := NewExecHook("bad", []string{}, nil)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestExecHook_OnEvent(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "exec-marker.txt")

	h, err := NewExecHook("test", []string{"sh", "-c", "echo ${SERVICE}:${PORT} > " + marker}, nil)
	if err != nil {
		t.Fatal(err)
	}

	event := Event{
		Type:       EventForwardConnected,
		Time:       time.Now(),
		Service:    "api",
		LocalPort:  9090,
		RemotePort: 8080,
	}

	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "api:9090" {
		t.Fatalf("expected 'api:9090', got %q", got)
	}
}

func TestExecHook_ServiceFilter(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "exec-filter.txt")

	h, err := NewExecHook("filtered", []string{"touch", marker}, []string{"allowed"})
	if err != nil {
		t.Fatal(err)
	}

	// Non-matching service
	event := Event{Type: EventForwardConnected, Service: "other"}
	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker should not exist for filtered service")
	}

	// Matching service
	event.Service = "allowed"
	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("marker should exist for allowed service")
	}
}

func TestExpandVars(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	event := Event{
		Type:       EventForwardConnected,
		Time:       now,
		Service:    "web",
		LocalPort:  8080,
		RemotePort: 80,
		PodName:    "web-pod-abc",
		Restarts:   3,
		Error:      context.DeadlineExceeded,
	}

	tests := []struct {
		input string
		want  string
	}{
		{"${EVENT}", "forward_connected"},
		{"${SERVICE}", "web"},
		{"${PORT}", "8080"},
		{"${REMOTE_PORT}", "80"},
		{"${POD}", "web-pod-abc"},
		{"${RESTARTS}", "3"},
		{"${ERROR}", "context deadline exceeded"},
		{"${TIME}", "2025-01-15T10:30:00Z"},
		{"svc=${SERVICE}:${PORT}", "svc=web:8080"},
		{"no-vars", "no-vars"},
	}

	for _, tt := range tests {
		if got := ExpandVars(tt.input, event); got != tt.want {
			t.Errorf("ExpandVars(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExpandVars_NoError(t *testing.T) {
	event := Event{
		Type:    EventForwardConnected,
		Service: "web",
	}

	got := ExpandVars("${ERROR}", event)
	if got != "" {
		t.Errorf("expected empty string for nil error, got %q", got)
	}
}

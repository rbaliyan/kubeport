package proxy

import (
	"testing"
	"time"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// newBareManager creates a minimal Manager with no config or Kubernetes client.
// Used to test UpdateChaos / ResetChaos directly without starting goroutines.
func newBareManager() *Manager {
	return &Manager{
		forwards: make(map[string]*portForward),
		children: make(map[string][]string),
	}
}

// addBareService inserts a portForward without Kubernetes calls.
func addBareService(m *Manager, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pf := &portForward{svc: config.ServiceConfig{Name: name}}
	m.forwards[name] = pf
	m.order = append(m.order, name)
}

func TestUpdateChaos_SingleService(t *testing.T) {
	m := newBareManager()
	addBareService(m, "web")

	cfg := config.ParsedChaosConfig{Enabled: true, ErrorRate: 0.5}
	updated, notFound := m.UpdateChaos([]string{"web"}, cfg)

	if len(updated) != 1 || updated[0] != "web" {
		t.Fatalf("expected updated=[web], got %v", updated)
	}
	if len(notFound) != 0 {
		t.Fatalf("expected no not-found, got %v", notFound)
	}

	pf := m.forwards["web"]
	if ptr := pf.chaosOverride.Load(); ptr == nil || ptr.ErrorRate != 0.5 {
		t.Fatalf("expected ErrorRate=0.5 in chaosOverride, got %v", ptr)
	}
	if !pf.hasChaosOverride.Load() {
		t.Fatal("expected hasChaosOverride=true")
	}
}

func TestUpdateChaos_AllServices(t *testing.T) {
	m := newBareManager()
	addBareService(m, "web")
	addBareService(m, "redis")

	cfg := config.ParsedChaosConfig{Enabled: true, ErrorRate: 0.1}
	updated, notFound := m.UpdateChaos(nil, cfg)

	if len(updated) != 2 {
		t.Fatalf("expected 2 updated, got %v", updated)
	}
	if len(notFound) != 0 {
		t.Fatalf("unexpected not-found: %v", notFound)
	}
}

func TestUpdateChaos_NotFound(t *testing.T) {
	m := newBareManager()

	cfg := config.ParsedChaosConfig{Enabled: true, ErrorRate: 0.1}
	updated, notFound := m.UpdateChaos([]string{"nonexistent"}, cfg)

	if len(updated) != 0 {
		t.Fatalf("expected no updated, got %v", updated)
	}
	if len(notFound) != 1 || notFound[0] != "nonexistent" {
		t.Fatalf("expected not-found=[nonexistent], got %v", notFound)
	}
}

func TestResetChaos(t *testing.T) {
	m := newBareManager()
	addBareService(m, "web")

	// Set an override first.
	m.UpdateChaos([]string{"web"}, config.ParsedChaosConfig{Enabled: true, ErrorRate: 0.9})

	// Now reset.
	updated, notFound := m.ResetChaos([]string{"web"})
	if len(updated) != 1 || updated[0] != "web" {
		t.Fatalf("expected updated=[web], got %v", updated)
	}
	if len(notFound) != 0 {
		t.Fatalf("unexpected not-found: %v", notFound)
	}

	pf := m.forwards["web"]
	if pf.hasChaosOverride.Load() {
		t.Fatal("expected hasChaosOverride=false after reset")
	}
}

func TestChaosPresets(t *testing.T) {
	presets := map[string]config.ParsedChaosConfig{
		"slow-network":     {Enabled: true, LatencySpikeProbability: 0.10, LatencySpikeDuration: 200 * time.Millisecond},
		"unstable-cluster": {Enabled: true, ErrorRate: 0.05, LatencySpikeProbability: 0.05, LatencySpikeDuration: 2 * time.Second},
		"packet-loss":      {Enabled: true, ErrorRate: 0.15},
	}

	for name, want := range presets {
		got, ok := ChaosPresets[name]
		if !ok {
			t.Errorf("preset %q not found", name)
			continue
		}
		if got != want {
			t.Errorf("preset %q: got %+v, want %+v", name, got, want)
		}
	}
}

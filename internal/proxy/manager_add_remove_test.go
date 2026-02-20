package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rbaliyan/kubeport/internal/config"
)

// newTestManager creates a minimal manager suitable for add/remove tests.
// It does NOT initialize a Kubernetes client so supervise will fail quickly,
// which is fine — we only need to verify the add/remove/reload orchestration.
func newTestManager() *Manager {
	return &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
		},
		forwards:             make(map[string]*portForward),
		output:               io.Discard,
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		backoffInitial:       100 * time.Millisecond,
		backoffMax:           100 * time.Millisecond,
		readyTimeout:         1 * time.Second,
		healthCheckInterval:  1 * time.Second,
		healthCheckThreshold: 3,
		maxRestarts:          1,
	}
}

func TestManager_AddService_NotStarted(t *testing.T) {
	m := newTestManager()
	err := m.AddService(config.ServiceConfig{
		Name:       "web",
		Service:    "nginx",
		RemotePort: 80,
		LocalPort:  8080,
	})
	if err == nil || err.Error() != "manager not started" {
		t.Fatalf("expected 'manager not started' error, got: %v", err)
	}
}

func TestManager_RemoveService_NotStarted(t *testing.T) {
	m := newTestManager()
	err := m.RemoveService("web")
	if err == nil || err.Error() != "manager not started" {
		t.Fatalf("expected 'manager not started' error, got: %v", err)
	}
}

func TestManager_AddService_Duplicate(t *testing.T) {
	m := newTestManager()

	// Simulate a running forward
	m.forwards["web"] = &portForward{
		svc: config.ServiceConfig{Name: "web", Service: "nginx", RemotePort: 80},
	}
	m.cmdCh = make(chan serviceCmd, 1) // won't be read but prevents nil check

	err := m.AddService(config.ServiceConfig{
		Name:       "web",
		Service:    "nginx",
		RemotePort: 80,
	})
	if !errors.Is(err, config.ErrServiceExists) {
		t.Fatalf("expected ErrServiceExists, got: %v", err)
	}
}

func TestManager_AddService_PortConflict(t *testing.T) {
	m := newTestManager()

	m.forwards["web"] = &portForward{
		svc:        config.ServiceConfig{Name: "web", Service: "nginx", RemotePort: 80, LocalPort: 8080},
		actualPort: 8080,
	}
	m.cmdCh = make(chan serviceCmd, 1)

	err := m.AddService(config.ServiceConfig{
		Name:       "api",
		Service:    "api-svc",
		RemotePort: 3000,
		LocalPort:  8080,
	})
	if err == nil {
		t.Fatal("expected port conflict error")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManager_AddService_ValidationError(t *testing.T) {
	m := newTestManager()
	m.cmdCh = make(chan serviceCmd, 1)

	err := m.AddService(config.ServiceConfig{
		Name:       "bad",
		RemotePort: 80,
		// Missing service or pod
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestManager_AddRemove_WithRunningManager(t *testing.T) {
	m := newTestManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start manager with no initial services (empty config)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Start(ctx)
	}()

	// Wait for cmdCh to be initialized
	deadline := time.After(2 * time.Second)
	for {
		m.mu.RLock()
		ch := m.cmdCh
		m.mu.RUnlock()
		if ch != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for manager to start")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Add a service (supervise will fail since no k8s client, but the goroutine starts)
	err := m.AddService(config.ServiceConfig{
		Name:       "test-svc",
		Service:    "nginx",
		RemotePort: 80,
		LocalPort:  9999,
	})
	if err != nil {
		t.Fatalf("AddService failed: %v", err)
	}

	// Verify it appears in forwards
	time.Sleep(50 * time.Millisecond) // let supervise register
	m.mu.RLock()
	_, exists := m.forwards["test-svc"]
	m.mu.RUnlock()
	if !exists {
		t.Fatal("service not found in forwards after add")
	}

	// Remove it
	err = m.RemoveService("test-svc")
	if err != nil {
		t.Fatalf("RemoveService failed: %v", err)
	}

	m.mu.RLock()
	_, exists = m.forwards["test-svc"]
	m.mu.RUnlock()
	if exists {
		t.Fatal("service still in forwards after remove")
	}

	// Remove nonexistent
	err = m.RemoveService("nonexistent")
	if !errors.Is(err, config.ErrServiceNotFound) {
		t.Fatalf("expected ErrServiceNotFound, got: %v", err)
	}

	cancel()
	wg.Wait()
}

func TestManager_Apply(t *testing.T) {
	m := newTestManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start with one existing service
	m.cfg.Services = []config.ServiceConfig{
		{Name: "existing", Service: "existing-svc", RemotePort: 80, LocalPort: 8080},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Start(ctx)
	}()

	// Wait for service to register
	deadline := time.After(2 * time.Second)
	for {
		m.mu.RLock()
		n := len(m.forwards)
		m.mu.RUnlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for initial service")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Apply: one new service + one conflicting name
	added, skipped, warnings := m.Apply([]config.ServiceConfig{
		{Name: "new-svc", Service: "new", RemotePort: 3000, LocalPort: 9090},
		{Name: "existing", Service: "existing-svc", RemotePort: 80, LocalPort: 8081},
	})

	if added != 1 {
		t.Fatalf("expected 1 added, got %d", added)
	}
	if skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", skipped)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	if !strings.Contains(warnings[0], "existing") {
		t.Fatalf("warning should mention 'existing', got: %s", warnings[0])
	}

	// Verify new-svc was added
	time.Sleep(50 * time.Millisecond)
	m.mu.RLock()
	_, hasNew := m.forwards["new-svc"]
	m.mu.RUnlock()
	if !hasNew {
		t.Fatal("expected 'new-svc' to be added")
	}

	cancel()
	wg.Wait()
}

func TestManager_Apply_AllNew(t *testing.T) {
	m := newTestManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Start(ctx)
	}()

	// Wait for cmdCh
	deadline := time.After(2 * time.Second)
	for {
		m.mu.RLock()
		ch := m.cmdCh
		m.mu.RUnlock()
		if ch != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for manager to start")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	added, skipped, warnings := m.Apply([]config.ServiceConfig{
		{Name: "svc-a", Service: "a", RemotePort: 80, LocalPort: 9001},
		{Name: "svc-b", Service: "b", RemotePort: 80, LocalPort: 9002},
	})

	if added != 2 {
		t.Fatalf("expected 2 added, got %d", added)
	}
	if skipped != 0 {
		t.Fatalf("expected 0 skipped, got %d", skipped)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected 0 warnings, got %d", len(warnings))
	}

	cancel()
	wg.Wait()
}

func TestManager_Reload(t *testing.T) {
	m := newTestManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start with one service
	m.cfg.Services = []config.ServiceConfig{
		{Name: "keep", Service: "keep-svc", RemotePort: 80, LocalPort: 8080},
		{Name: "remove-me", Service: "old-svc", RemotePort: 80, LocalPort: 8081},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Start(ctx)
	}()

	// Wait for both services to register
	deadline := time.After(2 * time.Second)
	for {
		m.mu.RLock()
		n := len(m.forwards)
		m.mu.RUnlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for initial services")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Reload with different config: keep "keep", remove "remove-me", add "new-svc"
	newCfg := &config.Config{
		Context:   "test",
		Namespace: "default",
		Services: []config.ServiceConfig{
			{Name: "keep", Service: "keep-svc", RemotePort: 80, LocalPort: 8080},
			{Name: "new-svc", Service: "new", RemotePort: 3000, LocalPort: 9090},
		},
	}

	added, removed, err := m.Reload(newCfg)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if added != 1 {
		t.Fatalf("expected 1 added, got %d", added)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}

	// Verify state
	time.Sleep(50 * time.Millisecond)
	m.mu.RLock()
	_, hasKeep := m.forwards["keep"]
	_, hasRemoved := m.forwards["remove-me"]
	_, hasNew := m.forwards["new-svc"]
	m.mu.RUnlock()

	if !hasKeep {
		t.Fatal("expected 'keep' to still be running")
	}
	if hasRemoved {
		t.Fatal("expected 'remove-me' to be gone")
	}
	if !hasNew {
		t.Fatal("expected 'new-svc' to be added")
	}

	cancel()
	wg.Wait()
}


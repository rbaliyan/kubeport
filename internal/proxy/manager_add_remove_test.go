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

	"github.com/rbaliyan/kubeport/pkg/config"
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
	// The duplicate check is now atomic with registration on the serial command
	// path (reserveForward), so it must be exercised through a running manager:
	// add "web", then a second add of "web" must observe the registered entry and
	// return ErrServiceExists.
	m := newTestManager()
	m.children = make(map[string][]string)

	_, stop := startTestManager(t, m)
	defer stop()

	if err := m.AddService(config.ServiceConfig{
		Name:       "web",
		Service:    "nginx",
		RemotePort: 80,
		LocalPort:  8080,
	}); err != nil {
		t.Fatalf("first AddService failed: %v", err)
	}
	eventually(t, 2*time.Second, func() bool {
		return forwardPtr(m, "web") != nil
	})

	err := m.AddService(config.ServiceConfig{
		Name:       "web",
		Service:    "nginx",
		RemotePort: 80,
		LocalPort:  8081,
	})
	if !errors.Is(err, config.ErrServiceExists) {
		t.Fatalf("expected ErrServiceExists, got: %v", err)
	}
}

func TestManager_AddService_PortConflict(t *testing.T) {
	// Local-port-conflict detection is now atomic with registration in
	// reserveForward, so it is exercised through a running manager: bind "web" on
	// 8080, then a second service requesting 8080 must be rejected.
	m := newTestManager()
	m.children = make(map[string][]string)

	_, stop := startTestManager(t, m)
	defer stop()

	if err := m.AddService(config.ServiceConfig{
		Name:       "web",
		Service:    "nginx",
		RemotePort: 80,
		LocalPort:  8080,
	}); err != nil {
		t.Fatalf("first AddService failed: %v", err)
	}
	eventually(t, 2*time.Second, func() bool {
		return forwardPtr(m, "web") != nil
	})

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

	// Verify it appears in forwards once supervise has registered it.
	eventually(t, 2*time.Second, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		_, exists := m.forwards["test-svc"]
		return exists
	})

	// Remove it
	err = m.RemoveService("test-svc")
	if err != nil {
		t.Fatalf("RemoveService failed: %v", err)
	}

	m.mu.RLock()
	_, exists := m.forwards["test-svc"]
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

// TestManager_AddRemove_AfterStop verifies that AddService and RemoveService
// return ErrManagerStopped (promptly, without blocking forever) once the event
// loop has exited, instead of blocking on an unbuffered cmdCh with no receiver.
func TestManager_AddRemove_AfterStop(t *testing.T) {
	m := newTestManager()

	ctx, cancel := context.WithCancel(context.Background())

	stopped := make(chan struct{})
	go func() {
		m.Start(ctx)
		close(stopped)
	}()

	// Wait for cmdCh to be initialized.
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

	// Cancel and wait for the event loop (and all supervisor goroutines) to exit.
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not stop after context cancellation")
	}

	// RemoveService must return ErrManagerStopped without blocking.
	assertReturnsStopped(t, "RemoveService", func() error {
		return m.RemoveService("anything")
	})

	// AddService (past validation) must also return ErrManagerStopped without blocking.
	assertReturnsStopped(t, "AddService", func() error {
		return m.AddService(config.ServiceConfig{
			Name:       "late",
			Service:    "nginx",
			RemotePort: 80,
			LocalPort:  9998,
		})
	})
}

// assertReturnsStopped runs fn in a goroutine and fails if it does not return
// ErrManagerStopped within a bounded timeout (a blocking send would hang here).
func assertReturnsStopped(t *testing.T, name string, fn func() error) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- fn() }()
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrManagerStopped) {
			t.Fatalf("%s: expected ErrManagerStopped, got: %v", name, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked after manager stopped (expected ErrManagerStopped)", name)
	}
}

// TestManager_Reload_RespectsExternalDetector verifies that Reload re-runs the
// installed external detector and does not (re-)start a service that another
// instance owns, regardless of which reload path invoked it.
func TestManager_Reload_RespectsExternalDetector(t *testing.T) {
	m := newTestManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Start(ctx)
	}()

	// Wait for cmdCh.
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

	// Detector marks "owned" as managed by another instance.
	m.SetExternalDetector(func(cfg *config.Config) []ExternalForward {
		var ext []ExternalForward
		for _, svc := range cfg.Services {
			if svc.Name == "owned" {
				ext = append(ext, ExternalForward{Service: svc, Instance: "other", PID: 4242})
			}
		}
		return ext
	})

	newCfg := &config.Config{
		Context:   "test",
		Namespace: "default",
		Services: []config.ServiceConfig{
			{Name: "local", Service: "local-svc", RemotePort: 80, LocalPort: 8080},
			{Name: "owned", Service: "owned-svc", RemotePort: 80, LocalPort: 8081},
		},
	}

	if _, _, err := m.Reload(newCfg); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// 'local' must be started; 'owned' must never start (another instance owns it).
	eventually(t, 2*time.Second, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		_, hasLocal := m.forwards["local"]
		return hasLocal
	})
	m.mu.RLock()
	_, hasOwned := m.forwards["owned"]
	m.mu.RUnlock()
	if hasOwned {
		t.Fatal("expected 'owned' NOT to be started (managed by another instance)")
	}

	// And it appears as external in Status.
	foundExternal := false
	for _, s := range m.Status() {
		if s.Service.Name == "owned" && s.State == StateExternal {
			foundExternal = true
		}
	}
	if !foundExternal {
		t.Fatal("expected 'owned' to appear as StateExternal in Status")
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

	// Verify new-svc was added.
	eventually(t, 2*time.Second, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		_, hasNew := m.forwards["new-svc"]
		return hasNew
	})

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

// startTestManager starts m in a background goroutine and blocks until its
// command channel is initialized, then returns a stop func that cancels the
// context and waits for the event loop and all supervisor goroutines to exit.
func startTestManager(t *testing.T, m *Manager) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Start(ctx)
	}()

	eventually(t, 2*time.Second, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return m.cmdCh != nil
	})

	return ctx, func() {
		cancel()
		wg.Wait()
	}
}

// svcFor builds a single-port, service-target ServiceConfig. With newTestManager
// (nil clientset) resolvePod returns a clean error for service targets — and the
// pod watcher is skipped — so the runner fails fast and retries on backoff while
// the *portForward stays registered in m.forwards. That registered entry is all
// the orchestration paths under test (Reload / SetExternalForwards / Add /
// Remove) operate on. Mirrors the existing add/remove/reload test fixtures. The
// local port is OS-assigned (0) so concurrent services never collide.
func svcFor(name string) config.ServiceConfig {
	return config.ServiceConfig{
		Name:       name,
		Service:    name + "-svc",
		RemotePort: 80,
		LocalPort:  0,
	}
}

// forwardPtr returns the *portForward registered under name (or nil), used to
// assert pointer identity is preserved across a Reload — the strongest possible
// "this forward was never torn down and re-supervised" signal.
func forwardPtr(m *Manager, name string) *portForward {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.forwards[name]
}

// TestManager_ConcurrentReloadAndTeardown drives the concurrent paths that touch
// both Manager locks against each other, exercising two invariants under the
// race detector:
//
//   - Lock ordering (mu first, externalMu second):
//     Reload -> refreshExternalForwards reads externalMu then takes mu (via
//     RemoveService) — the externalMu→mu direction; Status takes mu then
//     externalMu — the mu→externalMu direction; SetExternalForwards mutates
//     externalMu alone. A reverse ordering would deadlock (caught by the
//     bounded done channel); an unsynchronized map access would trip -race.
//
//   - Registration atomicity (m.order/m.forwards never diverge):
//     existence-check and registration happen atomically on the serial command
//     path (reserveForward), so an add/remove pair issued back-to-back can never
//     leave m.order holding a name whose m.forwards entry is absent. After the
//     churn quiesces, every name in m.order resolves to a non-nil forward, with
//     no duplicates, and Status() neither panics nor diverges from m.order.
//
// The churn goroutine repeatedly adds and removes "churn" while Reload,
// SetExternalForwards, and Status run concurrently; "churn" is single-writer so
// the only contention it adds is against the remove/reload/status paths.
func TestManager_ConcurrentReloadAndTeardown(t *testing.T) {
	m := newTestManager()
	m.children = make(map[string][]string)
	m.cfg.Services = []config.ServiceConfig{
		svcFor("a"),
		svcFor("b"),
		svcFor("c"),
	}

	_, stop := startTestManager(t, m)
	defer stop()

	// Wait for the initial set to register so Reload/Status operate on live data.
	eventually(t, 2*time.Second, func() bool {
		return forwardPtr(m, "a") != nil && forwardPtr(m, "b") != nil && forwardPtr(m, "c") != nil
	})

	// Detector marks "c" as externally owned, so each Reload exercises the
	// externalMu→mu path (refreshExternalForwards -> RemoveService) and then,
	// because "c" is external, does not re-add it.
	m.SetExternalDetector(func(cfg *config.Config) []ExternalForward {
		for _, svc := range cfg.Services {
			if svc.Name == "c" {
				return []ExternalForward{{Service: svc, Instance: "other", PID: 4242}}
			}
		}
		return nil
	})

	reloadCfg := &config.Config{Context: "test", Namespace: "default", Services: []config.ServiceConfig{
		svcFor("a"), svcFor("b"), svcFor("c"),
	}}

	const iterations = 50
	var wg sync.WaitGroup

	// Reloader: repeated reloads of the (stable) set drive refreshExternalForwards
	// on every call. reloadMu serialises these, so no two adds of a name race.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iterations {
			_, _, _ = m.Reload(reloadCfg)
		}
	}()

	// External toggler: flips the external overlay independently of Reload,
	// hammering externalMu while Status and Reload also contend for it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range iterations {
			if i%2 == 0 {
				m.SetExternalForwards([]ExternalForward{
					{Service: config.ServiceConfig{Name: "b"}, Instance: "other", PID: 99},
				})
			} else {
				m.SetExternalForwards(nil)
			}
		}
	}()

	// Churn: a single goroutine is the sole writer of "churn", so its add/remove
	// pairs never race another add of the same name. This drives the mu path
	// (AddService/RemoveService) concurrently with the above.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iterations {
			_ = m.AddService(svcFor("churn"))
			_ = m.RemoveService("churn")
		}
	}()

	// Reader: Status takes mu then externalMu — the opposite acquisition order
	// from the Reload path, which is exactly where a lock-ordering bug bites.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iterations {
			_ = m.Status()
		}
	}()

	// Bound the whole interleaving: a lock-ordering deadlock would hang here.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent reload/teardown deadlocked (did not complete within 15s)")
	}

	// Quiesce: all churn goroutines have returned (wg.Wait completed), so no
	// concurrent writer remains. Remove the lone direct-add name and clear the
	// external overlay so the structural assertions below are deterministic —
	// the overlay's final state (set by a late Reload or the toggler) is
	// otherwise a race, and an overlay name that shadows a live local forward
	// would make Status report it twice for reasons unrelated to this test.
	_ = m.RemoveService("churn")
	m.SetExternalForwards(nil)

	// m.order and m.forwards must never diverge: every name in m.order resolves
	// to a non-nil forward, and no name appears twice. A registration that was
	// not atomic with the existence check would let m.order retain a name with
	// no m.forwards entry, which is exactly the nil-deref Status() guards against.
	m.mu.RLock()
	orderSeen := make(map[string]bool, len(m.order))
	for _, name := range m.order {
		if orderSeen[name] {
			t.Errorf("duplicate name %q in m.order", name)
		}
		orderSeen[name] = true
		if m.forwards[name] == nil {
			t.Errorf("m.order has %q with no (non-nil) m.forwards entry", name)
		}
	}
	if len(orderSeen) != len(m.forwards) {
		t.Errorf("m.order/m.forwards divergence: %d order names vs %d forwards", len(orderSeen), len(m.forwards))
	}
	orderLen := len(m.order)
	m.mu.RUnlock()

	// Status must be callable and self-consistent — no panic (a nil forward in
	// m.order would crash here), no duplicate names, every entry named, and one
	// local entry per m.order name. External overlay entries may also appear, so
	// the local count is asserted via the order-name set rather than total length.
	seen := make(map[string]bool)
	localCount := 0
	for _, s := range m.Status() {
		if s.Service.Name == "" {
			t.Errorf("status entry with empty service name: %+v", s)
		}
		if seen[s.Service.Name] {
			t.Errorf("duplicate service %q in Status", s.Service.Name)
		}
		seen[s.Service.Name] = true
		if s.State != StateExternal {
			localCount++
		}
	}
	if localCount != orderLen {
		t.Errorf("Status returned %d local entries, want one per m.order name (%d)", localCount, orderLen)
	}
}

// TestManager_AddService_ConcurrentDuplicate fires two concurrent AddService
// calls for the same name at a running manager. Because the existence check and
// registration are atomic on the serial command path (reserveForward), exactly
// one must succeed and register a single forward; the other must observe the
// winner and return ErrServiceExists. A non-atomic check-then-register would let
// both pass the check and both register, leaving a duplicate in m.order. Run
// under -race, repeated, so the interleaving is exercised many times.
func TestManager_AddService_ConcurrentDuplicate(t *testing.T) {
	m := newTestManager()
	m.children = make(map[string][]string)

	_, stop := startTestManager(t, m)
	defer stop()

	const dup = "dup"
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = m.AddService(svcFor(dup))
		}(i)
	}
	wg.Wait()

	// Exactly one add succeeds; the loser gets ErrServiceExists.
	var successes, exists int
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, config.ErrServiceExists):
			exists++
		default:
			t.Fatalf("unexpected AddService error: %v", err)
		}
	}
	if successes != 1 || exists != 1 {
		t.Fatalf("expected exactly one success and one ErrServiceExists, got %d success / %d exists", successes, exists)
	}

	// Exactly one registration: no duplicate entry in m.order, one forward total.
	m.mu.RLock()
	orderCount := 0
	for _, name := range m.order {
		if name == dup {
			orderCount++
		}
	}
	forwardCount := len(m.forwards)
	orderLen := len(m.order)
	m.mu.RUnlock()

	if orderCount != 1 {
		t.Errorf("expected %q to appear once in m.order, got %d", dup, orderCount)
	}
	if forwardCount != 1 {
		t.Errorf("expected exactly 1 forward registered, got %d", forwardCount)
	}
	if orderLen != 1 {
		t.Errorf("expected m.order length 1, got %d", orderLen)
	}
}

// TestManager_ReloadPreservesUnchangedForwards asserts that a Reload which keeps
// some services and replaces others does not churn the unchanged forwards: the
// exact *portForward instances for the retained services must survive (proving
// they were never removed and re-supervised), while the dropped service is
// removed and the new one added. Pointer identity is the strongest no-churn
// signal: a teardown+re-add would replace the entry with a fresh struct.
func TestManager_ReloadPreservesUnchangedForwards(t *testing.T) {
	m := newTestManager()
	m.cfg.Services = []config.ServiceConfig{
		svcFor("a"),
		svcFor("b"),
		svcFor("c"),
	}

	_, stop := startTestManager(t, m)
	defer stop()

	// Wait until A, B, C have all registered their forward entries.
	eventually(t, 3*time.Second, func() bool {
		return forwardPtr(m, "a") != nil &&
			forwardPtr(m, "b") != nil &&
			forwardPtr(m, "c") != nil
	})

	beforeA := forwardPtr(m, "a")
	beforeB := forwardPtr(m, "b")

	// Reload: keep A and B unchanged, drop C, add D.
	newCfg := &config.Config{Context: "test", Namespace: "default", Services: []config.ServiceConfig{
		svcFor("a"),
		svcFor("b"),
		svcFor("d"),
	}}
	added, removed, err := m.Reload(newCfg)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if added != 1 {
		t.Fatalf("expected 1 added (d), got %d", added)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed (c), got %d", removed)
	}

	// C must be gone, D must appear.
	eventually(t, 3*time.Second, func() bool {
		return forwardPtr(m, "c") == nil && forwardPtr(m, "d") != nil
	})

	// A and B must be the SAME *portForward instances — Reload must not have
	// touched them at all.
	if afterA := forwardPtr(m, "a"); afterA != beforeA {
		t.Errorf("forward %q was replaced across reload (%p -> %p): unchanged forward churned", "a", beforeA, afterA)
	}
	if afterB := forwardPtr(m, "b"); afterB != beforeB {
		t.Errorf("forward %q was replaced across reload (%p -> %p): unchanged forward churned", "b", beforeB, afterB)
	}
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

	// Verify the post-reload state has settled: keep + new-svc present, remove-me gone.
	eventually(t, 2*time.Second, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		_, hasKeep := m.forwards["keep"]
		_, hasRemoved := m.forwards["remove-me"]
		_, hasNew := m.forwards["new-svc"]
		return hasKeep && hasNew && !hasRemoved
	})

	cancel()
	wg.Wait()
}

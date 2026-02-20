package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/rbaliyan/kubeport/internal/config"
)

// discardLogger returns a logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestForwardState_String(t *testing.T) {
	tests := []struct {
		state ForwardState
		want  string
	}{
		{StateStarting, "starting"},
		{StateRunning, "running"},
		{StateFailed, "failed"},
		{StateStopped, "stopped"},
		{ForwardState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("ForwardState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestAddJitter(t *testing.T) {
	base := 1 * time.Second
	min := time.Duration(float64(base) * 0.75) // -25%
	max := time.Duration(float64(base) * 1.25) // +25%

	for i := 0; i < 100; i++ {
		got := addJitter(base)
		if got < min || got > max {
			t.Fatalf("addJitter(%v) = %v, want in [%v, %v]", base, got, min, max)
		}
	}
}

func TestAddJitter_Zero(t *testing.T) {
	got := addJitter(0)
	if got != 0 {
		t.Fatalf("addJitter(0) = %v, want 0", got)
	}
}

func TestResolvePod_DirectPod(t *testing.T) {
	client := fake.NewSimpleClientset()
	m := &Manager{clientset: client}

	svc := config.ServiceConfig{
		Name:       "test",
		Pod:        "my-pod",
		RemotePort: 6379,
	}

	name, port, err := m.resolvePod(context.Background(), "default", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "my-pod" {
		t.Fatalf("expected 'my-pod', got %q", name)
	}
	if port != 6379 {
		t.Fatalf("expected port 6379, got %d", port)
	}
}

func TestResolvePod_ServiceWithRunningPod(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-svc",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "test"},
				Ports: []corev1.ServicePort{
					{Port: 80, TargetPort: intstr.FromInt32(8080)},
				},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-abc",
				Namespace: "default",
				Labels:    map[string]string{"app": "test"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	m := &Manager{clientset: client}

	svc := config.ServiceConfig{
		Name:       "test",
		Service:    "my-svc",
		RemotePort: 80,
	}

	name, port, err := m.resolvePod(context.Background(), "default", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "test-pod-abc" {
		t.Fatalf("expected 'test-pod-abc', got %q", name)
	}
	if port != 8080 {
		t.Fatalf("expected targetPort 8080, got %d", port)
	}
}

func TestResolvePod_ServicePortMatchesTargetPort(t *testing.T) {
	// When service port == targetPort, resolvePod should return the same port.
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-svc",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "test"},
				Ports: []corev1.ServicePort{
					{Port: 8080, TargetPort: intstr.FromInt32(8080)},
				},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-1",
				Namespace: "default",
				Labels:    map[string]string{"app": "test"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	m := &Manager{clientset: client}
	svc := config.ServiceConfig{
		Name:       "test",
		Service:    "my-svc",
		RemotePort: 8080,
	}

	_, port, err := m.resolvePod(context.Background(), "default", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 8080 {
		t.Fatalf("expected port 8080, got %d", port)
	}
}

func TestResolvePod_NoPortSpec(t *testing.T) {
	// When the service has no port spec matching remote_port, fall back to remote_port.
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-svc",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "test"},
				// No ports defined
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-1",
				Namespace: "default",
				Labels:    map[string]string{"app": "test"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	m := &Manager{clientset: client}
	svc := config.ServiceConfig{
		Name:       "test",
		Service:    "my-svc",
		RemotePort: 3000,
	}

	_, port, err := m.resolvePod(context.Background(), "default", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 3000 {
		t.Fatalf("expected fallback to remote_port 3000, got %d", port)
	}
}

func TestResolvePod_NamedTargetPort(t *testing.T) {
	// When targetPort is a named port (e.g., "http"), resolvePod should
	// look up the name in the pod's container ports to find the numeric value.
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "atlas-app-dev",
				Namespace: "dev",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "atlas"},
				Ports: []corev1.ServicePort{
					{Name: "http", Port: 80, TargetPort: intstr.FromString("http")},
				},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "atlas-pod-xyz",
				Namespace: "dev",
				Labels:    map[string]string{"app": "atlas"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "atlas",
						Ports: []corev1.ContainerPort{
							{Name: "http", ContainerPort: 8061},
							{Name: "metrics", ContainerPort: 9090},
						},
					},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	m := &Manager{clientset: client}
	svc := config.ServiceConfig{
		Name:       "Atlas HTTP",
		Service:    "atlas-app-dev",
		RemotePort: 80,
		LocalPort:  8061,
	}

	name, port, err := m.resolvePod(context.Background(), "dev", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "atlas-pod-xyz" {
		t.Fatalf("expected 'atlas-pod-xyz', got %q", name)
	}
	if port != 8061 {
		t.Fatalf("expected named port 'http' to resolve to 8061, got %d", port)
	}
}

func TestResolvePod_ServiceNoRunningPod(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-svc",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "test"},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-pending",
				Namespace: "default",
				Labels:    map[string]string{"app": "test"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		},
	)

	m := &Manager{clientset: client}

	svc := config.ServiceConfig{
		Name:    "test",
		Service: "my-svc",
	}

	_, _, err := m.resolvePod(context.Background(), "default", svc)
	if err == nil {
		t.Fatal("expected error for no running pods")
	}
}

func TestResolvePod_ServiceNoSelector(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-svc",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				// No selector
			},
		},
	)

	m := &Manager{clientset: client}

	svc := config.ServiceConfig{
		Name:    "test",
		Service: "my-svc",
	}

	_, _, err := m.resolvePod(context.Background(), "default", svc)
	if err == nil {
		t.Fatal("expected error for no selector")
	}
}

func TestResolvePod_ServiceNotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	m := &Manager{clientset: client}

	svc := config.ServiceConfig{
		Name:    "test",
		Service: "nonexistent",
	}

	_, _, err := m.resolvePod(context.Background(), "default", svc)
	if err == nil {
		t.Fatal("expected error for missing service")
	}
}

func TestResolvePod_PicksRunningPod(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-svc",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "test"},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-failed",
				Namespace: "default",
				Labels:    map[string]string{"app": "test"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodFailed},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-running",
				Namespace: "default",
				Labels:    map[string]string{"app": "test"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	m := &Manager{clientset: client}
	svc := config.ServiceConfig{
		Name:    "test",
		Service: "my-svc",
	}

	name, _, err := m.resolvePod(context.Background(), "default", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "pod-running" {
		t.Fatalf("expected 'pod-running', got %q", name)
	}
}

func TestCheckNamespace(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
		},
	)

	m := &Manager{
		clientset: client,
		cfg:       &config.Config{Namespace: "test-ns"},
	}

	if err := m.CheckNamespace(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckNamespace_Missing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m := &Manager{
		clientset: client,
		cfg:       &config.Config{Namespace: "nonexistent"},
	}

	if err := m.CheckNamespace(context.Background()); err == nil {
		t.Fatal("expected error for missing namespace")
	}
}

func TestGetContext(t *testing.T) {
	m := &Manager{cfg: &config.Config{Context: "my-cluster"}}
	if got := m.GetContext(); got != "my-cluster" {
		t.Fatalf("GetContext() = %q, want %q", got, "my-cluster")
	}
}

func TestStatus_Empty(t *testing.T) {
	m := &Manager{forwards: make(map[string]*portForward)}
	statuses := m.Status()
	if len(statuses) != 0 {
		t.Fatalf("expected 0 statuses, got %d", len(statuses))
	}
}

func TestStatus_ReturnsForwards(t *testing.T) {
	m := &Manager{
		forwards: map[string]*portForward{
			"svc-a": {
				svc:        config.ServiceConfig{Name: "svc-a", LocalPort: 8080, RemotePort: 80},
				state:      StateRunning,
				restarts:   2,
				actualPort: 8080,
				lastStart:  time.Now(),
			},
			"svc-b": {
				svc:        config.ServiceConfig{Name: "svc-b", LocalPort: 0, RemotePort: 443},
				state:      StateStarting,
				actualPort: 0,
			},
		},
		order: []string{"svc-a", "svc-b"},
	}

	statuses := m.Status()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	byName := make(map[string]ForwardStatus)
	for _, s := range statuses {
		byName[s.Service.Name] = s
	}

	a := byName["svc-a"]
	if a.State != StateRunning {
		t.Fatalf("svc-a: expected StateRunning, got %v", a.State)
	}
	if a.Restarts != 2 {
		t.Fatalf("svc-a: expected 2 restarts, got %d", a.Restarts)
	}
	if a.ActualPort != 8080 {
		t.Fatalf("svc-a: expected port 8080, got %d", a.ActualPort)
	}

	b := byName["svc-b"]
	if b.State != StateStarting {
		t.Fatalf("svc-b: expected StateStarting, got %v", b.State)
	}
}

func TestStatus_FallsBackToLocalPort(t *testing.T) {
	m := &Manager{
		forwards: map[string]*portForward{
			"svc": {
				svc:        config.ServiceConfig{Name: "svc", LocalPort: 9090, RemotePort: 80},
				state:      StateStarting,
				actualPort: 0, // Not yet assigned
			},
		},
		order: []string{"svc"},
	}

	statuses := m.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].ActualPort != 9090 {
		t.Fatalf("expected fallback to local_port 9090, got %d", statuses[0].ActualPort)
	}
}

func TestStop(t *testing.T) {
	cancelled := false
	fwCancelled := false

	m := &Manager{
		cancel: func() { cancelled = true },
		forwards: map[string]*portForward{
			"svc": {
				svc:    config.ServiceConfig{Name: "svc"},
				state:  StateRunning,
				cancel: func() { fwCancelled = true },
			},
		},
	}

	m.Stop()

	if !cancelled {
		t.Fatal("expected manager cancel to be called")
	}
	if !fwCancelled {
		t.Fatal("expected forward cancel to be called")
	}

	m.mu.RLock()
	pf := m.forwards["svc"]
	m.mu.RUnlock()

	pf.mu.Lock()
	state := pf.state
	pf.mu.Unlock()

	if state != StateStopped {
		t.Fatalf("expected StateStopped, got %v", state)
	}
}

func TestStop_NilCancel(t *testing.T) {
	m := &Manager{
		forwards: map[string]*portForward{
			"svc": {
				svc:   config.ServiceConfig{Name: "svc"},
				state: StateRunning,
				// cancel is nil
			},
		},
	}

	// Should not panic
	m.Stop()
}

func TestStatus_ConcurrentAccess(t *testing.T) {
	m := &Manager{
		forwards: map[string]*portForward{
			"svc": {
				svc:        config.ServiceConfig{Name: "svc", LocalPort: 8080, RemotePort: 80},
				state:      StateRunning,
				actualPort: 8080,
			},
		},
		order: []string{"svc"},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Status()
		}()
	}
	wg.Wait()
}

func TestSupervise_ContextCancellation(t *testing.T) {
	client := fake.NewSimpleClientset()

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
			Services: []config.ServiceConfig{
				{Name: "test-svc", Service: "svc", LocalPort: 18080, RemotePort: 80},
			},
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		output:               io.Discard,
		logger:               discardLogger(),
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       100 * time.Millisecond,
		backoffMax:           500 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	svc := m.cfg.Services[0]
	m.supervise(ctx, svc)

	m.mu.RLock()
	pf := m.forwards["test-svc"]
	m.mu.RUnlock()

	pf.mu.Lock()
	state := pf.state
	pf.mu.Unlock()

	if state != StateStopped {
		t.Fatalf("expected StateStopped after context cancel, got %v", state)
	}
}

func TestSupervise_MaxRestarts(t *testing.T) {
	// Create a fake client with the service but no running pods,
	// which makes resolvePod fail every time — triggering restarts.
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-svc",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "test"},
			},
		},
		// No pods — resolvePod always fails
	)

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
			Services: []config.ServiceConfig{
				{Name: "test-svc", Service: "my-svc", LocalPort: 18081, RemotePort: 80},
			},
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		output:               io.Discard,
		logger:               discardLogger(),
		maxRestarts:          3,
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       10 * time.Millisecond, // Fast backoff for test
		backoffMax:           50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	svc := m.cfg.Services[0]
	m.supervise(ctx, svc)

	m.mu.RLock()
	pf := m.forwards["test-svc"]
	m.mu.RUnlock()

	pf.mu.Lock()
	restarts := pf.restarts
	state := pf.state
	pf.mu.Unlock()

	if restarts != 3 {
		t.Fatalf("expected 3 restarts, got %d", restarts)
	}
	if state != StateFailed {
		t.Fatalf("expected StateFailed after max restarts, got %v", state)
	}
}

func TestSupervise_UnlimitedRestarts(t *testing.T) {
	// maxRestarts=0 means unlimited. Verify it retries more than a few times.
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-svc",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "test"},
			},
		},
	)

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
			Services: []config.ServiceConfig{
				{Name: "test-svc", Service: "my-svc", LocalPort: 18082, RemotePort: 80},
			},
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		output:               io.Discard,
		logger:               discardLogger(),
		maxRestarts:          0, // Unlimited
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       5 * time.Millisecond,
		backoffMax:           10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	svc := m.cfg.Services[0]
	m.supervise(ctx, svc)

	m.mu.RLock()
	pf := m.forwards["test-svc"]
	m.mu.RUnlock()

	pf.mu.Lock()
	restarts := pf.restarts
	pf.mu.Unlock()

	// With unlimited restarts and fast backoff, should have retried several times
	if restarts < 3 {
		t.Fatalf("expected at least 3 restarts with unlimited, got %d", restarts)
	}
}

func TestStart_MultipleServices(t *testing.T) {
	client := fake.NewSimpleClientset()

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
			Services: []config.ServiceConfig{
				{Name: "svc-a", Service: "a", LocalPort: 18083, RemotePort: 80},
				{Name: "svc-b", Service: "b", LocalPort: 18084, RemotePort: 443},
			},
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		output:               io.Discard,
		logger:               discardLogger(),
		maxRestarts:          1,
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       5 * time.Millisecond,
		backoffMax:           10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)

	// After Start returns, both services should have entries
	m.mu.RLock()
	count := len(m.forwards)
	m.mu.RUnlock()

	if count != 2 {
		t.Fatalf("expected 2 forwards, got %d", count)
	}
}

func TestSupervise_NamespaceOverride(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-svc",
				Namespace: "custom-ns",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "test"},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-1",
				Namespace: "custom-ns",
				Labels:    map[string]string{"app": "test"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	m := &Manager{clientset: client}

	svc := config.ServiceConfig{
		Name:      "test",
		Service:   "my-svc",
		Namespace: "custom-ns",
	}

	name, _, err := m.resolvePod(context.Background(), "custom-ns", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "pod-1" {
		t.Fatalf("expected 'pod-1', got %q", name)
	}
}

func TestStart_ConcurrentFailures(t *testing.T) {
	// All services will fail (no pods exist) — verify concurrent failures
	// don't cause panics, data races, or leave stale state.
	client := fake.NewSimpleClientset()

	services := make([]config.ServiceConfig, 5)
	for i := range services {
		services[i] = config.ServiceConfig{
			Name:       fmt.Sprintf("svc-%d", i),
			Service:    fmt.Sprintf("missing-%d", i),
			LocalPort:  18090 + i,
			RemotePort: 80,
		}
	}

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
			Services:  services,
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		output:               io.Discard,
		logger:               discardLogger(),
		maxRestarts:          2,
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       5 * time.Millisecond,
		backoffMax:           10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)

	// All 5 services should have entries
	m.mu.RLock()
	count := len(m.forwards)
	m.mu.RUnlock()

	if count != 5 {
		t.Fatalf("expected 5 forwards, got %d", count)
	}

	// Each service should have hit max restarts
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("svc-%d", i)
		m.mu.RLock()
		pf := m.forwards[name]
		m.mu.RUnlock()

		pf.mu.Lock()
		state := pf.state
		restarts := pf.restarts
		pf.mu.Unlock()

		if state != StateFailed {
			t.Errorf("service %s: expected StateFailed, got %v", name, state)
		}
		if restarts < 2 {
			t.Errorf("service %s: expected >= 2 restarts, got %d", name, restarts)
		}
	}
}

func TestStart_ContextCancellation_Cleanup(t *testing.T) {
	// Cancel context while services are running — verify all stop cleanly.
	client := fake.NewSimpleClientset()

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
			Services: []config.ServiceConfig{
				{Name: "svc-a", Service: "a", LocalPort: 18095, RemotePort: 80},
				{Name: "svc-b", Service: "b", LocalPort: 18096, RemotePort: 80},
				{Name: "svc-c", Service: "c", LocalPort: 18097, RemotePort: 80},
			},
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		output:               io.Discard,
		logger:               discardLogger(),
		maxRestarts:          0, // unlimited
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       50 * time.Millisecond,
		backoffMax:           100 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	m.Start(ctx)

	// All services should exist and be stopped
	m.mu.RLock()
	count := len(m.forwards)
	m.mu.RUnlock()

	if count != 3 {
		t.Fatalf("expected 3 forwards, got %d", count)
	}

	for _, name := range []string{"svc-a", "svc-b", "svc-c"} {
		m.mu.RLock()
		pf := m.forwards[name]
		m.mu.RUnlock()

		pf.mu.Lock()
		state := pf.state
		pf.mu.Unlock()

		// Service may be StateStopped (caught at top of loop) or StateFailed
		// (context cancelled during backoff sleep after a failure). Both are
		// acceptable terminal states after cancellation.
		if state != StateStopped && state != StateFailed {
			t.Errorf("service %s: expected StateStopped or StateFailed after cancel, got %v", name, state)
		}
	}
}

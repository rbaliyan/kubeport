package proxy

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/rbaliyan/kubeport/internal/config"
)

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
		Name: "test",
		Pod:  "my-pod",
	}

	name, err := m.resolvePod(context.Background(), "default", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "my-pod" {
		t.Fatalf("expected 'my-pod', got %q", name)
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
		Name:    "test",
		Service: "my-svc",
	}

	name, err := m.resolvePod(context.Background(), "default", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "test-pod-abc" {
		t.Fatalf("expected 'test-pod-abc', got %q", name)
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

	_, err := m.resolvePod(context.Background(), "default", svc)
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

	_, err := m.resolvePod(context.Background(), "default", svc)
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

	_, err := m.resolvePod(context.Background(), "default", svc)
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

	name, err := m.resolvePod(context.Background(), "default", svc)
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
	var buf bytes.Buffer

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
		output:               &buf,
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

	var buf bytes.Buffer

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
		output:               &buf,
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

	var buf bytes.Buffer

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
		output:               &buf,
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

	name, err := m.resolvePod(context.Background(), "custom-ns", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "pod-1" {
		t.Fatalf("expected 'pod-1', got %q", name)
	}
}

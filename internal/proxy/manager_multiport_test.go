package proxy

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// --- resolveServicePorts tests ---

func fakeServiceWithPorts(name, namespace string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    ports,
		},
	}
}

func TestResolveServicePorts_All(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
		corev1.ServicePort{Name: "grpc", Port: 9090},
		corev1.ServicePort{Name: "metrics", Port: 9100},
	)
	client := fake.NewClientset(svc)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "my-api",
		Ports:   config.PortsConfig{All: true},
	}

	resolved, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 3 {
		t.Fatalf("expected 3 resolved ports, got %d", len(resolved))
	}

	// Verify local == 0 (dynamic) by default
	for _, rp := range resolved {
		if rp.LocalPort != 0 {
			t.Errorf("port %s: expected local=0 (dynamic), got %d", rp.Name, rp.LocalPort)
		}
	}
}

func TestResolveServicePorts_ByName(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
		corev1.ServicePort{Name: "grpc", Port: 9090},
		corev1.ServicePort{Name: "metrics", Port: 9100},
	)
	client := fake.NewClientset(svc)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "my-api",
		Ports: config.PortsConfig{
			Selectors: []config.PortSelector{
				{Name: "http"},
				{Name: "grpc"},
			},
		},
	}

	resolved, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolved ports, got %d", len(resolved))
	}
	if resolved[0].Name != "http" || resolved[1].Name != "grpc" {
		t.Fatalf("unexpected ports: %v", resolved)
	}
}

func TestResolveServicePorts_ByPort(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
		corev1.ServicePort{Name: "grpc", Port: 9090},
	)
	client := fake.NewClientset(svc)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "my-api",
		Ports: config.PortsConfig{
			Selectors: []config.PortSelector{
				{Port: 9090},
			},
		},
	}

	resolved, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved port, got %d", len(resolved))
	}
	if resolved[0].RemotePort != 9090 {
		t.Fatalf("expected remote port 9090, got %d", resolved[0].RemotePort)
	}
}

func TestResolveServicePorts_SelectorNotFound(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
	)
	client := fake.NewClientset(svc)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "my-api",
		Ports: config.PortsConfig{
			Selectors: []config.PortSelector{
				{Name: "nonexistent"},
			},
		},
	}

	_, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err == nil {
		t.Fatal("expected error for missing port selector")
	}
}

func TestResolveServicePorts_ExcludePorts(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
		corev1.ServicePort{Name: "metrics", Port: 9100},
	)
	client := fake.NewClientset(svc)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:         "my-api",
		Service:      "my-api",
		Ports:        config.PortsConfig{All: true},
		ExcludePorts: []string{"metrics"},
	}

	resolved, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved port after exclude, got %d", len(resolved))
	}
	if resolved[0].Name != "http" {
		t.Fatalf("expected 'http', got %q", resolved[0].Name)
	}
}

func TestResolveServicePorts_AllExcluded(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
	)
	client := fake.NewClientset(svc)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:         "my-api",
		Service:      "my-api",
		Ports:        config.PortsConfig{All: true},
		ExcludePorts: []string{"http"},
	}

	_, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err == nil {
		t.Fatal("expected error when all ports excluded")
	}
}

func TestResolveServicePorts_WithOffset(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
		corev1.ServicePort{Name: "grpc", Port: 9090},
	)
	client := fake.NewClientset(svc)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:            "my-api",
		Service:         "my-api",
		Ports:           config.PortsConfig{All: true},
		LocalPortOffset: 10000,
	}

	resolved, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, rp := range resolved {
		expected := rp.RemotePort + 10000
		if rp.LocalPort != expected {
			t.Errorf("port %s: expected local %d, got %d", rp.Name, expected, rp.LocalPort)
		}
	}
}

func TestResolveServicePorts_OffsetOverflow(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 60000},
	)
	client := fake.NewClientset(svc)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:            "my-api",
		Service:         "my-api",
		Ports:           config.PortsConfig{All: true},
		LocalPortOffset: 10000,
	}

	_, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err == nil {
		t.Fatal("expected error for port overflow (60000 + 10000 > 65535)")
	}
}

func TestResolveServicePorts_SelectorLocalPortOverride(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
		corev1.ServicePort{Name: "grpc", Port: 9090},
	)
	client := fake.NewClientset(svc)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "my-api",
		Ports: config.PortsConfig{
			Selectors: []config.PortSelector{
				{Name: "http", LocalPort: 8080},
				{Name: "grpc"}, // no override → dynamic (0)
			},
		},
	}

	resolved, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved[0].LocalPort != 8080 {
		t.Errorf("http: expected local 8080, got %d", resolved[0].LocalPort)
	}
	if resolved[1].LocalPort != 0 {
		t.Errorf("grpc: expected local 0 (dynamic), got %d", resolved[1].LocalPort)
	}
}

func TestResolveServicePorts_ServiceNotFound(t *testing.T) {
	client := fake.NewClientset()
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "nonexistent",
		Ports:   config.PortsConfig{All: true},
	}

	_, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err == nil {
		t.Fatal("expected error for missing service")
	}
}

func TestResolveServicePorts_NilClientset(t *testing.T) {
	m := &Manager{clientset: nil}

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "my-api",
		Ports:   config.PortsConfig{All: true},
	}

	_, err := m.resolveServicePorts(context.Background(), "default", cfg)
	if err == nil {
		t.Fatal("expected error for nil clientset")
	}
}

// --- resolvePodPorts tests ---

func TestResolvePodPorts_All(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 8080},
						{Name: "debug", ContainerPort: 9229},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:  "my-pod",
		Pod:   "my-pod",
		Ports: config.PortsConfig{All: true},
	}

	resolved, err := m.resolvePodPorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(resolved))
	}
}

func TestResolvePodPorts_ByName(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 8080},
						{Name: "debug", ContainerPort: 9229},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name: "my-pod",
		Pod:  "my-pod",
		Ports: config.PortsConfig{
			Selectors: []config.PortSelector{{Name: "http"}},
		},
	}

	resolved, err := m.resolvePodPorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 port, got %d", len(resolved))
	}
	if resolved[0].Name != "http" {
		t.Fatalf("expected 'http', got %q", resolved[0].Name)
	}
}

func TestResolvePodPorts_Exclude(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 8080},
						{Name: "debug", ContainerPort: 9229},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:         "my-pod",
		Pod:          "my-pod",
		Ports:        config.PortsConfig{All: true},
		ExcludePorts: []string{"debug"},
	}

	resolved, err := m.resolvePodPorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 port after exclude, got %d", len(resolved))
	}
}

func TestResolvePodPorts_WithOffset(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: 8080},
					},
				},
			},
		},
	}
	client := fake.NewClientset(pod)
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:            "my-pod",
		Pod:             "my-pod",
		Ports:           config.PortsConfig{All: true},
		LocalPortOffset: 1000,
	}

	resolved, err := m.resolvePodPorts(context.Background(), "default", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved[0].LocalPort != 9080 {
		t.Fatalf("expected local port 9080, got %d", resolved[0].LocalPort)
	}
}

func TestResolvePodPorts_PodNotFound(t *testing.T) {
	client := fake.NewClientset()
	m := &Manager{clientset: client}

	cfg := config.ServiceConfig{
		Name:  "my-pod",
		Pod:   "nonexistent",
		Ports: config.PortsConfig{All: true},
	}

	_, err := m.resolvePodPorts(context.Background(), "default", cfg)
	if err == nil {
		t.Fatal("expected error for missing pod")
	}
}

// --- superviseMulti tests ---

func TestSuperviseMulti_ExpandsToChildren(t *testing.T) {
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
		corev1.ServicePort{Name: "grpc", Port: 9090},
	)
	client := fake.NewClientset(svc)

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		children:             make(map[string][]string),
		output:               io.Discard,
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxRestarts:          1,
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       5 * time.Millisecond,
		backoffMax:           10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "my-api",
		Ports:   config.PortsConfig{All: true},
	}

	m.superviseMulti(ctx, cfg)

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Should have children registered
	children, ok := m.children["my-api"]
	if !ok {
		t.Fatal("expected parent 'my-api' in children map")
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}

	// Each child should have a forward entry
	for _, childName := range children {
		if _, exists := m.forwards[childName]; !exists {
			t.Errorf("expected forward entry for child %q", childName)
		}
	}
}

func TestSuperviseMulti_FailedResolution(t *testing.T) {
	// No service exists — resolution should fail and register parent as failed
	client := fake.NewClientset()

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		children:             make(map[string][]string),
		output:               io.Discard,
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxRestarts:          1,
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       5 * time.Millisecond,
		backoffMax:           10 * time.Millisecond,
	}

	ctx := context.Background()

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "nonexistent",
		Ports:   config.PortsConfig{All: true},
	}

	m.superviseMulti(ctx, cfg)

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Parent should be registered with nil children
	if _, ok := m.children["my-api"]; !ok {
		t.Fatal("expected parent in children map after failed resolution")
	}
	if m.children["my-api"] != nil {
		t.Fatal("expected nil children list for failed parent")
	}

	// Parent forward should be in failed state
	pf, ok := m.forwards["my-api"]
	if !ok {
		t.Fatal("expected forward entry for failed parent")
	}
	pf.mu.Lock()
	state := pf.state
	pf.mu.Unlock()
	if state != StateFailed {
		t.Fatalf("expected StateFailed, got %v", state)
	}
}

func TestSuperviseMulti_ChildNaming(t *testing.T) {
	// Test naming convention: named ports use name, unnamed ports use port number
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
		corev1.ServicePort{Name: "", Port: 443}, // unnamed port
	)
	client := fake.NewClientset(svc)

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		children:             make(map[string][]string),
		output:               io.Discard,
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxRestarts:          1,
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       5 * time.Millisecond,
		backoffMax:           10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "my-api",
		Ports:   config.PortsConfig{All: true},
	}

	m.superviseMulti(ctx, cfg)

	m.mu.RLock()
	children := m.children["my-api"]
	m.mu.RUnlock()

	expected := map[string]bool{
		"my-api/http": true,
		"my-api/443":  true,
	}
	for _, child := range children {
		if !expected[child] {
			t.Errorf("unexpected child name: %q", child)
		}
		delete(expected, child)
	}
	if len(expected) > 0 {
		t.Errorf("missing expected children: %v", expected)
	}
}

// --- doRemove with multi-port parent tests ---

func TestDoRemove_MultiPortParent(t *testing.T) {
	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
		},
		forwards: map[string]*portForward{
			"my-api/http": {
				svc:    config.ServiceConfig{Name: "my-api/http", ParentName: "my-api"},
				state:  StateFailed,
				cancel: func() {},
			},
			"my-api/grpc": {
				svc:    config.ServiceConfig{Name: "my-api/grpc", ParentName: "my-api"},
				state:  StateFailed,
				cancel: func() {},
			},
		},
		children: map[string][]string{
			"my-api": {"my-api/http", "my-api/grpc"},
		},
		order:  []string{"my-api/http", "my-api/grpc"},
		output: io.Discard,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := m.doRemove(context.Background(), "my-api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.children["my-api"]; ok {
		t.Fatal("expected parent removed from children map")
	}
	if _, ok := m.forwards["my-api/http"]; ok {
		t.Fatal("expected child my-api/http removed")
	}
	if _, ok := m.forwards["my-api/grpc"]; ok {
		t.Fatal("expected child my-api/grpc removed")
	}
}

func TestDoRemove_FailedMultiPortParent(t *testing.T) {
	// When resolution fails, parent has its own forward entry + nil children
	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
		},
		forwards: map[string]*portForward{
			"my-api": {
				svc:   config.ServiceConfig{Name: "my-api"},
				state: StateFailed,
			},
		},
		children: map[string][]string{
			"my-api": nil,
		},
		order:  []string{"my-api"},
		output: io.Discard,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := m.doRemove(context.Background(), "my-api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.children["my-api"]; ok {
		t.Fatal("expected parent removed from children map")
	}
	if _, ok := m.forwards["my-api"]; ok {
		t.Fatal("expected parent forward entry removed")
	}
	if len(m.order) != 0 {
		t.Fatalf("expected order to be empty, got %v", m.order)
	}
}

// --- IsMultiPort integration test ---

func TestSupervise_RoutesMultiPort(t *testing.T) {
	// Verify that supervise() routes to superviseMulti when IsMultiPort is true
	svc := fakeServiceWithPorts("my-api", "default",
		corev1.ServicePort{Name: "http", Port: 80},
	)
	client := fake.NewClientset(svc)

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
		},
		clientset:            client,
		forwards:             make(map[string]*portForward),
		children:             make(map[string][]string),
		output:               io.Discard,
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxRestarts:          1,
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         1 * time.Second,
		backoffInitial:       5 * time.Millisecond,
		backoffMax:           10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := config.ServiceConfig{
		Name:    "my-api",
		Service: "my-api",
		Ports:   config.PortsConfig{All: true},
	}

	m.supervise(ctx, cfg)

	m.mu.RLock()
	_, hasChildren := m.children["my-api"]
	m.mu.RUnlock()

	if !hasChildren {
		t.Fatal("supervise() should have routed to superviseMulti for multi-port config")
	}
}

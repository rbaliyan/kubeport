package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/config"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// mockSupervisor implements the Supervisor interface for testing.
type mockSupervisor struct {
	statuses      []proxy.ForwardStatus
	stopCalls     atomic.Int32
	addErr        error
	removeErr     error
	reloadAdded   int
	reloadRemoved int
	reloadErr     error
	lastAddSvc    config.ServiceConfig
	lastRemove    string
	applyAdded    int
	applySkipped  int
	applyWarnings []string
}

func (m *mockSupervisor) Status() []proxy.ForwardStatus {
	return m.statuses
}

func (m *mockSupervisor) Stop() {
	m.stopCalls.Add(1)
}

func (m *mockSupervisor) AddService(svc config.ServiceConfig) error {
	m.lastAddSvc = svc
	return m.addErr
}

func (m *mockSupervisor) RemoveService(name string) error {
	m.lastRemove = name
	return m.removeErr
}

func (m *mockSupervisor) Reload(_ *config.Config) (int, int, error) {
	return m.reloadAdded, m.reloadRemoved, m.reloadErr
}

func (m *mockSupervisor) Apply(_ []config.ServiceConfig) (int, int, []string) {
	return m.applyAdded, m.applySkipped, m.applyWarnings
}

func TestServer_Status(t *testing.T) {
	mgr := &mockSupervisor{
		statuses: []proxy.ForwardStatus{
			{
				Service: config.ServiceConfig{
					Name:       "web",
					Service:    "web-svc",
					LocalPort:  8080,
					RemotePort: 80,
				},
				State:      proxy.StateRunning,
				Restarts:   2,
				LastStart:  time.Now(),
				Connected:  true,
				ActualPort: 8080,
			},
			{
				Service: config.ServiceConfig{
					Name:       "api",
					Pod:        "api-pod",
					LocalPort:  0,
					RemotePort: 3000,
				},
				State:      proxy.StateStarting,
				ActualPort: 49152,
			},
		},
	}

	cfg := &config.Config{
		Context:   "test-context",
		Namespace: "test-ns",
	}

	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.Status(context.Background(), &kubeportv1.StatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resp.Version is populated from go-version ldflags; may be empty in test builds
	if resp.Context != "test-context" {
		t.Fatalf("expected context 'test-context', got %s", resp.Context)
	}
	if resp.Namespace != "test-ns" {
		t.Fatalf("expected namespace 'test-ns', got %s", resp.Namespace)
	}
	if len(resp.Forwards) != 2 {
		t.Fatalf("expected 2 forwards, got %d", len(resp.Forwards))
	}

	fw1 := resp.Forwards[0]
	if fw1.Service.Name != "web" {
		t.Fatalf("expected service name 'web', got %s", fw1.Service.Name)
	}
	if fw1.State != kubeportv1.ForwardState_FORWARD_STATE_RUNNING {
		t.Fatalf("expected RUNNING state, got %v", fw1.State)
	}
	if fw1.Restarts != 2 {
		t.Fatalf("expected 2 restarts, got %d", fw1.Restarts)
	}
	if !fw1.Connected {
		t.Fatal("expected connected=true")
	}
	if fw1.ActualPort != 8080 {
		t.Fatalf("expected actual port 8080, got %d", fw1.ActualPort)
	}

	fw2 := resp.Forwards[1]
	if fw2.Service.Pod != "api-pod" {
		t.Fatalf("expected pod 'api-pod', got %s", fw2.Service.Pod)
	}
	if fw2.State != kubeportv1.ForwardState_FORWARD_STATE_STARTING {
		t.Fatalf("expected STARTING state, got %v", fw2.State)
	}
	if fw2.ActualPort != 49152 {
		t.Fatalf("expected actual port 49152, got %d", fw2.ActualPort)
	}
}

func TestServer_StatusWithError(t *testing.T) {
	mgr := &mockSupervisor{
		statuses: []proxy.ForwardStatus{
			{
				Service: config.ServiceConfig{
					Name:       "broken",
					Service:    "svc",
					LocalPort:  8080,
					RemotePort: 80,
				},
				State: proxy.StateFailed,
				Error: fmt.Errorf("port 8080 already in use"),
			},
		},
	}

	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.Status(context.Background(), &kubeportv1.StatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Forwards[0].Error != "port 8080 already in use" {
		t.Fatalf("expected error string, got %s", resp.Forwards[0].Error)
	}
	if resp.Forwards[0].State != kubeportv1.ForwardState_FORWARD_STATE_FAILED {
		t.Fatalf("expected FAILED state")
	}
}

func TestServer_StatusWithNextRetry(t *testing.T) {
	retryTime := time.Now().Add(10 * time.Second)
	mgr := &mockSupervisor{
		statuses: []proxy.ForwardStatus{
			{
				Service: config.ServiceConfig{
					Name:       "db",
					Service:    "postgres",
					LocalPort:  5432,
					RemotePort: 5432,
				},
				State:     proxy.StateFailed,
				Error:     fmt.Errorf("connection reset"),
				Restarts:  3,
				NextRetry: retryTime,
			},
		},
	}

	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.Status(context.Background(), &kubeportv1.StatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fw := resp.Forwards[0]
	if fw.NextRetry == nil {
		t.Fatal("expected NextRetry to be set")
	}
	got := fw.NextRetry.AsTime()
	if got.Sub(retryTime).Abs() > time.Second {
		t.Fatalf("NextRetry mismatch: got %v, want ~%v", got, retryTime)
	}
}

func TestServer_StatusNoNextRetryWhenZero(t *testing.T) {
	mgr := &mockSupervisor{
		statuses: []proxy.ForwardStatus{
			{
				Service: config.ServiceConfig{
					Name:       "web",
					Service:    "nginx",
					LocalPort:  8080,
					RemotePort: 80,
				},
				State: proxy.StateRunning,
				// NextRetry is zero (not reconnecting)
			},
		},
	}

	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.Status(context.Background(), &kubeportv1.StatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Forwards[0].NextRetry != nil {
		t.Fatalf("expected nil NextRetry for running service, got %v", resp.Forwards[0].NextRetry)
	}
}

func TestServer_Stop(t *testing.T) {
	mgr := &mockSupervisor{}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.Stop(context.Background(), &kubeportv1.StopRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}

	// Stop is deferred to a goroutine with 100ms delay
	time.Sleep(200 * time.Millisecond)
	if got := mgr.stopCalls.Load(); got != 1 {
		t.Fatalf("expected 1 stop call, got %d", got)
	}
}

func TestConvertState(t *testing.T) {
	tests := []struct {
		input proxy.ForwardState
		want  kubeportv1.ForwardState
	}{
		{proxy.StateStarting, kubeportv1.ForwardState_FORWARD_STATE_STARTING},
		{proxy.StateRunning, kubeportv1.ForwardState_FORWARD_STATE_RUNNING},
		{proxy.StateFailed, kubeportv1.ForwardState_FORWARD_STATE_FAILED},
		{proxy.StateStopped, kubeportv1.ForwardState_FORWARD_STATE_STOPPED},
		{proxy.ForwardState(99), kubeportv1.ForwardState_FORWARD_STATE_UNSPECIFIED},
	}

	for _, tt := range tests {
		if got := convertState(tt.input); got != tt.want {
			t.Errorf("convertState(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestServer_GRPCIntegration(t *testing.T) {
	// Use /tmp directly to avoid macOS long temp path exceeding Unix socket max (104 chars)
	socketPath := filepath.Join("/tmp", fmt.Sprintf("kp-grpc-test-%d.sock", os.Getpid()))
	os.Remove(socketPath)
	t.Cleanup(func() { os.Remove(socketPath) })

	mgr := &mockSupervisor{
		statuses: []proxy.ForwardStatus{
			{
				Service: config.ServiceConfig{
					Name:       "web",
					Service:    "web-svc",
					LocalPort:  8080,
					RemotePort: 80,
				},
				State:      proxy.StateRunning,
				Connected:  true,
				ActualPort: 8080,
			},
		},
	}

	cfg := &config.Config{
		Context:   "integration-ctx",
		Namespace: "integration-ns",
	}

	// Create and start server
	srv := NewServer(mgr, cfg)
	srv.socketPath = socketPath

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	// Wait for socket to appear
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Connect as client
	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer conn.Close()

	client := kubeportv1.NewDaemonServiceClient(conn)

	// Test Status RPC
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := client.Status(ctx, &kubeportv1.StatusRequest{})
	if err != nil {
		t.Fatalf("Status RPC error: %v", err)
	}

	if resp.Context != "integration-ctx" {
		t.Fatalf("expected context 'integration-ctx', got %s", resp.Context)
	}
	if len(resp.Forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(resp.Forwards))
	}
	if resp.Forwards[0].Service.Name != "web" {
		t.Fatalf("expected service 'web', got %s", resp.Forwards[0].Service.Name)
	}

	// Test Stop RPC
	stopResp, err := client.Stop(ctx, &kubeportv1.StopRequest{})
	if err != nil {
		t.Fatalf("Stop RPC error: %v", err)
	}
	if !stopResp.Success {
		t.Fatal("expected Stop success=true")
	}

	// Cleanup
	srv.Shutdown()

	// Verify socket is removed
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatal("socket should be removed after shutdown")
	}
}

func TestCleanStaleSocket_NoFile(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "nonexistent.sock")

	if err := CleanStaleSocket(socketPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCleanStaleSocket_StaleFile(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "stale.sock")

	// Create a regular file pretending to be a stale socket
	if err := os.WriteFile(socketPath, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := CleanStaleSocket(socketPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatal("stale socket should be removed")
	}
}

func TestCleanStaleSocket_ActiveSocket(t *testing.T) {
	// Use /tmp directly to avoid macOS long temp path exceeding Unix socket max (104 chars)
	socketPath := filepath.Join("/tmp", fmt.Sprintf("kp-test-%d.sock", os.Getpid()))
	os.Remove(socketPath)
	t.Cleanup(func() { os.Remove(socketPath) })

	// Create a real listening socket
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	// Should detect active daemon
	err = CleanStaleSocket(socketPath)
	if err == nil {
		t.Fatal("expected error for active socket")
	}
}

func TestServer_AddService(t *testing.T) {
	mgr := &mockSupervisor{}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.AddService(context.Background(), &kubeportv1.AddServiceRequest{
		Service: &kubeportv1.ServiceInfo{
			Name:       "web",
			Service:    "nginx",
			RemotePort: 80,
			LocalPort:  8080,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if mgr.lastAddSvc.Name != "web" {
		t.Fatalf("expected service name 'web', got %s", mgr.lastAddSvc.Name)
	}
	if mgr.lastAddSvc.Service != "nginx" {
		t.Fatalf("expected target 'nginx', got %s", mgr.lastAddSvc.Service)
	}
}

func TestServer_AddService_NilService(t *testing.T) {
	mgr := &mockSupervisor{}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.AddService(context.Background(), &kubeportv1.AddServiceRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for nil service")
	}
	if resp.Error != "service is required" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestServer_AddService_Error(t *testing.T) {
	mgr := &mockSupervisor{addErr: fmt.Errorf("service %q: %w", "web", config.ErrServiceExists)}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.AddService(context.Background(), &kubeportv1.AddServiceRequest{
		Service: &kubeportv1.ServiceInfo{
			Name:       "web",
			Service:    "nginx",
			RemotePort: 80,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure")
	}
	if resp.Error == "" {
		t.Fatal("expected error string")
	}
}

func TestServer_RemoveService(t *testing.T) {
	mgr := &mockSupervisor{}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.RemoveService(context.Background(), &kubeportv1.RemoveServiceRequest{
		Name: "web",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if mgr.lastRemove != "web" {
		t.Fatalf("expected remove 'web', got %s", mgr.lastRemove)
	}
}

func TestServer_RemoveService_EmptyName(t *testing.T) {
	mgr := &mockSupervisor{}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.RemoveService(context.Background(), &kubeportv1.RemoveServiceRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for empty name")
	}
}

func TestServer_RemoveService_Error(t *testing.T) {
	mgr := &mockSupervisor{removeErr: fmt.Errorf("service %q: %w", "web", config.ErrServiceNotFound)}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.RemoveService(context.Background(), &kubeportv1.RemoveServiceRequest{
		Name: "web",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure")
	}
}

func TestServer_Reload(t *testing.T) {
	mgr := &mockSupervisor{reloadAdded: 2, reloadRemoved: 1}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	// No config file path => error
	resp, err := srv.Reload(context.Background(), &kubeportv1.ReloadRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for CLI-only mode")
	}
	if resp.Error == "" {
		t.Fatal("expected error message")
	}
}

func TestServer_Apply(t *testing.T) {
	mgr := &mockSupervisor{applyAdded: 2, applySkipped: 1, applyWarnings: []string{"db: service already exists"}}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.Apply(context.Background(), &kubeportv1.ApplyRequest{
		Services: []*kubeportv1.ServiceInfo{
			{Name: "web", Service: "nginx", RemotePort: 80, LocalPort: 8080},
			{Name: "api", Service: "api-svc", RemotePort: 3000, LocalPort: 9090},
			{Name: "db", Service: "postgres", RemotePort: 5432, LocalPort: 5432},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if resp.Added != 2 {
		t.Fatalf("expected 2 added, got %d", resp.Added)
	}
	if resp.Skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", resp.Skipped)
	}
	if len(resp.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(resp.Warnings))
	}
}

func TestServer_Apply_EmptyServices(t *testing.T) {
	mgr := &mockSupervisor{}
	cfg := &config.Config{Context: "ctx", Namespace: "ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.Apply(context.Background(), &kubeportv1.ApplyRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for empty services")
	}
	if resp.Error == "" {
		t.Fatal("expected error message")
	}
}

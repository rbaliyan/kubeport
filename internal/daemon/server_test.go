package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	statuses  []proxy.ForwardStatus
	stopCalls int
}

func (m *mockSupervisor) Status() []proxy.ForwardStatus {
	return m.statuses
}

func (m *mockSupervisor) Stop() {
	m.stopCalls++
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
	if mgr.stopCalls != 1 {
		t.Fatalf("expected 1 stop call, got %d", mgr.stopCalls)
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

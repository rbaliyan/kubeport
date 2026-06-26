package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"github.com/rbaliyan/kubeport/pkg/config"
	"github.com/rbaliyan/kubeport/pkg/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// waitForSocket polls until the Unix socket file exists or the deadline passes.
func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %q never appeared", path)
}

// TestSmoke_DaemonStatusRoundTrip stands up a real daemon server on a Unix
// socket and confirms a Status RPC round-trips the manager state back to a real
// gRPC client.
func TestSmoke_DaemonStatusRoundTrip(t *testing.T) {
	// /tmp keeps the socket path under the 104-char Unix limit on macOS.
	socketPath := filepath.Join("/tmp", fmt.Sprintf("kp-smoke-%d.sock", os.Getpid()))
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })

	mgr := &mockSupervisor{
		statuses: []proxy.ForwardStatus{
			{
				Service: config.ServiceConfig{Name: "web", Service: "nginx", LocalPort: 8080, RemotePort: 80},
				State:   proxy.StateRunning,
			},
		},
	}
	cfg := &config.Config{Context: "smoke-ctx", Namespace: "smoke-ns"}

	srv := NewServer(mgr, cfg)
	srv.listenCfg = config.ListenConfig{Mode: config.ListenUnix, Address: socketPath}
	go func() { _ = srv.Start() }()
	t.Cleanup(srv.Shutdown)

	waitForSocket(t, socketPath)

	conn, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := kubeportv1.NewDaemonServiceClient(conn).Status(ctx, &kubeportv1.StatusRequest{})
	if err != nil {
		t.Fatalf("Status RPC: %v", err)
	}
	if resp.Context != "smoke-ctx" {
		t.Fatalf("Context = %q, want smoke-ctx", resp.Context)
	}
	if len(resp.Forwards) != 1 || resp.Forwards[0].Service.Name != "web" {
		t.Fatalf("unexpected forwards: %+v", resp.Forwards)
	}
}

// TestSmoke_GRPCAuthRejectsBadKey stands up a TCP+TLS daemon guarded by an API
// key and confirms a wrong key is rejected with codes.Unauthenticated.
func TestSmoke_GRPCAuthRejectsBadKey(t *testing.T) {
	const apiKey = "smoke-secret"

	tmpDir := t.TempDir()
	t.Cleanup(func() {
		os.Remove(filepath.Join(tmpDir, ".kubeport-tls.crt"))
		os.Remove(filepath.Join(tmpDir, ".kubeport-tls.key"))
	})

	mgr := &mockSupervisor{}
	cfg := config.NewInMemory("smoke-ctx", "smoke-ns", nil)
	cfg.APIKey = apiKey
	// Persist so cfg.FilePath() is non-empty and the self-signed cert lands in tmpDir.
	_ = cfg.SaveTo(filepath.Join(tmpDir, "kubeport.yaml"), config.FormatYAML)

	// Reserve an ephemeral port, then release it for the server to bind.
	tmpLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := tmpLis.Addr().String()
	_ = tmpLis.Close()

	srv := NewServer(mgr, cfg)
	srv.listenCfg = config.ListenConfig{Mode: config.ListenTCP, Address: addr}
	go func() { _ = srv.Start() }()
	t.Cleanup(srv.Shutdown)

	tlsCreds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // test; self-signed cert
		MinVersion:         tls.VersionTLS12,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wrongConn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithUnaryInterceptor(grpcauth.ClientInterceptor("wrong-key")),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = wrongConn.Close() })

	_, err = kubeportv1.NewDaemonServiceClient(wrongConn).Status(ctx, &kubeportv1.StatusRequest{})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("wrong key: expected Unauthenticated, got %v", err)
	}
}

// TestSmoke_GRPCAuthAcceptsGoodKey is the positive counterpart to the bad-key
// test: a TCP+TLS daemon guarded by an API key accepts a request bearing the
// correct key and returns a successful Status response.
func TestSmoke_GRPCAuthAcceptsGoodKey(t *testing.T) {
	const apiKey = "smoke-secret"

	tmpDir := t.TempDir()
	t.Cleanup(func() {
		os.Remove(filepath.Join(tmpDir, ".kubeport-tls.crt"))
		os.Remove(filepath.Join(tmpDir, ".kubeport-tls.key"))
	})

	mgr := &mockSupervisor{}
	cfg := config.NewInMemory("smoke-ctx", "smoke-ns", nil)
	cfg.APIKey = apiKey
	// Persist so cfg.FilePath() is non-empty and the self-signed cert lands in tmpDir.
	_ = cfg.SaveTo(filepath.Join(tmpDir, "kubeport.yaml"), config.FormatYAML)

	// Reserve an ephemeral port, then release it for the server to bind.
	tmpLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := tmpLis.Addr().String()
	_ = tmpLis.Close()

	srv := NewServer(mgr, cfg)
	srv.listenCfg = config.ListenConfig{Mode: config.ListenTCP, Address: addr}
	go func() { _ = srv.Start() }()
	t.Cleanup(srv.Shutdown)

	tlsCreds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // test; self-signed cert
		MinVersion:         tls.VersionTLS12,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithUnaryInterceptor(grpcauth.ClientInterceptor(apiKey)),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	resp, err := kubeportv1.NewDaemonServiceClient(conn).Status(ctx, &kubeportv1.StatusRequest{})
	if err != nil {
		t.Fatalf("correct key: expected success, got %v", err)
	}
	if resp.Context != "smoke-ctx" {
		t.Fatalf("Context = %q, want smoke-ctx", resp.Context)
	}
}

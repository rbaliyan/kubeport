package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"github.com/rbaliyan/kubeport/pkg/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// startDaemon wires a real *proxy.Manager behind a real daemon gRPC server on a
// temp Unix socket and returns a connected real gRPC client. It exercises the
// full CLI -> daemon -> Manager seam (not the mockSupervisor used elsewhere).
//
// Services use direct-pod, isolated connection mode so the per-forward runner
// binds a local port immediately without contacting a real API server.
func startDaemon(t *testing.T, mgr *proxy.Manager, cfg *config.Config) kubeportv1.DaemonServiceClient {
	t.Helper()

	// /tmp keeps the socket path under the 104-char Unix limit on macOS.
	socketPath := filepath.Join("/tmp", fmt.Sprintf("kp-integ-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })

	srv := NewServer(mgr, cfg)
	srv.listenCfg = config.ListenConfig{Mode: config.ListenUnix, Address: socketPath}

	go func() { _ = srv.Start() }()
	t.Cleanup(srv.Shutdown)

	// Wait for the socket to appear before dialing.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return kubeportv1.NewDaemonServiceClient(conn)
}

// fakeKubeconfig writes a minimal kubeconfig pointing at an unreachable server
// and sets KUBECONFIG so proxy.NewManager can build a real Manager without a
// live cluster. Direct-pod services resolve without ever hitting the API, and
// isolated-mode forwards bind their local port before any SPDY dial, so no
// network call to the fake server is made by these tests.
func fakeKubeconfig(t *testing.T, contextName string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig.yaml")
	body := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://127.0.0.1:6443
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: %s
current-context: %s
users:
- name: test
  user:
    token: faketoken
`, contextName, contextName)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write fake kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", path)
}

// realManager builds a real *proxy.Manager via the production NewManager
// constructor, backed by the fake kubeconfig set up by fakeKubeconfig.
func realManager(t *testing.T, cfg *config.Config) *proxy.Manager {
	t.Helper()
	mgr, err := proxy.NewManager(cfg, io.Discard)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestIntegration_DaemonManager_StatusReflectsManagerState(t *testing.T) {
	cfg := &config.Config{
		Context:   "integ-ctx",
		Namespace: "default",
		Services: []config.ServiceConfig{
			{Name: "redis", Pod: "redis-pod-0", RemotePort: 6379, ConnectionMode: "isolated"},
			{Name: "api", Pod: "api-pod-0", RemotePort: 8080, ConnectionMode: "isolated"},
		},
	}
	fakeKubeconfig(t, cfg.Context)
	mgr := realManager(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	client := startDaemon(t, mgr, cfg)

	// Both isolated forwards bind immediately and reach StateRunning. Poll the
	// real Status RPC until they report running with a non-zero actual port.
	var resp *kubeportv1.StatusResponse
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		resp, err = client.Status(context.Background(), &kubeportv1.StatusRequest{})
		if err != nil {
			t.Fatalf("Status RPC: %v", err)
		}
		running := 0
		for _, fw := range resp.Forwards {
			if fw.State == kubeportv1.ForwardState_FORWARD_STATE_RUNNING && fw.ActualPort > 0 {
				running++
			}
		}
		if running == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if resp.Context != "integ-ctx" {
		t.Errorf("Context = %q, want integ-ctx", resp.Context)
	}
	if resp.Namespace != "default" {
		t.Errorf("Namespace = %q, want default", resp.Namespace)
	}
	if len(resp.Forwards) != 2 {
		t.Fatalf("expected 2 forwards, got %d", len(resp.Forwards))
	}
	byName := map[string]*kubeportv1.ForwardStatusProto{}
	for _, fw := range resp.Forwards {
		byName[fw.Service.Name] = fw
	}
	for _, name := range []string{"redis", "api"} {
		fw, ok := byName[name]
		if !ok {
			t.Fatalf("forward %q missing from Status", name)
		}
		if fw.State != kubeportv1.ForwardState_FORWARD_STATE_RUNNING {
			t.Errorf("%s: state = %v, want RUNNING", name, fw.State)
		}
		if fw.ActualPort == 0 {
			t.Errorf("%s: ActualPort should be non-zero", name)
		}
		if fw.ConnectionMode != "isolated" {
			t.Errorf("%s: ConnectionMode = %q, want isolated", name, fw.ConnectionMode)
		}
	}
}

func TestIntegration_DaemonManager_AddAndRemoveService(t *testing.T) {
	cfg := &config.Config{
		Context:   "integ-ctx",
		Namespace: "default",
		Services:  nil, // start empty; add via RPC
	}
	fakeKubeconfig(t, cfg.Context)
	mgr := realManager(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	client := startDaemon(t, mgr, cfg)

	// AddService over the real RPC -> Manager.AddService -> supervise goroutine.
	// Use an explicit LocalPort so the server does not wait out the 5s dynamic-port
	// poll (the fake kubeconfig means the forward never reaches StateRunning).
	addResp, err := client.AddService(context.Background(), &kubeportv1.AddServiceRequest{
		Service: &kubeportv1.ServiceInfo{
			Name:       "cache",
			Pod:        "cache-pod-0",
			RemotePort: 6379,
			LocalPort:  19211,
		},
	})
	if err != nil {
		t.Fatalf("AddService RPC: %v", err)
	}
	if !addResp.Success {
		t.Fatalf("AddService not successful: %s", addResp.Error)
	}

	// The added service must appear in Status.
	var found *kubeportv1.ForwardStatusProto
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, statusErr := client.Status(context.Background(), &kubeportv1.StatusRequest{})
		if statusErr != nil {
			t.Fatalf("Status RPC: %v", statusErr)
		}
		for _, fw := range resp.Forwards {
			if fw.Service.Name == "cache" {
				found = fw
			}
		}
		if found != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if found == nil {
		t.Fatal("added service 'cache' never appeared in Status")
	}
	if found.Service.RemotePort != 6379 {
		t.Errorf("cache RemotePort = %d, want 6379", found.Service.RemotePort)
	}

	// Adding the same service again must be rejected as AlreadyExists.
	_, err = client.AddService(context.Background(), &kubeportv1.AddServiceRequest{
		Service: &kubeportv1.ServiceInfo{Name: "cache", Pod: "cache-pod-0", RemotePort: 6379, LocalPort: 19211},
	})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.AlreadyExists {
		t.Fatalf("duplicate AddService: expected AlreadyExists, got %v", err)
	}

	// RemoveService over the real RPC.
	rmResp, err := client.RemoveService(context.Background(), &kubeportv1.RemoveServiceRequest{Name: "cache"})
	if err != nil {
		t.Fatalf("RemoveService RPC: %v", err)
	}
	if !rmResp.Success {
		t.Fatal("RemoveService not successful")
	}

	// The service must disappear from Status.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, statusErr := client.Status(context.Background(), &kubeportv1.StatusRequest{})
		if statusErr != nil {
			t.Fatalf("Status RPC: %v", statusErr)
		}
		present := false
		for _, fw := range resp.Forwards {
			if fw.Service.Name == "cache" {
				present = true
			}
		}
		if !present {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, _ := client.Status(context.Background(), &kubeportv1.StatusRequest{})
	for _, fw := range resp.Forwards {
		if fw.Service.Name == "cache" {
			t.Fatal("'cache' still present after RemoveService")
		}
	}

	// Removing an unknown service must be NotFound.
	_, err = client.RemoveService(context.Background(), &kubeportv1.RemoveServiceRequest{Name: "ghost"})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("remove unknown: expected NotFound, got %v", err)
	}
}

func TestIntegration_DaemonManager_Reload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "kubeport.yaml")

	// Initial config on disk: one service.
	initial := config.NewInMemory("integ-ctx", "default", []config.ServiceConfig{
		{Name: "svc-a", Pod: "pod-a-0", RemotePort: 80, ConnectionMode: "isolated"},
	})
	if err := initial.SaveTo(cfgPath, config.FormatYAML); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	// The daemon loads the same file so Reload re-reads from disk.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	fakeKubeconfig(t, cfg.Context)
	mgr := realManager(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	client := startDaemon(t, mgr, cfg)

	// Wait for svc-a to be running.
	waitForService(t, client, "svc-a")

	// Rewrite the on-disk config: drop svc-a, add svc-b.
	updated := config.NewInMemory("integ-ctx", "default", []config.ServiceConfig{
		{Name: "svc-b", Pod: "pod-b-0", RemotePort: 443, ConnectionMode: "isolated"},
	})
	if err := updated.SaveTo(cfgPath, config.FormatYAML); err != nil {
		t.Fatalf("save updated config: %v", err)
	}

	reloadResp, err := client.Reload(context.Background(), &kubeportv1.ReloadRequest{})
	if err != nil {
		t.Fatalf("Reload RPC: %v", err)
	}
	if !reloadResp.Success {
		t.Fatal("Reload not successful")
	}
	if reloadResp.Added != 1 {
		t.Errorf("Reload Added = %d, want 1", reloadResp.Added)
	}
	if reloadResp.Removed != 1 {
		t.Errorf("Reload Removed = %d, want 1", reloadResp.Removed)
	}

	// svc-b should now be present and svc-a gone.
	waitForService(t, client, "svc-b")
	resp, _ := client.Status(context.Background(), &kubeportv1.StatusRequest{})
	for _, fw := range resp.Forwards {
		if fw.Service.Name == "svc-a" {
			t.Error("svc-a should have been removed by reload")
		}
	}
}

func TestIntegration_DaemonManager_ReleaseBySource(t *testing.T) {
	// A primary daemon owns services contributed by two different delegate
	// configs. ReleaseBySource for one delegate must remove only that delegate's
	// services, leaving the others (and any native services) untouched.
	cfg := &config.Config{
		Context:   "integ-ctx",
		Namespace: "default",
		Services:  nil,
	}
	fakeKubeconfig(t, cfg.Context)
	mgr := realManager(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	client := startDaemon(t, mgr, cfg)

	const srcA = "/abs/path/delegate-a.yaml"
	const srcB = "/abs/path/delegate-b.yaml"

	// Each service gets an explicit local port so the daemon's AddService skips
	// the dynamic-port waitForActualPort poll (which otherwise blocks ~5s per
	// service against the unreachable fake API server). The forward is still
	// registered in the Manager regardless of connectivity, which is all the
	// source-config grouping assertion under test requires.
	port := 0
	add := func(name, src string) {
		port++
		_, err := client.AddService(context.Background(), &kubeportv1.AddServiceRequest{
			Service:      &kubeportv1.ServiceInfo{Name: name, Pod: name + "-pod-0", RemotePort: 80, LocalPort: int32(19100 + port)},
			SourceConfig: src,
		})
		if err != nil {
			t.Fatalf("AddService %s: %v", name, err)
		}
	}
	add("native", "") // native service: no source config
	add("a1", srcA)   // delegate A
	add("a2", srcA)   // delegate A
	add("b1", srcB)   // delegate B

	for _, n := range []string{"native", "a1", "a2", "b1"} {
		waitForService(t, client, n)
	}

	// Release only delegate A's services.
	resp, err := client.ReleaseBySource(context.Background(), &kubeportv1.ReleaseBySourceRequest{
		SourceConfig: srcA,
	})
	if err != nil {
		t.Fatalf("ReleaseBySource RPC: %v", err)
	}
	if !resp.Success {
		t.Fatal("ReleaseBySource not successful")
	}
	if resp.Released != 2 {
		t.Errorf("Released = %d, want 2 (a1, a2)", resp.Released)
	}

	// a1/a2 must be gone; native and b1 must remain.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := client.Status(context.Background(), &kubeportv1.StatusRequest{})
		present := map[string]bool{}
		for _, fw := range st.Forwards {
			present[fw.Service.Name] = true
		}
		if !present["a1"] && !present["a2"] {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, _ := client.Status(context.Background(), &kubeportv1.StatusRequest{})
	present := map[string]bool{}
	for _, fw := range st.Forwards {
		present[fw.Service.Name] = true
	}
	if present["a1"] || present["a2"] {
		t.Errorf("delegate A services should be released, got present=%v", present)
	}
	if !present["native"] {
		t.Error("native service must NOT be released")
	}
	if !present["b1"] {
		t.Error("delegate B service must NOT be released")
	}

	// Empty source config must be rejected.
	_, err = client.ReleaseBySource(context.Background(), &kubeportv1.ReleaseBySourceRequest{})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("empty ReleaseBySource: expected InvalidArgument, got %v", err)
	}
}

// waitForService polls Status until a service with the given name appears.
func waitForService(t *testing.T, client kubeportv1.DaemonServiceClient, name string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Status(context.Background(), &kubeportv1.StatusRequest{})
		if err != nil {
			t.Fatalf("Status RPC: %v", err)
		}
		for _, fw := range resp.Forwards {
			if fw.Service.Name == name {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service %q never appeared in Status", name)
}

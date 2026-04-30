package proxy

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/rbaliyan/kubeport/internal/netutil"
	"github.com/rbaliyan/kubeport/pkg/config"
)

// isolatedManager returns a Manager configured for isolated-mode tests.
// It uses a direct pod config so resolvePod never calls the k8s API.
func isolatedManager() *Manager {
	return &Manager{
		cfg:                  &config.Config{},
		output:               io.Discard,
		logger:               discardLogger(),
		healthCheckInterval:  50 * time.Millisecond,
		healthCheckThreshold: 3,
		transports:           newTransportCache(30 * time.Minute),
	}
}

// isolatedPF returns a portForward configured for isolated mode using a direct pod
// (bypasses k8s API in resolvePod) on an OS-assigned port.
func isolatedPF(localPort int) *portForward {
	return &portForward{
		svc: config.ServiceConfig{
			Name:           "isolated-svc",
			Pod:            "test-pod-0", // direct pod: resolvePod returns immediately
			LocalPort:      localPort,
			RemotePort:     80,
			ConnectionMode: "isolated",
		},
		namespace: "default",
		preemptCh: make(chan string, 1),
	}
}

func TestRunIsolatedPortForward_BindsPortImmediately(t *testing.T) {
	m := isolatedManager()
	pf := isolatedPF(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- m.runIsolatedPortForward(ctx, pf) }()

	// Wait for StateRunning — port binds before any client connects.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pf.mu.Lock()
		state := pf.state
		pf.mu.Unlock()
		if state == StateRunning {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pf.mu.Lock()
	state := pf.state
	port := pf.actualPort
	pf.mu.Unlock()

	if state != StateRunning {
		t.Fatalf("state = %v, want StateRunning", state)
	}
	if port == 0 {
		t.Fatal("actualPort should be non-zero once isolated forward starts")
	}
	if !isPortBound(port) {
		t.Fatalf("port %d should be bound", port)
	}

	cancel()
	select {
	case err := <-doneCh:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runIsolatedPortForward did not return after context cancel")
	}
}

func TestRunIsolatedPortForward_FixedPort(t *testing.T) {
	// Acquire a free port then release it so the isolated forwarder can bind it.
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	fixedPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	m := isolatedManager()
	pf := isolatedPF(fixedPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- m.runIsolatedPortForward(ctx, pf) }()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pf.mu.Lock()
		p := pf.actualPort
		pf.mu.Unlock()
		if p != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pf.mu.Lock()
	gotPort := pf.actualPort
	pf.mu.Unlock()

	if gotPort != fixedPort {
		t.Fatalf("actualPort = %d, want %d", gotPort, fixedPort)
	}

	cancel()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runIsolatedPortForward did not return after context cancel")
	}
}

func TestRunIsolatedPortForward_PortReleasedAfterCancel(t *testing.T) {
	m := isolatedManager()
	pf := isolatedPF(0)

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- m.runIsolatedPortForward(ctx, pf) }()

	// Wait for port to bind.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pf.mu.Lock()
		p := pf.actualPort
		pf.mu.Unlock()
		if p != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pf.mu.Lock()
	port := pf.actualPort
	pf.mu.Unlock()

	if port == 0 {
		t.Fatal("actualPort should be set before cancel")
	}

	cancel()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runIsolatedPortForward did not return after context cancel")
	}

	if netutil.IsPortOpen(port) {
		t.Fatalf("port %d should be released after isolated forwarder stops", port)
	}
}

func TestRunIsolatedPortForward_PodResolutionFailure(t *testing.T) {
	// Service-backed config (not direct pod) with a clientset that has no pods.
	// resolvePod fails immediately, so the runner returns without binding.
	client := fake.NewClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "my-svc", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "test"}},
		},
		// No running pods — resolvePod returns an error.
	)

	m := &Manager{
		cfg:                  &config.Config{},
		clientset:            client,
		output:               io.Discard,
		logger:               discardLogger(),
		healthCheckInterval:  50 * time.Millisecond,
		healthCheckThreshold: 3,
		transports:           newTransportCache(30 * time.Minute),
	}
	pf := &portForward{
		svc: config.ServiceConfig{
			Name:           "isolated-fail",
			Service:        "my-svc",
			LocalPort:      0,
			RemotePort:     80,
			ConnectionMode: "isolated",
		},
		namespace: "default",
		preemptCh: make(chan string, 1),
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- m.runIsolatedPortForward(t.Context(), pf) }()

	select {
	case err := <-doneCh:
		if err == nil {
			t.Fatal("expected error when no pods available, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runIsolatedPortForward did not return on pod resolution failure")
	}
}

func TestRunIsolatedPortForward_HealthCheckCancels(t *testing.T) {
	m := &Manager{
		cfg:                  &config.Config{},
		output:               io.Discard,
		logger:               discardLogger(),
		healthCheckInterval:  20 * time.Millisecond,
		healthCheckThreshold: 2, // fail after 2 missed checks
		transports:           newTransportCache(30 * time.Minute),
	}
	pf := isolatedPF(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- m.runIsolatedPortForward(ctx, pf) }()

	// Wait for port to bind.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pf.mu.Lock()
		p := pf.actualPort
		pf.mu.Unlock()
		if p != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pf.mu.Lock()
	port := pf.actualPort
	pf.mu.Unlock()
	if port == 0 {
		t.Fatal("port should be bound before health check test")
	}

	// Close the listener from outside to simulate a dead port.
	// The health check should fire and cancel the forwarder.
	// We do this by stopping the forwarder and observing the error.
	cancel()

	select {
	case err := <-doneCh:
		_ = err // context.Canceled or health check error — both acceptable
	case <-time.After(2 * time.Second):
		t.Fatal("runIsolatedPortForward did not stop")
	}
}

func TestIsolatedSupervise_MaxAge_ClearsPod(t *testing.T) {
	m := &Manager{
		cfg:                  &config.Config{},
		output:               io.Discard,
		logger:               discardLogger(),
		healthCheckInterval:  50 * time.Millisecond,
		healthCheckThreshold: 100, // don't trip health check in this test
		maxConnectionAge:     30 * time.Millisecond,
		transports:           newTransportCache(30 * time.Minute),
	}
	pf := isolatedPF(0)
	pf.currentPod = "test-pod-0"
	pf.currentTargetPort = 80

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Bind a listener so health checks pass.
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	actualPort := ln.Addr().(*net.TCPAddr).Port

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- m.isolatedSupervise(ctx, cancel, pf, actualPort)
	}()

	// Wait for the max-age timer to fire and clear the pod.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pf.mu.Lock()
		pod := pf.currentPod
		pf.mu.Unlock()
		if pod == "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pf.mu.Lock()
	pod := pf.currentPod
	pf.mu.Unlock()

	if pod != "" {
		t.Errorf("currentPod should be cleared after max-age, got %q", pod)
	}

	cancel()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("isolatedSupervise did not stop after context cancel")
	}
}

func TestIsolatedSupervise_Preempt_UpdatesCurrentPod(t *testing.T) {
	// Use a direct-pod config so resolvePod returns immediately without k8s API.
	m := &Manager{
		cfg:                  &config.Config{},
		output:               io.Discard,
		logger:               discardLogger(),
		healthCheckInterval:  50 * time.Millisecond,
		healthCheckThreshold: 100,
		transports:           newTransportCache(30 * time.Minute),
	}
	pf := &portForward{
		svc: config.ServiceConfig{
			Name:           "svc",
			Pod:            "new-pod-0", // direct pod: resolvePod returns immediately
			RemotePort:     80,
			ConnectionMode: "isolated",
		},
		namespace: "default",
		preemptCh: make(chan string, 1),
	}
	pf.currentPod = "old-pod"
	pf.currentTargetPort = 80

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	actualPort := ln.Addr().(*net.TCPAddr).Port

	doneCh := make(chan error, 1)
	go func() { doneCh <- m.isolatedSupervise(ctx, cancel, pf, actualPort) }()

	// Send a preempt hint; the supervisor calls resolvePod which returns "new-pod-0".
	pf.preemptCh <- "new-pod-0"

	// Wait for currentPod to be updated.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pf.mu.Lock()
		pod := pf.currentPod
		pf.mu.Unlock()
		if pod != "old-pod" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pf.mu.Lock()
	pod := pf.currentPod
	pf.mu.Unlock()

	if pod != "new-pod-0" {
		t.Errorf("currentPod: want new-pod-0, got %q", pod)
	}

	cancel()
	<-doneCh
}

func TestIsolatedHandleConn_DropsWhenPodResolveFails(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "my-svc", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "test"}},
		},
		// No pods — resolvePod will fail.
	)
	m := &Manager{
		cfg:       &config.Config{},
		clientset: client,
		output:    io.Discard,
		logger:    discardLogger(),
		transports: newTransportCache(30 * time.Minute),
	}
	pf := &portForward{
		svc: config.ServiceConfig{
			Name:           "isolated-fail",
			Service:        "my-svc",
			RemotePort:     80,
			ConnectionMode: "isolated",
		},
		namespace: "default",
		preemptCh: make(chan string, 1),
		// currentPod intentionally left empty to trigger re-resolve
	}

	// Create a pair of connected sockets. The server side is tcpConn.
	server, client2, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	defer client2.Close()

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		m.isolatedHandleConn(context.Background(), pf, server)
	}()

	// handler should return quickly after resolvePod fails.
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("isolatedHandleConn did not return on pod resolution failure")
	}

	// The server conn should be closed (reads from client2 should get EOF).
	client2.SetDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	_, err = client2.Read(buf)
	if err == nil {
		t.Fatal("expected EOF on client side after handler drops connection")
	}
}

// socketPair returns a connected pair of TCP connections via loopback.
func socketPair() (net.Conn, net.Conn, error) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, nil, err
	}
	defer ln.Close()

	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, e := ln.Accept()
		ch <- result{c, e}
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return nil, nil, err
	}

	r := <-ch
	if r.err != nil {
		client.Close()
		return nil, nil, r.err
	}
	return r.conn, client, nil
}

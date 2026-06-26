package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/streaming/pkg/httpstream"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// muxDialer is a fake httpstream.Dialer for the mux runner. Its Dial returns a
// fakeSPDYConn wired to an in-process echo backend, so portforward.ForwardPorts
// performs a real local bind and relays real bytes through the fake tunnel.
//
// failFirst, when >0, makes the first N Dial calls fail. This drives the
// supervisor's restart-on-failure backoff branch: ForwardPorts returns the
// dial error, superviseSingle records a restart and retries after backoff.
type muxDialer struct {
	backendAddr string
	dials       atomic.Int32
	failFirst   int32
	lastConn    atomic.Pointer[fakeSPDYConn]
}

func (d *muxDialer) Dial(protocols ...string) (httpstream.Connection, string, error) {
	n := d.dials.Add(1)
	if n <= d.failFirst {
		return nil, "", errDialRefused
	}
	conn := newFakeSPDYConn(d.backendAddr)
	d.lastConn.Store(conn)
	// The runner requires the dialer to negotiate exactly this protocol.
	proto := portforward.PortForwardProtocolV1Name
	if len(protocols) > 0 {
		proto = protocols[0]
	}
	return conn, proto, nil
}

// errDialRefused is returned by muxDialer when a dial is configured to fail.
var errDialRefused = errors.New("dial refused (fake)")

// muxTestManager builds a Manager whose mux runner dials the supplied fake. It
// targets a direct pod so resolvePod returns immediately without a real cluster.
//
// The clientset is a real client-go client built from a dummy rest.Config: it is
// used only to construct the port-forward request URL (a pure string build that
// never hits the network because the fake dialer intercepts the connection).
func muxTestManager(t *testing.T, factory dialerFactory) *Manager {
	t.Helper()
	restConfig := &rest.Config{Host: "https://fake.test:6443"}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	return &Manager{
		cfg:                  &config.Config{Namespace: "default"},
		restConfig:           restConfig,
		clientset:            clientset,
		forwards:             make(map[string]*portForward),
		children:             make(map[string][]string),
		externalForwards:     make(map[string]ExternalForward),
		output:               io.Discard,
		logger:               discardLogger(),
		healthCheckInterval:  50 * time.Millisecond,
		healthCheckThreshold: 3,
		readyTimeout:         2 * time.Second,
		backoffInitial:       10 * time.Millisecond,
		backoffMax:           50 * time.Millisecond,
		transports:           newTransportCache(30 * time.Minute),
		dialerFactory:        factory,
	}
}

// muxDirectPodPF returns a portForward targeting a pod directly so resolvePod
// returns without touching the k8s API.
func muxDirectPodPF(localPort int) *portForward {
	return &portForward{
		svc: config.ServiceConfig{
			Name:       "mux-svc",
			Pod:        "test-pod-0",
			LocalPort:  localPort,
			RemotePort: 80,
		},
		namespace: "default",
		preemptCh: make(chan string, 1),
	}
}

func TestMuxRunner_BindsAndRelays(t *testing.T) {
	backend := newEchoBackend(t)
	defer backend.close()

	dialer := &muxDialer{backendAddr: backend.addr()}
	m := muxTestManager(t, func(*rest.Config, *url.URL) (httpstream.Dialer, error) {
		return dialer, nil
	})
	pf := muxDirectPodPF(0)

	// Register the forward so Status() observes it (mirrors superviseSingle).
	m.mu.Lock()
	m.forwards[pf.svc.Name] = pf
	m.order = append(m.order, pf.svc.Name)
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- m.runPortForward(ctx, pf) }()

	// The mux runner binds a real local listener and reports the actual port.
	eventually(t, 2*time.Second, func() bool {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		return pf.state == StateRunning && pf.actualPort > 0
	})

	pf.mu.Lock()
	port := pf.actualPort
	pf.mu.Unlock()
	if !isPortBound(port) {
		t.Fatalf("port %d should be bound by mux runner", port)
	}

	// Connect a real TCP client and assert the payload echoes back through the
	// fake tunnel (client-go relays it via the fakeSPDYConn data stream).
	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatalf("dial localhost:%d: %v", port, err)
	}
	defer func() { _ = client.Close() }()

	payload := []byte("hello through the mux tunnel")
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
	got := make([]byte, len(payload))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo mismatch: got %q want %q", got, payload)
	}

	// Observability: the relayed bytes must surface through the Manager's Status
	// view (the same path the daemon's Status RPC reads), not just the raw
	// counters. A full echo round-trip moves len(payload) bytes each direction.
	want := int64(len(payload))
	eventually(t, 2*time.Second, func() bool {
		for _, s := range m.Status() {
			if s.Service.Name == "mux-svc" {
				return s.BytesOut >= want && s.BytesIn >= want
			}
		}
		return false
	})
	var muxStatus ForwardStatus
	for _, s := range m.Status() {
		if s.Service.Name == "mux-svc" {
			muxStatus = s
		}
	}
	if muxStatus.BytesOut < want {
		t.Fatalf("Status BytesOut = %d, want >= %d", muxStatus.BytesOut, want)
	}
	if muxStatus.BytesIn < want {
		t.Fatalf("Status BytesIn = %d, want >= %d", muxStatus.BytesIn, want)
	}
	if muxStatus.ConnectionMode != "mux" {
		t.Fatalf("Status ConnectionMode = %q, want mux", muxStatus.ConnectionMode)
	}

	cancel()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runPortForward did not return after context cancel")
	}
}

func TestMuxRunner_RestartsOnFailure(t *testing.T) {
	backend := newEchoBackend(t)
	defer backend.close()

	// Fail the first dial, succeed on the second. superviseSingle must record a
	// restart, back off, and then bind successfully.
	dialer := &muxDialer{backendAddr: backend.addr(), failFirst: 1}
	m := muxTestManager(t, func(*rest.Config, *url.URL) (httpstream.Dialer, error) {
		return dialer, nil
	})
	m.maxRestarts = 5
	pf := muxDirectPodPF(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Drive through superviseSingle so the restart/backoff loop is exercised.
	// superviseSingle no longer registers the forward; reserve it first (as the
	// serial command path does) so Status() and the assertions below observe it.
	pf.svc.Namespace = "default"
	m.cfg.Services = []config.ServiceConfig{pf.svc}
	reserved, reserveErr := m.reserveForward(pf.svc)
	if reserveErr != nil {
		t.Fatalf("reserveForward: %v", reserveErr)
	}

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		m.superviseSingle(ctx, reserved.svc, reserved)
	}()

	// After the first dial fails and the runner backs off, the second dial
	// succeeds and the forward reaches StateRunning with at least one restart.
	eventually(t, 3*time.Second, func() bool {
		m.mu.RLock()
		got := m.forwards["mux-svc"]
		m.mu.RUnlock()
		if got == nil {
			return false
		}
		got.mu.Lock()
		defer got.mu.Unlock()
		return got.state == StateRunning && got.restarts >= 1 && got.actualPort > 0
	})

	if d := dialer.dials.Load(); d < 2 {
		t.Fatalf("expected at least 2 dial attempts (1 fail + 1 success), got %d", d)
	}

	cancel()
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("superviseSingle did not return after context cancel")
	}
}

func TestMuxRunner_DialerFactoryError(t *testing.T) {
	// A factory error must surface from runPortForward unchanged so the
	// supervisor treats it as a connection failure.
	m := muxTestManager(t, func(*rest.Config, *url.URL) (httpstream.Dialer, error) {
		return nil, errDialRefused
	})
	pf := muxDirectPodPF(0)

	err := m.runPortForward(context.Background(), pf)
	if err == nil {
		t.Fatal("expected error from failing dialer factory, got nil")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestDefaultDialer_BuildsSPDYDialer(t *testing.T) {
	// The production default factory must build a non-nil dialer from a rest.Config
	// without performing any network I/O (it only constructs the transport).
	m := &Manager{transports: newTransportCache(time.Minute)}
	reqURL, err := url.Parse("https://fake.test:6443/api/v1/namespaces/default/pods/p/portforward")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	d, err := m.defaultDialer(&rest.Config{Host: "https://fake.test:6443"}, reqURL)
	if err != nil {
		t.Fatalf("defaultDialer: %v", err)
	}
	if d == nil {
		t.Fatal("defaultDialer returned a nil dialer")
	}
}

package proxy

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/rbaliyan/kubeport/internal/hook"
	"github.com/rbaliyan/kubeport/pkg/config"
)

// recordingHook captures every event it receives so tests can assert which
// lifecycle events fired and with what payload.
type recordingHook struct {
	mu     sync.Mutex
	events []hook.Event
}

func (h *recordingHook) Name() string { return "recording" }

func (h *recordingHook) OnEvent(_ context.Context, e hook.Event) error {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
	return nil
}

func (h *recordingHook) byType(t hook.EventType) []hook.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []hook.Event
	for _, e := range h.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

func (h *recordingHook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func TestHookIntegration_ConnectedFires(t *testing.T) {
	// Isolated direct-pod forward binds immediately and fires forward:connected.
	rec := &recordingHook{}
	disp := hook.NewDispatcher(discardLogger())
	disp.Register(hook.Registration{Hook: rec, FailMode: hook.FailOpen, Timeout: time.Second})

	m := &Manager{
		cfg:                  &config.Config{},
		output:               io.Discard,
		logger:               discardLogger(),
		hooks:                disp,
		healthCheckInterval:  50 * time.Millisecond,
		healthCheckThreshold: 100,
		transports:           newTransportCache(30 * time.Minute),
	}
	pf := isolatedPF(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- m.runIsolatedPortForward(ctx, pf) }()

	// Wait for forward:connected to fire.
	eventually(t, 2*time.Second, func() bool {
		return len(rec.byType(hook.EventForwardConnected)) > 0
	})
	disp.Wait()

	connected := rec.byType(hook.EventForwardConnected)
	if len(connected) == 0 {
		t.Fatal("expected forward:connected event")
	}
	ev := connected[0]
	if ev.Service != "isolated-svc" {
		t.Errorf("connected event Service = %q, want isolated-svc", ev.Service)
	}
	if ev.PodName != "test-pod-0" {
		t.Errorf("connected event PodName = %q, want test-pod-0", ev.PodName)
	}
	if ev.LocalPort == 0 {
		t.Error("connected event LocalPort should be non-zero")
	}

	cancel()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runIsolatedPortForward did not stop")
	}
}

func TestHookIntegration_DisconnectAndFailFire(t *testing.T) {
	// A service-backed forward whose service has no running pods fails every
	// attempt. With maxRestarts capped, the supervisor fires forward:disconnected
	// on each failure and forward:failed once it gives up.
	rec := &recordingHook{}
	disp := hook.NewDispatcher(discardLogger())
	disp.Register(hook.Registration{Hook: rec, FailMode: hook.FailOpen, Timeout: time.Second})

	client := fake.NewClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "ghost-svc", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "ghost"}},
		},
		// No pods: resolvePod fails every attempt.
	)

	m := &Manager{
		cfg: &config.Config{
			Context:   "test",
			Namespace: "default",
			Services: []config.ServiceConfig{
				{Name: "ghost", Service: "ghost-svc", LocalPort: 0, RemotePort: 80},
			},
		},
		clientset:            client,
		output:               io.Discard,
		logger:               discardLogger(),
		hooks:                disp,
		forwards:             make(map[string]*portForward),
		maxRestarts:          2,
		healthCheckInterval:  10 * time.Second,
		healthCheckThreshold: 3,
		readyTimeout:         time.Second,
		backoffInitial:       5 * time.Millisecond,
		backoffMax:           10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	svc := m.cfg.Services[0]
	pf, reserveErr := m.reserveForward(svc)
	if reserveErr != nil {
		t.Fatalf("reserveForward: %v", reserveErr)
	}
	m.supervise(ctx, svc, pf)
	disp.Wait()

	disconnects := rec.byType(hook.EventForwardDisconnected)
	if len(disconnects) == 0 {
		t.Fatal("expected at least one forward:disconnected event")
	}
	for _, ev := range disconnects {
		if ev.Service != "ghost" {
			t.Errorf("disconnect event Service = %q, want ghost", ev.Service)
		}
		if ev.Error == nil {
			t.Error("disconnect event should carry a non-nil Error")
		}
	}

	failed := rec.byType(hook.EventForwardFailed)
	if len(failed) != 1 {
		t.Fatalf("expected exactly one forward:failed event, got %d", len(failed))
	}
	if failed[0].Service != "ghost" {
		t.Errorf("failed event Service = %q, want ghost", failed[0].Service)
	}
	if failed[0].Error == nil {
		t.Error("failed event should carry a non-nil Error")
	}
}

// gateHook blocks EventManagerStarting until released, recording whether Gate ran.
type gateHook struct {
	mu       sync.Mutex
	gated    bool
	blockErr error
}

func (g *gateHook) Name() string { return "gate" }

func (g *gateHook) OnEvent(_ context.Context, _ hook.Event) error { return nil }

func (g *gateHook) Gate(_ context.Context, _ hook.Event) error {
	g.mu.Lock()
	g.gated = true
	g.mu.Unlock()
	return g.blockErr
}

func TestHookIntegration_GateClosedBlocksOperation(t *testing.T) {
	// A fail_mode: closed gate hook that returns an error must block the
	// triggering operation (Fire returns the error).
	disp := hook.NewDispatcher(discardLogger())
	gate := &gateHook{blockErr: errors.New("vpn not ready")}
	disp.Register(hook.Registration{
		Hook:     gate,
		Events:   []hook.EventType{hook.EventManagerStarting},
		FailMode: hook.FailClosed,
		Timeout:  time.Second,
	})

	err := disp.Fire(context.Background(), hook.Event{Type: hook.EventManagerStarting, Time: time.Now()})
	if err == nil {
		t.Fatal("expected gate to block operation with an error")
	}
	gate.mu.Lock()
	gated := gate.gated
	gate.mu.Unlock()
	if !gated {
		t.Fatal("gate hook should have run")
	}

	// A fail_mode: open gate must NOT block even when it errors.
	dispOpen := hook.NewDispatcher(discardLogger())
	gateOpen := &gateHook{blockErr: errors.New("soft failure")}
	dispOpen.Register(hook.Registration{
		Hook:     gateOpen,
		Events:   []hook.EventType{hook.EventManagerStarting},
		FailMode: hook.FailOpen,
		Timeout:  time.Second,
	})
	if err := dispOpen.Fire(context.Background(), hook.Event{Type: hook.EventManagerStarting}); err != nil {
		t.Fatalf("fail-open gate should not block, got %v", err)
	}
}

func TestHookIntegration_EventFilterScopesDelivery(t *testing.T) {
	// A hook registered for only forward:connected must not receive other events.
	rec := &recordingHook{}
	disp := hook.NewDispatcher(discardLogger())
	disp.Register(hook.Registration{
		Hook:     rec,
		Events:   []hook.EventType{hook.EventForwardConnected},
		FailMode: hook.FailOpen,
		Timeout:  time.Second,
	})

	_ = disp.Fire(context.Background(), hook.Event{Type: hook.EventForwardDisconnected, Service: "x"})
	_ = disp.Fire(context.Background(), hook.Event{Type: hook.EventForwardConnected, Service: "x"})
	disp.Wait()

	if got := len(rec.byType(hook.EventForwardConnected)); got != 1 {
		t.Errorf("expected 1 connected event, got %d", got)
	}
	if got := rec.count(); got != 1 {
		t.Errorf("filtered hook received %d events, want 1 (only connected)", got)
	}
}

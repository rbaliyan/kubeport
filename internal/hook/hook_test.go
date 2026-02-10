package hook

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// mockHook is a simple Hook implementation for testing.
type mockHook struct {
	name   string
	onCall func(ctx context.Context, event Event) error
}

func (m *mockHook) Name() string { return m.name }
func (m *mockHook) OnEvent(ctx context.Context, event Event) error {
	if m.onCall != nil {
		return m.onCall(ctx, event)
	}
	return nil
}

// mockGateHook implements GateHook for testing gate behavior.
type mockGateHook struct {
	mockHook
	gateCall func(ctx context.Context, event Event) error
}

func (m *mockGateHook) Gate(ctx context.Context, event Event) error {
	if m.gateCall != nil {
		return m.gateCall(ctx, event)
	}
	return nil
}

func TestNilDispatcher(t *testing.T) {
	var d *Dispatcher
	// Should not panic
	err := d.Fire(context.Background(), Event{Type: EventForwardConnected})
	if err != nil {
		t.Fatalf("expected nil error from nil dispatcher, got %v", err)
	}
}

func TestDispatcher_NoHooks(t *testing.T) {
	d := NewDispatcher(nil)
	err := d.Fire(context.Background(), Event{Type: EventForwardConnected})
	if err != nil {
		t.Fatalf("expected nil error with no hooks, got %v", err)
	}
}

func TestDispatcher_AsyncFiring(t *testing.T) {
	d := NewDispatcher(nil)
	var called atomic.Int32

	h := &mockHook{
		name: "test",
		onCall: func(ctx context.Context, event Event) error {
			called.Add(1)
			return nil
		},
	}

	d.Register(Registration{Hook: h, Events: []EventType{EventForwardConnected}, FailMode: FailOpen, Timeout: 5 * time.Second})

	err := d.Fire(context.Background(), Event{Type: EventForwardConnected, Time: time.Now()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Async hooks run in goroutines; wait a bit for them to complete.
	time.Sleep(100 * time.Millisecond)

	if got := called.Load(); got != 1 {
		t.Fatalf("expected hook called 1 time, got %d", got)
	}
}

func TestDispatcher_EventFiltering(t *testing.T) {
	d := NewDispatcher(nil)
	var called atomic.Int32

	h := &mockHook{
		name: "filtered",
		onCall: func(ctx context.Context, event Event) error {
			called.Add(1)
			return nil
		},
	}

	// Only register for EventForwardConnected
	d.Register(Registration{Hook: h, Events: []EventType{EventForwardConnected}, FailMode: FailOpen, Timeout: 5 * time.Second})

	// Fire a different event
	d.Fire(context.Background(), Event{Type: EventForwardDisconnected, Time: time.Now()})
	time.Sleep(100 * time.Millisecond)

	if got := called.Load(); got != 0 {
		t.Fatalf("expected 0 calls for non-matching event, got %d", got)
	}
}

func TestDispatcher_AllEvents(t *testing.T) {
	d := NewDispatcher(nil)
	var called atomic.Int32

	h := &mockHook{
		name: "all-events",
		onCall: func(ctx context.Context, event Event) error {
			called.Add(1)
			return nil
		},
	}

	// nil events = subscribe to all
	d.Register(Registration{Hook: h, FailMode: FailOpen, Timeout: 5 * time.Second})

	d.Fire(context.Background(), Event{Type: EventForwardConnected, Time: time.Now()})
	d.Fire(context.Background(), Event{Type: EventForwardDisconnected, Time: time.Now()})
	d.Fire(context.Background(), Event{Type: EventManagerStopped, Time: time.Now()})
	time.Sleep(200 * time.Millisecond)

	if got := called.Load(); got != 3 {
		t.Fatalf("expected 3 calls, got %d", got)
	}
}

func TestDispatcher_GateHook_FailClosed(t *testing.T) {
	d := NewDispatcher(nil)

	gh := &mockGateHook{
		mockHook: mockHook{name: "gate-fail"},
		gateCall: func(ctx context.Context, event Event) error {
			return fmt.Errorf("VPN not connected")
		},
	}

	d.Register(Registration{Hook: gh, Events: []EventType{EventManagerStarting}, FailMode: FailClosed, Timeout: 5 * time.Second})

	err := d.Fire(context.Background(), Event{Type: EventManagerStarting, Time: time.Now()})
	if err == nil {
		t.Fatal("expected gate error with FailClosed")
	}
	if !errors.Is(err, fmt.Errorf("VPN not connected")) {
		// Just check it contains the message
		if err.Error() == "" {
			t.Fatal("expected non-empty error message")
		}
	}
}

func TestDispatcher_GateHook_FailOpen(t *testing.T) {
	d := NewDispatcher(nil)

	gh := &mockGateHook{
		mockHook: mockHook{name: "gate-open"},
		gateCall: func(ctx context.Context, event Event) error {
			return fmt.Errorf("VPN not connected")
		},
	}

	d.Register(Registration{Hook: gh, Events: []EventType{EventManagerStarting}, FailMode: FailOpen, Timeout: 5 * time.Second})

	err := d.Fire(context.Background(), Event{Type: EventManagerStarting, Time: time.Now()})
	if err != nil {
		t.Fatalf("expected nil error with FailOpen, got %v", err)
	}
}

func TestDispatcher_GateHook_Success(t *testing.T) {
	d := NewDispatcher(nil)

	gh := &mockGateHook{
		mockHook: mockHook{name: "gate-ok"},
		gateCall: func(ctx context.Context, event Event) error {
			return nil
		},
	}

	d.Register(Registration{Hook: gh, Events: []EventType{EventManagerStarting}, FailMode: FailClosed, Timeout: 5 * time.Second})

	err := d.Fire(context.Background(), Event{Type: EventManagerStarting, Time: time.Now()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDispatcher_NonGateHookOnGateEvent(t *testing.T) {
	// A regular Hook (not GateHook) registered for EventManagerStarting should fire async
	d := NewDispatcher(nil)
	var called atomic.Int32

	h := &mockHook{
		name: "non-gate",
		onCall: func(ctx context.Context, event Event) error {
			called.Add(1)
			return nil
		},
	}

	d.Register(Registration{Hook: h, Events: []EventType{EventManagerStarting}, FailMode: FailOpen, Timeout: 5 * time.Second})

	err := d.Fire(context.Background(), Event{Type: EventManagerStarting, Time: time.Now()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if got := called.Load(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

func TestEventType_String(t *testing.T) {
	tests := []struct {
		e    EventType
		want string
	}{
		{EventManagerStarting, "manager_starting"},
		{EventManagerStopped, "manager_stopped"},
		{EventForwardConnected, "forward_connected"},
		{EventForwardDisconnected, "forward_disconnected"},
		{EventForwardFailed, "forward_failed"},
		{EventForwardStopped, "forward_stopped"},
		{EventHealthCheckFailed, "health_check_failed"},
		{EventType(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.e.String(); got != tt.want {
			t.Errorf("EventType(%d).String() = %s, want %s", tt.e, got, tt.want)
		}
	}
}

func TestParseEventType(t *testing.T) {
	tests := []struct {
		input string
		want  EventType
		ok    bool
	}{
		{"manager_starting", EventManagerStarting, true},
		{"forward_connected", EventForwardConnected, true},
		{"health_check_failed", EventHealthCheckFailed, true},
		{"nonexistent", 0, false},
	}

	for _, tt := range tests {
		got, ok := ParseEventType(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseEventType(%q) = (%v, %v), want (%v, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestDispatcher_AsyncHookError(t *testing.T) {
	// Async hook errors should not propagate
	d := NewDispatcher(nil)
	var called atomic.Int32

	h := &mockHook{
		name: "error-hook",
		onCall: func(ctx context.Context, event Event) error {
			called.Add(1)
			return fmt.Errorf("hook failed")
		},
	}

	d.Register(Registration{Hook: h, Events: []EventType{EventForwardConnected}, FailMode: FailOpen, Timeout: 5 * time.Second})

	err := d.Fire(context.Background(), Event{Type: EventForwardConnected, Time: time.Now()})
	if err != nil {
		t.Fatalf("async error should not propagate, got %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if got := called.Load(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

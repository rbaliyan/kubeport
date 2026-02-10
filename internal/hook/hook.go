// Package hook provides a lifecycle event system for kubeport.
//
// Hooks are notified of manager and port-forward events. They can be
// asynchronous (fire-and-forget) or synchronous gates that can block
// a lifecycle transition (e.g., start VPN before forwarding begins).
package hook

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// EventType identifies a lifecycle event in the port-forward supervisor.
type EventType int

const (
	EventManagerStarting     EventType = iota // Before any forwards begin (gate)
	EventManagerStopped                       // All forwards stopped, cleanup done
	EventForwardConnected                     // Port-forward is ready and healthy
	EventForwardDisconnected                  // Port-forward dropped (will retry)
	EventForwardFailed                        // Port-forward failed permanently
	EventForwardStopped                       // Port-forward intentionally stopped
	EventHealthCheckFailed                    // A single health check failed
)

func (e EventType) String() string {
	switch e {
	case EventManagerStarting:
		return "manager_starting"
	case EventManagerStopped:
		return "manager_stopped"
	case EventForwardConnected:
		return "forward_connected"
	case EventForwardDisconnected:
		return "forward_disconnected"
	case EventForwardFailed:
		return "forward_failed"
	case EventForwardStopped:
		return "forward_stopped"
	case EventHealthCheckFailed:
		return "health_check_failed"
	default:
		return "unknown"
	}
}

// ParseEventType converts a string name to an EventType.
func ParseEventType(s string) (EventType, bool) {
	t, ok := eventNames[s]
	return t, ok
}

var eventNames = map[string]EventType{
	"manager_starting":     EventManagerStarting,
	"manager_stopped":      EventManagerStopped,
	"forward_connected":    EventForwardConnected,
	"forward_disconnected": EventForwardDisconnected,
	"forward_failed":       EventForwardFailed,
	"forward_stopped":      EventForwardStopped,
	"health_check_failed":  EventHealthCheckFailed,
}

// Event carries all context for a lifecycle event.
type Event struct {
	Type       EventType
	Time       time.Time
	Service    string // Service name from config (empty for manager events)
	LocalPort  int    // Actual local port (0 if not yet assigned)
	RemotePort int    // Configured remote port
	PodName    string // Resolved pod name (empty if not yet resolved)
	Restarts   int    // Number of restarts so far
	Error      error  // Non-nil for failure/disconnect events
}

// FailMode controls hook behavior on error.
type FailMode int

const (
	FailOpen   FailMode = iota // Log and continue
	FailClosed                 // Abort the operation
)

// Hook processes lifecycle events. Implementations must be safe for
// concurrent use — multiple forwards fire events concurrently.
type Hook interface {
	// Name returns a human-readable identifier for logging.
	Name() string
	// OnEvent is called for notification events.
	OnEvent(ctx context.Context, event Event) error
}

// GateHook is an optional extension for hooks that need to block or abort a
// lifecycle transition. Gate is only called for EventManagerStarting.
type GateHook interface {
	Hook
	// Gate is called before a transition proceeds. Returning an error
	// aborts the transition when FailMode is FailClosed.
	Gate(ctx context.Context, event Event) error
}

type registeredHook struct {
	hook     Hook
	events   map[EventType]bool // nil = all events
	failMode FailMode
	timeout  time.Duration
}

// Dispatcher manages hook registration and event fan-out.
type Dispatcher struct {
	hooks  []registeredHook
	logger *slog.Logger
	mu     sync.RWMutex
	wg     sync.WaitGroup
}

// NewDispatcher creates a new hook event dispatcher.
func NewDispatcher(logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{logger: logger}
}

// Register adds a hook to the dispatcher.
func (d *Dispatcher) Register(reg Registration) {
	timeout := reg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	rh := registeredHook{
		hook:     reg.Hook,
		failMode: reg.FailMode,
		timeout:  timeout,
	}
	if len(reg.Events) > 0 {
		rh.events = make(map[EventType]bool, len(reg.Events))
		for _, e := range reg.Events {
			rh.events[e] = true
		}
	}
	d.mu.Lock()
	d.hooks = append(d.hooks, rh)
	d.mu.Unlock()
}

// Fire sends an event to all registered hooks.
// For gate events (EventManagerStarting), fail-closed hooks run sequentially
// and can abort the operation. All other events run asynchronously.
// Fire is nil-safe: calling Fire on a nil Dispatcher is a no-op.
func (d *Dispatcher) Fire(ctx context.Context, event Event) error {
	if d == nil {
		return nil
	}

	d.mu.RLock()
	hooks := d.hooks
	d.mu.RUnlock()

	if len(hooks) == 0 {
		return nil
	}

	isGate := event.Type == EventManagerStarting

	for i := range hooks {
		rh := hooks[i]
		if rh.events != nil && !rh.events[event.Type] {
			continue
		}
		if isGate {
			if err := d.fireGate(ctx, rh, event); err != nil {
				return err
			}
		} else {
			d.fireAsync(ctx, rh, event)
		}
	}

	return nil
}

func (d *Dispatcher) fireGate(ctx context.Context, rh registeredHook, event Event) error {
	gh, ok := rh.hook.(GateHook)
	if !ok {
		d.fireAsync(ctx, rh, event)
		return nil
	}

	hookCtx, cancel := context.WithTimeout(ctx, rh.timeout)
	defer cancel()

	if err := gh.Gate(hookCtx, event); err != nil {
		d.logger.Error("hook gate failed",
			"hook", rh.hook.Name(),
			"event", event.Type.String(),
			"error", err,
		)
		if rh.failMode == FailClosed {
			return fmt.Errorf("hook %s blocked operation: %w", rh.hook.Name(), err)
		}
	}
	return nil
}

func (d *Dispatcher) fireAsync(ctx context.Context, rh registeredHook, event Event) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		hookCtx, cancel := context.WithTimeout(ctx, rh.timeout)
		defer cancel()

		if err := rh.hook.OnEvent(hookCtx, event); err != nil {
			d.logger.Warn("hook error",
				"hook", rh.hook.Name(),
				"event", event.Type.String(),
				"error", err,
			)
		}
	}()
}

// Wait blocks until all in-flight async hooks have completed.
// Wait is nil-safe: calling Wait on a nil Dispatcher is a no-op.
func (d *Dispatcher) Wait() {
	if d == nil {
		return
	}
	d.wg.Wait()
}

// buildFilter converts a slice of service names into a lookup map.
// Returns nil when the slice is empty (meaning "all services").
func buildFilter(services []string) map[string]bool {
	if len(services) == 0 {
		return nil
	}
	m := make(map[string]bool, len(services))
	for _, s := range services {
		m[s] = true
	}
	return m
}

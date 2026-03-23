// Package proxy manages Kubernetes port-forward connections using client-go.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/rbaliyan/kubeport/pkg/config"
	"github.com/rbaliyan/kubeport/internal/hook"
	"github.com/rbaliyan/kubeport/internal/netutil"
)

// errPreempted is returned by runPortForward when a pod watcher detects the
// current pod is terminating and a replacement pod is available.
var errPreempted = errors.New("preempted by pod lifecycle event")

// ForwardState represents the state of a port forward.
type ForwardState int

const (
	StateStarting ForwardState = iota
	StateRunning
	StateFailed
	StateStopped
)

func (s ForwardState) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateFailed:
		return "failed"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// ForwardStatus holds the status of a single port forward.
type ForwardStatus struct {
	Service    config.ServiceConfig
	State      ForwardState
	Error      error
	Restarts   int
	LastStart  time.Time
	Connected  bool
	ActualPort int       // The actual local port (differs from config when local_port is 0)
	NextRetry  time.Time // When next reconnection attempt will be made (zero if not reconnecting)
	BytesIn    int64     // Total bytes received from the remote side
	BytesOut   int64     // Total bytes sent to the remote side
}

type portForward struct {
	svc        config.ServiceConfig
	namespace  string
	cancel     context.CancelFunc // cancels the current port-forward attempt (idempotent)
	state      ForwardState
	err        error
	restarts   int
	lastStart  time.Time
	actualPort int // Actual port assigned by OS (relevant when local_port is 0)
	nextRetry  time.Time
	counter    byteCounter // cumulative bytes across all connection attempts
	currentPod string      // name of the pod currently being forwarded to
	preemptCh  chan string  // carries replacement pod name for predictive reconnection
	mu         sync.Mutex
}

// serviceCmd represents an add or remove command sent to the event loop.
type serviceCmd struct {
	add    *config.ServiceConfig // non-nil for add
	remove string                // non-empty for remove
	result chan error
}

// resolvedPort is a single port pair produced by expanding a multi-port ServiceConfig.
type resolvedPort struct {
	Name       string // Kubernetes port name (e.g., "http") or "" for unnamed
	RemotePort int    // The service port number
	LocalPort  int    // Computed local port (0 = dynamic)
}

// Manager supervises multiple Kubernetes port forwards.
type Manager struct {
	cfg        *config.Config
	restConfig *rest.Config
	clientset  kubernetes.Interface
	forwards   map[string]*portForward
	order      []string            // service names in config-defined insertion order
	children   map[string][]string // parent service name → list of expanded forward names
	mu         sync.RWMutex
	reloadMu   sync.Mutex // serialises Reload to prevent interleaved remove/add sequences
	output     io.Writer
	cancel     context.CancelFunc
	hooks      *hook.Dispatcher
	logger     *slog.Logger
	cmdCh      chan serviceCmd

	// Supervisor tuning (populated from config.SupervisorConfig in NewManager)
	maxRestarts          int
	healthCheckInterval  time.Duration
	healthCheckThreshold int
	readyTimeout         time.Duration
	backoffInitial       time.Duration
	backoffMax           time.Duration
	maxConnectionAge     time.Duration

	transports *transportCache // reuses SPDY transports across forwards
}

// Option configures optional Manager behavior.
type Option func(*managerOptions)

type managerOptions struct {
	hooks  *hook.Dispatcher
	logger *slog.Logger
}

// WithHooks sets the hook dispatcher for lifecycle events.
func WithHooks(d *hook.Dispatcher) Option {
	return func(o *managerOptions) {
		o.hooks = d
	}
}

// WithLogger sets a structured logger for internal diagnostics.
func WithLogger(l *slog.Logger) Option {
	return func(o *managerOptions) {
		o.logger = l
	}
}

// NewManager creates a new port-forward manager and initializes the Kubernetes client.
func NewManager(cfg *config.Config, output io.Writer, opts ...Option) (*Manager, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	if cfg.Context != "" {
		configOverrides.CurrentContext = cfg.Context
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	restConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig (context %q): %w", cfg.Context, err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	sup, err := cfg.Supervisor.ParsedSupervisor()
	if err != nil {
		return nil, fmt.Errorf("supervisor config: %w", err)
	}

	o := &managerOptions{
		logger: slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
	for _, opt := range opts {
		opt(o)
	}

	transportMaxAge := sup.MaxConnectionAge
	if transportMaxAge == 0 {
		transportMaxAge = 30 * time.Minute
	}

	m := &Manager{
		cfg:                  cfg,
		restConfig:           restConfig,
		clientset:            clientset,
		forwards:             make(map[string]*portForward),
		children:             make(map[string][]string),
		output:               output,
		hooks:                o.hooks,
		logger:               o.logger,
		maxRestarts:          sup.MaxRestarts,
		healthCheckInterval:  sup.HealthCheckInterval,
		healthCheckThreshold: sup.HealthCheckThreshold,
		readyTimeout:         sup.ReadyTimeout,
		backoffInitial:       sup.BackoffInitial,
		backoffMax:           sup.BackoffMax,
		maxConnectionAge:     sup.MaxConnectionAge,
		transports:           newTransportCache(transportMaxAge),
	}
	return m, nil
}

// GetContext returns the Kubernetes context name being used.
func (m *Manager) GetContext() string {
	return m.cfg.Context
}

// CheckNamespace verifies the configured namespace exists.
func (m *Manager) CheckNamespace(ctx context.Context) error {
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := m.clientset.CoreV1().Namespaces().Get(opCtx, m.cfg.Namespace, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("namespace %q not accessible: %w", m.cfg.Namespace, err)
	}
	return nil
}

// Start begins port-forwarding all configured services. Blocks until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.cmdCh = make(chan serviceCmd)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, svc := range m.cfg.Services {
		wg.Add(1)
		go func(s config.ServiceConfig) {
			defer wg.Done()
			m.supervise(ctx, s)
		}(svc)
	}

	// Event loop: listen for add/remove commands until ctx cancelled.
	// The event loop is tracked by the WaitGroup so Start blocks until
	// both the event loop and all service goroutines have finished.
	cmdCh := m.cmdCh
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case cmd := <-cmdCh:
				if cmd.add != nil {
					svc := *cmd.add
					wg.Add(1)
					go func() {
						defer wg.Done()
						m.supervise(ctx, svc)
					}()
					_ = m.hooks.Fire(ctx, hook.Event{
						Type:       hook.EventServiceAdded,
						Time:       time.Now(),
						Service:    svc.Name,
						ParentName: svc.ParentName,
						PortName:   svc.PortName,
						RemotePort: svc.RemotePort,
					})
					cmd.result <- nil
				} else if cmd.remove != "" {
					cmd.result <- m.doRemove(ctx, cmd.remove)
				}
			}
		}
	}()

	wg.Wait()
}

// AddService adds a service to the running manager. Thread-safe.
func (m *Manager) AddService(svc config.ServiceConfig) error {
	if err := config.ValidateService(svc); err != nil {
		return err
	}

	m.mu.RLock()
	if _, exists := m.forwards[svc.Name]; exists {
		m.mu.RUnlock()
		return fmt.Errorf("service %q: %w", svc.Name, config.ErrServiceExists)
	}
	// Also check if a multi-port parent with this name already exists
	if _, exists := m.children[svc.Name]; exists {
		m.mu.RUnlock()
		return fmt.Errorf("service %q: %w", svc.Name, config.ErrServiceExists)
	}
	if !svc.IsMultiPort() && svc.LocalPort != 0 {
		for _, pf := range m.forwards {
			pf.mu.Lock()
			port := pf.svc.LocalPort
			if port == 0 {
				port = pf.actualPort
			}
			pf.mu.Unlock()
			if port == svc.LocalPort {
				m.mu.RUnlock()
				return fmt.Errorf("local port %d already in use by %q", svc.LocalPort, pf.svc.Name)
			}
		}
	}
	cmdCh := m.cmdCh
	m.mu.RUnlock()

	if cmdCh == nil {
		return fmt.Errorf("manager not started")
	}

	result := make(chan error, 1)
	cmdCh <- serviceCmd{add: &svc, result: result}
	return <-result
}

// RemoveService stops and removes a service by name. Thread-safe.
func (m *Manager) RemoveService(name string) error {
	m.mu.RLock()
	cmdCh := m.cmdCh
	m.mu.RUnlock()

	if cmdCh == nil {
		return fmt.Errorf("manager not started")
	}

	result := make(chan error, 1)
	cmdCh <- serviceCmd{remove: name, result: result}
	return <-result
}

// removeSingle cancels and removes a single leaf port-forward entry.
// Must not be called while holding m.mu.
func (m *Manager) removeSingle(ctx context.Context, name string) error {
	m.mu.Lock()
	pf, exists := m.forwards[name]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("service %q: %w", name, config.ErrServiceNotFound)
	}
	delete(m.forwards, name)
	for i, n := range m.order {
		if n == name {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()

	pf.mu.Lock()
	if pf.cancel != nil {
		pf.cancel()
	}
	pf.state = StateStopped
	pf.mu.Unlock()

	_ = m.hooks.Fire(ctx, hook.Event{
		Type:       hook.EventServiceRemoved,
		Time:       time.Now(),
		Service:    name,
		ParentName: pf.svc.ParentName,
		PortName:   pf.svc.PortName,
		RemotePort: pf.svc.RemotePort,
	})
	return nil
}

// doRemove cancels and removes a service (called from event loop).
// If the name is a multi-port parent, all its children are also removed.
// Returns the first child removal error encountered, if any.
func (m *Manager) doRemove(ctx context.Context, name string) error {
	m.mu.Lock()

	// Check if this is a multi-port parent
	childNames, isParent := m.children[name]
	if isParent {
		delete(m.children, name)
		// Also remove the parent's own forward entry (present when resolution failed)
		if _, hasFwd := m.forwards[name]; hasFwd {
			delete(m.forwards, name)
			for i, n := range m.order {
				if n == name {
					m.order = append(m.order[:i], m.order[i+1:]...)
					break
				}
			}
		}
		m.mu.Unlock()

		// Remove all children; collect first error.
		var firstErr error
		for _, childName := range childNames {
			if err := m.removeSingle(ctx, childName); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	m.mu.Unlock()
	return m.removeSingle(ctx, name)
}

// Reload diffs current config vs running services. Adds new, removes deleted.
// Multi-port services are re-resolved on reload (their ports may have changed).
func (m *Manager) Reload(cfg *config.Config) (added, removed int, err error) {
	// Serialise concurrent reloads to prevent interleaved remove/add sequences.
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	// Build sets of running service names and parent names
	m.mu.RLock()
	running := make(map[string]bool, len(m.forwards))
	for name := range m.forwards {
		running[name] = true
	}
	// Also track multi-port parents
	parents := make(map[string]bool, len(m.children))
	for name := range m.children {
		parents[name] = true
	}
	m.mu.RUnlock()

	desired := make(map[string]config.ServiceConfig, len(cfg.Services))
	for _, svc := range cfg.Services {
		desired[svc.Name] = svc
	}

	// Remove services/parents no longer in config
	for name := range parents {
		if _, ok := desired[name]; !ok {
			if removeErr := m.RemoveService(name); removeErr != nil {
				m.logger.Warn("reload: failed to remove multi-port service", "service", name, "error", removeErr)
				continue
			}
			removed++
		}
	}
	// Pre-build child set for O(1) lookup
	m.mu.RLock()
	childSet := make(map[string]bool)
	for _, children := range m.children {
		for _, child := range children {
			childSet[child] = true
		}
	}
	m.mu.RUnlock()

	for name := range running {
		if _, ok := desired[name]; !ok {
			// Skip children of multi-port parents (handled above)
			if childSet[name] {
				continue
			}
			if removeErr := m.RemoveService(name); removeErr != nil {
				m.logger.Warn("reload: failed to remove service", "service", name, "error", removeErr)
				continue
			}
			removed++
		}
	}

	// For multi-port services already running, remove and re-add to pick up port changes
	for name, svc := range desired {
		if svc.IsMultiPort() && parents[name] {
			if removeErr := m.RemoveService(name); removeErr != nil {
				m.logger.Warn("reload: failed to remove multi-port service for re-add", "service", name, "error", removeErr)
				continue
			}
			if addErr := m.AddService(svc); addErr != nil {
				m.logger.Warn("reload: failed to re-add multi-port service", "service", name, "error", addErr)
				continue
			}
			// Net effect counted as re-resolution, not add+remove
			continue
		}
	}

	// Add new services from config
	for name, svc := range desired {
		if !running[name] && !parents[name] {
			if addErr := m.AddService(svc); addErr != nil {
				m.logger.Warn("reload: failed to add service", "service", name, "error", addErr)
				continue
			}
			added++
		}
	}

	return added, removed, nil
}

// Apply adds services that aren't already running. Services whose names conflict
// with already-running services are skipped with a warning. Returns counts and warnings.
func (m *Manager) Apply(services []config.ServiceConfig) (added, skipped int, warnings []string) {
	for _, svc := range services {
		if err := m.AddService(svc); err != nil {
			skipped++
			warnings = append(warnings, fmt.Sprintf("%s: %v", svc.Name, err))
		} else {
			added++
		}
	}
	return added, skipped, warnings
}

func (m *Manager) supervise(ctx context.Context, svc config.ServiceConfig) {
	if svc.IsMultiPort() {
		m.superviseMulti(ctx, svc)
		return
	}
	m.superviseSingle(ctx, svc)
}

// superviseMulti resolves all ports from a multi-port service and spawns a
// superviseSingle goroutine for each expanded port.
func (m *Manager) superviseMulti(ctx context.Context, svc config.ServiceConfig) {
	namespace := svc.Namespace
	if namespace == "" {
		namespace = m.cfg.Namespace
	}

	resolved, err := m.resolveServicePorts(ctx, namespace, svc)
	if err != nil {
		m.logger.Error("failed to resolve multi-port service",
			"service", svc.Name,
			"error", err,
		)
		// Register parent as failed so status shows something
		pf := &portForward{
			svc:       svc,
			namespace: namespace,
			state:     StateFailed,
			err:       err,
		}
		m.mu.Lock()
		m.forwards[svc.Name] = pf
		m.order = append(m.order, svc.Name)
		m.children[svc.Name] = nil // register as parent so Reload can retry
		m.mu.Unlock()
		return
	}

	var childNames []string
	var wg sync.WaitGroup
	for _, rp := range resolved {
		childName := svc.Name + "/" + rp.Name
		if rp.Name == "" {
			childName = fmt.Sprintf("%s/%d", svc.Name, rp.RemotePort)
		}
		childNames = append(childNames, childName)

		childSvc := config.ServiceConfig{
			Name:       childName,
			Service:    svc.Service,
			Pod:        svc.Pod,
			LocalPort:  rp.LocalPort,
			RemotePort: rp.RemotePort,
			Namespace:  svc.Namespace,
			ParentName: svc.Name,
			PortName:   rp.Name,
		}

		wg.Add(1)
		go func(s config.ServiceConfig) {
			defer wg.Done()
			m.superviseSingle(ctx, s)
		}(childSvc)
	}

	m.mu.Lock()
	m.children[svc.Name] = childNames
	m.mu.Unlock()

	wg.Wait()
}

// resolveServicePorts queries the Kubernetes API for a service's ports and
// returns a list of resolvedPort entries based on the multi-port config.
func (m *Manager) resolveServicePorts(ctx context.Context, namespace string, svc config.ServiceConfig) ([]resolvedPort, error) {
	if m.clientset == nil {
		return nil, fmt.Errorf("kubernetes client not initialized")
	}

	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if svc.IsPod() {
		return m.resolvePodPorts(opCtx, namespace, svc)
	}

	service, err := m.clientset.CoreV1().Services(namespace).Get(opCtx, svc.Target(), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get service %s/%s: %w", namespace, svc.Target(), err)
	}

	var ports []corev1.ServicePort
	if svc.Ports.All {
		ports = service.Spec.Ports
	} else {
		for _, sel := range svc.Ports.Selectors {
			found := false
			for _, sp := range service.Spec.Ports {
				if (sel.Name != "" && sp.Name == sel.Name) || (sel.Port != 0 && int(sp.Port) == sel.Port) {
					ports = append(ports, sp)
					found = true
					break
				}
			}
			if !found {
				id := sel.Name
				if id == "" {
					id = fmt.Sprintf("%d", sel.Port)
				}
				return nil, fmt.Errorf("port %s not found on service %s", id, svc.Target())
			}
		}
	}

	excludeSet, selectorOverrides := buildPortMaps(svc)

	var resolved []resolvedPort
	for _, sp := range ports {
		if excludeSet[sp.Name] {
			continue
		}

		localPort := computeLocalPort(selectorOverrides, sp.Name, int(sp.Port), svc.LocalPortOffset)
		if localPort < 0 || localPort > 65535 {
			return nil, fmt.Errorf("port %s: computed local port %d out of range (remote %d + offset %d)",
				sp.Name, localPort, sp.Port, svc.LocalPortOffset)
		}

		resolved = append(resolved, resolvedPort{
			Name:       sp.Name,
			RemotePort: int(sp.Port),
			LocalPort:  localPort,
		})
	}

	if len(resolved) == 0 {
		return nil, fmt.Errorf("no ports resolved for service %s after filtering", svc.Target())
	}

	return resolved, nil
}

// resolvePodPorts resolves ports from a pod's container spec.
func (m *Manager) resolvePodPorts(ctx context.Context, namespace string, svc config.ServiceConfig) ([]resolvedPort, error) {
	pod, err := m.clientset.CoreV1().Pods(namespace).Get(ctx, svc.Target(), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", namespace, svc.Target(), err)
	}

	excludeSet, selectorOverrides := buildPortMaps(svc)

	var resolved []resolvedPort
	for _, c := range pod.Spec.Containers {
		for _, cp := range c.Ports {
			if excludeSet[cp.Name] {
				continue
			}

			if !svc.Ports.All {
				// Match against selectors
				matched := false
				for _, sel := range svc.Ports.Selectors {
					if (sel.Name != "" && cp.Name == sel.Name) || (sel.Port != 0 && int(cp.ContainerPort) == sel.Port) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}

			localPort := computeLocalPort(selectorOverrides, cp.Name, int(cp.ContainerPort), svc.LocalPortOffset)
			if localPort < 0 || localPort > 65535 {
				return nil, fmt.Errorf("port %s: computed local port %d out of range (remote %d + offset %d)",
					cp.Name, localPort, cp.ContainerPort, svc.LocalPortOffset)
			}

			resolved = append(resolved, resolvedPort{
				Name:       cp.Name,
				RemotePort: int(cp.ContainerPort),
				LocalPort:  localPort,
			})
		}
	}

	if len(resolved) == 0 {
		return nil, fmt.Errorf("no ports resolved for pod %s after filtering", svc.Target())
	}

	return resolved, nil
}

// buildPortMaps constructs the exclude set and selector override map from a ServiceConfig.
func buildPortMaps(svc config.ServiceConfig) (excludeSet map[string]bool, selectorOverrides map[string]config.PortSelector) {
	excludeSet = make(map[string]bool, len(svc.ExcludePorts))
	for _, name := range svc.ExcludePorts {
		excludeSet[name] = true
	}
	selectorOverrides = make(map[string]config.PortSelector, len(svc.Ports.Selectors))
	for _, sel := range svc.Ports.Selectors {
		if sel.Name != "" {
			selectorOverrides[sel.Name] = sel
		}
	}
	return
}

// computeLocalPort determines the local port for a given remote port.
// Returns 0 (dynamic OS-assigned) when no explicit override or offset is set,
// allowing the OS to pick a free port. Clients connecting via SOCKS5/HTTP proxy
// do not need predictable local port numbers; use local_port_offset or a
// per-port local_port selector when a fixed local port is required.
func computeLocalPort(overrides map[string]config.PortSelector, portName string, remotePort, offset int) int {
	if sel, ok := overrides[portName]; ok && sel.LocalPort != 0 {
		return sel.LocalPort
	}
	if offset > 0 {
		return remotePort + offset
	}
	return 0
}

func (m *Manager) superviseSingle(ctx context.Context, svc config.ServiceConfig) {
	namespace := svc.Namespace
	if namespace == "" {
		namespace = m.cfg.Namespace
	}

	pf := &portForward{
		svc:       svc,
		namespace: namespace,
		state:     StateStarting,
		preemptCh: make(chan string, 1),
	}

	m.mu.Lock()
	m.forwards[svc.Name] = pf
	m.order = append(m.order, svc.Name)
	m.mu.Unlock()

	// Start pod watcher for service-backed forwards (not direct pod targets).
	if !svc.IsPod() && m.clientset != nil {
		selector, err := m.getServiceSelector(ctx, namespace, svc)
		if err == nil && selector != "" {
			watcher := newPodWatcher(m.clientset, namespace, selector, m.logger)
			go watcher.run(ctx)
			go m.handlePodEvents(ctx, pf, watcher)
		}
	}

	backoff := m.backoffInitial

	for {
		select {
		case <-ctx.Done():
			pf.mu.Lock()
			pf.state = StateStopped
			pf.mu.Unlock()
			_ = m.hooks.Fire(ctx, hook.Event{
				Type:       hook.EventForwardStopped,
				Time:       time.Now(),
				Service:    svc.Name,
				ParentName: svc.ParentName,
				PortName:   svc.PortName,
				RemotePort: svc.RemotePort,
			})
			return
		default:
		}

		// Log warning if port appears in use, but still attempt — the port may
		// free up between our check and the actual bind in port-forward.
		if svc.LocalPort != 0 && netutil.IsPortOpen(svc.LocalPort) {
			m.logger.Warn("port appears in use, will attempt anyway",
				"service", svc.Name,
				"port", svc.LocalPort,
			)
		}

		pf.mu.Lock()
		pf.state = StateStarting
		pf.lastStart = time.Now()
		pf.nextRetry = time.Time{}
		pf.mu.Unlock()

		startTime := time.Now()
		err := m.runPortForward(ctx, pf)
		duration := time.Since(startTime)

		if ctx.Err() != nil {
			pf.mu.Lock()
			pf.state = StateStopped
			pf.mu.Unlock()
			return
		}

		// Preemptive reconnection: skip backoff and retry immediately.
		if errors.Is(err, errPreempted) {
			m.logger.Info("preemptive reconnection, reconnecting immediately",
				"service", svc.Name,
			)
			backoff = m.backoffInitial
			continue
		}

		pf.mu.Lock()
		pf.state = StateFailed
		pf.err = err
		pf.restarts++
		restarts := pf.restarts
		pf.mu.Unlock()

		m.logger.Warn("forward disconnected",
			"service", svc.Name,
			"duration", duration.Round(time.Second),
			"error", err,
			"restarts", restarts,
		)

		_ = m.hooks.Fire(ctx, hook.Event{
			Type:       hook.EventForwardDisconnected,
			Time:       time.Now(),
			Service:    svc.Name,
			ParentName: svc.ParentName,
			PortName:   svc.PortName,
			LocalPort:  pf.actualPort,
			RemotePort: svc.RemotePort,
			Restarts:   restarts,
			Error:      err,
		})

		// Check max restarts limit (0 = unlimited)
		if m.maxRestarts > 0 && restarts >= m.maxRestarts {
			m.logger.Error("max restarts reached, giving up",
				"service", svc.Name,
				"max_restarts", m.maxRestarts,
			)
			_ = m.hooks.Fire(ctx, hook.Event{
				Type:       hook.EventForwardFailed,
				Time:       time.Now(),
				Service:    svc.Name,
				ParentName: svc.ParentName,
				PortName:   svc.PortName,
				LocalPort:  pf.actualPort,
				RemotePort: svc.RemotePort,
				Restarts:   restarts,
				Error:      fmt.Errorf("max restarts (%d) exceeded", m.maxRestarts),
			})
			return
		}

		// Reset backoff if connection lasted long enough
		if duration > 30*time.Second {
			backoff = m.backoffInitial
		}

		// Add ±25% jitter to backoff to prevent thundering herd
		jittered := addJitter(backoff)

		pf.mu.Lock()
		pf.nextRetry = time.Now().Add(jittered)
		pf.mu.Unlock()

		backoffTimer := time.NewTimer(jittered)
		select {
		case <-ctx.Done():
			backoffTimer.Stop()
			return
		case <-backoffTimer.C:
		}

		backoff = min(backoff*2, m.backoffMax)
	}
}

func (m *Manager) runPortForward(ctx context.Context, pf *portForward) error {
	// Create a cancellable context for this specific port-forward attempt.
	// fwCancel is idempotent and safe to call from multiple goroutines,
	// which eliminates the double-close race on stopChan.
	fwCtx, fwCancel := context.WithCancel(ctx)
	defer fwCancel()

	pf.mu.Lock()
	pf.cancel = fwCancel
	pf.mu.Unlock()

	// Resolve to a pod and target port (for services, find a running pod
	// via selector and translate service port → container targetPort)
	podName, targetPort, err := m.resolvePod(ctx, pf.namespace, pf.svc)
	if err != nil {
		return err
	}

	pf.mu.Lock()
	pf.currentPod = podName
	pf.mu.Unlock()

	// Build the SPDY port-forward URL
	reqURL := m.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pf.namespace).
		Name(podName).
		SubResource("portforward").
		URL()

	entry, err := m.transports.getOrCreate(m.restConfig.Host, func() (http.RoundTripper, spdy.Upgrader, error) {
		return spdy.RoundTripperFor(m.restConfig)
	})
	if err != nil {
		return fmt.Errorf("create SPDY transport: %w", err)
	}

	rawDialer := spdy.NewDialer(entry.upgrader, entry.client, http.MethodPost, reqURL)
	dialer := &countingDialer{dialer: rawDialer, counter: &pf.counter}

	// stopChan is closed exactly once when fwCtx is cancelled.
	stopChan := make(chan struct{})
	readyChan := make(chan struct{})

	go func() {
		<-fwCtx.Done()
		close(stopChan)
	}()

	ports := []string{fmt.Sprintf("%d:%d", pf.svc.LocalPort, targetPort)}

	fw, err := portforward.New(dialer, ports, stopChan, readyChan, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("create port forwarder: %w", err)
	}

	// Run in a goroutine since ForwardPorts blocks
	errChan := make(chan error, 1)
	go func() {
		errChan <- fw.ForwardPorts()
	}()

	// Wait for the tunnel to be ready
	select {
	case <-readyChan:
		// Ready
	case err := <-errChan:
		if err != nil {
			return fmt.Errorf("port forward startup: %w", err)
		}
		return fmt.Errorf("port forward closed before ready")
	case <-fwCtx.Done():
		return fwCtx.Err()
	case <-time.After(m.readyTimeout):
		fwCancel()
		return fmt.Errorf("timeout waiting for port forward to become ready")
	}

	// Get actual ports (important for dynamic port allocation when local_port is 0)
	actualPort := pf.svc.LocalPort
	forwardedPorts, err := fw.GetPorts()
	if err == nil && len(forwardedPorts) > 0 {
		actualPort = int(forwardedPorts[0].Local)
	}

	pf.mu.Lock()
	pf.state = StateRunning
	pf.actualPort = actualPort
	pf.err = nil
	pf.mu.Unlock()

	_, _ = fmt.Fprintf(m.output, "[%s] forwarding localhost:%d -> %s:%d (pod: %s)\n",
		pf.svc.Name, actualPort, pf.svc.Target(), pf.svc.RemotePort, podName)

	_ = m.hooks.Fire(ctx, hook.Event{
		Type:       hook.EventForwardConnected,
		Time:       time.Now(),
		Service:    pf.svc.Name,
		ParentName: pf.svc.ParentName,
		PortName:   pf.svc.PortName,
		LocalPort:  actualPort,
		RemotePort: pf.svc.RemotePort,
		PodName:    podName,
	})

	// Health check loop
	ticker := time.NewTicker(m.healthCheckInterval)
	defer ticker.Stop()

	// Max connection age timer — proactively reconnect to prevent SPDY degradation.
	// The SPDY connection can become stale over time (load balancer timeouts,
	// API server connection limits), causing stream creation to fail silently.
	var maxAgeCh <-chan time.Time
	if m.maxConnectionAge > 0 {
		maxAgeTimer := time.NewTimer(m.maxConnectionAge)
		defer maxAgeTimer.Stop()
		maxAgeCh = maxAgeTimer.C
	}

	failCount := 0
	lastStreamErrors := pf.counter.streamErrors.Load()

	for {
		select {
		case err := <-errChan:
			if err != nil {
				return fmt.Errorf("port forward: %w", err)
			}
			return fmt.Errorf("port forward closed")
		case <-fwCtx.Done():
			return fwCtx.Err()
		case <-maxAgeCh:
			m.logger.Info("max connection age reached, reconnecting",
				"service", pf.svc.Name,
				"max_age", m.maxConnectionAge,
			)
			fwCancel()
			return fmt.Errorf("max connection age (%s) reached", m.maxConnectionAge)
		case replacementPod := <-pf.preemptCh:
			m.logger.Info("preemptive reconnection triggered",
				"service", pf.svc.Name,
				"current_pod", podName,
				"replacement_pod", replacementPod,
			)
			fwCancel()
			return errPreempted
		case <-ticker.C:
			// Check for SPDY stream creation failures. Each failure means a
			// client connection was dropped after a 30-second timeout waiting
			// for a SYN_REPLY that never came — the SPDY connection is degraded.
			currentStreamErrors := pf.counter.streamErrors.Load()
			if currentStreamErrors > lastStreamErrors {
				newErrors := currentStreamErrors - lastStreamErrors
				m.logger.Warn("SPDY stream errors detected, forcing reconnection",
					"service", pf.svc.Name,
					"new_errors", newErrors,
					"total_errors", currentStreamErrors,
				)
				m.transports.evict(m.restConfig.Host)
				fwCancel()
				return fmt.Errorf("SPDY connection degraded: %d stream creation failures", currentStreamErrors)
			}

			if !netutil.IsPortOpen(actualPort) {
				failCount++
				_ = m.hooks.Fire(ctx, hook.Event{
					Type:       hook.EventHealthCheckFailed,
					Time:       time.Now(),
					Service:    pf.svc.Name,
					ParentName: pf.svc.ParentName,
					PortName:   pf.svc.PortName,
					LocalPort:  actualPort,
					RemotePort: pf.svc.RemotePort,
					PodName:    podName,
				})
				if failCount >= m.healthCheckThreshold {
					fwCancel()
					return fmt.Errorf("health check failed %d consecutive times", failCount)
				}
			} else {
				failCount = 0
			}
		}
	}
}

// addJitter adds ±25% random jitter to a duration to prevent thundering herd.
func addJitter(d time.Duration) time.Duration {
	jitter := float64(d) * 0.25
	return d + time.Duration(rand.Float64()*2*jitter-jitter)
}

func (m *Manager) resolvePod(ctx context.Context, namespace string, svc config.ServiceConfig) (string, int, error) {
	if svc.IsPod() {
		return svc.Target(), svc.RemotePort, nil
	}

	if m.clientset == nil {
		return "", 0, fmt.Errorf("kubernetes client not initialized")
	}

	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get the service to find its pod selector and port mapping
	service, err := m.clientset.CoreV1().Services(namespace).Get(opCtx, svc.Target(), metav1.GetOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("get service %s/%s: %w", namespace, svc.Target(), err)
	}

	if len(service.Spec.Selector) == 0 {
		return "", 0, fmt.Errorf("service %s has no pod selector", svc.Target())
	}

	// Find the service port spec that matches remote_port
	var namedTargetPort string
	targetPort := svc.RemotePort
	for _, p := range service.Spec.Ports {
		if int(p.Port) == svc.RemotePort {
			if p.TargetPort.IntValue() != 0 {
				// Numeric targetPort (e.g., targetPort: 8061)
				targetPort = p.TargetPort.IntValue()
			} else if p.TargetPort.String() != "" && p.TargetPort.String() != "0" {
				// Named targetPort (e.g., targetPort: "http") — resolve from pod spec
				namedTargetPort = p.TargetPort.String()
			}
			break
		}
	}

	selector := labels.SelectorFromSet(service.Spec.Selector)
	pods, err := m.clientset.CoreV1().Pods(namespace).List(opCtx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return "", 0, fmt.Errorf("list pods for service %s: %w", svc.Target(), err)
	}

	// Two-pass selection: prefer Ready pods, fall back to Running pods.
	// Skip pods marked for deletion (e.g., during rolling updates).
	var fallback *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if pod.DeletionTimestamp != nil {
			continue
		}
		if isPodReady(pod) {
			if namedTargetPort != "" {
				if resolved := resolveNamedPort(pod, namedTargetPort); resolved != 0 {
					targetPort = resolved
				}
			}
			return pod.Name, targetPort, nil
		}
		if fallback == nil {
			fallback = pod
		}
	}

	// No Ready pod found — use a Running (but not-yet-ready) pod as fallback.
	// The port-forward may fail, but the supervisor will retry.
	if fallback != nil {
		if namedTargetPort != "" {
			if resolved := resolveNamedPort(fallback, namedTargetPort); resolved != 0 {
				targetPort = resolved
			}
		}
		return fallback.Name, targetPort, nil
	}

	return "", 0, fmt.Errorf("no running pods for service %s (selector: %s)", svc.Target(), selector.String())
}

// resolveNamedPort looks up a named port (e.g., "http") in the pod's container
// specs and returns the numeric containerPort. Returns 0 if not found.
func resolveNamedPort(pod *corev1.Pod, portName string) int {
	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			if p.Name == portName {
				return int(p.ContainerPort)
			}
		}
	}
	return 0
}

// isPodReady returns true if the pod has the PodReady condition set to True.
func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// getServiceSelector returns the label selector string for a Kubernetes Service.
func (m *Manager) getServiceSelector(ctx context.Context, namespace string, svc config.ServiceConfig) (string, error) {
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	service, err := m.clientset.CoreV1().Services(namespace).Get(opCtx, svc.Target(), metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get service %s/%s: %w", namespace, svc.Target(), err)
	}
	if len(service.Spec.Selector) == 0 {
		return "", fmt.Errorf("service %s has no pod selector", svc.Target())
	}
	return labels.SelectorFromSet(service.Spec.Selector).String(), nil
}

// handlePodEvents processes pod lifecycle events from the watcher.
// When the current pod enters Terminating state, it resolves a replacement
// pod and signals runPortForward to preemptively reconnect.
func (m *Manager) handlePodEvents(ctx context.Context, pf *portForward, watcher *podWatcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-watcher.events():
			if !ok {
				return
			}
			if evt.eventType != podTerminating {
				continue
			}

			pf.mu.Lock()
			currentPod := pf.currentPod
			pf.mu.Unlock()

			if evt.podName != currentPod {
				continue // not our pod
			}

			m.logger.Info("current pod terminating, initiating preemptive reconnection",
				"service", pf.svc.Name,
				"pod", evt.podName,
			)

			_ = m.hooks.Fire(ctx, hook.Event{
				Type:       hook.EventPodTerminating,
				Time:       time.Now(),
				Service:    pf.svc.Name,
				ParentName: pf.svc.ParentName,
				PortName:   pf.svc.PortName,
				PodName:    evt.podName,
				RemotePort: pf.svc.RemotePort,
			})

			// Find a replacement pod.
			replacementPod, _, err := m.resolvePod(ctx, pf.namespace, pf.svc)
			if err != nil || replacementPod == currentPod {
				m.logger.Warn("no replacement pod available, will reconnect normally",
					"service", pf.svc.Name,
					"error", err,
				)
				continue
			}

			// Signal the running forward to preempt.
			select {
			case pf.preemptCh <- replacementPod:
			default:
				// Already a preempt pending.
			}
		}
	}
}

// Stop terminates all port forwards.
func (m *Manager) Stop() {
	m.mu.RLock()
	cancel := m.cancel
	m.mu.RUnlock()

	if cancel != nil {
		cancel()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, pf := range m.forwards {
		pf.mu.Lock()
		if pf.cancel != nil {
			pf.cancel()
		}
		pf.state = StateStopped
		pf.mu.Unlock()
	}

	if m.transports != nil {
		m.transports.close()
	}
}

// AddressMapping represents a single K8s internal address to local address mapping.
type AddressMapping struct {
	InternalAddr string // e.g., "web-api.demo.svc.cluster.local:80"
	LocalAddr    string // e.g., "localhost:8080"
	ServiceName  string // kubeport service name
}

// Mappings returns address mappings for all running forwards. For each connected forward,
// it generates all Kubernetes DNS name variants that could be used to reach the service
// (short name, namespace-qualified, svc-qualified, and FQDN) mapped to localhost:<port>.
func (m *Manager) Mappings(clusterDomain string) []AddressMapping {
	if clusterDomain == "" {
		clusterDomain = "cluster.local"
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	defaultNS := m.cfg.Namespace

	var mappings []AddressMapping
	for _, name := range m.order {
		pf := m.forwards[name]
		pf.mu.Lock()
		state := pf.state
		port := pf.actualPort
		if port == 0 {
			port = pf.svc.LocalPort
		}
		svc := pf.svc
		pf.mu.Unlock()

		if state != StateRunning || port <= 0 {
			continue
		}

		localAddr := fmt.Sprintf("localhost:%d", port)
		ns := svc.Namespace
		if ns == "" {
			ns = defaultNS
		}
		remotePort := fmt.Sprintf("%d", svc.RemotePort)

		// For service-backed forwards, generate DNS name variants
		target := svc.Service
		if target == "" {
			target = svc.Pod
		}
		if target == "" {
			continue
		}

		if svc.Service != "" {
			// Service DNS variants:
			//   <svc>:<port>
			//   <svc>.<ns>:<port>
			//   <svc>.<ns>.svc:<port>
			//   <svc>.<ns>.svc.<domain>:<port>
			dnsNames := []string{
				target,
				target + "." + ns,
				target + "." + ns + ".svc",
				target + "." + ns + ".svc." + clusterDomain,
			}
			for _, dns := range dnsNames {
				mappings = append(mappings, AddressMapping{
					InternalAddr: dns + ":" + remotePort,
					LocalAddr:    localAddr,
					ServiceName:  name,
				})
			}
		} else {
			// Pod DNS variants. StatefulSet pods are accessed via
			// <pod>.<headless-svc>.<ns>.svc.<domain>. We generate a
			// short entry plus a namespace-qualified entry so the proxy
			// can disambiguate pods with the same name across namespaces.
			mappings = append(mappings,
				AddressMapping{
					InternalAddr: target + ":" + remotePort,
					LocalAddr:    localAddr,
					ServiceName:  name,
				},
				AddressMapping{
					InternalAddr: target + "." + ns + ":" + remotePort,
					LocalAddr:    localAddr,
					ServiceName:  name,
				},
			)
		}
	}
	return mappings
}

// Status returns the status of all port forwards.
func (m *Manager) Status() []ForwardStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make([]ForwardStatus, 0, len(m.order))
	for _, name := range m.order {
		pf := m.forwards[name]
		pf.mu.Lock()
		port := pf.actualPort
		if port == 0 {
			port = pf.svc.LocalPort
		}
		s := ForwardStatus{
			Service:    pf.svc,
			State:      pf.state,
			Error:      pf.err,
			Restarts:   pf.restarts,
			LastStart:  pf.lastStart,
			Connected:  port > 0 && netutil.IsPortOpen(port),
			ActualPort: port,
			NextRetry:  pf.nextRetry,
			BytesIn:    pf.counter.bytesIn.Load(),
			BytesOut:   pf.counter.bytesOut.Load(),
		}
		pf.mu.Unlock()
		statuses = append(statuses, s)
	}
	return statuses
}

// Package proxy manages Kubernetes port-forward connections using client-go.
package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
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

	"github.com/rbaliyan/kubeport/internal/config"
	"github.com/rbaliyan/kubeport/internal/hook"
	"github.com/rbaliyan/kubeport/internal/netutil"
)

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
	mu         sync.Mutex
}

// serviceCmd represents an add or remove command sent to the event loop.
type serviceCmd struct {
	add    *config.ServiceConfig // non-nil for add
	remove string               // non-empty for remove
	result chan error
}

// Manager supervises multiple Kubernetes port forwards.
type Manager struct {
	cfg        *config.Config
	restConfig *rest.Config
	clientset  kubernetes.Interface
	forwards   map[string]*portForward
	mu         sync.RWMutex
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
}

// Option configures optional Manager behavior.
type Option func(*Manager)

// WithHooks sets the hook dispatcher for lifecycle events.
func WithHooks(d *hook.Dispatcher) Option {
	return func(m *Manager) {
		m.hooks = d
	}
}

// WithLogger sets a structured logger for internal diagnostics.
func WithLogger(l *slog.Logger) Option {
	return func(m *Manager) {
		m.logger = l
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

	sup := cfg.Supervisor.ParsedSupervisor()

	m := &Manager{
		cfg:                  cfg,
		restConfig:           restConfig,
		clientset:            clientset,
		forwards:             make(map[string]*portForward),
		output:               output,
		logger:               slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo})),
		maxRestarts:          sup.MaxRestarts,
		healthCheckInterval:  sup.HealthCheckInterval,
		healthCheckThreshold: sup.HealthCheckThreshold,
		readyTimeout:         sup.ReadyTimeout,
		backoffInitial:       sup.BackoffInitial,
		backoffMax:           sup.BackoffMax,
	}
	for _, opt := range opts {
		opt(m)
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
	if svc.LocalPort != 0 {
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

// doRemove cancels and removes a service (called from event loop).
func (m *Manager) doRemove(ctx context.Context, name string) error {
	m.mu.Lock()
	pf, exists := m.forwards[name]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("service %q: %w", name, config.ErrServiceNotFound)
	}
	delete(m.forwards, name)
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
		RemotePort: pf.svc.RemotePort,
	})
	return nil
}

// Reload diffs current config vs running services. Adds new, removes deleted.
func (m *Manager) Reload(cfg *config.Config) (added, removed int, err error) {
	// Build sets of running and desired service names
	m.mu.RLock()
	running := make(map[string]bool, len(m.forwards))
	for name := range m.forwards {
		running[name] = true
	}
	m.mu.RUnlock()

	desired := make(map[string]config.ServiceConfig, len(cfg.Services))
	for _, svc := range cfg.Services {
		desired[svc.Name] = svc
	}

	// Remove services no longer in config
	for name := range running {
		if _, ok := desired[name]; !ok {
			if removeErr := m.RemoveService(name); removeErr != nil {
				m.logger.Warn("reload: failed to remove service", "service", name, "error", removeErr)
				continue
			}
			removed++
		}
	}

	// Add new services from config
	for name, svc := range desired {
		if !running[name] {
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
	namespace := svc.Namespace
	if namespace == "" {
		namespace = m.cfg.Namespace
	}

	pf := &portForward{
		svc:       svc,
		namespace: namespace,
		state:     StateStarting,
	}

	m.mu.Lock()
	m.forwards[svc.Name] = pf
	m.mu.Unlock()

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

	// Build the SPDY port-forward URL
	reqURL := m.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pf.namespace).
		Name(podName).
		SubResource("portforward").
		URL()

	transport, upgrader, err := spdy.RoundTripperFor(m.restConfig)
	if err != nil {
		return fmt.Errorf("create SPDY transport: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, reqURL)

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
	pf.mu.Unlock()

	_, _ = fmt.Fprintf(m.output, "[%s] forwarding localhost:%d -> %s:%d (pod: %s)\n",
		pf.svc.Name, actualPort, pf.svc.Target(), pf.svc.RemotePort, podName)

	_ = m.hooks.Fire(ctx, hook.Event{
		Type:       hook.EventForwardConnected,
		Time:       time.Now(),
		Service:    pf.svc.Name,
		LocalPort:  actualPort,
		RemotePort: pf.svc.RemotePort,
		PodName:    podName,
	})

	// Health check loop
	ticker := time.NewTicker(m.healthCheckInterval)
	defer ticker.Stop()

	failCount := 0

	for {
		select {
		case err := <-errChan:
			if err != nil {
				return fmt.Errorf("port forward: %w", err)
			}
			return fmt.Errorf("port forward closed")
		case <-fwCtx.Done():
			return fwCtx.Err()
		case <-ticker.C:
			if !netutil.IsPortOpen(actualPort) {
				failCount++
				_ = m.hooks.Fire(ctx, hook.Event{
					Type:       hook.EventHealthCheckFailed,
					Time:       time.Now(),
					Service:    pf.svc.Name,
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

	for i := range pods.Items {
		if pods.Items[i].Status.Phase != corev1.PodRunning {
			continue
		}
		// Resolve named targetPort from the pod's container port definitions
		if namedTargetPort != "" {
			if resolved := resolveNamedPort(&pods.Items[i], namedTargetPort); resolved != 0 {
				targetPort = resolved
			}
		}
		return pods.Items[i].Name, targetPort, nil
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
}

// Status returns the status of all port forwards.
func (m *Manager) Status() []ForwardStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make([]ForwardStatus, 0, len(m.forwards))
	for _, pf := range m.forwards {
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
		}
		pf.mu.Unlock()
		statuses = append(statuses, s)
	}
	return statuses
}

// Package proxy manages Kubernetes port-forward connections using client-go.
package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
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
)

const (
	healthCheckInterval      = 10 * time.Second
	healthCheckFailThreshold = 3
	initialBackoff           = 1 * time.Second
	maxBackoff               = 30 * time.Second
	readyTimeout             = 15 * time.Second
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
	ActualPort int // The actual local port (differs from config when local_port is 0)
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
	mu         sync.Mutex
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

	m := &Manager{
		cfg:        cfg,
		restConfig: restConfig,
		clientset:  clientset,
		forwards:   make(map[string]*portForward),
		output:     output,
		logger:     slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo})),
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
	_, err := m.clientset.CoreV1().Namespaces().Get(ctx, m.cfg.Namespace, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("namespace %q not accessible: %w", m.cfg.Namespace, err)
	}
	return nil
}

// Start begins port-forwarding all configured services. Blocks until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	var wg sync.WaitGroup
	for _, svc := range m.cfg.Services {
		wg.Add(1)
		go func(s config.ServiceConfig) {
			defer wg.Done()
			m.supervise(ctx, s)
		}(svc)
	}

	wg.Wait()
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

	backoff := initialBackoff

	for {
		select {
		case <-ctx.Done():
			pf.mu.Lock()
			pf.state = StateStopped
			pf.mu.Unlock()
			m.hooks.Fire(ctx, hook.Event{
				Type:       hook.EventForwardStopped,
				Time:       time.Now(),
				Service:    svc.Name,
				RemotePort: svc.RemotePort,
			})
			return
		default:
		}

		// Check if port already in use by another process (skip for dynamic ports)
		if svc.LocalPort != 0 && isPortOpen(svc.LocalPort) {
			pf.mu.Lock()
			pf.state = StateFailed
			pf.err = fmt.Errorf("port %d already in use", svc.LocalPort)
			pf.mu.Unlock()
			fmt.Fprintf(m.output, "[%s] port %d already in use, skipping\n", svc.Name, svc.LocalPort)
			m.hooks.Fire(ctx, hook.Event{
				Type:       hook.EventForwardFailed,
				Time:       time.Now(),
				Service:    svc.Name,
				LocalPort:  svc.LocalPort,
				RemotePort: svc.RemotePort,
				Error:      pf.err,
			})
			return
		}

		pf.mu.Lock()
		pf.state = StateStarting
		pf.lastStart = time.Now()
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

		fmt.Fprintf(m.output, "[%s] disconnected after %s: %v (restart #%d)\n",
			svc.Name, duration.Round(time.Second), err, restarts)

		m.hooks.Fire(ctx, hook.Event{
			Type:       hook.EventForwardDisconnected,
			Time:       time.Now(),
			Service:    svc.Name,
			LocalPort:  pf.actualPort,
			RemotePort: svc.RemotePort,
			Restarts:   restarts,
			Error:      err,
		})

		// Reset backoff if connection lasted long enough
		if duration > 30*time.Second {
			backoff = initialBackoff
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, maxBackoff)
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

	// Resolve to a pod (for services, find a running pod via selector)
	podName, err := m.resolvePod(ctx, pf.namespace, pf.svc)
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

	ports := []string{fmt.Sprintf("%d:%d", pf.svc.LocalPort, pf.svc.RemotePort)}

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
	case <-time.After(readyTimeout):
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

	fmt.Fprintf(m.output, "[%s] forwarding localhost:%d -> %s:%d (pod: %s)\n",
		pf.svc.Name, actualPort, pf.svc.Target(), pf.svc.RemotePort, podName)

	m.hooks.Fire(ctx, hook.Event{
		Type:       hook.EventForwardConnected,
		Time:       time.Now(),
		Service:    pf.svc.Name,
		LocalPort:  actualPort,
		RemotePort: pf.svc.RemotePort,
		PodName:    podName,
	})

	// Health check loop
	ticker := time.NewTicker(healthCheckInterval)
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
			if !isPortOpen(actualPort) {
				failCount++
				m.hooks.Fire(ctx, hook.Event{
					Type:       hook.EventHealthCheckFailed,
					Time:       time.Now(),
					Service:    pf.svc.Name,
					LocalPort:  actualPort,
					RemotePort: pf.svc.RemotePort,
					PodName:    podName,
				})
				if failCount >= healthCheckFailThreshold {
					fwCancel()
					return fmt.Errorf("health check failed %d consecutive times", failCount)
				}
			} else {
				failCount = 0
			}
		}
	}
}

func (m *Manager) resolvePod(ctx context.Context, namespace string, svc config.ServiceConfig) (string, error) {
	if svc.IsPod() {
		return svc.Target(), nil
	}

	// Get the service to find its pod selector
	service, err := m.clientset.CoreV1().Services(namespace).Get(ctx, svc.Target(), metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get service %s/%s: %w", namespace, svc.Target(), err)
	}

	if len(service.Spec.Selector) == 0 {
		return "", fmt.Errorf("service %s has no pod selector", svc.Target())
	}

	selector := labels.SelectorFromSet(service.Spec.Selector)
	pods, err := m.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return "", fmt.Errorf("list pods for service %s: %w", svc.Target(), err)
	}

	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			return pods.Items[i].Name, nil
		}
	}

	return "", fmt.Errorf("no running pods for service %s (selector: %s)", svc.Target(), selector.String())
}

// Stop terminates all port forwards.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
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
			Connected:  port > 0 && isPortOpen(port),
			ActualPort: port,
		}
		pf.mu.Unlock()
		statuses = append(statuses, s)
	}
	return statuses
}

func isPortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

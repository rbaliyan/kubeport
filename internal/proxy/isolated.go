package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/rbaliyan/kubeport/internal/hook"
	"github.com/rbaliyan/kubeport/internal/netutil"
	"github.com/rbaliyan/kubeport/pkg/config"
)

// runIsolatedPortForward binds the local port immediately and gives each
// incoming TCP connection its own SPDY tunnel to the k8s API server, removing
// the ~128-concurrent-client limit imposed by the SPDY 256-stream cap.
//
// Unlike runPortForward (which dials once and multiplexes all clients over one
// SPDY connection), this function creates a fresh SPDY connection per client.
// The cost is one extra TLS handshake per client connection; the benefit is
// unlimited concurrency with no stream-limit exhaustion.
func (m *Manager) runIsolatedPortForward(ctx context.Context, pf *portForward) error {
	fwCtx, fwCancel := context.WithCancel(ctx)
	defer fwCancel()

	pf.mu.Lock()
	pf.cancel = fwCancel
	pf.mu.Unlock()

	// Resolve pod once at startup; updated in-place on preemptive reconnect.
	podName, targetPort, err := m.resolvePod(ctx, pf.namespace, pf.svc)
	if err != nil {
		return err
	}

	pf.mu.Lock()
	pf.currentPod = podName
	pf.currentTargetPort = targetPort
	pf.mu.Unlock()

	listenAddr := fmt.Sprintf("localhost:%d", pf.svc.LocalPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("bind local port: %w", err)
	}
	defer func() { _ = ln.Close() }()

	actualPort := ln.Addr().(*net.TCPAddr).Port

	pf.mu.Lock()
	pf.state = StateRunning
	pf.actualPort = actualPort
	pf.err = nil
	pf.mu.Unlock()

	_, _ = fmt.Fprintf(m.output, "[%s] isolated: localhost:%d -> %s:%d (pod: %s)\n",
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

	// Close listener when context is cancelled to unblock Accept.
	go func() {
		<-fwCtx.Done()
		_ = ln.Close()
	}()

	// Supervise health checks, max-age, and preemptive reconnect in background.
	// When a fatal condition is detected, superviseCh receives the error and
	// fwCancel is called, which unblocks the Accept loop below.
	superviseCh := make(chan error, 1)
	go func() {
		superviseCh <- m.isolatedSupervise(fwCtx, fwCancel, pf, actualPort)
	}()

	for {
		tcpConn, acceptErr := ln.Accept()
		if acceptErr != nil {
			// Drain the supervisor result before deciding which error to surface.
			select {
			case supErr := <-superviseCh:
				return supErr
			default:
			}
			if fwCtx.Err() != nil {
				return fwCtx.Err()
			}
			return fmt.Errorf("accept: %w", acceptErr)
		}
		go m.isolatedHandleConn(fwCtx, pf, tcpConn)
	}
}

// isolatedSupervise runs the health-check ticker, max-connection-age timer, and
// preemptive-reconnect channel for an isolated port-forward session.
//
// Key differences from the mux health-check loop:
//   - Preemptive reconnect updates pf.currentPod in-place without cancelling
//     fwCtx, so existing per-client SPDY connections are unaffected.
//   - Max connection age clears pf.currentPod so the next client re-resolves
//     the pod rather than tearing down the listener.
//   - Stream errors are not monitored here; each client handles its own SPDY
//     failure locally in isolatedHandleConn.
func (m *Manager) isolatedSupervise(ctx context.Context, cancel context.CancelFunc, pf *portForward, actualPort int) error {
	ticker := time.NewTicker(m.healthCheckInterval)
	defer ticker.Stop()

	var maxAgeTimer *time.Timer
	var maxAgeCh <-chan time.Time
	if m.maxConnectionAge > 0 {
		maxAgeTimer = time.NewTimer(m.maxConnectionAge)
		defer maxAgeTimer.Stop()
		maxAgeCh = maxAgeTimer.C
	}

	failCount := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-maxAgeCh:
			// Re-resolve the pod on the next client connection rather than
			// dropping the listener; existing connections are unaffected.
			pf.mu.Lock()
			pf.currentPod = ""
			pf.currentTargetPort = 0
			pf.mu.Unlock()
			m.logger.Info("isolated: max connection age reached, will re-resolve pod on next connection",
				"service", pf.svc.Name,
				"max_age", m.maxConnectionAge,
			)
			maxAgeTimer.Reset(m.maxConnectionAge)

		case hintPod := <-pf.preemptCh:
			// Update the cached pod so new clients connect to the replacement pod.
			// Existing per-client SPDY connections are not interrupted.
			// Re-resolve rather than using hintPod directly so we also get targetPort.
			newPod, newTarget, resolveErr := m.resolvePod(ctx, pf.namespace, pf.svc)
			if resolveErr != nil {
				m.logger.Warn("isolated: preempt pod resolve failed, keeping current pod",
					"service", pf.svc.Name,
					"hint_pod", hintPod,
					"error", resolveErr,
				)
			} else {
				pf.mu.Lock()
				pf.currentPod = newPod
				pf.currentTargetPort = newTarget
				pf.mu.Unlock()
				m.logger.Info("isolated: updated current pod (new clients will use replacement)",
					"service", pf.svc.Name,
					"hint_pod", hintPod,
					"new_pod", newPod,
				)
			}

		case <-ticker.C:
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
				})
				if failCount >= m.healthCheckThreshold {
					cancel()
					return fmt.Errorf("health check failed %d consecutive times", failCount)
				}
			} else {
				failCount = 0
			}
		}
	}
}

// isolatedHandleConn handles a single incoming TCP connection by creating a
// dedicated SPDY tunnel to the k8s API server for that connection only.
func (m *Manager) isolatedHandleConn(ctx context.Context, pf *portForward, tcpConn net.Conn) {
	defer func() { _ = tcpConn.Close() }()

	pf.mu.Lock()
	podName := pf.currentPod
	targetPort := pf.currentTargetPort
	pf.mu.Unlock()

	// Re-resolve if pod was cleared (max-age expiry) or not yet set.
	if podName == "" {
		var err error
		podName, targetPort, err = m.resolvePod(ctx, pf.namespace, pf.svc)
		if err != nil {
			m.logger.Warn("isolated: resolve pod failed, dropping connection",
				"service", pf.svc.Name, "error", err)
			return
		}
		pf.mu.Lock()
		if pf.currentPod == "" {
			pf.currentPod = podName
			pf.currentTargetPort = targetPort
		}
		pf.mu.Unlock()
	}

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
		m.logger.Warn("isolated: create SPDY transport failed, dropping connection",
			"service", pf.svc.Name, "error", err)
		return
	}

	rawDialer := spdy.NewDialerForStreaming(entry.upgrader, entry.client, http.MethodPost, reqURL)

	netCfg, netErr := config.ResolveNetwork(m.cfg.Network, pf.svc.Network).Parse()
	if netErr != nil {
		m.logger.Warn("invalid network config, simulation disabled", "service", pf.svc.Name, "error", netErr)
	}
	if !pf.hasChaosOverride.Load() {
		chaosCfg, chaosErr := config.ResolveChaos(m.cfg.Chaos, pf.svc.Chaos).Parse()
		if chaosErr != nil {
			m.logger.Warn("invalid chaos config, injection disabled", "service", pf.svc.Name, "error", chaosErr)
		}
		pf.chaosOverride.Store(&chaosCfg)
	}

	dialer := &countingDialer{
		dialer:        rawDialer,
		counter:       &pf.counter,
		networkCfg:    netCfg,
		chaosPtr:      &pf.chaosOverride,
		ctx:           ctx,
		skipConnReset: true, // cumulative counters; don't reset on each per-client Dial
	}

	spdyConn, _, dialErr := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if dialErr != nil {
		m.logger.Warn("isolated: SPDY dial failed, dropping connection",
			"service", pf.svc.Name, "error", dialErr)
		m.transports.evict(m.restConfig.Host)
		return
	}
	defer func() { _ = spdyConn.Close() }()

	// requestID=1: each client gets its own SPDY connection, so stream IDs never collide.
	spdyForwardConn(ctx, tcpConn, spdyConn, targetPort, 1, m.logger, pf.svc.Name)
}

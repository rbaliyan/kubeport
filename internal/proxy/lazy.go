package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/streaming/pkg/httpstream"

	"github.com/rbaliyan/kubeport/internal/hook"
	"github.com/rbaliyan/kubeport/pkg/config"
)

// runLazyPortForward binds the local port immediately but defers SPDY tunnel
// establishment to the API server until the first TCP connection arrives.
// The SPDY connection is re-established inline when it is found to be closed,
// keeping the local listener bound throughout the lifetime of this call.
func (m *Manager) runLazyPortForward(ctx context.Context, pf *portForward) error {
	fwCtx, fwCancel := context.WithCancel(ctx)
	defer fwCancel()

	pf.mu.Lock()
	pf.cancel = fwCancel
	pf.mu.Unlock()

	listenAddr := fmt.Sprintf("localhost:%d", pf.svc.LocalPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("bind local port: %w", err)
	}
	defer func() { _ = ln.Close() }()

	actualPort := ln.Addr().(*net.TCPAddr).Port

	pf.mu.Lock()
	pf.state = StateWaiting
	pf.actualPort = actualPort
	pf.tunnelOpen = false
	pf.err = nil
	pf.mu.Unlock()

	_, _ = fmt.Fprintf(m.output, "[%s] lazy: localhost:%d -> :%d (waiting for first connection)\n",
		pf.svc.Name, actualPort, pf.svc.RemotePort)

	// Close the listener when the context is cancelled so Accept unblocks.
	go func() {
		<-fwCtx.Done()
		_ = ln.Close()
	}()

	var (
		spdyConn   httpstream.Connection
		targetPort int
		requestID  atomic.Int64
	)

	dialTunnel := func() error {
		pod, tPort, resolveErr := m.resolvePod(fwCtx, pf.namespace, pf.svc)
		if resolveErr != nil {
			return resolveErr
		}

		pf.mu.Lock()
		pf.currentPod = pod
		pf.mu.Unlock()

		reqURL := m.clientset.CoreV1().RESTClient().Post().
			Resource("pods").
			Namespace(pf.namespace).
			Name(pod).
			SubResource("portforward").
			URL()

		entry, entryErr := m.transports.getOrCreate(m.restConfig.Host, func() (http.RoundTripper, spdy.Upgrader, error) {
			return spdy.RoundTripperFor(m.restConfig)
		})
		if entryErr != nil {
			return fmt.Errorf("create SPDY transport: %w", entryErr)
		}

		rawDialer := spdy.NewDialerForStreaming(entry.upgrader, entry.client, http.MethodPost, reqURL)

		netCfg, netErr := config.ResolveNetwork(m.cfg.Network, pf.svc.Network).Parse()
		if netErr != nil {
			m.logger.Warn("invalid network config, simulation disabled", "service", pf.svc.Name, "error", netErr)
		}
		chaosCfg, chaosErr := config.ResolveChaos(m.cfg.Chaos, pf.svc.Chaos).Parse()
		if chaosErr != nil {
			m.logger.Warn("invalid chaos config, injection disabled", "service", pf.svc.Name, "error", chaosErr)
		}

		dialer := &countingDialer{
			dialer:     rawDialer,
			counter:    &pf.counter,
			networkCfg: netCfg,
			chaosCfg:   chaosCfg,
			ctx:        fwCtx,
		}

		c, _, dialErr := dialer.Dial(portforward.PortForwardProtocolV1Name)
		if dialErr != nil {
			return fmt.Errorf("dial SPDY: %w", dialErr)
		}

		spdyConn = c
		targetPort = tPort

		pf.mu.Lock()
		pf.state = StateRunning
		pf.tunnelOpen = true
		pf.mu.Unlock()

		_ = m.hooks.Fire(ctx, hook.Event{
			Type:       hook.EventForwardConnected,
			Time:       time.Now(),
			Service:    pf.svc.Name,
			ParentName: pf.svc.ParentName,
			PortName:   pf.svc.PortName,
			LocalPort:  actualPort,
			RemotePort: pf.svc.RemotePort,
			PodName:    pod,
		})

		return nil
	}

	for {
		tcpConn, acceptErr := ln.Accept()
		if acceptErr != nil {
			if fwCtx.Err() != nil {
				return fwCtx.Err()
			}
			return fmt.Errorf("accept: %w", acceptErr)
		}

		// Re-establish the tunnel if it has not been opened yet or has since closed.
		needsDial := spdyConn == nil || isConnClosed(spdyConn)

		if needsDial {
			if spdyConn != nil {
				_ = spdyConn.Close()
				spdyConn = nil
			}
			pf.mu.Lock()
			pf.state = StateWaiting
			pf.tunnelOpen = false
			pf.mu.Unlock()

			if dialErr := dialTunnel(); dialErr != nil {
				_ = tcpConn.Close()
				m.logger.Warn("lazy: SPDY dial failed, dropping connection",
					"service", pf.svc.Name,
					"error", dialErr,
				)
				m.transports.evict(m.restConfig.Host)
				return dialErr
			}
		}

		id := int(requestID.Add(1))
		go lazyForwardConn(fwCtx, tcpConn, spdyConn, targetPort, id, m.logger, pf.svc.Name)
	}
}

// lazyForwardConn forwards one TCP connection through an existing SPDY connection,
// following the k8s port-forward protocol: one data stream + one error stream per connection.
func lazyForwardConn(ctx context.Context, tcpConn net.Conn, spdyConn httpstream.Connection, remotePort, requestID int, logger *slog.Logger, svcName string) {
	defer func() { _ = tcpConn.Close() }()

	portStr := strconv.Itoa(remotePort)
	idStr := strconv.Itoa(requestID)

	// Error stream: server writes any forwarding errors here; we only read from it.
	errHeaders := http.Header{}
	errHeaders.Set(corev1.StreamType, corev1.StreamTypeError)
	errHeaders.Set(corev1.PortHeader, portStr)
	errHeaders.Set(corev1.PortForwardRequestIDHeader, idStr)
	errorStream, err := spdyConn.CreateStream(errHeaders)
	if err != nil {
		logger.Warn("lazy: create error stream failed", "service", svcName, "error", err)
		return
	}
	_ = errorStream.Close() // signal to server: we won't write here
	defer spdyConn.RemoveStreams(errorStream)

	remoteErrCh := make(chan error, 1)
	go func() {
		msg, readErr := io.ReadAll(errorStream)
		if readErr != nil {
			remoteErrCh <- fmt.Errorf("read error stream: %w", readErr)
		} else if len(msg) > 0 {
			remoteErrCh <- fmt.Errorf("remote: %s", msg)
		}
		close(remoteErrCh)
	}()

	// Data stream: bidirectional byte tunnel between TCP connection and pod.
	dataHeaders := http.Header{}
	dataHeaders.Set(corev1.StreamType, corev1.StreamTypeData)
	dataHeaders.Set(corev1.PortHeader, portStr)
	dataHeaders.Set(corev1.PortForwardRequestIDHeader, idStr)
	dataStream, err := spdyConn.CreateStream(dataHeaders)
	if err != nil {
		logger.Warn("lazy: create data stream failed", "service", svcName, "error", err)
		return
	}
	defer spdyConn.RemoveStreams(dataStream)

	remoteDone := make(chan struct{})
	go func() {
		defer close(remoteDone)
		_, _ = io.Copy(tcpConn, dataStream)
	}()

	localDone := make(chan struct{})
	go func() {
		defer close(localDone)
		defer func() { _ = dataStream.Close() }()
		_, _ = io.Copy(dataStream, tcpConn)
	}()

	select {
	case <-remoteDone:
	case <-localDone:
	case <-ctx.Done():
	case remoteErr := <-remoteErrCh:
		if remoteErr != nil {
			logger.Warn("lazy: remote forwarding error", "service", svcName, "error", remoteErr)
		}
	}
}

// isConnClosed reports whether an httpstream connection has already been closed.
func isConnClosed(conn httpstream.Connection) bool {
	select {
	case <-conn.CloseChan():
		return true
	default:
		return false
	}
}

package proxy

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HTTPProxyServer is an HTTP/HTTPS proxy that translates addresses using a Proxy.
// It handles CONNECT for HTTPS tunneling and forwards plain HTTP requests.
type HTTPProxyServer struct {
	proxy    Proxy
	listener net.Listener
	logger   *slog.Logger
	username string
	password string
	wg       sync.WaitGroup
}

// HTTPProxyOption configures the HTTP proxy server.
type HTTPProxyOption func(*HTTPProxyServer)

// WithHTTPProxyAuth sets basic authentication credentials.
// When set, clients must provide a Proxy-Authorization header.
func WithHTTPProxyAuth(username, password string) HTTPProxyOption {
	return func(s *HTTPProxyServer) {
		s.username = username
		s.password = password
	}
}

// WithHTTPProxyLogger sets the logger for the HTTP proxy server.
func WithHTTPProxyLogger(l *slog.Logger) HTTPProxyOption {
	return func(s *HTTPProxyServer) { s.logger = l }
}

// NewHTTPProxyServer creates an HTTP proxy server that uses the given Proxy for
// address translation. The server listens on the provided address
// (e.g., "127.0.0.1:3128").
func NewHTTPProxyServer(p Proxy, listenAddr string, opts ...HTTPProxyOption) (*HTTPProxyServer, error) {
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("http proxy listen: %w", err)
	}
	s := &HTTPProxyServer{
		proxy:    p,
		listener: lis,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Addr returns the listener's address.
func (s *HTTPProxyServer) Addr() net.Addr {
	return s.listener.Addr()
}

// Serve accepts connections until the listener is closed or ctx is cancelled.
func (s *HTTPProxyServer) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			s.logger.Warn("http proxy accept error", slog.String("error", err.Error()))
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}

	s.wg.Wait()
	return nil
}

// Close stops the HTTP proxy server.
func (s *HTTPProxyServer) Close() error {
	return s.listener.Close()
}

func (s *HTTPProxyServer) requiresAuth() bool {
	return s.username != "" || s.password != ""
}

func (s *HTTPProxyServer) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close() //nolint:errcheck

	// Set a deadline for reading the initial request to prevent slow-client goroutine leaks.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		s.logger.Debug("http proxy read request failed", slog.String("error", err.Error()))
		return
	}

	if s.requiresAuth() && !s.checkAuth(req) {
		resp := &http.Response{
			StatusCode: http.StatusProxyAuthRequired,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     http.Header{"Proxy-Authenticate": {"Basic realm=\"kubeport\""}},
			Body:       http.NoBody,
		}
		_ = resp.Write(conn)
		return
	}

	if req.Method == http.MethodConnect {
		s.handleConnect(ctx, conn, req)
	} else {
		s.handleHTTP(ctx, conn, br, req)
	}
}

func (s *HTTPProxyServer) checkAuth(req *http.Request) bool {
	auth := req.Header.Get("Proxy-Authorization")
	if auth == "" {
		return false
	}
	encoded, ok := strings.CutPrefix(auth, "Basic ")
	if !ok {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(user), []byte(s.username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(pass), []byte(s.password)) == 1
}

// handleConnect handles HTTPS tunneling via the CONNECT method.
func (s *HTTPProxyServer) handleConnect(ctx context.Context, conn net.Conn, req *http.Request) {
	addr := req.Host
	s.logger.Debug("http proxy CONNECT", slog.String("addr", addr))

	target, err := s.proxy.DialContext(ctx, "tcp", addr)
	if err != nil {
		resp := &http.Response{
			StatusCode: http.StatusBadGateway,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Body:       http.NoBody,
		}
		_ = resp.Write(conn)
		s.logger.Debug("http proxy dial failed", slog.String("addr", addr), slog.String("error", err.Error()))
		return
	}
	defer target.Close() //nolint:errcheck

	_, _ = fmt.Fprintf(conn, "HTTP/%d.%d 200 Connection Established\r\n\r\n", req.ProtoMajor, req.ProtoMinor)

	// Clear the handshake deadline before relaying data.
	_ = conn.SetDeadline(time.Time{})

	relay(conn, target)
}

// handleHTTP forwards plain HTTP requests, stripping proxy headers from each
// request in the keep-alive stream so credentials are never leaked upstream.
func (s *HTTPProxyServer) handleHTTP(ctx context.Context, conn net.Conn, br *bufio.Reader, req *http.Request) {
	addr := req.Host
	if !strings.Contains(addr, ":") {
		addr += ":80"
	}
	s.logger.Debug("http proxy forward", slog.String("addr", addr))

	target, err := s.proxy.DialContext(ctx, "tcp", addr)
	if err != nil {
		resp := &http.Response{
			StatusCode: http.StatusBadGateway,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Body:       http.NoBody,
		}
		_ = resp.Write(conn)
		s.logger.Debug("http proxy dial failed", slog.String("addr", addr), slog.String("error", err.Error()))
		return
	}
	defer target.Close() //nolint:errcheck

	// Clear the handshake deadline before forwarding data.
	_ = conn.SetDeadline(time.Time{})

	tbr := bufio.NewReader(target)
	for {
		// Strip proxy-specific headers from every request in the stream.
		req.Header.Del("Proxy-Authorization")
		req.Header.Del("Proxy-Connection")
		req.RequestURI = req.URL.RequestURI()

		if err := req.Write(target); err != nil {
			s.logger.Debug("http proxy write request failed", slog.String("error", err.Error()))
			return
		}

		resp, err := http.ReadResponse(tbr, req)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.logger.Debug("http proxy read response failed", slog.String("error", err.Error()))
			}
			return
		}

		keepAlive := !req.Close && !resp.Close
		if err := resp.Write(conn); err != nil {
			resp.Body.Close() //nolint:errcheck
			s.logger.Debug("http proxy write response failed", slog.String("error", err.Error()))
			return
		}
		resp.Body.Close() //nolint:errcheck

		if !keepAlive {
			return
		}

		// Read the next request in the keep-alive stream.
		req, err = http.ReadRequest(br)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.logger.Debug("http proxy read next request failed", slog.String("error", err.Error()))
			}
			return
		}

		// Reject requests that target a different host on the same connection.
		// Keep-alive connections are bound to the original backend; routing a
		// different host to it would silently misdeliver the request.
		nextAddr := req.Host
		if !strings.Contains(nextAddr, ":") {
			nextAddr += ":80"
		}
		if nextAddr != addr {
			resp := &http.Response{
				StatusCode: http.StatusBadRequest,
				ProtoMajor: 1,
				ProtoMinor: 1,
				Body:       http.NoBody,
			}
			_ = resp.Write(conn)
			s.logger.Debug("http proxy host changed mid-connection, closing",
				slog.String("original", addr), slog.String("new", nextAddr))
			return
		}
	}
}

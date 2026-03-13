package proxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestHTTPProxy_Connect(t *testing.T) {
	// Start an echo TCP server as the target.
	echoLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLis.Close()
	go acceptEcho(echoLis)

	p := &client{
		addrs:        map[string]string{"my-service:443": echoLis.Addr().String()},
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewHTTPProxyServer(p, "127.0.0.1:0", WithHTTPProxyLogger(logger))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Issue a CONNECT request through the proxy.
	conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT my-service:443 HTTP/1.1\r\nHost: my-service:443\r\n\r\n")

	// Read the 200 response.
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	resp := string(buf[:n])
	if resp[:12] != "HTTP/1.1 200" {
		t.Fatalf("expected 200, got %q", resp)
	}

	// Now the tunnel is established — send data and verify echo.
	msg := []byte("hello http proxy")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatal(err)
	}
	if string(echo) != string(msg) {
		t.Errorf("echo = %q, want %q", echo, msg)
	}
}

func TestHTTPProxy_PlainHTTP(t *testing.T) {
	// Start a simple HTTP server as the target.
	httpLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpLis.Close()
	httpSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s", r.Host)
	})}
	go httpSrv.Serve(httpLis)
	defer httpSrv.Close()

	p := &client{
		addrs:        map[string]string{"my-api:80": httpLis.Addr().String()},
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewHTTPProxyServer(p, "127.0.0.1:0", WithHTTPProxyLogger(logger))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Use Go's HTTP client with the proxy.
	proxyURL, _ := url.Parse("http://" + srv.Addr().String())
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}

	resp, err := client.Get("http://my-api:80/test")
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from my-api:80" {
		t.Errorf("body = %q, want 'hello from my-api:80'", body)
	}
}

func TestHTTPProxy_Auth(t *testing.T) {
	echoLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLis.Close()
	go acceptEcho(echoLis)

	p := &client{
		addrs:        map[string]string{"svc:443": echoLis.Addr().String()},
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewHTTPProxyServer(p, "127.0.0.1:0",
		WithHTTPProxyLogger(logger),
		WithHTTPProxyAuth("admin", "secret"),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// No auth → 407.
	conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "CONNECT svc:443 HTTP/1.1\r\nHost: svc:443\r\n\r\n")
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	if resp := string(buf[:n]); resp[:12] != "HTTP/1.1 407" {
		t.Fatalf("no auth: expected 407, got %q", resp)
	}
	conn.Close()

	// Wrong auth → 407.
	conn, err = net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	creds := base64.StdEncoding.EncodeToString([]byte("admin:wrong"))
	fmt.Fprintf(conn, "CONNECT svc:443 HTTP/1.1\r\nHost: svc:443\r\nProxy-Authorization: Basic %s\r\n\r\n", creds)
	n, _ = conn.Read(buf)
	if resp := string(buf[:n]); resp[:12] != "HTTP/1.1 407" {
		t.Fatalf("wrong auth: expected 407, got %q", resp)
	}
	conn.Close()

	// Correct auth → 200.
	conn, err = net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	creds = base64.StdEncoding.EncodeToString([]byte("admin:secret"))
	fmt.Fprintf(conn, "CONNECT svc:443 HTTP/1.1\r\nHost: svc:443\r\nProxy-Authorization: Basic %s\r\n\r\n", creds)
	n, _ = conn.Read(buf)
	if resp := string(buf[:n]); resp[:12] != "HTTP/1.1 200" {
		t.Fatalf("correct auth: expected 200, got %q", resp)
	}
}

func TestHTTPProxy_ConnectFailure(t *testing.T) {
	p := &client{
		addrs:        map[string]string{},
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewHTTPProxyServer(p, "127.0.0.1:0", WithHTTPProxyLogger(logger))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	conn, err := net.DialTimeout("tcp", srv.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT 127.0.0.1:1 HTTP/1.1\r\nHost: 127.0.0.1:1\r\n\r\n")
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	if resp := string(buf[:n]); resp[:12] != "HTTP/1.1 502" {
		t.Fatalf("expected 502, got %q", resp)
	}
}

func TestHTTPProxy_Close(t *testing.T) {
	p := Noop()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewHTTPProxyServer(p, "127.0.0.1:0", WithHTTPProxyLogger(logger))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		srv.Serve(ctx)
		close(done)
	}()

	cancel()
	srv.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
}

// acceptEcho accepts connections and echoes data back.
func acceptEcho(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			io.Copy(conn, conn)
		}()
	}
}

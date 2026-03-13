package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestSOCKSServer_ConnectAndRelay(t *testing.T) {
	// Start a TCP echo server as the target.
	echoLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLis.Close()
	go func() {
		for {
			conn, err := echoLis.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()

	echoHost, echoPort, _ := net.SplitHostPort(echoLis.Addr().String())

	// Create a proxy that maps "my-service:8080" → echo server.
	p := &client{
		addrs: map[string]string{
			"my-service:8080": echoLis.Addr().String(),
		},
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// Start SOCKS server.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewSOCKSServer(p, "127.0.0.1:0", WithSOCKSLogger(logger))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Connect via SOCKS5 to "my-service:8080" (translated to echo server).
	conn, err := socksConnect(srv.Addr().String(), "my-service", "8080")
	if err != nil {
		t.Fatalf("socks connect: %v", err)
	}
	defer conn.Close()

	// Send data and verify echo.
	msg := []byte("hello kubeport socks")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(msg) {
		t.Errorf("echo = %q, want %q", buf, msg)
	}

	// Also test direct IP connect (no translation needed).
	conn2, err := socksConnect(srv.Addr().String(), echoHost, echoPort)
	if err != nil {
		t.Fatalf("socks direct connect: %v", err)
	}
	defer conn2.Close()
	if _, err := conn2.Write(msg); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn2, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(msg) {
		t.Errorf("direct echo = %q, want %q", buf, msg)
	}
}

func TestSOCKSServer_HeadlessFQDN(t *testing.T) {
	// Start echo server.
	echoLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLis.Close()
	go func() {
		for {
			conn, err := echoLis.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()

	// Map pod name only — SOCKS should resolve FQDN via fuzzy match.
	p := &client{
		addrs: map[string]string{
			"redis-node-0:6379": echoLis.Addr().String(),
		},
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewSOCKSServer(p, "127.0.0.1:0", WithSOCKSLogger(logger))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Connect using headless service FQDN.
	conn, err := socksConnect(srv.Addr().String(), "redis-node-0.redis-headless.dev.svc.cluster.local", "6379")
	if err != nil {
		t.Fatalf("socks connect FQDN: %v", err)
	}
	defer conn.Close()

	msg := []byte("ping")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Errorf("echo = %q, want ping", buf)
	}
}

func TestSOCKSServer_ConnectFailure(t *testing.T) {
	// Proxy pointing to nothing.
	p := &client{
		addrs:        map[string]string{},
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewSOCKSServer(p, "127.0.0.1:0", WithSOCKSLogger(logger))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Try connecting to an address that's not listening.
	_, err = socksConnect(srv.Addr().String(), "127.0.0.1", "1")
	if err == nil {
		t.Fatal("expected SOCKS connect to fail")
	}
}

func TestSOCKSServer_Close(t *testing.T) {
	p := Noop()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewSOCKSServer(p, "127.0.0.1:0", WithSOCKSLogger(logger))
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

func TestSOCKSServer_Auth(t *testing.T) {
	echoLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLis.Close()
	go func() {
		for {
			conn, err := echoLis.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()

	p := &client{
		addrs:        map[string]string{"svc:80": echoLis.Addr().String()},
		shutdownChan: make(chan struct{}),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewSOCKSServer(p, "127.0.0.1:0",
		WithSOCKSLogger(logger),
		WithSOCKSAuth("admin", "secret"),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	defer srv.Close()

	// Correct credentials should succeed.
	conn, err := socksConnectAuth(srv.Addr().String(), "svc", "80", "admin", "secret")
	if err != nil {
		t.Fatalf("auth connect: %v", err)
	}
	conn.Close()

	// Wrong credentials should fail.
	_, err = socksConnectAuth(srv.Addr().String(), "svc", "80", "admin", "wrong")
	if err == nil {
		t.Fatal("expected auth failure with wrong password")
	}

	// No auth should fail when server requires it.
	_, err = socksConnect(srv.Addr().String(), "svc", "80")
	if err == nil {
		t.Fatal("expected failure when no auth provided to auth-required server")
	}
}

// socksConnect performs a SOCKS5 handshake (no auth) and CONNECT to host:port.
func socksConnect(proxyAddr, host, port string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		return nil, err
	}

	// Greeting: version 5, 1 method (no auth)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}

	// Read server choice
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks auth rejected: %x", resp)
	}

	// CONNECT request: VER CMD RSV ATYP(domain) LEN DOMAIN PORT
	portNum, _ := net.LookupPort("tcp", port)
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(portNum>>8), byte(portNum&0xFF))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	// Read reply (at least 10 bytes for IPv4 reply)
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		conn.Close()
		return nil, err
	}
	if reply[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks connect failed: reply code %d", reply[1])
	}

	return conn, nil
}

// socksConnectAuth performs a SOCKS5 handshake with username/password auth.
func socksConnectAuth(proxyAddr, host, port, user, pass string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		return nil, err
	}

	// Greeting: version 5, 1 method (username/password)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		conn.Close()
		return nil, err
	}

	// Read server choice
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	if resp[0] != 0x05 || resp[1] != 0x02 {
		conn.Close()
		return nil, fmt.Errorf("server did not select userpass auth: %x", resp)
	}

	// Send username/password: VER ULEN UNAME PLEN PASSWD
	auth := []byte{0x01, byte(len(user))}
	auth = append(auth, []byte(user)...)
	auth = append(auth, byte(len(pass)))
	auth = append(auth, []byte(pass)...)
	if _, err := conn.Write(auth); err != nil {
		conn.Close()
		return nil, err
	}

	// Read auth response
	authResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, authResp); err != nil {
		conn.Close()
		return nil, err
	}
	if authResp[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("auth failed: status %d", authResp[1])
	}

	// CONNECT request
	portNum, _ := net.LookupPort("tcp", port)
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(portNum>>8), byte(portNum&0xFF))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	// Read reply
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		conn.Close()
		return nil, err
	}
	if reply[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks connect failed: reply code %d", reply[1])
	}

	return conn, nil
}

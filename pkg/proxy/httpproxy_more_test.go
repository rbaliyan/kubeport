package proxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestCheckAuth(t *testing.T) {
	s := &HTTPProxyServer{username: "admin", password: "secret"}

	mkReq := func(authHeader string) *http.Request {
		r := &http.Request{Header: http.Header{}}
		if authHeader != "" {
			r.Header.Set("Proxy-Authorization", authHeader)
		}
		return r
	}
	validCreds := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret"))

	tests := []struct {
		name string
		auth string
		want bool
	}{
		{"valid credentials", validCreds, true},
		{"missing header", "", false},
		{"non-basic scheme", "Bearer xyz", false},
		{"invalid base64", "Basic !!!notbase64", false},
		{"missing colon separator", "Basic " + base64.StdEncoding.EncodeToString([]byte("adminsecret")), false},
		{"wrong password", "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:wrong")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := s.checkAuth(mkReq(tt.auth)); got != tt.want {
				t.Errorf("checkAuth(%q) = %v, want %v", tt.auth, got, tt.want)
			}
		})
	}
}

func TestHandleHTTP_PlainHTTPDialFailure(t *testing.T) {
	// Proxy with no mappings; the plain-HTTP forward path will fail to dial the
	// unresolvable host and must return 502.
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

	// Plain GET (not CONNECT) to an unresolvable host on a closed port.
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: 127.0.0.1:1\r\n\r\n")
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	if resp := string(buf[:n]); len(resp) < 12 || resp[:12] != "HTTP/1.1 502" {
		t.Fatalf("expected 502 on dial failure, got %q", resp)
	}
}

func TestHandleConnect_DialFailureReturns502(t *testing.T) {
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
	if resp := string(buf[:n]); len(resp) < 12 || resp[:12] != "HTTP/1.1 502" {
		t.Fatalf("expected 502, got %q", resp)
	}
}

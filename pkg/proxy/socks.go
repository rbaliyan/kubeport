package proxy

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

// SOCKSServer is a SOCKS5 proxy that translates addresses using a Proxy.
// It supports the CONNECT command only (no BIND or UDP ASSOCIATE).
type SOCKSServer struct {
	proxy    Proxy
	listener net.Listener
	logger   *slog.Logger
	username string
	password string
	wg       sync.WaitGroup
}

// SOCKSOption configures the SOCKS server.
type SOCKSOption func(*SOCKSServer)

// WithSOCKSAuth sets username/password authentication (RFC 1929).
// When set, clients must authenticate with these credentials.
func WithSOCKSAuth(username, password string) SOCKSOption {
	return func(s *SOCKSServer) {
		s.username = username
		s.password = password
	}
}

// WithSOCKSLogger sets the logger for the SOCKS server.
func WithSOCKSLogger(l *slog.Logger) SOCKSOption {
	return func(s *SOCKSServer) { s.logger = l }
}

// NewSOCKSServer creates a SOCKS5 server that uses the given Proxy for
// address translation. The server listens on the provided address
// (e.g., "127.0.0.1:1080").
func NewSOCKSServer(p Proxy, listenAddr string, opts ...SOCKSOption) (*SOCKSServer, error) {
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("socks listen: %w", err)
	}
	s := &SOCKSServer{
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
func (s *SOCKSServer) Addr() net.Addr {
	return s.listener.Addr()
}

// Serve accepts connections until the listener is closed or ctx is cancelled.
func (s *SOCKSServer) Serve(ctx context.Context) error {
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
			s.logger.Warn("socks accept error", slog.String("error", err.Error()))
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

// Close stops the SOCKS server.
func (s *SOCKSServer) Close() error {
	return s.listener.Close()
}

// SOCKS5 constants.
const (
	socks5Version    = 0x05
	authNone         = 0x00
	authUserPass     = 0x02
	authNoAcceptable = 0xFF
	cmdConnect       = 0x01
	addrIPv4         = 0x01
	addrDomain       = 0x03
	addrIPv6         = 0x04

	// Username/password auth sub-negotiation (RFC 1929).
	authUserPassVersion = 0x01
	authSuccess         = 0x00
	authFailure         = 0x01
)

// SOCKS5 reply codes.
const (
	replySuccess             = 0x00
	replyGeneralFailure      = 0x01
	replyCommandNotSupported = 0x07
	replyAddrNotSupported    = 0x08
)

func (s *SOCKSServer) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close() //nolint:errcheck // best-effort close on connection teardown

	// Set a deadline for the handshake phase to prevent slow-client goroutine leaks.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// 1. Greeting: client sends version + auth methods
	if err := s.negotiate(conn); err != nil {
		s.logger.Debug("socks negotiate failed", slog.String("error", err.Error()))
		return
	}

	// 2. Request: client sends CONNECT with target address
	addr, err := s.readRequest(conn)
	if err != nil {
		s.logger.Debug("socks request failed", slog.String("error", err.Error()))
		return
	}

	s.logger.Debug("socks connect", slog.String("addr", addr))

	// 3. Connect to target via proxy (address translation happens here)
	target, err := s.proxy.DialContext(ctx, "tcp", addr)
	if err != nil {
		s.sendReply(conn, replyGeneralFailure)
		s.logger.Debug("socks dial failed", slog.String("addr", addr), slog.String("error", err.Error()))
		return
	}
	defer target.Close() //nolint:errcheck // best-effort close on connection teardown

	// 4. Send success reply
	s.sendReply(conn, replySuccess)

	// Clear the handshake deadline before relaying data.
	_ = conn.SetDeadline(time.Time{})

	// 5. Bidirectional copy
	relay(conn, target)
}

func (s *SOCKSServer) requiresAuth() bool {
	return s.username != "" || s.password != ""
}

func (s *SOCKSServer) negotiate(conn net.Conn) error {
	// Read: VER | NMETHODS | METHODS
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != socks5Version {
		return fmt.Errorf("unsupported SOCKS version: %d", buf[0])
	}

	nMethods := int(buf[1])
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}

	if s.requiresAuth() {
		return s.negotiateUserPass(conn, methods)
	}

	// No auth required — accept if client offers it.
	for _, m := range methods {
		if m == authNone {
			_, err := conn.Write([]byte{socks5Version, authNone})
			return err
		}
	}
	_, _ = conn.Write([]byte{socks5Version, authNoAcceptable})
	return fmt.Errorf("no acceptable auth method")
}

func (s *SOCKSServer) negotiateUserPass(conn net.Conn, methods []byte) error {
	// Check if client supports username/password auth.
	hasUserPass := false
	for _, m := range methods {
		if m == authUserPass {
			hasUserPass = true
			break
		}
	}
	if !hasUserPass {
		_, _ = conn.Write([]byte{socks5Version, authNoAcceptable})
		return fmt.Errorf("client does not support username/password auth")
	}

	// Select username/password auth.
	if _, err := conn.Write([]byte{socks5Version, authUserPass}); err != nil {
		return err
	}

	// RFC 1929: Read VER | ULEN | UNAME | PLEN | PASSWD
	ver := make([]byte, 1)
	if _, err := io.ReadFull(conn, ver); err != nil {
		return err
	}
	if ver[0] != authUserPassVersion {
		return fmt.Errorf("unsupported auth version: %d", ver[0])
	}

	ulenBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, ulenBuf); err != nil {
		return err
	}
	uname := make([]byte, ulenBuf[0])
	if _, err := io.ReadFull(conn, uname); err != nil {
		return err
	}

	plenBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, plenBuf); err != nil {
		return err
	}
	passwd := make([]byte, plenBuf[0])
	if _, err := io.ReadFull(conn, passwd); err != nil {
		return err
	}

	if subtle.ConstantTimeCompare(uname, []byte(s.username)) != 1 ||
		subtle.ConstantTimeCompare(passwd, []byte(s.password)) != 1 {
		_, _ = conn.Write([]byte{authUserPassVersion, authFailure})
		return fmt.Errorf("authentication failed")
	}

	_, err := conn.Write([]byte{authUserPassVersion, authSuccess})
	return err
}

func (s *SOCKSServer) readRequest(conn net.Conn) (string, error) {
	// Read: VER | CMD | RSV | ATYP
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", err
	}
	if buf[0] != socks5Version {
		return "", fmt.Errorf("unsupported version: %d", buf[0])
	}
	if buf[1] != cmdConnect {
		s.sendReply(conn, replyCommandNotSupported)
		return "", fmt.Errorf("unsupported command: %d", buf[1])
	}

	var host string
	switch buf[3] {
	case addrIPv4:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", err
		}
		host = net.IP(ip).String()

	case addrDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", err
		}
		domain := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", err
		}
		host = string(domain)

	case addrIPv6:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", err
		}
		host = net.IP(ip).String()

	default:
		s.sendReply(conn, replyAddrNotSupported)
		return "", fmt.Errorf("unsupported address type: %d", buf[3])
	}

	// Read port (2 bytes, big-endian)
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)

	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func (s *SOCKSServer) sendReply(conn net.Conn, reply byte) {
	// VER | REP | RSV | ATYP(IPv4) | BND.ADDR(0.0.0.0) | BND.PORT(0)
	_, _ = conn.Write([]byte{
		socks5Version, reply, 0x00, addrIPv4,
		0, 0, 0, 0, // bind addr
		0, 0, // bind port
	})
}


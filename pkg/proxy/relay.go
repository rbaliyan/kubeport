package proxy

import (
	"bufio"
	"io"
	"net"
)

// relay performs bidirectional data transfer between two connections.
// It blocks until both directions are complete.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		// Signal half-close so the other direction finishes.
		// Use an interface check so this works with both *net.TCPConn
		// and wrapper types like bufferedConn.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// bufferedConn wraps a net.Conn so that reads first drain a bufio.Reader's
// internal buffer before reading from the underlying connection.
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.br.Read(p)
}

// CloseWrite delegates to the underlying connection if it supports half-close.
func (c *bufferedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

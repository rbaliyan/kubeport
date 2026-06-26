package proxy

import (
	"fmt"
	"io"
	"net"
	"testing"
)

// tcpPair returns two ends of a connected TCP loopback socket. Unlike net.Pipe,
// *net.TCPConn implements CloseWrite, which relay uses to propagate half-close
// so each copy direction can terminate independently.
func tcpPair(b *testing.B) (client, server net.Conn) {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()

	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	r := <-ch
	if r.err != nil {
		b.Fatalf("accept: %v", r.err)
	}
	return client, r.c
}

// BenchmarkRelay measures the throughput of relay's per-direction copy loop.
//
// relay shuttles bytes between two connections with one io.Copy per direction
// until both reach EOF (relying on CloseWrite for half-close). The interesting,
// hot cost is that byte movement — not the listen/dial/accept handshake that
// sets up the sockets. An earlier version created a fresh connected pair inside
// the timed loop, so the reported numbers were dominated by socket setup and
// teardown rather than the copy.
//
// To time only the copy data path, the benchmark sets up a single persistent
// connected TCP pair once, outside b.Loop(), and starts a long-lived reader
// goroutine that drains the receiving end with io.Copy(io.Discard, ...) — the
// exact call relay uses for each direction. Each iteration then writes one
// payload-sized chunk into the sending end; SetBytes(payloadLen) makes the
// reported MB/s reflect copying payloadLen bytes through the kernel socket
// buffer, and ReportAllocs reflects only the steady-state copy (the io.Copy
// internal buffer is amortized across iterations), not per-connection setup.
//
// The drain goroutine is stopped after the timed region by closing the write
// end, which lets its io.Copy return so the goroutine does not leak.
func BenchmarkRelay(b *testing.B) {
	chunkSizes := []struct {
		name string
		size int
	}{
		{"1KiB", 1 << 10},
		{"64KiB", 64 << 10},
		{"1MiB", 1 << 20},
	}

	for _, cs := range chunkSizes {
		payload := make([]byte, cs.size)
		b.Run(fmt.Sprintf("chunk=%s", cs.name), func(b *testing.B) {
			writeEnd, readEnd := tcpPair(b)

			// Drain the receiving end for the lifetime of the benchmark using the
			// same io.Copy call relay performs per direction. It returns when
			// writeEnd is closed below.
			drained := make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, readEnd)
				close(drained)
			}()

			b.ReportAllocs()
			b.SetBytes(int64(cs.size))
			for b.Loop() {
				if _, err := writeEnd.Write(payload); err != nil {
					b.Fatalf("write: %v", err)
				}
			}
			b.StopTimer()

			_ = writeEnd.Close()
			<-drained
			_ = readEnd.Close()
		})
	}
}

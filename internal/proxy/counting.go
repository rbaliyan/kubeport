package proxy

import (
	"net/http"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
)

// byteCounter tracks cumulative bytes read and written across all streams.
type byteCounter struct {
	bytesIn  atomic.Int64
	bytesOut atomic.Int64
}

// countingDialer wraps an httpstream.Dialer to count bytes on all streams.
type countingDialer struct {
	dialer  httpstream.Dialer
	counter *byteCounter
}

func (d *countingDialer) Dial(protocols ...string) (httpstream.Connection, string, error) {
	conn, proto, err := d.dialer.Dial(protocols...)
	if err != nil {
		return nil, proto, err
	}
	return &countingConnection{conn: conn, counter: d.counter}, proto, nil
}

// countingConnection wraps an httpstream.Connection to count bytes on created streams.
type countingConnection struct {
	conn    httpstream.Connection
	counter *byteCounter
}

func (c *countingConnection) CreateStream(headers http.Header) (httpstream.Stream, error) {
	stream, err := c.conn.CreateStream(headers)
	if err != nil {
		return nil, err
	}
	return &countingStream{stream: stream, counter: c.counter}, nil
}

func (c *countingConnection) Close() error                        { return c.conn.Close() }
func (c *countingConnection) CloseChan() <-chan bool               { return c.conn.CloseChan() }
func (c *countingConnection) SetIdleTimeout(timeout time.Duration) { c.conn.SetIdleTimeout(timeout) }
func (c *countingConnection) RemoveStreams(streams ...httpstream.Stream) {
	c.conn.RemoveStreams(streams...)
}

// countingStream wraps an httpstream.Stream to count bytes read and written.
type countingStream struct {
	stream  httpstream.Stream
	counter *byteCounter
}

func (s *countingStream) Read(p []byte) (int, error) {
	n, err := s.stream.Read(p)
	if n > 0 {
		s.counter.bytesIn.Add(int64(n))
	}
	return n, err
}

func (s *countingStream) Write(p []byte) (int, error) {
	n, err := s.stream.Write(p)
	if n > 0 {
		s.counter.bytesOut.Add(int64(n))
	}
	return n, err
}

func (s *countingStream) Close() error              { return s.stream.Close() }
func (s *countingStream) Reset() error              { return s.stream.Reset() }
func (s *countingStream) Headers() http.Header      { return s.stream.Headers() }
func (s *countingStream) Identifier() uint32        { return s.stream.Identifier() }

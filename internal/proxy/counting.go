package proxy

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// byteCounter tracks cumulative bytes and stream health across all streams.
type byteCounter struct {
	bytesIn      atomic.Int64
	bytesOut     atomic.Int64
	streamErrors atomic.Int64 // stream creation failures (per-connection, reset on reconnect)
	chaos        chaosCounters
}

// countingDialer wraps an httpstream.Dialer to count bytes on all streams
// and optionally apply network simulation (latency/jitter/bandwidth) and chaos injection.
type countingDialer struct {
	dialer     httpstream.Dialer
	counter    *byteCounter
	networkCfg config.ParsedNetworkConfig
	chaosCfg   config.ParsedChaosConfig
	ctx        context.Context
}

func (d *countingDialer) Dial(protocols ...string) (httpstream.Connection, string, error) {
	conn, proto, err := d.dialer.Dial(protocols...)
	if err != nil {
		return nil, proto, err
	}
	// Reset per-connection stream error count on fresh connection
	d.counter.streamErrors.Store(0)

	cc := &countingConnection{conn: conn, counter: d.counter, ctx: d.ctx}
	if d.networkCfg.IsEnabled() {
		cc.networkCfg = d.networkCfg
		if d.networkCfg.BytesPerSec > 0 {
			cc.limiter = newRateLimiter(d.networkCfg.BytesPerSec)
		}
	}
	if d.chaosCfg.IsEnabled() {
		cc.chaosCfg = d.chaosCfg
	}
	return cc, proto, nil
}

// countingConnection wraps an httpstream.Connection to count bytes on created streams
// and detect SPDY connection degradation via stream creation failures.
type countingConnection struct {
	conn       httpstream.Connection
	counter    *byteCounter
	networkCfg config.ParsedNetworkConfig
	chaosCfg   config.ParsedChaosConfig
	limiter    *rateLimiter // shared across all streams; nil when no bandwidth cap
	ctx        context.Context
}

func (c *countingConnection) CreateStream(headers http.Header) (httpstream.Stream, error) {
	stream, err := c.conn.CreateStream(headers)
	if err != nil {
		// Stream creation failure indicates SPDY connection degradation.
		// The 30-second CreateStream timeout in client-go means every failure
		// cost a client 30s of waiting before their connection was dropped.
		c.counter.streamErrors.Add(1)
		return nil, err
	}

	// Wrap with throttling if network simulation is configured.
	inner := httpstream.Stream(stream)
	if c.networkCfg.IsEnabled() {
		inner = &throttledStream{
			stream:  stream,
			ctx:     c.ctx,
			latency: c.networkCfg.Latency,
			jitter:  c.networkCfg.Jitter,
			limiter: c.limiter,
		}
	}
	// Wrap with chaos injection if configured.
	if c.chaosCfg.IsEnabled() {
		inner = &chaosStream{
			stream:   inner,
			ctx:      c.ctx,
			cfg:      c.chaosCfg,
			counters: &c.counter.chaos,
		}
	}
	return &countingStream{stream: inner, counter: c.counter}, nil
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

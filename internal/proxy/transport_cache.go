package proxy

import (
	"net/http"
	"sync"
	"time"

	"k8s.io/client-go/transport/spdy"
)

// transportEntry holds a cached SPDY transport pair.
type transportEntry struct {
	transport http.RoundTripper
	upgrader  spdy.Upgrader
	client    *http.Client
	createdAt time.Time
}

// transportCache caches SPDY transports to avoid repeated TLS handshakes.
// All forwards sharing the same API server reuse the same transport.
type transportCache struct {
	mu      sync.Mutex
	entries map[string]*transportEntry
	maxAge  time.Duration
}

func newTransportCache(maxAge time.Duration) *transportCache {
	return &transportCache{
		entries: make(map[string]*transportEntry),
		maxAge:  maxAge,
	}
}

// getOrCreate returns a cached transport or creates a new one via factory.
func (c *transportCache) getOrCreate(key string, factory func() (http.RoundTripper, spdy.Upgrader, error)) (*transportEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[key]; ok {
		if c.maxAge <= 0 || time.Since(e.createdAt) < c.maxAge {
			return e, nil
		}
		// Expired — fall through to create new entry.
		delete(c.entries, key)
	}

	transport, upgrader, err := factory()
	if err != nil {
		return nil, err
	}

	e := &transportEntry{
		transport: transport,
		upgrader:  upgrader,
		client:    &http.Client{Transport: transport},
		createdAt: time.Now(),
	}
	c.entries[key] = e
	return e, nil
}

// evict removes a cached entry (e.g., when SPDY degradation is detected).
func (c *transportCache) evict(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// close removes all cached entries.
func (c *transportCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*transportEntry)
}

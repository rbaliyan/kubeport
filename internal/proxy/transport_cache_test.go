package proxy

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/transport/spdy"
)

// mockFactory returns a factory function that counts invocations.
func mockFactory(calls *atomic.Int32) func() (http.RoundTripper, spdy.Upgrader, error) {
	return func() (http.RoundTripper, spdy.Upgrader, error) {
		calls.Add(1)
		return http.DefaultTransport, nil, nil
	}
}

func TestTransportCache_GetOrCreate_CachesEntry(t *testing.T) {
	c := newTransportCache(time.Hour)
	var calls atomic.Int32

	e1, err := c.getOrCreate("host1", mockFactory(&calls))
	if err != nil {
		t.Fatal(err)
	}

	e2, err := c.getOrCreate("host1", mockFactory(&calls))
	if err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 1 {
		t.Fatalf("factory called %d times, want 1", calls.Load())
	}
	if e1 != e2 {
		t.Fatal("expected same entry on cache hit")
	}
}

func TestTransportCache_GetOrCreate_DifferentKeys(t *testing.T) {
	c := newTransportCache(time.Hour)
	var calls atomic.Int32

	_, _ = c.getOrCreate("host1", mockFactory(&calls))
	_, _ = c.getOrCreate("host2", mockFactory(&calls))

	if calls.Load() != 2 {
		t.Fatalf("factory called %d times, want 2", calls.Load())
	}
}

func TestTransportCache_GetOrCreate_Expiry(t *testing.T) {
	c := newTransportCache(1 * time.Millisecond)
	var calls atomic.Int32

	_, _ = c.getOrCreate("host1", mockFactory(&calls))
	time.Sleep(5 * time.Millisecond)
	_, _ = c.getOrCreate("host1", mockFactory(&calls))

	if calls.Load() != 2 {
		t.Fatalf("factory called %d times, want 2 (expired entry should be replaced)", calls.Load())
	}
}

func TestTransportCache_Evict(t *testing.T) {
	c := newTransportCache(time.Hour)
	var calls atomic.Int32

	_, _ = c.getOrCreate("host1", mockFactory(&calls))
	c.evict("host1")
	_, _ = c.getOrCreate("host1", mockFactory(&calls))

	if calls.Load() != 2 {
		t.Fatalf("factory called %d times, want 2 (evicted entry should be replaced)", calls.Load())
	}
}

func TestTransportCache_Close(t *testing.T) {
	c := newTransportCache(time.Hour)
	var calls atomic.Int32

	_, _ = c.getOrCreate("host1", mockFactory(&calls))
	_, _ = c.getOrCreate("host2", mockFactory(&calls))
	c.close()
	_, _ = c.getOrCreate("host1", mockFactory(&calls))

	if calls.Load() != 3 {
		t.Fatalf("factory called %d times, want 3", calls.Load())
	}
}

func TestTransportCache_ConcurrentAccess(t *testing.T) {
	c := newTransportCache(time.Hour)
	var calls atomic.Int32
	factory := mockFactory(&calls)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.getOrCreate("host1", factory)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("factory called %d times, want 1 (concurrent access should share)", calls.Load())
	}
}

func TestTransportCache_FactoryError(t *testing.T) {
	c := newTransportCache(time.Hour)
	errFactory := func() (http.RoundTripper, spdy.Upgrader, error) {
		return nil, nil, fmt.Errorf("connection refused")
	}

	_, err := c.getOrCreate("host1", errFactory)
	if err == nil {
		t.Fatal("expected error from factory")
	}

	// Error should not be cached — next call with working factory succeeds.
	var calls atomic.Int32
	_, err = c.getOrCreate("host1", mockFactory(&calls))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory called %d times, want 1", calls.Load())
	}
}

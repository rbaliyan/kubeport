package proxy

import (
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// Note: Manager.Status is intentionally not benchmarked here. It calls
// netutil.IsPortOpen, which performs a real TCP dial per forward, so it cannot
// be measured cleanly without a production seam to stub the probe. Mappings is
// the pure in-memory projection and is the high-value benchmark target.

// newMappingsManager builds a Manager whose forward map holds n running,
// service-backed forwards. Each forward expands into four DNS-name mappings, so
// the result slice grows as 4*n.
func newMappingsManager(n int) *Manager {
	m := &Manager{
		cfg:      &config.Config{Context: "test", Namespace: "default"},
		forwards: make(map[string]*portForward, n),
		order:    make([]string, 0, n),
		output:   io.Discard,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for i := range n {
		name := fmt.Sprintf("svc-%d", i)
		pf := &portForward{
			svc: config.ServiceConfig{
				Name:       name,
				Service:    name,
				Namespace:  fmt.Sprintf("ns-%d", i),
				RemotePort: 8000 + i,
			},
			state:      StateRunning,
			actualPort: 20000 + i,
		}
		m.forwards[name] = pf
		m.order = append(m.order, name)
	}
	return m
}

// BenchmarkManagerMappings measures the cost of projecting all running forwards
// into their Kubernetes DNS-name variants, over a range of forward counts.
func BenchmarkManagerMappings(b *testing.B) {
	for _, n := range []int{1, 10, 100, 500} {
		m := newMappingsManager(n)
		b.Run(fmt.Sprintf("forwards=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var sink []AddressMapping
			for b.Loop() {
				sink = m.Mappings("cluster.local")
			}
			_ = sink
		})
	}
}

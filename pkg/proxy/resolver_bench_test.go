package proxy

import (
	"fmt"
	"testing"
)

// buildAddrs builds a mapping table of the requested size whose keys cover the
// host:port and host-only forms resolveAddr probes, so a benchmark can target a
// specific lookup tier regardless of table size.
func buildAddrs(size int) map[string]string {
	addrs := make(map[string]string, size*2)
	for i := range size {
		// Exact host:port key plus a host-only key, mirroring how the SDK
		// registers both forms for a forwarded service.
		hostPort := fmt.Sprintf("svc-%d.ns-%d.svc.cluster.local:6379", i, i)
		host := fmt.Sprintf("svc-%d.ns-%d.svc.cluster.local", i, i)
		local := fmt.Sprintf("localhost:%d", 20000+i)
		addrs[hostPort] = local
		addrs[host] = local
	}
	// Keys exercised by the fuzzy tiers: namespace-qualified pod and short pod.
	addrs["pod-0.prod:6379"] = "localhost:30000"
	addrs["pod-0:6379"] = "localhost:30001"
	return addrs
}

// BenchmarkResolveAddr measures the SDK address-translation hot path across each
// lookup tier and over a range of mapping-table sizes.
func BenchmarkResolveAddr(b *testing.B) {
	tiers := []struct {
		name  string
		addr  string
		fuzzy bool
	}{
		// First map probe hits.
		{"exact-hit", "svc-0.ns-0.svc.cluster.local:6379", true},
		// host:port miss, host-only key hits.
		{"host-only-hit", "svc-0.ns-0.svc.cluster.local:7000", true},
		// Falls through to the namespace-qualified fuzzy probe.
		{"ns-qualified-fuzzy-hit", "pod-0.headless.prod.svc.cluster.local:6379", true},
		// Falls through to the short-pod fuzzy probe.
		{"short-pod-fuzzy-hit", "pod-0.headless:6379", true},
		// Worst case: every probe misses and the input is returned verbatim.
		{"miss-fallthrough", "absent.ns.svc.cluster.local:6379", true},
		// Same miss input with fuzzy matching disabled (no fuzzy probes run).
		{"fuzzy-disabled", "pod-0.headless.prod.svc.cluster.local:6379", false},
	}

	for _, size := range []int{1, 10, 100, 1000} {
		addrs := buildAddrs(size)
		for _, tier := range tiers {
			b.Run(fmt.Sprintf("%s/size=%d", tier.name, size), func(b *testing.B) {
				b.ReportAllocs()
				var sink string
				for b.Loop() {
					sink = resolveAddr(addrs, tier.addr, tier.fuzzy)
				}
				_ = sink
			})
		}
	}
}

// BenchmarkTranslateAddr measures the locked client.translateAddr wrapper that
// guards the mapping table, isolating the per-call mutex round-trip on top of
// resolveAddr.
func BenchmarkTranslateAddr(b *testing.B) {
	for _, size := range []int{1, 10, 100, 1000} {
		addrs := buildAddrs(size)
		c := &client{addrs: addrs}
		b.Run(fmt.Sprintf("exact-hit/size=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			var sink string
			for b.Loop() {
				sink = c.translateAddr("svc-0.ns-0.svc.cluster.local:6379")
			}
			_ = sink
		})
	}
}

package registry

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// seedRegistry opens a registry in a fresh temp dir and registers n entries. All
// entries use the current process PID so they survive List's liveness pruning
// (isAlive uses kill(pid, 0)).
func seedRegistry(b *testing.B, n int) *Registry {
	b.Helper()
	reg, err := Open(b.TempDir())
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	pid := os.Getpid()
	for i := range n {
		e := Entry{
			PID:        pid,
			ConfigFile: fmt.Sprintf("/tmp/kubeport-%d/kubeport.yaml", i),
			Socket:     fmt.Sprintf("/tmp/kubeport-%d.sock", i),
			StartedAt:  time.Now(),
		}
		// Register dedups on PID, so vary PID per entry to build up n rows.
		e.PID = pid + i
		if err := reg.Register(e); err != nil {
			b.Fatalf("seed Register: %v", err)
		}
	}
	return reg
}

// BenchmarkRegistryList measures the flock + read + JSON decode + liveness-prune
// path that backs `kubeport instances`.
func BenchmarkRegistryList(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		reg := seedRegistry(b, n)
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := reg.List(); err != nil {
					b.Fatalf("List: %v", err)
				}
			}
		})
	}
}

// BenchmarkRegistryRegister measures the full read-modify-write cycle (flock,
// decode, mutate, encode, atomic rename) for an update to an existing entry.
func BenchmarkRegistryRegister(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		reg := seedRegistry(b, n)
		pid := os.Getpid()
		e := Entry{
			PID:        pid, // updates the first seeded entry in place
			ConfigFile: "/tmp/kubeport-0/kubeport.yaml",
			Socket:     "/tmp/kubeport-0.sock",
			StartedAt:  time.Now(),
		}
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := reg.Register(e); err != nil {
					b.Fatalf("Register: %v", err)
				}
			}
		})
	}
}

// BenchmarkRegistryFindByConfig measures lookup by config path, which lists,
// resolves absolute paths, and scans for a match.
func BenchmarkRegistryFindByConfig(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		reg := seedRegistry(b, n)
		// Target the last seeded entry to exercise a full scan.
		target := fmt.Sprintf("/tmp/kubeport-%d/kubeport.yaml", n-1)
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := reg.FindByConfig(target); err != nil {
					b.Fatalf("FindByConfig: %v", err)
				}
			}
		})
	}
}

// BenchmarkRegistryRegisterParallel measures the flock serialization cost when
// multiple goroutines contend on the same registry file.
func BenchmarkRegistryRegisterParallel(b *testing.B) {
	reg := seedRegistry(b, 10)
	pid := os.Getpid()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		e := Entry{
			PID:        pid,
			ConfigFile: "/tmp/kubeport-0/kubeport.yaml",
			Socket:     "/tmp/kubeport-0.sock",
			StartedAt:  time.Now(),
		}
		for pb.Next() {
			if err := reg.Register(e); err != nil {
				b.Fatalf("Register: %v", err)
			}
		}
	})
}

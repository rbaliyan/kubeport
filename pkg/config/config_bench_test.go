package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// yamlFlat builds a flat YAML config with the requested number of services.
func yamlFlat(services int) string {
	var b strings.Builder
	b.WriteString("context: prod\nnamespace: default\nservices:\n")
	for i := range services {
		fmt.Fprintf(&b, "  - name: svc-%d\n    service: svc-%d\n    local_port: %d\n    remote_port: %d\n",
			i, i, 20000+i, 8000+i)
	}
	return b.String()
}

// tomlFlat builds a flat TOML config with the requested number of services.
func tomlFlat(services int) string {
	var b strings.Builder
	b.WriteString("context = \"prod\"\nnamespace = \"default\"\n")
	for i := range services {
		fmt.Fprintf(&b, "\n[[services]]\nname = \"svc-%d\"\nservice = \"svc-%d\"\nlocal_port = %d\nremote_port = %d\n",
			i, i, 20000+i, 8000+i)
	}
	return b.String()
}

// writeFixture writes content to a uniquely named file under dir and returns the
// path. ext selects the parser (".yaml" or ".toml").
func writeFixture(b *testing.B, dir, name, ext, content string) string {
	b.Helper()
	path := filepath.Join(dir, name+ext)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		b.Fatalf("write fixture: %v", err)
	}
	return path
}

// BenchmarkLoad measures the full public load path (read + parse + inheritance
// resolution + env overrides) across representative config shapes. Fixtures are
// written in setup, outside the timed loop.
func BenchmarkLoad(b *testing.B) {
	dir := b.TempDir()

	yamlFlatPath := writeFixture(b, dir, "yaml-flat", ".yaml", yamlFlat(5))
	tomlFlatPath := writeFixture(b, dir, "toml-flat", ".toml", tomlFlat(5))
	largePath := writeFixture(b, dir, "large", ".yaml", yamlFlat(50))

	// Three-level extends chain: child extends mid, mid extends base.
	basePath := writeFixture(b, dir, "chain-base", ".yaml",
		"context: prod\nnamespace: base-ns\nservices:\n  - name: base-svc\n    service: base-svc\n    remote_port: 80\n")
	midPath := writeFixture(b, dir, "chain-mid", ".yaml",
		fmt.Sprintf("extends: %s\nservices:\n  - name: mid-svc\n    service: mid-svc\n    remote_port: 81\n", basePath))
	childPath := writeFixture(b, dir, "chain-child", ".yaml",
		fmt.Sprintf("extends: %s\nnamespace: child-ns\nservices:\n  - name: child-svc\n    service: child-svc\n    remote_port: 82\n", midPath))

	cases := []struct {
		name string
		path string
	}{
		{"yaml-flat", yamlFlatPath},
		{"toml-flat", tomlFlatPath},
		{"yaml-extends-chain", childPath},
		{"large-50-services", largePath},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				cfg, err := Load(c.path)
				if err != nil {
					b.Fatalf("Load: %v", err)
				}
				_ = cfg
			}
		})
	}
}

// BenchmarkLoadWithInheritance isolates the inheritance-resolution path (no env
// overrides) using the same three-level chain.
func BenchmarkLoadWithInheritance(b *testing.B) {
	dir := b.TempDir()
	basePath := writeFixture(b, dir, "chain-base", ".yaml",
		"context: prod\nnamespace: base-ns\nservices:\n  - name: base-svc\n    service: base-svc\n    remote_port: 80\n")
	midPath := writeFixture(b, dir, "chain-mid", ".yaml",
		fmt.Sprintf("extends: %s\nservices:\n  - name: mid-svc\n    service: mid-svc\n    remote_port: 81\n", basePath))
	childPath := writeFixture(b, dir, "chain-child", ".yaml",
		fmt.Sprintf("extends: %s\nnamespace: child-ns\nservices:\n  - name: child-svc\n    service: child-svc\n    remote_port: 82\n", midPath))

	b.ReportAllocs()
	for b.Loop() {
		cfg, err := loadWithInheritance(childPath, nil)
		if err != nil {
			b.Fatalf("loadWithInheritance: %v", err)
		}
		_ = cfg
	}
}

// BenchmarkUnmarshalTOML measures the TOML decode path in isolation, which goes
// through intermediate structs because go-toml lacks an any-unmarshal hook.
func BenchmarkUnmarshalTOML(b *testing.B) {
	for _, size := range []int{5, 50} {
		data := []byte(tomlFlat(size))
		b.Run(fmt.Sprintf("services=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var cfg Config
				if err := unmarshalTOML(data, &cfg); err != nil {
					b.Fatalf("unmarshalTOML: %v", err)
				}
			}
		})
	}
}

// BenchmarkMergeConfigs measures the pure parent-into-child merge with no I/O,
// over a representative two-config overlap.
func BenchmarkMergeConfigs(b *testing.B) {
	mkConfig := func(services int, prefix string) *Config {
		svcs := make([]ServiceConfig, services)
		for i := range svcs {
			svcs[i] = ServiceConfig{
				Name:       fmt.Sprintf("%s-svc-%d", prefix, i),
				Service:    fmt.Sprintf("%s-svc-%d", prefix, i),
				RemotePort: 8000 + i,
			}
		}
		return &Config{Context: "prod", Namespace: prefix, Services: svcs}
	}

	for _, size := range []int{5, 50} {
		base := mkConfig(size, "base")
		override := mkConfig(size, "child")
		b.Run(fmt.Sprintf("services=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = mergeConfigs(base, override)
			}
		})
	}
}

// BenchmarkConfigResolvers measures the small per-service resolution helpers
// invoked on the supervise hot path and in status rendering.
func BenchmarkConfigResolvers(b *testing.B) {
	net := NetworkConfig{Latency: "50ms", Jitter: "10ms", Bandwidth: "5mbps"}
	globalNet := NetworkConfig{Latency: "100ms", Bandwidth: "1gbps"}
	chaos := ChaosConfig{
		Enabled:      boolPtr(true),
		ErrorRate:    0.1,
		LatencySpike: LatencySpikeConfig{Probability: 0.2, Duration: "5s"},
	}
	globalSup := SupervisorConfig{ConnectionMode: "isolated"}
	svc := ServiceConfig{Name: "svc", ConnectionMode: ""}

	b.Run("NetworkConfig.Parse", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := net.Parse(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ChaosConfig.Parse", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := chaos.Parse(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parseBandwidth", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := parseBandwidth("5mbps"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ResolveNetwork", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = ResolveNetwork(globalNet, net)
		}
	})
	b.Run("ResolveConnectionMode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = ResolveConnectionMode(globalSup, svc)
		}
	})
}

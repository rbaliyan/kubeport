package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSmoke_ConfigRoundTrip writes a minimal valid config in both YAML and TOML,
// loads each from disk, and confirms it validates and yields a stable instance
// ID. It is a shallow breadth check across the two supported config formats.
func TestSmoke_ConfigRoundTrip(t *testing.T) {
	const yamlBody = `context: smoke-ctx
namespace: default
services:
  - name: web
    service: nginx
    remote_port: 80
    local_port: 8080
`
	const tomlBody = `context = "smoke-ctx"
namespace = "default"

[[services]]
name = "web"
service = "nginx"
remote_port = 80
local_port = 8080
`

	cases := []struct {
		name string
		file string
		body string
	}{
		{"yaml", "kubeport.yaml", yamlBody},
		{"toml", "kubeport.toml", tomlBody},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.file)
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if cfg.InstanceID() == "" {
				t.Fatal("InstanceID is empty")
			}
			if cfg.Context != "smoke-ctx" {
				t.Fatalf("Context = %q, want smoke-ctx", cfg.Context)
			}
			if len(cfg.Services) != 1 || cfg.Services[0].Name != "web" {
				t.Fatalf("unexpected services: %+v", cfg.Services)
			}
		})
	}
}

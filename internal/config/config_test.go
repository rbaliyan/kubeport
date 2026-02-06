package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidate_NoServices(t *testing.T) {
	cfg := &Config{Services: []ServiceConfig{}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty services")
	}
}

func TestValidate_ServiceOrPodRequired(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "test", LocalPort: 8080, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when neither service nor pod is set")
	}
}

func TestValidate_BothServiceAndPod(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "test", Service: "my-svc", Pod: "my-pod", LocalPort: 8080, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when both service and pod are set")
	}
}

func TestValidate_DuplicateLocalPorts(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "a", Service: "svc-a", LocalPort: 8080, RemotePort: 80},
			{Name: "b", Service: "svc-b", LocalPort: 8080, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for duplicate local ports")
	}
}

func TestValidate_DynamicPortsAllowed(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "a", Service: "svc-a", LocalPort: 0, RemotePort: 80},
			{Name: "b", Service: "svc-b", LocalPort: 0, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error for dynamic ports: %v", err)
	}
}

func TestValidate_InvalidPorts(t *testing.T) {
	tests := []struct {
		name       string
		localPort  int
		remotePort int
	}{
		{"negative local", -1, 80},
		{"local too high", 70000, 80},
		{"remote zero", 8080, 0},
		{"remote negative", 8080, -1},
		{"remote too high", 8080, 70000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Services: []ServiceConfig{
					{Name: "test", Service: "svc", LocalPort: tt.localPort, RemotePort: tt.remotePort},
				},
			}
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "web", Service: "web-svc", LocalPort: 8080, RemotePort: 80},
			{Name: "api", Service: "api-svc", LocalPort: 9090, RemotePort: 8080},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServiceConfig_IsPod(t *testing.T) {
	svc := ServiceConfig{Pod: "my-pod"}
	if !svc.IsPod() {
		t.Fatal("expected IsPod() = true")
	}
	svc2 := ServiceConfig{Service: "my-svc"}
	if svc2.IsPod() {
		t.Fatal("expected IsPod() = false")
	}
}

func TestServiceConfig_Target(t *testing.T) {
	svc := ServiceConfig{Pod: "my-pod"}
	if got := svc.Target(); got != "my-pod" {
		t.Fatalf("expected my-pod, got %s", got)
	}
	svc2 := ServiceConfig{Service: "my-svc"}
	if got := svc2.Target(); got != "my-svc" {
		t.Fatalf("expected my-svc, got %s", got)
	}
}

func TestAddService(t *testing.T) {
	cfg := &Config{}
	svc := ServiceConfig{Name: "web", Service: "web-svc", LocalPort: 8080, RemotePort: 80}
	if err := cfg.AddService(svc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(cfg.Services))
	}

	// Duplicate should fail
	if err := cfg.AddService(svc); err == nil {
		t.Fatal("expected error for duplicate service")
	}
}

func TestRemoveService(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "web", Service: "web-svc"},
			{Name: "api", Service: "api-svc"},
		},
	}
	if err := cfg.RemoveService("web"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "api" {
		t.Fatalf("expected only 'api' service remaining")
	}

	// Not found
	if err := cfg.RemoveService("nope"); err == nil {
		t.Fatal("expected error for missing service")
	}
}

func TestSocketFile(t *testing.T) {
	cfg := &Config{filePath: "/home/user/project/kubeport.yaml"}
	if got := cfg.SocketFile(); got != "/home/user/project/.kubeport.sock" {
		t.Fatalf("expected /home/user/project/.kubeport.sock, got %s", got)
	}
}

func TestSocketFile_Empty(t *testing.T) {
	cfg := &Config{}
	if got := cfg.SocketFile(); got != ".kubeport.sock" {
		t.Fatalf("expected .kubeport.sock, got %s", got)
	}
}

func TestPIDFile(t *testing.T) {
	cfg := &Config{filePath: "/tmp/kubeport.yaml"}
	if got := cfg.PIDFile(); got != "/tmp/.kubeport.pid" {
		t.Fatalf("expected /tmp/.kubeport.pid, got %s", got)
	}
}

func TestLogFile(t *testing.T) {
	cfg := &Config{filePath: "/tmp/kubeport.yaml"}
	if got := cfg.LogFile(); got != "/tmp/.kubeport.log" {
		t.Fatalf("expected /tmp/.kubeport.log, got %s", got)
	}
}

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	content := `context: my-context
namespace: my-namespace
services:
  - name: web
    service: web-svc
    local_port: 8080
    remote_port: 80
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Context != "my-context" {
		t.Fatalf("expected my-context, got %s", cfg.Context)
	}
	if cfg.Namespace != "my-namespace" {
		t.Fatalf("expected my-namespace, got %s", cfg.Namespace)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "web" {
		t.Fatalf("unexpected services: %+v", cfg.Services)
	}
	if cfg.FilePath() != path {
		t.Fatalf("expected file path %s, got %s", path, cfg.FilePath())
	}
	if cfg.FileFormat() != FormatYAML {
		t.Fatalf("expected YAML format, got %s", cfg.FileFormat())
	}
}

func TestLoadTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.toml")
	content := `context = "my-context"
namespace = "my-namespace"

[[services]]
name = "web"
service = "web-svc"
local_port = 8080
remote_port = 80
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Context != "my-context" {
		t.Fatalf("expected my-context, got %s", cfg.Context)
	}
	if cfg.FileFormat() != FormatTOML {
		t.Fatalf("expected TOML format, got %s", cfg.FileFormat())
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	content := `context: original
namespace: original-ns
services:
  - name: web
    service: web-svc
    local_port: 8080
    remote_port: 80
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("K8S_CONTEXT", "override-ctx")
	t.Setenv("K8S_NAMESPACE", "override-ns")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Context != "override-ctx" {
		t.Fatalf("expected override-ctx, got %s", cfg.Context)
	}
	if cfg.Namespace != "override-ns" {
		t.Fatalf("expected override-ns, got %s", cfg.Namespace)
	}
}

func TestLoadForEdit_NoEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	content := `context: original
namespace: original-ns
services:
  - name: web
    service: web-svc
    local_port: 8080
    remote_port: 80
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("K8S_CONTEXT", "override-ctx")

	cfg, err := LoadForEdit(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Context != "original" {
		t.Fatalf("expected original (LoadForEdit should not apply env), got %s", cfg.Context)
	}
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")

	cfg := &Config{
		Context:   "test-ctx",
		Namespace: "test-ns",
		Services: []ServiceConfig{
			{Name: "web", Service: "web-svc", LocalPort: 8080, RemotePort: 80},
		},
		filePath: path,
		format:   FormatYAML,
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("save error: %v", err)
	}

	cfg2, err := Load(path)
	if err != nil {
		t.Fatalf("reload error: %v", err)
	}

	if cfg2.Context != "test-ctx" || cfg2.Namespace != "test-ns" {
		t.Fatalf("unexpected config after reload: %+v", cfg2)
	}
	if len(cfg2.Services) != 1 || cfg2.Services[0].Name != "web" {
		t.Fatalf("unexpected services after reload: %+v", cfg2.Services)
	}
}

func TestInit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")

	cfg, err := Init(path, FormatYAML)
	if err != nil {
		t.Fatalf("init error: %v", err)
	}

	if cfg.Namespace != "default" {
		t.Fatalf("expected default namespace, got %s", cfg.Namespace)
	}

	// Init again should fail
	_, err = Init(path, FormatYAML)
	if err == nil {
		t.Fatal("expected error for existing file")
	}
}

func TestDetectFormat(t *testing.T) {
	if f := detectFormat("config.toml"); f != FormatTOML {
		t.Fatalf("expected TOML, got %s", f)
	}
	if f := detectFormat("config.yaml"); f != FormatYAML {
		t.Fatalf("expected YAML, got %s", f)
	}
	if f := detectFormat("config.yml"); f != FormatYAML {
		t.Fatalf("expected YAML, got %s", f)
	}
	if f := detectFormat("config.json"); f != FormatYAML {
		t.Fatalf("expected YAML (default), got %s", f)
	}
}

func TestLoadWithHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	content := `context: test
namespace: default
services:
  - name: web
    service: web-svc
    local_port: 8080
    remote_port: 80
hooks:
  - name: vpn
    type: shell
    fail_mode: closed
    timeout: "30s"
    shell:
      manager_starting: "echo starting"
      forward_connected: "echo connected"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.Hooks))
	}
	h := cfg.Hooks[0]
	if h.Name != "vpn" {
		t.Fatalf("expected hook name 'vpn', got %s", h.Name)
	}
	if h.Type != "shell" {
		t.Fatalf("expected hook type 'shell', got %s", h.Type)
	}
	if h.FailMode != "closed" {
		t.Fatalf("expected fail_mode 'closed', got %s", h.FailMode)
	}
	if h.Timeout != "30s" {
		t.Fatalf("expected timeout '30s', got %s", h.Timeout)
	}
	if len(h.Shell) != 2 {
		t.Fatalf("expected 2 shell commands, got %d", len(h.Shell))
	}
}

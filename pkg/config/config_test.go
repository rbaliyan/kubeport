package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// ResolvedKeyID
// ---------------------------------------------------------------------------

func TestResolvedKeyID_UserProvided(t *testing.T) {
	cfg := &Config{APIKey: "s3cr3t", KeyID: "prod"}
	if got := cfg.ResolvedKeyID(); got != "prod" {
		t.Fatalf("expected %q, got %q", "prod", got)
	}
}

func TestResolvedKeyID_Derived(t *testing.T) {
	cfg := &Config{APIKey: "s3cr3t"}
	got := cfg.ResolvedKeyID()
	if got == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	// Stable across calls.
	if cfg.ResolvedKeyID() != got {
		t.Fatal("expected stable fingerprint")
	}
	// Different key → different fingerprint.
	cfg2 := &Config{APIKey: "other-key"}
	if cfg2.ResolvedKeyID() == got {
		t.Fatal("expected different fingerprint for different key")
	}
	// 16 hex chars (8 bytes * 2).
	if len(got) != 16 {
		t.Fatalf("expected 16-char fingerprint, got %d chars: %q", len(got), got)
	}
}

func TestResolvedKeyID_NoKey(t *testing.T) {
	cfg := &Config{}
	if got := cfg.ResolvedKeyID(); got != "" {
		t.Fatalf("expected empty for no API key, got %q", got)
	}
}

func TestResolvedKeyID_KeyIDOnlyNoKey(t *testing.T) {
	// KeyID set but no APIKey — still returns the user-provided KeyID.
	cfg := &Config{KeyID: "my-id"}
	if got := cfg.ResolvedKeyID(); got != "my-id" {
		t.Fatalf("expected %q, got %q", "my-id", got)
	}
}

// ---------------------------------------------------------------------------
// NewInMemory
// ---------------------------------------------------------------------------

func TestNewInMemory(t *testing.T) {
	svcs := []ServiceConfig{
		{Name: "api", Service: "my-svc", LocalPort: 8080, RemotePort: 80},
	}
	cfg := NewInMemory("my-context", "my-ns", svcs)
	if cfg.Context != "my-context" {
		t.Errorf("Context = %q, want %q", cfg.Context, "my-context")
	}
	if cfg.Namespace != "my-ns" {
		t.Errorf("Namespace = %q, want %q", cfg.Namespace, "my-ns")
	}
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "api" {
		t.Errorf("unexpected services: %+v", cfg.Services)
	}
	if cfg.FilePath() != "" {
		t.Errorf("expected empty file path for in-memory config, got %q", cfg.FilePath())
	}
	// Runtime paths should be non-empty even without a file.
	if cfg.PIDFile() == "" {
		t.Error("expected non-empty PIDFile for in-memory config")
	}
	if cfg.LogFile() == "" {
		t.Error("expected non-empty LogFile for in-memory config")
	}
	if cfg.SocketFile() == "" {
		t.Error("expected non-empty SocketFile for in-memory config")
	}
}

// ---------------------------------------------------------------------------
// KeyID round-trips through YAML and TOML
// ---------------------------------------------------------------------------

func TestKeyID_YAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	yaml := `
context: test
services:
  - name: api
    service: my-svc
    local_port: 8080
    remote_port: 80
api_key: secret
key_id: staging
`
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KeyID != "staging" {
		t.Errorf("KeyID = %q, want %q", cfg.KeyID, "staging")
	}
	if cfg.ResolvedKeyID() != "staging" {
		t.Errorf("ResolvedKeyID = %q, want %q", cfg.ResolvedKeyID(), "staging")
	}
}

func TestKeyID_TOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.toml")
	content := `
context = "test"
api_key = "secret"
key_id  = "prod"

[[services]]
name        = "api"
service     = "my-svc"
local_port  = 8080
remote_port = 80
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KeyID != "prod" {
		t.Errorf("KeyID = %q, want %q", cfg.KeyID, "prod")
	}

	// Save and reload to verify marshal path.
	out := filepath.Join(dir, "out.toml")
	if err := cfg.SaveTo(out, FormatTOML); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	cfg2, err := Load(out)
	if err != nil {
		t.Fatalf("Load reloaded: %v", err)
	}
	if cfg2.KeyID != "prod" {
		t.Errorf("reloaded KeyID = %q, want %q", cfg2.KeyID, "prod")
	}
}

func TestKeyID_SaveAndReload_YAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	content := `
context: test
api_key: token
key_id: ci-runner
services:
  - name: api
    service: my-svc
    local_port: 8080
    remote_port: 80
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := filepath.Join(dir, "out.yaml")
	if err := cfg.SaveTo(out, FormatYAML); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	cfg2, err := Load(out)
	if err != nil {
		t.Fatalf("Load reloaded: %v", err)
	}
	if cfg2.KeyID != "ci-runner" {
		t.Errorf("reloaded KeyID = %q, want %q", cfg2.KeyID, "ci-runner")
	}
}

// ---------------------------------------------------------------------------
// CentralDir
// ---------------------------------------------------------------------------

func TestCentralDir_Empty(t *testing.T) {
	// Smoke test: must return a non-empty path.
	dir := CentralDir("")
	if dir == "" {
		t.Fatal("expected non-empty CentralDir for empty input")
	}
}

func TestCentralDir_InsideXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := filepath.Join(home, ".config", "kubeport")
	if err := os.MkdirAll(xdg, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfgPath := filepath.Join(xdg, "kubeport.yaml")
	got := CentralDir(cfgPath)
	if got != xdg {
		t.Errorf("CentralDir = %q, want %q", got, xdg)
	}
}

// ---------------------------------------------------------------------------
// InstanceID
// ---------------------------------------------------------------------------

func TestInstanceID_Stable(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{filePath: filepath.Join(dir, "kubeport.yaml")}
	id1 := cfg.InstanceID()
	id2 := cfg.InstanceID()
	if id1 != id2 {
		t.Errorf("InstanceID not stable: %q vs %q", id1, id2)
	}
	if id1 == "" {
		t.Fatal("expected non-empty InstanceID")
	}
}

func TestInstanceID_DifferentPaths(t *testing.T) {
	dir := t.TempDir()
	c1 := &Config{filePath: filepath.Join(dir, "a", "kubeport.yaml")}
	c2 := &Config{filePath: filepath.Join(dir, "b", "kubeport.yaml")}
	if c1.InstanceID() == c2.InstanceID() {
		t.Errorf("expected different InstanceIDs for different paths: both %q", c1.InstanceID())
	}
}

func TestInstanceID_InMemory(t *testing.T) {
	cfg := &Config{}
	id := cfg.InstanceID()
	if id == "" {
		t.Fatal("expected non-empty InstanceID for in-memory config")
	}
}

// ---------------------------------------------------------------------------
// PortsConfig.IsSet
// ---------------------------------------------------------------------------

func TestPortsConfig_IsSet(t *testing.T) {
	if (PortsConfig{}).IsSet() {
		t.Error("empty PortsConfig should not be set")
	}
	if !(PortsConfig{All: true}).IsSet() {
		t.Error("All=true should be set")
	}
	if !(PortsConfig{Selectors: []PortSelector{{Name: "http"}}}).IsSet() {
		t.Error("non-empty selectors should be set")
	}
}

// ---------------------------------------------------------------------------
// validateHook
// ---------------------------------------------------------------------------

func TestValidateHook(t *testing.T) {
	svcNames := map[string]struct{}{"api": {}}

	tests := []struct {
		name    string
		hook    HookConfig
		wantErr bool
	}{
		{
			name:    "missing hook name",
			hook:    HookConfig{Type: "exec"},
			wantErr: true,
		},
		{
			name:    "missing hook type",
			hook:    HookConfig{Name: "my-hook"},
			wantErr: true,
		},
		{
			name:    "invalid timeout",
			hook:    HookConfig{Name: "h", Type: "exec", Timeout: "not-a-duration"},
			wantErr: true,
		},
		{
			name:    "invalid fail_mode",
			hook:    HookConfig{Name: "h", Type: "exec", FailMode: "invalid"},
			wantErr: true,
		},
		{
			name:    "unknown filter service",
			hook:    HookConfig{Name: "h", Type: "exec", FilterServices: []string{"unknown"}},
			wantErr: true,
		},
		{
			name:    "valid hook",
			hook:    HookConfig{Name: "h", Type: "exec", Timeout: "5s", FailMode: "open", FilterServices: []string{"api"}},
			wantErr: false,
		},
		{
			name:    "valid hook closed fail_mode",
			hook:    HookConfig{Name: "h", Type: "exec", FailMode: "closed"},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHook(0, tt.hook, svcNames)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SupervisorConfig.validate and ParsedSupervisor
// ---------------------------------------------------------------------------

func TestSupervisorConfig_Validate(t *testing.T) {
	valid := SupervisorConfig{
		HealthCheckInterval: "10s",
		ReadyTimeout:        "15s",
		BackoffInitial:      "1s",
		BackoffMax:          "30s",
		MaxConnectionAge:    "1m",
	}
	if err := valid.validate(); err != nil {
		t.Errorf("unexpected error for valid config: %v", err)
	}

	invalid := SupervisorConfig{HealthCheckInterval: "not-a-duration"}
	if err := invalid.validate(); err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestParsedSupervisor_Defaults(t *testing.T) {
	s := SupervisorConfig{}
	p, err := s.ParsedSupervisor()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.HealthCheckInterval != 10*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 10s", p.HealthCheckInterval)
	}
	if p.ReadyTimeout != 15*time.Second {
		t.Errorf("ReadyTimeout = %v, want 15s", p.ReadyTimeout)
	}
	if p.BackoffInitial != time.Second {
		t.Errorf("BackoffInitial = %v, want 1s", p.BackoffInitial)
	}
	if p.BackoffMax != 30*time.Second {
		t.Errorf("BackoffMax = %v, want 30s", p.BackoffMax)
	}
	if p.HealthCheckThreshold != 3 {
		t.Errorf("HealthCheckThreshold = %d, want 3", p.HealthCheckThreshold)
	}
}

func TestParsedSupervisor_CustomValues(t *testing.T) {
	s := SupervisorConfig{
		HealthCheckInterval:  "5s",
		ReadyTimeout:         "20s",
		BackoffInitial:       "2s",
		BackoffMax:           "60s",
		MaxConnectionAge:     "2m",
		MaxRestarts:          5,
		HealthCheckThreshold: 2,
	}
	p, err := s.ParsedSupervisor()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.HealthCheckInterval != 5*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 5s", p.HealthCheckInterval)
	}
	if p.MaxConnectionAge != 2*time.Minute {
		t.Errorf("MaxConnectionAge = %v, want 2m", p.MaxConnectionAge)
	}
	if p.MaxRestarts != 5 {
		t.Errorf("MaxRestarts = %d, want 5", p.MaxRestarts)
	}
	if p.HealthCheckThreshold != 2 {
		t.Errorf("HealthCheckThreshold = %d, want 2", p.HealthCheckThreshold)
	}
}

func TestParsedSupervisor_InvalidDuration(t *testing.T) {
	s := SupervisorConfig{HealthCheckInterval: "bad"}
	if _, err := s.ParsedSupervisor(); err == nil {
		t.Error("expected error for invalid duration")
	}
}

// ---------------------------------------------------------------------------
// LoadServices
// ---------------------------------------------------------------------------

func TestLoadServices(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "services.yaml")
	content := `
services:
  - name: api
    service: my-svc
    local_port: 8080
    remote_port: 80
  - name: db
    service: postgres
    local_port: 5432
    remote_port: 5432
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	svcs, err := LoadServices(path)
	if err != nil {
		t.Fatalf("LoadServices: %v", err)
	}
	if len(svcs) != 2 {
		t.Fatalf("expected 2 services, got %d", len(svcs))
	}
	if svcs[0].Name != "api" || svcs[1].Name != "db" {
		t.Errorf("unexpected services: %+v", svcs)
	}
}

func TestLoadServices_InvalidService(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "services.yaml")
	content := `
services:
  - name: bad
    local_port: 8080
    remote_port: 80
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadServices(path); err == nil {
		t.Error("expected error for invalid service (missing service/pod)")
	}
}

func TestLoadServices_FileNotFound(t *testing.T) {
	if _, err := LoadServices("/no/such/file.yaml"); err == nil {
		t.Error("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// marshalTOML — service port branches
// ---------------------------------------------------------------------------

func TestMarshalTOML_AllPorts(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Context:   "test",
		filePath:  filepath.Join(dir, "kubeport.toml"),
		format:    FormatTOML,
		Services: []ServiceConfig{
			{
				Name:    "api",
				Service: "my-svc",
				Ports:   PortsConfig{All: true},
			},
		},
	}
	out := filepath.Join(dir, "out.toml")
	if err := cfg.SaveTo(out, FormatTOML); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "all") {
		t.Errorf("expected 'all' in TOML output, got:\n%s", data)
	}
}

func TestMarshalTOML_ComplexPortSelectors(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Context:  "test",
		filePath: filepath.Join(dir, "kubeport.toml"),
		format:   FormatTOML,
		Services: []ServiceConfig{
			{
				Name:    "api",
				Service: "my-svc",
				Ports: PortsConfig{
					Selectors: []PortSelector{
						{Name: "http", Port: 80, LocalPort: 8080},
					},
				},
			},
		},
	}
	out := filepath.Join(dir, "out.toml")
	if err := cfg.SaveTo(out, FormatTOML); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
}

func TestMarshalTOML_SimpleNamedPorts(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Context:  "test",
		filePath: filepath.Join(dir, "kubeport.toml"),
		format:   FormatTOML,
		Services: []ServiceConfig{
			{
				Name:    "api",
				Service: "my-svc",
				Ports: PortsConfig{
					Selectors: []PortSelector{
						{Name: "http"},
						{Name: "grpc"},
					},
				},
			},
		},
	}
	out := filepath.Join(dir, "out.toml")
	if err := cfg.SaveTo(out, FormatTOML); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "http") {
		t.Errorf("expected port names in TOML output, got:\n%s", data)
	}
}

// ---------------------------------------------------------------------------
// Save (no file path error path)
// ---------------------------------------------------------------------------

func TestSave_NoFilePath(t *testing.T) {
	cfg := &Config{}
	if err := cfg.Save(); err == nil {
		t.Error("expected error for Save with no file path")
	}
}

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
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "project", "kubeport.yaml")
	cfg := &Config{filePath: cfgPath}
	got := cfg.SocketFile()
	want := filepath.Join(CentralDir(cfgPath), cfg.InstanceID()+".sock")
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
	if !strings.HasSuffix(got, ".sock") {
		t.Fatalf("expected .sock suffix, got %s", got)
	}
}

func TestSocketFile_Empty(t *testing.T) {
	cfg := &Config{}
	got := cfg.SocketFile()
	// With no config path, falls back to CWD-based central dir; result must be absolute and end in .sock.
	if !filepath.IsAbs(got) {
		t.Fatalf("expected absolute path, got %s", got)
	}
	if !strings.HasSuffix(got, ".sock") {
		t.Fatalf("expected .sock suffix, got %s", got)
	}
}

func TestPIDFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "kubeport.yaml")
	cfg := &Config{filePath: cfgPath}
	got := cfg.PIDFile()
	want := filepath.Join(CentralDir(cfgPath), cfg.InstanceID()+".pid")
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestLogFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "kubeport.yaml")
	cfg := &Config{filePath: cfgPath}
	got := cfg.LogFile()
	want := filepath.Join(CentralDir(cfgPath), "logs", cfg.InstanceID()+".log")
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
	if !strings.Contains(got, "/logs/") {
		t.Fatalf("expected log file under logs/ subdirectory, got %s", got)
	}
}

func TestLogFile_Custom(t *testing.T) {
	cfg := &Config{
		LogFilePath: "/var/log/kubeport.log",
		filePath:    "/tmp/kubeport.yaml",
	}
	if got := cfg.LogFile(); got != "/var/log/kubeport.log" {
		t.Fatalf("expected /var/log/kubeport.log, got %s", got)
	}
}

func TestSocketFile_Listen(t *testing.T) {
	cfg := &Config{
		Listen:   "sock:///tmp/custom.sock",
		filePath: "/home/user/project/kubeport.yaml",
	}
	if got := cfg.SocketFile(); got != "/tmp/custom.sock" {
		t.Fatalf("expected /tmp/custom.sock, got %s", got)
	}
}

func TestValidate_ListenInvalidScheme(t *testing.T) {
	cfg := &Config{
		Listen: "http://localhost:9090",
		Services: []ServiceConfig{
			{Name: "web", Service: "web-svc", LocalPort: 8080, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for unsupported listen scheme")
	}
}

func TestValidate_ListenTCP_Valid(t *testing.T) {
	cfg := &Config{
		Listen: "tcp://0.0.0.0:9090",
		APIKey: "test-secret",
		Services: []ServiceConfig{
			{Name: "web", Service: "web-svc", LocalPort: 8080, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_ListenTCP_MissingAPIKey(t *testing.T) {
	cfg := &Config{
		Listen: "tcp://0.0.0.0:9090",
		Services: []ServiceConfig{
			{Name: "web", Service: "web-svc", LocalPort: 8080, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when api_key missing with tcp://")
	}
}

func TestValidate_ListenTCP_InvalidAddress(t *testing.T) {
	cfg := &Config{
		Listen: "tcp://not-a-valid-address",
		APIKey: "test-secret",
		Services: []ServiceConfig{
			{Name: "web", Service: "web-svc", LocalPort: 8080, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid tcp address")
	}
}

func TestListenAddress_TCP(t *testing.T) {
	cfg := &Config{Listen: "tcp://0.0.0.0:9090"}
	lc := cfg.ListenAddress()
	if lc.Mode != ListenTCP {
		t.Fatalf("expected ListenTCP, got %d", lc.Mode)
	}
	if lc.Address != "0.0.0.0:9090" {
		t.Fatalf("expected 0.0.0.0:9090, got %s", lc.Address)
	}
}

func TestListenAddress_Unix(t *testing.T) {
	cfg := &Config{Listen: "sock:///tmp/custom.sock", filePath: "/home/user/kubeport.yaml"}
	lc := cfg.ListenAddress()
	if lc.Mode != ListenUnix {
		t.Fatalf("expected ListenUnix, got %d", lc.Mode)
	}
	if lc.Address != "/tmp/custom.sock" {
		t.Fatalf("expected /tmp/custom.sock, got %s", lc.Address)
	}
}

func TestListenAddress_Default(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "kubeport.yaml")
	cfg := &Config{filePath: cfgPath}
	lc := cfg.ListenAddress()
	if lc.Mode != ListenUnix {
		t.Fatalf("expected ListenUnix, got %d", lc.Mode)
	}
	// Address must be the socket file in the central directory.
	if lc.Address != cfg.SocketFile() {
		t.Fatalf("expected %s, got %s", cfg.SocketFile(), lc.Address)
	}
	if !strings.HasSuffix(lc.Address, ".sock") {
		t.Fatalf("expected .sock suffix, got %s", lc.Address)
	}
}

func TestLoad_APIKeyEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	content := `context: test
namespace: default
services:
  - name: web
    service: web-svc
    local_port: 8080
    remote_port: 80
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KUBEPORT_API_KEY", "env-secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.APIKey != "env-secret" {
		t.Fatalf("expected env-secret, got %s", cfg.APIKey)
	}
}

func TestValidate_ListenEmptyPath(t *testing.T) {
	cfg := &Config{
		Listen: "sock://",
		Services: []ServiceConfig{
			{Name: "web", Service: "web-svc", LocalPort: 8080, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty path after sock://")
	}
}

func TestValidate_ListenValid(t *testing.T) {
	cfg := &Config{
		Listen: "sock:///tmp/kubeport.sock",
		Services: []ServiceConfig{
			{Name: "web", Service: "web-svc", LocalPort: 8080, RemotePort: 80},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
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
      manager:starting: "echo starting"
      forward:connected: "echo connected"
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

func TestLoadConfig_LegacyHookEventMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	content := `
context: test-context
namespace: default
services:
  - name: web
    service: web-svc
    local_port: 8080
    remote_port: 80
hooks:
  - name: notify
    type: shell
    events: [forward_connected, manager_starting]
    shell:
      forward_connected: "echo connected"
      manager_starting: "echo starting"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	h := cfg.Hooks[0]
	// Events should be migrated to colon-based names
	for _, e := range h.Events {
		if strings.Contains(e, "_") && !strings.Contains(e, ":") {
			t.Errorf("event %q was not migrated to colon-based name", e)
		}
	}
	// Shell keys should be migrated
	for key := range h.Shell {
		if strings.Contains(key, "_") && !strings.Contains(key, ":") {
			t.Errorf("shell key %q was not migrated to colon-based name", key)
		}
	}
	if _, ok := h.Shell["forward:connected"]; !ok {
		t.Error("expected shell key 'forward:connected' after migration")
	}
	if _, ok := h.Shell["manager:starting"]; !ok {
		t.Error("expected shell key 'manager:starting' after migration")
	}
}

func TestIsMultiPort(t *testing.T) {
	tests := []struct {
		name string
		svc  ServiceConfig
		want bool
	}{
		{"legacy", ServiceConfig{RemotePort: 80}, false},
		{"ports all", ServiceConfig{Ports: PortsConfig{All: true}}, true},
		{"ports selectors", ServiceConfig{Ports: PortsConfig{Selectors: []PortSelector{{Name: "http"}}}}, true},
		{"empty", ServiceConfig{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.svc.IsMultiPort(); got != tt.want {
				t.Errorf("IsMultiPort() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidate_MultiPort_All(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", Ports: PortsConfig{All: true}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_MultiPort_Selectors(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", Ports: PortsConfig{
				Selectors: []PortSelector{{Name: "http"}, {Name: "grpc"}},
			}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_MultiPort_RejectsRemotePort(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", RemotePort: 80, Ports: PortsConfig{All: true}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when remote_port set in multi-port mode")
	}
}

func TestValidate_MultiPort_RejectsLocalPort(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", LocalPort: 8080, Ports: PortsConfig{All: true}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when local_port set in multi-port mode")
	}
}

func TestValidate_MultiPort_ExcludePortsOnlyWithAll(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", Ports: PortsConfig{
				Selectors: []PortSelector{{Name: "http"}},
			}, ExcludePorts: []string{"metrics"}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when exclude_ports used without ports: all")
	}
}

func TestValidate_MultiPort_ExcludePortsWithAll(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", Ports: PortsConfig{All: true}, ExcludePorts: []string{"metrics"}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_Legacy_RejectsExcludePorts(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", RemotePort: 80, ExcludePorts: []string{"metrics"}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when exclude_ports used in legacy mode")
	}
}

func TestValidate_Legacy_RejectsLocalPortOffset(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", RemotePort: 80, LocalPortOffset: 10000},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when local_port_offset used in legacy mode")
	}
}

func TestValidateService_MultiPort(t *testing.T) {
	svc := ServiceConfig{
		Name:    "api",
		Service: "my-api",
		Ports:   PortsConfig{All: true},
	}
	if err := ValidateService(svc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateService_MultiPort_WithOffset(t *testing.T) {
	svc := ServiceConfig{
		Name:            "api",
		Service:         "my-api",
		Ports:           PortsConfig{All: true},
		LocalPortOffset: 10000,
	}
	if err := ValidateService(svc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_MultiPort_YAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	content := `context: test
namespace: default
services:
  - name: api-all
    service: my-api
    ports: all
  - name: api-named
    service: my-api
    ports:
      - http
      - grpc
  - name: api-detailed
    service: my-api
    ports:
      - name: http
        local_port: 8080
      - name: grpc
  - name: api-exclude
    service: my-api
    ports: all
    exclude_ports: [metrics]
    local_port_offset: 10000
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Services) != 4 {
		t.Fatalf("expected 4 services, got %d", len(cfg.Services))
	}

	// all
	if !cfg.Services[0].Ports.All {
		t.Error("expected ports.All=true for api-all")
	}

	// named
	if len(cfg.Services[1].Ports.Selectors) != 2 {
		t.Errorf("expected 2 selectors for api-named, got %d", len(cfg.Services[1].Ports.Selectors))
	}
	if cfg.Services[1].Ports.Selectors[0].Name != "http" {
		t.Errorf("expected first selector name 'http', got %q", cfg.Services[1].Ports.Selectors[0].Name)
	}

	// detailed
	if len(cfg.Services[2].Ports.Selectors) != 2 {
		t.Errorf("expected 2 selectors for api-detailed, got %d", len(cfg.Services[2].Ports.Selectors))
	}
	if cfg.Services[2].Ports.Selectors[0].LocalPort != 8080 {
		t.Errorf("expected first selector local_port 8080, got %d", cfg.Services[2].Ports.Selectors[0].LocalPort)
	}

	// exclude + offset
	if !cfg.Services[3].Ports.All {
		t.Error("expected ports.All=true for api-exclude")
	}
	if len(cfg.Services[3].ExcludePorts) != 1 || cfg.Services[3].ExcludePorts[0] != "metrics" {
		t.Errorf("unexpected exclude_ports: %v", cfg.Services[3].ExcludePorts)
	}
	if cfg.Services[3].LocalPortOffset != 10000 {
		t.Errorf("expected local_port_offset 10000, got %d", cfg.Services[3].LocalPortOffset)
	}
}

func TestSaveAndReload_MultiPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")

	cfg := &Config{
		Context:   "test",
		Namespace: "default",
		Services: []ServiceConfig{
			{
				Name:            "api",
				Service:         "my-api",
				Ports:           PortsConfig{All: true},
				ExcludePorts:    []string{"metrics"},
				LocalPortOffset: 10000,
			},
			{
				Name:    "web",
				Service: "web-svc",
				Ports: PortsConfig{
					Selectors: []PortSelector{{Name: "http"}, {Name: "grpc"}},
				},
			},
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

	if len(cfg2.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(cfg2.Services))
	}
	if !cfg2.Services[0].Ports.All {
		t.Error("expected ports.All after round-trip")
	}
	if cfg2.Services[0].LocalPortOffset != 10000 {
		t.Errorf("expected offset 10000, got %d", cfg2.Services[0].LocalPortOffset)
	}
	if len(cfg2.Services[1].Ports.Selectors) != 2 {
		t.Errorf("expected 2 selectors after round-trip, got %d", len(cfg2.Services[1].Ports.Selectors))
	}
}

func TestLoad_MultiPort_TOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.toml")
	content := `context = "test"
namespace = "default"

[[services]]
name = "api-all"
service = "my-api"
ports = "all"

[[services]]
name = "api-named"
service = "my-api"
ports = ["http", "grpc"]

[[services]]
name = "api-exclude"
service = "my-api"
ports = "all"
exclude_ports = ["metrics"]
local_port_offset = 10000
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Services) != 3 {
		t.Fatalf("expected 3 services, got %d", len(cfg.Services))
	}
	if !cfg.Services[0].Ports.All {
		t.Error("expected ports.All=true for api-all")
	}
	if len(cfg.Services[1].Ports.Selectors) != 2 {
		t.Errorf("expected 2 selectors for api-named, got %d", len(cfg.Services[1].Ports.Selectors))
	}
	if !cfg.Services[2].Ports.All {
		t.Error("expected ports.All=true for api-exclude")
	}
	if cfg.Services[2].LocalPortOffset != 10000 {
		t.Errorf("expected offset 10000, got %d", cfg.Services[2].LocalPortOffset)
	}
}

func TestSaveAndReload_MultiPort_TOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.toml")

	cfg := &Config{
		Context:   "test",
		Namespace: "default",
		Services: []ServiceConfig{
			{
				Name:            "api",
				Service:         "my-api",
				Ports:           PortsConfig{All: true},
				ExcludePorts:    []string{"metrics"},
				LocalPortOffset: 10000,
			},
		},
		filePath: path,
		format:   FormatTOML,
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("save error: %v", err)
	}

	cfg2, err := Load(path)
	if err != nil {
		t.Fatalf("reload error: %v", err)
	}

	if !cfg2.Services[0].Ports.All {
		t.Error("expected ports.All after TOML round-trip")
	}
	if cfg2.Services[0].LocalPortOffset != 10000 {
		t.Errorf("expected offset 10000 after TOML round-trip, got %d", cfg2.Services[0].LocalPortOffset)
	}
}

func TestPortsConfig_UnmarshalYAML_InvalidString(t *testing.T) {
	var p PortsConfig
	err := yaml.Unmarshal([]byte(`"invalid"`), &p)
	if err == nil {
		t.Fatal("expected error for invalid ports string")
	}
}

func TestPortsConfig_UnmarshalYAML_InvalidType(t *testing.T) {
	var p PortsConfig
	err := yaml.Unmarshal([]byte(`123`), &p)
	if err == nil {
		t.Fatal("expected error for numeric ports value")
	}
}

func TestPortsConfig_UnmarshalYAML_InvalidListItem(t *testing.T) {
	var p PortsConfig
	err := yaml.Unmarshal([]byte(`[123]`), &p)
	if err == nil {
		t.Fatal("expected error for non-string/non-object list item")
	}
}

func TestValidate_MultiPort_NegativeOffset(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", Ports: PortsConfig{All: true}, LocalPortOffset: -1},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative local_port_offset")
	}
}

func TestValidate_MultiPort_SelectorWithPort(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", Ports: PortsConfig{
				Selectors: []PortSelector{{Port: 8080}},
			}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error for selector with port number: %v", err)
	}
}

func TestValidate_MultiPort_SelectorEmptyNameAndPort(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", Ports: PortsConfig{
				Selectors: []PortSelector{{Name: "", Port: 0}},
			}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for selector with neither name nor port")
	}
}

func TestValidate_MultiPort_SelectorInvalidPort(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", Ports: PortsConfig{
				Selectors: []PortSelector{{Port: 70000}},
			}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for selector with port > 65535")
	}
}

func TestValidate_MultiPort_SelectorInvalidLocalPort(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "my-api", Ports: PortsConfig{
				Selectors: []PortSelector{{Name: "http", LocalPort: -1}},
			}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for selector with negative local_port")
	}
}

func TestValidate_MixedLegacyAndMultiPort(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "redis", Service: "redis", LocalPort: 6379, RemotePort: 6379},
			{Name: "api", Service: "my-api", Ports: PortsConfig{All: true}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error for mixed config: %v", err)
	}
}

// --- Network Config Tests ---

func TestParseBandwidth(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"5mbps", 5 * 1_000_000 / 8, false},
		{"500kbps", 500 * 1_000 / 8, false},
		{"1gbps", 1_000_000_000 / 8, false},
		{"10Mbps", 10 * 1_000_000 / 8, false},
		{"10mbytes", 10 * 1_000_000, false},
		{"500kbytes", 500 * 1_000, false},
		{"1gbytes", 1_000_000_000, false},
		{"", 0, true},
		{"abc", 0, true},
		{"-5mbps", 0, true},
		{"0mbps", 0, true},
		{"5xyz", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseBandwidth(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseBandwidth(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("parseBandwidth(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestNetworkConfig_Parse(t *testing.T) {
	tests := []struct {
		name    string
		cfg     NetworkConfig
		wantErr bool
	}{
		{"empty", NetworkConfig{}, false},
		{"latency only", NetworkConfig{Latency: "50ms"}, false},
		{"latency and jitter", NetworkConfig{Latency: "100ms", Jitter: "20ms"}, false},
		{"bandwidth only", NetworkConfig{Bandwidth: "5mbps"}, false},
		{"all fields", NetworkConfig{Latency: "50ms", Jitter: "10ms", Bandwidth: "1mbps"}, false},
		{"jitter without latency", NetworkConfig{Jitter: "10ms"}, true},
		{"jitter exceeds latency", NetworkConfig{Latency: "10ms", Jitter: "20ms"}, true},
		{"negative latency", NetworkConfig{Latency: "-5ms"}, true},
		{"negative jitter", NetworkConfig{Latency: "50ms", Jitter: "-5ms"}, true},
		{"invalid latency", NetworkConfig{Latency: "abc"}, true},
		{"invalid bandwidth", NetworkConfig{Bandwidth: "abc"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.cfg.Parse()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveNetwork(t *testing.T) {
	global := NetworkConfig{Latency: "50ms", Jitter: "10ms", Bandwidth: "5mbps"}

	t.Run("global only", func(t *testing.T) {
		merged := ResolveNetwork(global, NetworkConfig{})
		if merged != global {
			t.Fatalf("expected global, got %+v", merged)
		}
	})

	t.Run("per-service overrides all", func(t *testing.T) {
		perSvc := NetworkConfig{Latency: "100ms", Jitter: "20ms", Bandwidth: "1mbps"}
		merged := ResolveNetwork(global, perSvc)
		if merged != perSvc {
			t.Fatalf("expected per-service, got %+v", merged)
		}
	})

	t.Run("partial override", func(t *testing.T) {
		perSvc := NetworkConfig{Bandwidth: "1mbps"}
		merged := ResolveNetwork(global, perSvc)
		if merged.Latency != "50ms" || merged.Jitter != "10ms" || merged.Bandwidth != "1mbps" {
			t.Fatalf("unexpected merge result: %+v", merged)
		}
	})

	t.Run("both empty", func(t *testing.T) {
		merged := ResolveNetwork(NetworkConfig{}, NetworkConfig{})
		if merged.IsSet() {
			t.Fatal("expected empty")
		}
	})
}

func TestNetworkConfig_YAMLRoundTrip(t *testing.T) {
	cfg := &Config{
		Context:   "test",
		Namespace: "default",
		Network:   NetworkConfig{Latency: "50ms", Jitter: "10ms", Bandwidth: "5mbps"},
		Services: []ServiceConfig{
			{
				Name:       "api",
				Service:    "api-svc",
				LocalPort:  8080,
				RemotePort: 80,
				Network:    NetworkConfig{Latency: "100ms", Bandwidth: "1mbps"},
			},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Network.Latency != "50ms" || loaded.Network.Bandwidth != "5mbps" {
		t.Fatalf("global network not round-tripped: %+v", loaded.Network)
	}
	if loaded.Services[0].Network.Latency != "100ms" || loaded.Services[0].Network.Bandwidth != "1mbps" {
		t.Fatalf("service network not round-tripped: %+v", loaded.Services[0].Network)
	}
}

func TestValidate_NetworkConfig(t *testing.T) {
	t.Run("valid global network", func(t *testing.T) {
		cfg := &Config{
			Network: NetworkConfig{Latency: "50ms", Bandwidth: "5mbps"},
			Services: []ServiceConfig{
				{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid global network", func(t *testing.T) {
		cfg := &Config{
			Network: NetworkConfig{Bandwidth: "abc"},
			Services: []ServiceConfig{
				{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error for invalid global network")
		}
	})

	t.Run("invalid per-service network", func(t *testing.T) {
		cfg := &Config{
			Services: []ServiceConfig{
				{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80,
					Network: NetworkConfig{Jitter: "10ms"}},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error for jitter without latency")
		}
	})
}

func TestNetworkConfig_TOMLRoundTrip(t *testing.T) {
	cfg := &Config{
		Context:   "test",
		Namespace: "default",
		Network:   NetworkConfig{Latency: "50ms", Jitter: "10ms", Bandwidth: "5mbps"},
		Services: []ServiceConfig{
			{
				Name:       "api",
				Service:    "api-svc",
				LocalPort:  8080,
				RemotePort: 80,
				Network:    NetworkConfig{Latency: "100ms", Bandwidth: "1mbps"},
			},
		},
		format: FormatTOML,
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.toml")
	if err := cfg.SaveTo(path, FormatTOML); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Network.Latency != "50ms" || loaded.Network.Jitter != "10ms" || loaded.Network.Bandwidth != "5mbps" {
		t.Fatalf("global network not round-tripped: %+v", loaded.Network)
	}
	if loaded.Services[0].Network.Latency != "100ms" || loaded.Services[0].Network.Bandwidth != "1mbps" {
		t.Fatalf("service network not round-tripped: %+v", loaded.Services[0].Network)
	}
}

func TestParsedNetworkConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  ParsedNetworkConfig
		want bool
	}{
		{"zero", ParsedNetworkConfig{}, false},
		{"latency only", ParsedNetworkConfig{Latency: 50}, true},
		{"jitter only", ParsedNetworkConfig{Jitter: 10}, true},
		{"bandwidth only", ParsedNetworkConfig{BytesPerSec: 1000}, true},
		{"all", ParsedNetworkConfig{Latency: 50, Jitter: 10, BytesPerSec: 1000}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsEnabled(); got != tt.want {
				t.Fatalf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Chaos Config Tests ---

func TestChaosConfig_Parse(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ChaosConfig
		wantErr bool
	}{
		{"disabled", ChaosConfig{}, false},
		{"enabled no faults", ChaosConfig{Enabled: true}, false},
		{"error rate only", ChaosConfig{Enabled: true, ErrorRate: 0.02}, false},
		{"latency spike only", ChaosConfig{Enabled: true, LatencySpike: LatencySpikeConfig{Probability: 0.01, Duration: "5s"}}, false},
		{"all fields", ChaosConfig{Enabled: true, ErrorRate: 0.05, LatencySpike: LatencySpikeConfig{Probability: 0.1, Duration: "2s"}}, false},
		{"error rate too high", ChaosConfig{Enabled: true, ErrorRate: 1.5}, true},
		{"error rate negative", ChaosConfig{Enabled: true, ErrorRate: -0.1}, true},
		{"spike probability too high", ChaosConfig{Enabled: true, LatencySpike: LatencySpikeConfig{Probability: 2.0, Duration: "1s"}}, true},
		{"spike probability negative", ChaosConfig{Enabled: true, LatencySpike: LatencySpikeConfig{Probability: -0.1, Duration: "1s"}}, true},
		{"spike without duration", ChaosConfig{Enabled: true, LatencySpike: LatencySpikeConfig{Probability: 0.1}}, true},
		{"invalid duration", ChaosConfig{Enabled: true, LatencySpike: LatencySpikeConfig{Probability: 0.1, Duration: "abc"}}, true},
		{"negative duration", ChaosConfig{Enabled: true, LatencySpike: LatencySpikeConfig{Probability: 0.1, Duration: "-5s"}}, true},
		{"boundary error rate 0", ChaosConfig{Enabled: true, ErrorRate: 0.0}, false},
		{"boundary error rate 1", ChaosConfig{Enabled: true, ErrorRate: 1.0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.cfg.Parse()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveChaos(t *testing.T) {
	global := ChaosConfig{Enabled: true, ErrorRate: 0.02, LatencySpike: LatencySpikeConfig{Probability: 0.01, Duration: "5s"}}

	t.Run("global only", func(t *testing.T) {
		merged := ResolveChaos(global, ChaosConfig{})
		if merged != global {
			t.Fatalf("expected global, got %+v", merged)
		}
	})

	t.Run("per-service overrides", func(t *testing.T) {
		perSvc := ChaosConfig{Enabled: true, ErrorRate: 0.1}
		merged := ResolveChaos(global, perSvc)
		if merged != perSvc {
			t.Fatalf("expected per-service, got %+v", merged)
		}
	})

	t.Run("both disabled", func(t *testing.T) {
		merged := ResolveChaos(ChaosConfig{}, ChaosConfig{})
		if merged.IsSet() {
			t.Fatal("expected disabled")
		}
	})

	t.Run("per-service disabled global enabled", func(t *testing.T) {
		merged := ResolveChaos(global, ChaosConfig{Enabled: false})
		if merged != global {
			t.Fatalf("expected global when per-service not enabled, got %+v", merged)
		}
	})
}

func TestChaosConfig_YAMLRoundTrip(t *testing.T) {
	cfg := &Config{
		Context:   "test",
		Namespace: "default",
		Chaos:     ChaosConfig{Enabled: true, ErrorRate: 0.02, LatencySpike: LatencySpikeConfig{Probability: 0.01, Duration: "5s"}},
		Services: []ServiceConfig{
			{
				Name:       "api",
				Service:    "api-svc",
				LocalPort:  8080,
				RemotePort: 80,
				Chaos:      ChaosConfig{Enabled: true, ErrorRate: 0.1},
			},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if !loaded.Chaos.Enabled || loaded.Chaos.ErrorRate != 0.02 {
		t.Fatalf("global chaos not round-tripped: %+v", loaded.Chaos)
	}
	if !loaded.Services[0].Chaos.Enabled || loaded.Services[0].Chaos.ErrorRate != 0.1 {
		t.Fatalf("service chaos not round-tripped: %+v", loaded.Services[0].Chaos)
	}
}

func TestChaosConfig_TOMLRoundTrip(t *testing.T) {
	cfg := &Config{
		Context:   "test",
		Namespace: "default",
		Chaos:     ChaosConfig{Enabled: true, ErrorRate: 0.02, LatencySpike: LatencySpikeConfig{Probability: 0.01, Duration: "5s"}},
		Services: []ServiceConfig{
			{
				Name:       "api",
				Service:    "api-svc",
				LocalPort:  8080,
				RemotePort: 80,
				Chaos:      ChaosConfig{Enabled: true, ErrorRate: 0.1},
			},
		},
		format: FormatTOML,
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.toml")
	if err := cfg.SaveTo(path, FormatTOML); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if !loaded.Chaos.Enabled || loaded.Chaos.ErrorRate != 0.02 {
		t.Fatalf("global chaos not round-tripped: %+v", loaded.Chaos)
	}
	if !loaded.Services[0].Chaos.Enabled || loaded.Services[0].Chaos.ErrorRate != 0.1 {
		t.Fatalf("service chaos not round-tripped: %+v", loaded.Services[0].Chaos)
	}
}

func TestValidate_ChaosConfig(t *testing.T) {
	t.Run("valid global chaos", func(t *testing.T) {
		cfg := &Config{
			Chaos: ChaosConfig{Enabled: true, ErrorRate: 0.02},
			Services: []ServiceConfig{
				{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid global chaos", func(t *testing.T) {
		cfg := &Config{
			Chaos: ChaosConfig{Enabled: true, ErrorRate: 2.0},
			Services: []ServiceConfig{
				{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error for invalid global chaos")
		}
	})

	t.Run("invalid per-service chaos", func(t *testing.T) {
		cfg := &Config{
			Services: []ServiceConfig{
				{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80,
					Chaos: ChaosConfig{Enabled: true, LatencySpike: LatencySpikeConfig{Probability: 0.5}}},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error for spike without duration")
		}
	})
}

func TestParsedChaosConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  ParsedChaosConfig
		want bool
	}{
		{"zero", ParsedChaosConfig{}, false},
		{"enabled no faults", ParsedChaosConfig{Enabled: true}, false},
		{"error rate", ParsedChaosConfig{Enabled: true, ErrorRate: 0.02}, true},
		{"spike", ParsedChaosConfig{Enabled: true, LatencySpikeProbability: 0.01, LatencySpikeDuration: 5 * time.Second}, true},
		{"disabled with rate", ParsedChaosConfig{Enabled: false, ErrorRate: 0.02}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsEnabled(); got != tt.want {
				t.Fatalf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

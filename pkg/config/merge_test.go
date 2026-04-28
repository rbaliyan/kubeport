package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// resolveExtendsPath
// ---------------------------------------------------------------------------

func TestResolveExtendsPath_Absolute(t *testing.T) {
	got, err := resolveExtendsPath("/etc/kubeport/global.yaml", "/home/user/project/kubeport.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/etc/kubeport/global.yaml" {
		t.Fatalf("expected absolute path unchanged, got %q", got)
	}
}

func TestResolveExtendsPath_Relative(t *testing.T) {
	got, err := resolveExtendsPath("../global/kubeport.yaml", "/home/user/project/kubeport.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := "/home/user/global/kubeport.yaml"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestResolveExtendsPath_Tilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got, err := resolveExtendsPath("~/.config/kubeport/global.yaml", "/project/kubeport.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config/kubeport/global.yaml")
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// ---------------------------------------------------------------------------
// mergeServices
// ---------------------------------------------------------------------------

func TestMergeServices_OverrideReplacesBase(t *testing.T) {
	base := []ServiceConfig{
		{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80},
	}
	override := []ServiceConfig{
		{Name: "api", Service: "api-svc", LocalPort: 9090, RemotePort: 80},
	}
	got := mergeServices(base, override)
	if len(got) != 1 {
		t.Fatalf("expected 1 service, got %d", len(got))
	}
	if got[0].LocalPort != 9090 {
		t.Fatalf("expected LocalPort 9090, got %d", got[0].LocalPort)
	}
}

func TestMergeServices_BaseOnlyInherited(t *testing.T) {
	base := []ServiceConfig{
		{Name: "db", Service: "postgres", LocalPort: 5432, RemotePort: 5432},
	}
	override := []ServiceConfig{
		{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80},
	}
	got := mergeServices(base, override)
	if len(got) != 2 {
		t.Fatalf("expected 2 services, got %d", len(got))
	}
	if got[0].Name != "db" {
		t.Fatalf("expected base-only service first, got %q", got[0].Name)
	}
	if got[1].Name != "api" {
		t.Fatalf("expected override-only service second, got %q", got[1].Name)
	}
}

func TestMergeServices_OverrideOnlyAppended(t *testing.T) {
	base := []ServiceConfig{}
	override := []ServiceConfig{
		{Name: "redis", Service: "redis", LocalPort: 6379, RemotePort: 6379},
	}
	got := mergeServices(base, override)
	if len(got) != 1 || got[0].Name != "redis" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// mergeHooks
// ---------------------------------------------------------------------------

func TestMergeHooks_OverrideReplacesBase(t *testing.T) {
	base := []HookConfig{{Name: "notify", Type: "shell"}}
	override := []HookConfig{{Name: "notify", Type: "webhook"}}
	got := mergeHooks(base, override)
	if len(got) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(got))
	}
	if got[0].Type != "webhook" {
		t.Fatalf("expected webhook, got %q", got[0].Type)
	}
}

func TestMergeHooks_Additive(t *testing.T) {
	base := []HookConfig{{Name: "global-notify", Type: "shell"}}
	override := []HookConfig{{Name: "local-notify", Type: "webhook"}}
	got := mergeHooks(base, override)
	if len(got) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(got))
	}
	if got[0].Name != "global-notify" || got[1].Name != "local-notify" {
		t.Fatalf("unexpected order: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// mergeSupervisor
// ---------------------------------------------------------------------------

func TestMergeSupervisor_ChildOverrides(t *testing.T) {
	base := SupervisorConfig{
		HealthCheckInterval: "10s",
		MaxRestarts:         5,
		ReadyTimeout:        "15s",
	}
	override := SupervisorConfig{
		HealthCheckInterval: "30s",
	}
	got := mergeSupervisor(base, override)
	if got.HealthCheckInterval != "30s" {
		t.Fatalf("expected 30s, got %q", got.HealthCheckInterval)
	}
	if got.MaxRestarts != 5 {
		t.Fatalf("expected MaxRestarts inherited as 5, got %d", got.MaxRestarts)
	}
	if got.ReadyTimeout != "15s" {
		t.Fatalf("expected ReadyTimeout inherited as 15s, got %q", got.ReadyTimeout)
	}
}

// ---------------------------------------------------------------------------
// mergeConfigs
// ---------------------------------------------------------------------------

func TestMergeConfigs_ScalarInheritance(t *testing.T) {
	base := &Config{
		APIKey:    "sk-global",
		Context:   "prod",
		Namespace: "default",
		Listen:    "tcp://0.0.0.0:50500",
	}
	override := &Config{
		Namespace: "myapp",
	}
	got := mergeConfigs(base, override)

	if got.APIKey != "sk-global" {
		t.Fatalf("APIKey: want %q, got %q", "sk-global", got.APIKey)
	}
	if got.Context != "prod" {
		t.Fatalf("Context: want %q, got %q", "prod", got.Context)
	}
	if got.Namespace != "myapp" {
		t.Fatalf("Namespace: want %q, got %q", "myapp", got.Namespace)
	}
	if got.Listen != "tcp://0.0.0.0:50500" {
		t.Fatalf("Listen: want %q, got %q", "tcp://0.0.0.0:50500", got.Listen)
	}
}

func TestMergeConfigs_ExtendsCleared(t *testing.T) {
	base := &Config{Context: "prod"}
	override := &Config{Extends: "global.yaml", Context: "dev"}
	got := mergeConfigs(base, override)
	if got.Extends != "" {
		t.Fatalf("expected Extends cleared in merged result, got %q", got.Extends)
	}
}

func TestMergeConfigs_SupervisorFieldByField(t *testing.T) {
	base := &Config{
		Supervisor: SupervisorConfig{HealthCheckInterval: "10s", MaxRestarts: 5},
	}
	override := &Config{
		Supervisor: SupervisorConfig{HealthCheckInterval: "30s"},
	}
	got := mergeConfigs(base, override)
	if got.Supervisor.HealthCheckInterval != "30s" {
		t.Fatalf("want 30s, got %q", got.Supervisor.HealthCheckInterval)
	}
	if got.Supervisor.MaxRestarts != 5 {
		t.Fatalf("want MaxRestarts=5 inherited, got %d", got.Supervisor.MaxRestarts)
	}
}

func TestMergeConfigs_ChaosInherited(t *testing.T) {
	base := &Config{
		Chaos: ChaosConfig{Enabled: boolPtr(true), ErrorRate: 0.1},
	}
	override := &Config{}
	got := mergeConfigs(base, override)
	if !boolVal(got.Chaos.Enabled) {
		t.Fatal("expected Chaos inherited from base")
	}
	if got.Chaos.ErrorRate != 0.1 {
		t.Fatalf("expected ErrorRate 0.1, got %v", got.Chaos.ErrorRate)
	}
}

func TestMergeConfigs_ChaosExplicitlyDisabled(t *testing.T) {
	base := &Config{
		Chaos: ChaosConfig{Enabled: boolPtr(true), ErrorRate: 0.1},
	}
	override := &Config{
		Chaos: ChaosConfig{Enabled: boolPtr(false)},
	}
	got := mergeConfigs(base, override)
	if boolVal(got.Chaos.Enabled) {
		t.Fatal("expected Chaos disabled when child sets Enabled: false")
	}
}

func TestMergeConfigs_ChaosOverrideWhenEnabled(t *testing.T) {
	base := &Config{
		Chaos: ChaosConfig{Enabled: boolPtr(true), ErrorRate: 0.1},
	}
	override := &Config{
		Chaos: ChaosConfig{Enabled: boolPtr(true), ErrorRate: 0.5},
	}
	got := mergeConfigs(base, override)
	if got.Chaos.ErrorRate != 0.5 {
		t.Fatalf("expected ErrorRate 0.5 from override, got %v", got.Chaos.ErrorRate)
	}
}

// ---------------------------------------------------------------------------
// Load with extends (integration)
// ---------------------------------------------------------------------------

func writeConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_Extends_ScalarInheritance(t *testing.T) {
	dir := t.TempDir()

	writeConfig(t, dir, "global.yaml", `
api_key: sk-global
context: prod
listen: tcp://0.0.0.0:50500
supervisor:
  health_check_interval: 10s
  max_restarts: 5
`)

	childPath := writeConfig(t, dir, "kubeport.yaml", `
extends: global.yaml
namespace: myapp
services:
  - name: postgres
    service: postgres
    remote_port: 5432
    local_port: 5432
`)

	cfg, err := Load(childPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.APIKey != "sk-global" {
		t.Errorf("APIKey: want sk-global, got %q", cfg.APIKey)
	}
	if cfg.Context != "prod" {
		t.Errorf("Context: want prod, got %q", cfg.Context)
	}
	if cfg.Namespace != "myapp" {
		t.Errorf("Namespace: want myapp, got %q", cfg.Namespace)
	}
	if cfg.Listen != "tcp://0.0.0.0:50500" {
		t.Errorf("Listen: want tcp://0.0.0.0:50500, got %q", cfg.Listen)
	}
	if cfg.Supervisor.HealthCheckInterval != "10s" {
		t.Errorf("Supervisor.HealthCheckInterval: want 10s, got %q", cfg.Supervisor.HealthCheckInterval)
	}
	if cfg.Supervisor.MaxRestarts != 5 {
		t.Errorf("Supervisor.MaxRestarts: want 5, got %d", cfg.Supervisor.MaxRestarts)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "postgres" {
		t.Errorf("Services: unexpected %+v", cfg.Services)
	}
}

func TestLoad_Extends_ChildOverridesParent(t *testing.T) {
	dir := t.TempDir()

	writeConfig(t, dir, "global.yaml", `
api_key: sk-global
context: prod
namespace: default
`)

	childPath := writeConfig(t, dir, "kubeport.yaml", `
extends: global.yaml
api_key: sk-local
context: staging
namespace: myapp
`)

	cfg, err := Load(childPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.APIKey != "sk-local" {
		t.Errorf("APIKey: want sk-local, got %q", cfg.APIKey)
	}
	if cfg.Context != "staging" {
		t.Errorf("Context: want staging, got %q", cfg.Context)
	}
	if cfg.Namespace != "myapp" {
		t.Errorf("Namespace: want myapp, got %q", cfg.Namespace)
	}
}

func TestLoad_Extends_ServicesFromBothConfigs(t *testing.T) {
	dir := t.TempDir()

	writeConfig(t, dir, "global.yaml", `
services:
  - name: db
    service: postgres
    remote_port: 5432
    local_port: 5432
  - name: cache
    service: redis
    remote_port: 6379
    local_port: 6379
`)

	childPath := writeConfig(t, dir, "kubeport.yaml", `
extends: global.yaml
services:
  - name: cache
    service: redis
    remote_port: 6379
    local_port: 16379
  - name: api
    service: my-api
    remote_port: 8080
    local_port: 8080
`)

	cfg, err := Load(childPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Services) != 3 {
		t.Fatalf("expected 3 services (db, cache, api), got %d: %+v", len(cfg.Services), cfg.Services)
	}

	svcByName := make(map[string]ServiceConfig, len(cfg.Services))
	for _, s := range cfg.Services {
		svcByName[s.Name] = s
	}

	if svcByName["db"].LocalPort != 5432 {
		t.Errorf("db LocalPort: want 5432, got %d", svcByName["db"].LocalPort)
	}
	// cache should be overridden by child
	if svcByName["cache"].LocalPort != 16379 {
		t.Errorf("cache LocalPort: want 16379, got %d", svcByName["cache"].LocalPort)
	}
	if svcByName["api"].LocalPort != 8080 {
		t.Errorf("api LocalPort: want 8080, got %d", svcByName["api"].LocalPort)
	}
}

func TestLoad_Extends_CircularDetected(t *testing.T) {
	dir := t.TempDir()

	aPath := filepath.Join(dir, "a.yaml")
	bPath := filepath.Join(dir, "b.yaml")

	if err := os.WriteFile(aPath, []byte("extends: b.yaml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("extends: a.yaml\n"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(aPath)
	if err == nil {
		t.Fatal("expected error for circular extends")
	}
	if !strings.Contains(err.Error(), "circular extends") {
		t.Fatalf("expected circular extends error, got: %v", err)
	}
}

func TestLoad_Extends_MissingParent(t *testing.T) {
	dir := t.TempDir()

	childPath := writeConfig(t, dir, "kubeport.yaml", `
extends: nonexistent.yaml
namespace: myapp
`)

	_, err := Load(childPath)
	if err == nil {
		t.Fatal("expected error for missing parent config")
	}
}

func TestLoad_Extends_Chain(t *testing.T) {
	dir := t.TempDir()

	// C (root) → B → A (leaf we load)
	writeConfig(t, dir, "c.yaml", `
api_key: sk-root
context: root-ctx
supervisor:
  health_check_interval: 5s
`)

	writeConfig(t, dir, "b.yaml", `
extends: c.yaml
context: b-ctx
supervisor:
  max_restarts: 10
`)

	aPath := writeConfig(t, dir, "a.yaml", `
extends: b.yaml
namespace: leaf-ns
`)

	cfg, err := Load(aPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// From C (root)
	if cfg.APIKey != "sk-root" {
		t.Errorf("APIKey from root: want sk-root, got %q", cfg.APIKey)
	}
	if cfg.Supervisor.HealthCheckInterval != "5s" {
		t.Errorf("HealthCheckInterval from root: want 5s, got %q", cfg.Supervisor.HealthCheckInterval)
	}

	// From B (middle)
	if cfg.Context != "b-ctx" {
		t.Errorf("Context from B: want b-ctx, got %q", cfg.Context)
	}
	if cfg.Supervisor.MaxRestarts != 10 {
		t.Errorf("MaxRestarts from B: want 10, got %d", cfg.Supervisor.MaxRestarts)
	}

	// From A (leaf)
	if cfg.Namespace != "leaf-ns" {
		t.Errorf("Namespace from A: want leaf-ns, got %q", cfg.Namespace)
	}
}

func TestLoadForEdit_DoesNotResolveExtends(t *testing.T) {
	dir := t.TempDir()

	writeConfig(t, dir, "global.yaml", `
api_key: sk-global
context: prod
`)

	childPath := writeConfig(t, dir, "kubeport.yaml", `
extends: global.yaml
namespace: myapp
`)

	// LoadForEdit should return the raw child config, not the merged result.
	cfg, err := LoadForEdit(childPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Extends != "global.yaml" {
		t.Errorf("Extends: want global.yaml, got %q", cfg.Extends)
	}
	if cfg.APIKey != "" {
		t.Errorf("APIKey: want empty (not inherited), got %q", cfg.APIKey)
	}
	if cfg.Context != "" {
		t.Errorf("Context: want empty (not inherited), got %q", cfg.Context)
	}
}

func TestLoad_Extends_EnvOverrideAppliedToMergedConfig(t *testing.T) {
	dir := t.TempDir()

	writeConfig(t, dir, "global.yaml", `
context: prod
`)

	childPath := writeConfig(t, dir, "kubeport.yaml", `
extends: global.yaml
namespace: myapp
`)

	t.Setenv("K8S_CONTEXT", "env-ctx")

	cfg, err := Load(childPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Context != "env-ctx" {
		t.Errorf("Context: want env-ctx (from env), got %q", cfg.Context)
	}
}

// ---------------------------------------------------------------------------
// mergeSupervisor — all fields
// ---------------------------------------------------------------------------

func TestMergeSupervisor_AllFields(t *testing.T) {
	base := SupervisorConfig{
		MaxRestarts:          5,
		HealthCheckInterval:  "10s",
		HealthCheckThreshold: 3,
		ReadyTimeout:         "15s",
		BackoffInitial:       "1s",
		BackoffMax:           "30s",
		MaxConnectionAge:     "1h",
	}
	override := SupervisorConfig{
		MaxRestarts:          10,
		HealthCheckInterval:  "30s",
		HealthCheckThreshold: 5,
		ReadyTimeout:         "60s",
		BackoffInitial:       "2s",
		BackoffMax:           "60s",
		MaxConnectionAge:     "2h",
	}
	got := mergeSupervisor(base, override)

	if got.MaxRestarts != 10 {
		t.Errorf("MaxRestarts: want 10, got %d", got.MaxRestarts)
	}
	if got.HealthCheckInterval != "30s" {
		t.Errorf("HealthCheckInterval: want 30s, got %q", got.HealthCheckInterval)
	}
	if got.HealthCheckThreshold != 5 {
		t.Errorf("HealthCheckThreshold: want 5, got %d", got.HealthCheckThreshold)
	}
	if got.ReadyTimeout != "60s" {
		t.Errorf("ReadyTimeout: want 60s, got %q", got.ReadyTimeout)
	}
	if got.BackoffInitial != "2s" {
		t.Errorf("BackoffInitial: want 2s, got %q", got.BackoffInitial)
	}
	if got.BackoffMax != "60s" {
		t.Errorf("BackoffMax: want 60s, got %q", got.BackoffMax)
	}
	if got.MaxConnectionAge != "2h" {
		t.Errorf("MaxConnectionAge: want 2h, got %q", got.MaxConnectionAge)
	}
}

func TestMergeSupervisor_BaseOnlyWhenOverrideEmpty(t *testing.T) {
	base := SupervisorConfig{
		MaxRestarts:          3,
		HealthCheckInterval:  "5s",
		HealthCheckThreshold: 2,
		ReadyTimeout:         "20s",
		BackoffInitial:       "500ms",
		BackoffMax:           "10s",
		MaxConnectionAge:     "30m",
	}
	got := mergeSupervisor(base, SupervisorConfig{})

	if got != base {
		t.Fatalf("expected base unchanged when override is zero, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// mergeHooks — unnamed hook passthrough
// ---------------------------------------------------------------------------

func TestMergeHooks_UnnamedBaseAlwaysIncluded(t *testing.T) {
	unnamed := HookConfig{Type: "shell"} // no Name
	base := []HookConfig{unnamed}
	override := []HookConfig{{Name: "named", Type: "webhook"}}
	got := mergeHooks(base, override)
	if len(got) != 2 {
		t.Fatalf("expected 2 hooks (1 unnamed + 1 named), got %d", len(got))
	}
	if got[0].Name != "" {
		t.Errorf("first hook should be unnamed, got name %q", got[0].Name)
	}
	if got[1].Name != "named" {
		t.Errorf("second hook should be 'named', got %q", got[1].Name)
	}
}

// ---------------------------------------------------------------------------
// mergeConfigs — ProxyServerConfig enable/disable
// ---------------------------------------------------------------------------

func TestMergeConfigs_SOCKSInherited(t *testing.T) {
	base := &Config{
		SOCKS: ProxyServerConfig{Enabled: boolPtr(true), Listen: "127.0.0.1:1080"},
	}
	got := mergeConfigs(base, &Config{})
	if !got.SOCKS.IsEnabled() {
		t.Fatal("SOCKS: expected inherited from base")
	}
	if got.SOCKS.Listen != "127.0.0.1:1080" {
		t.Errorf("SOCKS.Listen: want 127.0.0.1:1080, got %q", got.SOCKS.Listen)
	}
}

func TestMergeConfigs_SOCKSExplicitlyDisabled(t *testing.T) {
	base := &Config{
		SOCKS: ProxyServerConfig{Enabled: boolPtr(true), Listen: "127.0.0.1:1080"},
	}
	override := &Config{
		SOCKS: ProxyServerConfig{Enabled: boolPtr(false)},
	}
	got := mergeConfigs(base, override)
	if got.SOCKS.IsEnabled() {
		t.Fatal("SOCKS: expected disabled when child sets Enabled: false")
	}
}

func TestMergeConfigs_HTTPProxyInherited(t *testing.T) {
	base := &Config{
		HTTPProxy: ProxyServerConfig{Enabled: boolPtr(true), Listen: "127.0.0.1:3128"},
	}
	got := mergeConfigs(base, &Config{})
	if !got.HTTPProxy.IsEnabled() {
		t.Fatal("HTTPProxy: expected inherited from base")
	}
}

// ---------------------------------------------------------------------------
// Load with extends — TOML parent, YAML child (cross-format chain)
// ---------------------------------------------------------------------------

func TestLoad_Extends_TOMLParent(t *testing.T) {
	dir := t.TempDir()

	tomlParent := filepath.Join(dir, "global.toml")
	if err := os.WriteFile(tomlParent, []byte(`
api_key = "sk-toml"
context = "toml-ctx"
`), 0600); err != nil {
		t.Fatal(err)
	}

	childPath := writeConfig(t, dir, "kubeport.yaml", `
extends: global.toml
namespace: cross-format
`)

	cfg, err := Load(childPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIKey != "sk-toml" {
		t.Errorf("APIKey: want sk-toml, got %q", cfg.APIKey)
	}
	if cfg.Context != "toml-ctx" {
		t.Errorf("Context: want toml-ctx, got %q", cfg.Context)
	}
	if cfg.Namespace != "cross-format" {
		t.Errorf("Namespace: want cross-format, got %q", cfg.Namespace)
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// ProxyServerConfig.IsEnabled
// ---------------------------------------------------------------------------

func TestProxyServerConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  ProxyServerConfig
		want bool
	}{
		{"nil (not set)", ProxyServerConfig{}, false},
		{"explicitly false", ProxyServerConfig{Enabled: boolPtr(false)}, false},
		{"explicitly true", ProxyServerConfig{Enabled: boolPtr(true)}, true},
		{"true with listen", ProxyServerConfig{Enabled: boolPtr(true), Listen: "127.0.0.1:1080"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsEnabled(); got != tt.want {
				t.Fatalf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parsePortsFromRaw — uncovered branches
// ---------------------------------------------------------------------------

func TestParsePortsFromRaw_StringAll(t *testing.T) {
	got, err := parsePortsFromRaw("all")
	if err != nil {
		t.Fatal(err)
	}
	if !got.All {
		t.Fatal("expected All=true")
	}
}

func TestParsePortsFromRaw_StringInvalid(t *testing.T) {
	_, err := parsePortsFromRaw("http")
	if err == nil {
		t.Fatal("expected error for non-'all' string")
	}
}

func TestParsePortsFromRaw_SliceWithAllString(t *testing.T) {
	got, err := parsePortsFromRaw([]any{"all"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.All {
		t.Fatal("expected All=true")
	}
}

func TestParsePortsFromRaw_SliceWithMapAndIntFields(t *testing.T) {
	// TOML decodes ints as int64
	raw := []any{
		map[string]any{"name": "http", "port": int64(8080), "local_port": int64(18080)},
	}
	got, err := parsePortsFromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Selectors) != 1 {
		t.Fatalf("expected 1 selector, got %d", len(got.Selectors))
	}
	sel := got.Selectors[0]
	if sel.Name != "http" {
		t.Errorf("Name: want http, got %q", sel.Name)
	}
	if sel.Port != 8080 {
		t.Errorf("Port: want 8080, got %d", sel.Port)
	}
	if sel.LocalPort != 18080 {
		t.Errorf("LocalPort: want 18080, got %d", sel.LocalPort)
	}
}

func TestParsePortsFromRaw_SliceInvalidItem(t *testing.T) {
	_, err := parsePortsFromRaw([]any{42})
	if err == nil {
		t.Fatal("expected error for non-string/non-map item")
	}
	if !strings.Contains(err.Error(), "invalid port selector") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParsePortsFromRaw_InvalidType(t *testing.T) {
	_, err := parsePortsFromRaw(123)
	if err == nil {
		t.Fatal("expected error for integer input")
	}
	if !strings.Contains(err.Error(), "ports must be") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PortsConfig.MarshalYAML — non-simple selectors (Port or LocalPort set)
// ---------------------------------------------------------------------------

func TestPortsConfig_MarshalYAML_NonSimpleSelectors(t *testing.T) {
	cfg := &Config{
		Context:   "test",
		Namespace: "default",
		Services: []ServiceConfig{
			{
				Name:    "api",
				Service: "api-svc",
				Ports: PortsConfig{
					Selectors: []PortSelector{
						{Name: "http", Port: 8080, LocalPort: 18080},
					},
				},
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

	if len(loaded.Services) == 0 {
		t.Fatal("no services after round-trip")
	}
	sels := loaded.Services[0].Ports.Selectors
	if len(sels) != 1 {
		t.Fatalf("expected 1 selector, got %d", len(sels))
	}
	if sels[0].Port != 8080 {
		t.Errorf("Port: want 8080, got %d", sels[0].Port)
	}
	if sels[0].LocalPort != 18080 {
		t.Errorf("LocalPort: want 18080, got %d", sels[0].LocalPort)
	}
}

// ---------------------------------------------------------------------------
// Validate — listen path edge cases
// ---------------------------------------------------------------------------

func TestValidate_Listen_SockPathWithDotDot(t *testing.T) {
	cfg := &Config{
		Listen: "sock://../../tmp/kubeport.sock",
		Services: []ServiceConfig{
			{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for sock:// path with '..'")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_Supervisor_InvalidDuration(t *testing.T) {
	cfg := &Config{
		Supervisor: SupervisorConfig{HealthCheckInterval: "notaduration"},
		Services: []ServiceConfig{
			{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid supervisor duration")
	}
	if !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("expected 'supervisor' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParsedSupervisor — invalid duration error paths
// ---------------------------------------------------------------------------

func TestParsedSupervisor_InvalidDurations(t *testing.T) {
	tests := []struct {
		name   string
		cfg    SupervisorConfig
		errMsg string
	}{
		{"ready_timeout", SupervisorConfig{ReadyTimeout: "bad"}, "ready_timeout"},
		{"backoff_initial", SupervisorConfig{BackoffInitial: "bad"}, "backoff_initial"},
		{"backoff_max", SupervisorConfig{BackoffMax: "bad"}, "backoff_max"},
		{"max_connection_age", SupervisorConfig{MaxConnectionAge: "bad"}, "max_connection_age"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.cfg.ParsedSupervisor()
			if err == nil {
				t.Fatalf("expected error for invalid %s", tt.name)
			}
			if !strings.Contains(err.Error(), tt.errMsg) {
				t.Fatalf("expected %q in error, got: %v", tt.errMsg, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseBandwidth — invalid numeric part
// ---------------------------------------------------------------------------

func TestParseBandwidth_InvalidNumber(t *testing.T) {
	_, err := parseBandwidth("notanumbermbs")
	if err == nil {
		t.Fatal("expected error for non-numeric bandwidth")
	}
}

func TestParseBandwidth_InvalidUnit(t *testing.T) {
	_, err := parseBandwidth("100bits")
	if err == nil {
		t.Fatal("expected error for unknown bandwidth unit")
	}
}

func TestParseBandwidth_ZeroValue(t *testing.T) {
	_, err := parseBandwidth("0mbps")
	if err == nil {
		t.Fatal("expected error for zero bandwidth")
	}
}

// ---------------------------------------------------------------------------
// loadRaw — parse error paths
// ---------------------------------------------------------------------------

func TestLoadRaw_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte(":\tinvalid: yaml: {{{"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadRaw(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRaw_InvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(path, []byte("[[[[invalid toml"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadRaw(path)
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRaw_FileNotFound(t *testing.T) {
	_, err := loadRaw("/nonexistent/path/kubeport.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Discover — config file discovery
// ---------------------------------------------------------------------------

func TestDiscover_FindsInCWD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	if err := os.WriteFile(path, []byte("context: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := Discover()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Fatalf("want %q, got %q", path, got)
	}
}

func TestDiscover_FindsDotPrefixedInCWD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".kubeport.yaml")
	if err := os.WriteFile(path, []byte("context: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := Discover()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Fatalf("want %q, got %q", path, got)
	}
}

func TestDiscover_FindsYMLExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yml")
	if err := os.WriteFile(path, []byte("context: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := Discover()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Fatalf("want %q, got %q", path, got)
	}
}

func TestDiscover_PrefersKubeportYamlOverDotPrefixed(t *testing.T) {
	dir := t.TempDir()
	// Both files exist — kubeport.yaml should win
	plain := filepath.Join(dir, "kubeport.yaml")
	dot := filepath.Join(dir, ".kubeport.yaml")
	if err := os.WriteFile(plain, []byte("context: plain\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dot, []byte("context: dot\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := Discover()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != plain {
		t.Fatalf("expected plain kubeport.yaml, got %q", got)
	}
}

func TestDiscover_NoConfigReturnsError(t *testing.T) {
	// Use an empty temp dir with no config files and no relevant home dirs.
	dir := t.TempDir()
	t.Chdir(dir)

	_, err := Discover()
	if err == nil {
		t.Fatal("expected ErrNoConfig")
	}
	if !strings.Contains(err.Error(), ErrNoConfig.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// InstanceID — sanitizes special characters
// ---------------------------------------------------------------------------

func TestInstanceID_SanitizesSpecialChars(t *testing.T) {
	cfg := &Config{filePath: "/home/user/my-project/kubeport.yaml"}
	id := cfg.InstanceID()
	if id == "" {
		t.Fatal("expected non-empty InstanceID")
	}
	// ID must be filesystem-safe: only alphanumeric, hyphens, underscores allowed
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			t.Fatalf("InstanceID contains unsafe char %q: %q", string(r), id)
		}
	}
}

func TestInstanceID_StableForSamePath(t *testing.T) {
	cfg := &Config{filePath: "/home/user/project/kubeport.yaml"}
	first, second := cfg.InstanceID(), cfg.InstanceID()
	if first != second {
		t.Fatalf("InstanceID must be stable across calls: %q vs %q", first, second)
	}
	cfg2 := &Config{filePath: "/home/user/other/kubeport.yaml"}
	if cfg.InstanceID() == cfg2.InstanceID() {
		t.Fatal("InstanceID must differ for different paths")
	}
}

// ---------------------------------------------------------------------------
// Network.Parse — jitter error path (uncovered branch)
// ---------------------------------------------------------------------------

func TestNetworkConfig_Parse_InvalidJitter(t *testing.T) {
	n := NetworkConfig{Latency: "10ms", Jitter: "notaduration"}
	_, err := n.Parse()
	if err == nil {
		t.Fatal("expected error for invalid jitter")
	}
	if !strings.Contains(err.Error(), "jitter") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CentralDir — path inside ~/.kubeport/ returns that dir
// ---------------------------------------------------------------------------

func TestCentralDir_PathInsideDotKubeport(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	dotDir := filepath.Join(home, ".kubeport")
	fakePath := filepath.Join(dotDir, "kubeport.yaml")
	got := CentralDir(fakePath)
	if got != dotDir {
		t.Fatalf("want %q, got %q", dotDir, got)
	}
}

func TestCentralDir_PathInsideXDG(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	xdg := filepath.Join(home, ".config", "kubeport")
	fakePath := filepath.Join(xdg, "kubeport.yaml")
	got := CentralDir(fakePath)
	if got != xdg {
		t.Fatalf("want %q, got %q", xdg, got)
	}
}

// ---------------------------------------------------------------------------
// InstanceID — edge cases for base fallback
// ---------------------------------------------------------------------------

func TestInstanceID_InlineConfigUsesPrefix(t *testing.T) {
	// Empty filePath triggers the cwd/_inline fallback.
	cfg := &Config{}
	id := cfg.InstanceID()
	if id == "" {
		t.Fatal("expected non-empty InstanceID for in-memory config")
	}
}

func TestInstanceID_RootLevelConfig(t *testing.T) {
	// When the config is at the filesystem root, base falls back to "kubeport".
	cfg := &Config{filePath: "/kubeport.yaml"}
	id := cfg.InstanceID()
	if !strings.HasPrefix(id, "kubeport-") {
		t.Fatalf("expected id to start with 'kubeport-', got %q", id)
	}
}

// ---------------------------------------------------------------------------
// Validate — additional uncovered paths
// ---------------------------------------------------------------------------

func TestValidate_Hook_MissingName(t *testing.T) {
	cfg := &Config{
		Services: []ServiceConfig{
			{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80},
		},
		Hooks: []HookConfig{{Type: "shell"}}, // no name
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for hook without name")
	}
}

// ---------------------------------------------------------------------------
// SaveTo — TOML marshal and directory creation
// ---------------------------------------------------------------------------

func TestSaveTo_TOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "kubeport.toml")
	cfg := &Config{
		Context:   "test-ctx",
		Namespace: "test-ns",
		Services:  []ServiceConfig{{Name: "api", Service: "api-svc", LocalPort: 8080, RemotePort: 80}},
	}
	if err := cfg.SaveTo(path, FormatTOML); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error loading saved TOML: %v", err)
	}
	if loaded.Context != "test-ctx" {
		t.Errorf("Context: want test-ctx, got %q", loaded.Context)
	}
}

// ---------------------------------------------------------------------------
// Init — creates config file correctly
// ---------------------------------------------------------------------------

func TestInit_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	cfg, err := Init(path, FormatYAML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestSaveTo_CreatesIntermediateDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "kubeport.yaml")
	cfg := &Config{
		Context:   "ctx",
		Namespace: "ns",
		Services:  []ServiceConfig{{Name: "x", Service: "x-svc", LocalPort: 9090, RemotePort: 90}},
	}
	if err := cfg.SaveTo(path, FormatYAML); err != nil {
		t.Fatalf("unexpected error creating intermediate dirs: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestInit_ErrorIfExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.yaml")
	if err := os.WriteFile(path, []byte("context: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Init(path, FormatYAML)
	if err == nil {
		t.Fatal("expected error when file already exists")
	}
}

// ---------------------------------------------------------------------------
// parseBandwidth — invalid numeric part with valid suffix
// ---------------------------------------------------------------------------

func TestParseBandwidth_InvalidNumberWithValidSuffix(t *testing.T) {
	_, err := parseBandwidth("@@@mbps")
	if err == nil {
		t.Fatal("expected error for non-numeric value with valid suffix")
	}
	if !strings.Contains(err.Error(), "invalid number") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Discover — finds TOML config in CWD
// ---------------------------------------------------------------------------

func TestDiscover_FindsTOMLInCWD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeport.toml")
	if err := os.WriteFile(path, []byte("context = \"test\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := Discover()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Fatalf("want %q, got %q", path, got)
	}
}

// ---------------------------------------------------------------------------
// UnmarshalYAML — integer port in ports list
// ---------------------------------------------------------------------------

func TestUnmarshalYAML_PortsWithIntPort(t *testing.T) {
	input := `
services:
  - name: api
    service: api-svc
    ports:
      - name: http
        port: 8080
        local_port: 18080
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Services) == 0 {
		t.Fatal("no services")
	}
	sels := cfg.Services[0].Ports.Selectors
	if len(sels) != 1 {
		t.Fatalf("expected 1 selector, got %d", len(sels))
	}
	if sels[0].Port != 8080 {
		t.Errorf("Port: want 8080, got %d", sels[0].Port)
	}
	if sels[0].LocalPort != 18080 {
		t.Errorf("LocalPort: want 18080, got %d", sels[0].LocalPort)
	}
}

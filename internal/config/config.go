// Package config provides configuration loading for kubeport.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// ListenMode describes how the daemon listens for connections.
type ListenMode int

const (
	ListenUnix ListenMode = iota
	ListenTCP
)

// ListenConfig holds the resolved listen mode and address.
type ListenConfig struct {
	Mode    ListenMode
	Address string
}


// Sentinel errors for common failure modes.
var (
	ErrNoConfig       = errors.New("no config file found")
	ErrNoServices     = errors.New("no services defined")
	ErrServiceExists  = errors.New("service already exists")
	ErrServiceNotFound = errors.New("service not found")
	ErrConfigExists   = errors.New("config file already exists")
)

// Format represents the config file format.
type Format string

const (
	FormatYAML Format = "yaml"
	FormatTOML Format = "toml"
)

// PortSelector specifies a port to forward with optional local port override.
type PortSelector struct {
	Name      string `yaml:"name,omitempty" toml:"name,omitempty"`
	Port      int    `yaml:"port,omitempty" toml:"port,omitempty"`
	LocalPort int    `yaml:"local_port,omitempty" toml:"local_port,omitempty"`
}

// PortsConfig handles the polymorphic "ports" field.
// Can be the string "all" or a list of PortSelector.
type PortsConfig struct {
	All       bool
	Selectors []PortSelector
}

// IsSet returns true if the PortsConfig specifies any port selection.
func (p PortsConfig) IsSet() bool {
	return p.All || len(p.Selectors) > 0
}

// UnmarshalYAML implements custom YAML unmarshaling for PortsConfig.
// Accepts: "all", ["http", "grpc"], or [{name: http, local_port: 8080}].
func (p *PortsConfig) UnmarshalYAML(unmarshal func(any) error) error {
	// Try string first ("all")
	var s string
	if err := unmarshal(&s); err == nil {
		if s == "all" {
			p.All = true
			return nil
		}
		return fmt.Errorf("invalid ports value %q: expected \"all\" or a list", s)
	}

	// Try list of mixed items (strings or objects)
	var raw []any
	if err := unmarshal(&raw); err != nil {
		return fmt.Errorf("ports must be \"all\" or a list of port selectors")
	}

	for _, item := range raw {
		switch v := item.(type) {
		case string:
			p.Selectors = append(p.Selectors, PortSelector{Name: v})
		case map[string]any:
			ps := PortSelector{}
			if name, ok := v["name"].(string); ok {
				ps.Name = name
			}
			if port, ok := v["port"].(int); ok {
				ps.Port = port
			} else if port, ok := v["port"].(float64); ok {
				ps.Port = int(port)
			}
			if lp, ok := v["local_port"].(int); ok {
				ps.LocalPort = lp
			} else if lp, ok := v["local_port"].(float64); ok {
				ps.LocalPort = int(lp)
			}
			p.Selectors = append(p.Selectors, ps)
		default:
			return fmt.Errorf("invalid port selector: expected string or object")
		}
	}
	return nil
}

// MarshalYAML implements custom YAML marshaling for PortsConfig.
func (p PortsConfig) MarshalYAML() (interface{}, error) {
	if p.All {
		return "all", nil
	}
	if len(p.Selectors) == 0 {
		return nil, nil
	}
	// If all selectors are name-only, emit as string list
	allSimple := true
	for _, s := range p.Selectors {
		if s.Port != 0 || s.LocalPort != 0 {
			allSimple = false
			break
		}
	}
	if allSimple {
		names := make([]string, len(p.Selectors))
		for i, s := range p.Selectors {
			names[i] = s.Name
		}
		return names, nil
	}
	return p.Selectors, nil
}

// UnmarshalTOML implements custom TOML unmarshaling for PortsConfig.
func (p *PortsConfig) UnmarshalTOML(data any) error {
	switch v := data.(type) {
	case string:
		if v == "all" {
			p.All = true
			return nil
		}
		return fmt.Errorf("invalid ports value %q: expected \"all\" or a list", v)
	case []any:
		for _, item := range v {
			switch elem := item.(type) {
			case string:
				if elem == "all" {
					p.All = true
					return nil
				}
				p.Selectors = append(p.Selectors, PortSelector{Name: elem})
			case map[string]any:
				ps := PortSelector{}
				if name, ok := elem["name"].(string); ok {
					ps.Name = name
				}
				if port, ok := elem["port"].(int64); ok {
					ps.Port = int(port)
				}
				if lp, ok := elem["local_port"].(int64); ok {
					ps.LocalPort = int(lp)
				}
				p.Selectors = append(p.Selectors, ps)
			default:
				return fmt.Errorf("invalid port selector: expected string or object")
			}
		}
		return nil
	default:
		return fmt.Errorf("ports must be \"all\" or a list")
	}
}

// ServiceConfig defines a Kubernetes service or pod to port-forward.
type ServiceConfig struct {
	Name            string      `yaml:"name" toml:"name"`
	Service         string      `yaml:"service,omitempty" toml:"service,omitempty"`
	Pod             string      `yaml:"pod,omitempty" toml:"pod,omitempty"`
	LocalPort       int         `yaml:"local_port,omitempty" toml:"local_port,omitempty"`
	RemotePort      int         `yaml:"remote_port,omitempty" toml:"remote_port,omitempty"`
	Namespace       string      `yaml:"namespace,omitempty" toml:"namespace,omitempty"`
	Ports           PortsConfig `yaml:"ports,omitempty" toml:"ports,omitempty"`
	ExcludePorts    []string    `yaml:"exclude_ports,omitempty" toml:"exclude_ports,omitempty"`
	LocalPortOffset int         `yaml:"local_port_offset,omitempty" toml:"local_port_offset,omitempty"`
	ParentName      string      `yaml:"-" toml:"-"` // set at runtime for expanded multi-port forwards
	PortName        string      `yaml:"-" toml:"-"` // set at runtime for expanded multi-port forwards
}

// IsPod returns true if this config targets a pod directly.
func (s ServiceConfig) IsPod() bool {
	return s.Pod != ""
}

// Target returns the Kubernetes resource name (pod or service).
func (s ServiceConfig) Target() string {
	if s.Pod != "" {
		return s.Pod
	}
	return s.Service
}

// IsMultiPort returns true if this config uses multi-port mode.
func (s ServiceConfig) IsMultiPort() bool {
	return s.Ports.All || len(s.Ports.Selectors) > 0
}

// HookConfig defines a lifecycle hook.
type HookConfig struct {
	Name           string            `yaml:"name" toml:"name"`
	Type           string            `yaml:"type" toml:"type"`                                           // "shell", "webhook", "exec"
	Events         []string          `yaml:"events,omitempty" toml:"events,omitempty"`                   // event names to listen for
	Timeout        string            `yaml:"timeout,omitempty" toml:"timeout,omitempty"`                 // duration string (e.g., "30s")
	FailMode       string            `yaml:"fail_mode,omitempty" toml:"fail_mode,omitempty"`             // "open" (default) or "closed"
	Shell          map[string]string `yaml:"shell,omitempty" toml:"shell,omitempty"`                     // event_name -> shell command
	Webhook        *WebhookConfig    `yaml:"webhook,omitempty" toml:"webhook,omitempty"`                 // webhook config
	Exec           *ExecConfig       `yaml:"exec,omitempty" toml:"exec,omitempty"`                       // exec config
	FilterServices []string          `yaml:"filter_services,omitempty" toml:"filter_services,omitempty"` // only trigger for these services
}

// WebhookConfig defines the configuration for a webhook hook.
type WebhookConfig struct {
	URL          string            `yaml:"url" toml:"url"`
	Headers      map[string]string `yaml:"headers,omitempty" toml:"headers,omitempty"`
	BodyTemplate string            `yaml:"body_template,omitempty" toml:"body_template,omitempty"` // optional; uses ${VAR} expansion
}

// ExecConfig defines the configuration for an exec hook.
type ExecConfig struct {
	Command []string `yaml:"command" toml:"command"`
}

// SupervisorConfig holds tuning parameters for the port-forward supervisor.
type SupervisorConfig struct {
	MaxRestarts          int    `yaml:"max_restarts,omitempty" toml:"max_restarts,omitempty"`                     // 0 = unlimited
	HealthCheckInterval  string `yaml:"health_check_interval,omitempty" toml:"health_check_interval,omitempty"`   // e.g., "10s"
	HealthCheckThreshold int    `yaml:"health_check_threshold,omitempty" toml:"health_check_threshold,omitempty"` // consecutive failures
	ReadyTimeout         string `yaml:"ready_timeout,omitempty" toml:"ready_timeout,omitempty"`                   // e.g., "15s"
	BackoffInitial       string `yaml:"backoff_initial,omitempty" toml:"backoff_initial,omitempty"`               // e.g., "1s"
	BackoffMax           string `yaml:"backoff_max,omitempty" toml:"backoff_max,omitempty"`                       // e.g., "30s"
}

// Config holds the full proxy configuration.
type Config struct {
	Context     string           `yaml:"context" toml:"context"`
	Namespace   string           `yaml:"namespace" toml:"namespace"`
	LogFilePath string           `yaml:"log_file,omitempty" toml:"log_file,omitempty"`
	Listen      string           `yaml:"listen,omitempty" toml:"listen,omitempty"`
	APIKey      string           `yaml:"api_key,omitempty" toml:"api_key,omitempty"`
	Host        string           `yaml:"host,omitempty" toml:"host,omitempty"`
	Services    []ServiceConfig  `yaml:"services" toml:"services"`
	Hooks       []HookConfig     `yaml:"hooks,omitempty" toml:"hooks,omitempty"`
	Supervisor  SupervisorConfig `yaml:"supervisor,omitempty" toml:"supervisor,omitempty"`

	// Runtime fields (not serialized)
	filePath string
	format   Format
}

// NewInMemory creates a Config from CLI arguments without a file.
// PIDFile/LogFile/SocketFile default to CWD-relative paths.
func NewInMemory(kubeContext, namespace string, services []ServiceConfig) *Config {
	return &Config{
		Context:   kubeContext,
		Namespace: namespace,
		Services:  services,
	}
}

// FilePath returns the path the config was loaded from.
func (c *Config) FilePath() string {
	return c.filePath
}

// Format returns the config file format.
func (c *Config) FileFormat() Format {
	return c.format
}

// PIDFile returns the path for the PID file, derived from the config file location.
func (c *Config) PIDFile() string {
	if c.filePath == "" {
		return ".kubeport.pid"
	}
	return filepath.Join(filepath.Dir(c.filePath), ".kubeport.pid")
}

// LogFile returns the path for the log file. If LogFilePath is set, it is used directly.
// Otherwise the path is derived from the config file location.
func (c *Config) LogFile() string {
	if c.LogFilePath != "" {
		return c.LogFilePath
	}
	if c.filePath == "" {
		return ".kubeport.log"
	}
	return filepath.Join(filepath.Dir(c.filePath), ".kubeport.log")
}

// SocketFile returns the path for the Unix domain socket. If Listen is set with a
// "sock://" prefix, that path is used. Otherwise the path is derived from the config
// file location.
func (c *Config) SocketFile() string {
	if path, ok := strings.CutPrefix(c.Listen, "sock://"); ok {
		return path
	}
	if c.filePath == "" {
		return ".kubeport.sock"
	}
	return filepath.Join(filepath.Dir(c.filePath), ".kubeport.sock")
}

// ListenAddress returns the resolved listen configuration.
// If Listen starts with "tcp://", mode is ListenTCP; otherwise ListenUnix.
func (c *Config) ListenAddress() ListenConfig {
	if addr, ok := strings.CutPrefix(c.Listen, "tcp://"); ok {
		return ListenConfig{Mode: ListenTCP, Address: addr}
	}
	return ListenConfig{Mode: ListenUnix, Address: c.SocketFile()}
}

// detectFormat returns the format based on file extension.
func detectFormat(path string) Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return FormatTOML
	default:
		return FormatYAML
	}
}

// Load reads and parses a config file (YAML or TOML), applying environment variable overrides.
func Load(path string) (*Config, error) {
	cfg, err := loadRaw(path)
	if err != nil {
		return nil, err
	}

	// Apply environment overrides
	if v := os.Getenv("K8S_CONTEXT"); v != "" {
		cfg.Context = v
	}
	if v := os.Getenv("K8S_NAMESPACE"); v != "" {
		cfg.Namespace = v
	}
	if v := os.Getenv("KUBEPORT_API_KEY"); v != "" {
		cfg.APIKey = v
	}

	return cfg, nil
}

// LoadForEdit reads and parses a config file without applying environment variable overrides.
// Use this when modifying and saving config to avoid persisting env var values.
func LoadForEdit(path string) (*Config, error) {
	return loadRaw(path)
}

// loadRaw reads and parses a config file without env overrides.
func loadRaw(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{}
	format := detectFormat(path)

	switch format {
	case FormatTOML:
		if err := toml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	default:
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	cfg.filePath = path
	cfg.format = format

	return cfg, nil
}

// Save writes the config back to the file it was loaded from, in the same format.
func (c *Config) Save() error {
	if c.filePath == "" {
		return fmt.Errorf("no file path set; use SaveTo instead")
	}
	return c.SaveTo(c.filePath, c.format)
}

// SaveTo writes the config to the given path in the specified format.
func (c *Config) SaveTo(path string, format Format) error {
	var data []byte
	var err error

	switch format {
	case FormatTOML:
		data, err = toml.Marshal(c)
	default:
		data, err = yaml.Marshal(c)
	}
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}

	c.filePath = path
	c.format = format
	return nil
}

// Init creates a new config file with defaults at the given path.
func Init(path string, format Format) (*Config, error) {
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s: %w", path, ErrConfigExists)
	}

	cfg := &Config{
		Context:   "",
		Namespace: "default",
		Services:  []ServiceConfig{},
		filePath:  path,
		format:    format,
	}

	if err := cfg.SaveTo(path, format); err != nil {
		return nil, err
	}

	return cfg, nil
}

// AddService adds a service to the config. Returns an error if a service with the same name exists.
func (c *Config) AddService(svc ServiceConfig) error {
	for _, existing := range c.Services {
		if existing.Name == svc.Name {
			return fmt.Errorf("service %q: %w", svc.Name, ErrServiceExists)
		}
	}
	c.Services = append(c.Services, svc)
	return nil
}

// RemoveService removes a service by name. Returns an error if not found.
func (c *Config) RemoveService(name string) error {
	for i, svc := range c.Services {
		if svc.Name == name {
			c.Services = append(c.Services[:i], c.Services[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("service %q: %w", name, ErrServiceNotFound)
}

// Discover searches for a config file in standard locations.
// Search order: CWD (kubeport.* then .kubeport.*) > ~/.config/kubeport > ~/.kubeport
func Discover() (string, error) {
	candidates := []string{
		"kubeport.yaml",
		"kubeport.yml",
		"kubeport.toml",
	}

	dotCandidates := []string{
		".kubeport.yaml",
		".kubeport.yml",
		".kubeport.toml",
	}

	// Check current directory: kubeport.* first, then .kubeport.*
	for _, names := range [][]string{candidates, dotCandidates} {
		for _, name := range names {
			if _, err := os.Stat(name); err == nil {
				abs, err := filepath.Abs(name)
				if err != nil {
					return name, nil
				}
				return abs, nil
			}
		}
	}

	home, err := os.UserHomeDir()
	if err == nil {
		// Check ~/.config/kubeport/
		for _, name := range candidates {
			p := filepath.Join(home, ".config", "kubeport", name)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}

		// Check ~/.kubeport/
		for _, name := range candidates {
			p := filepath.Join(home, ".kubeport", name)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}

	return "", fmt.Errorf("%w; create kubeport.yaml or use --config", ErrNoConfig)
}

// ValidateService checks a single service config for structural correctness.
func ValidateService(svc ServiceConfig) error {
	if svc.Name == "" {
		return fmt.Errorf("service name is required")
	}
	if svc.Service == "" && svc.Pod == "" {
		return fmt.Errorf("service %q: must set either 'service' or 'pod'", svc.Name)
	}
	if svc.Service != "" && svc.Pod != "" {
		return fmt.Errorf("service %q: set 'service' or 'pod', not both", svc.Name)
	}

	if svc.IsMultiPort() {
		// Multi-port mode: RemotePort and LocalPort must be zero
		if svc.RemotePort != 0 {
			return fmt.Errorf("service %q: remote_port must not be set in multi-port mode", svc.Name)
		}
		if svc.LocalPort != 0 {
			return fmt.Errorf("service %q: local_port must not be set in multi-port mode", svc.Name)
		}
		// ExcludePorts only valid with ports: all
		if len(svc.ExcludePorts) > 0 && !svc.Ports.All {
			return fmt.Errorf("service %q: exclude_ports can only be used with ports: all", svc.Name)
		}
		if svc.LocalPortOffset < 0 {
			return fmt.Errorf("service %q: local_port_offset must be non-negative", svc.Name)
		}
	} else {
		// Legacy mode: multi-port fields must not be set
		if len(svc.ExcludePorts) > 0 {
			return fmt.Errorf("service %q: exclude_ports requires multi-port mode (set ports field)", svc.Name)
		}
		if svc.LocalPortOffset != 0 {
			return fmt.Errorf("service %q: local_port_offset requires multi-port mode (set ports field)", svc.Name)
		}
		if svc.LocalPort < 0 || svc.LocalPort > 65535 {
			return fmt.Errorf("service %q: invalid local_port %d", svc.Name, svc.LocalPort)
		}
		if svc.RemotePort <= 0 || svc.RemotePort > 65535 {
			return fmt.Errorf("service %q: invalid remote_port %d", svc.Name, svc.RemotePort)
		}
	}
	return nil
}

// Validate checks the config for errors.
func (c *Config) Validate() error {
	if len(c.Services) == 0 {
		return ErrNoServices
	}

	seen := make(map[int]string)
	for i, svc := range c.Services {
		if svc.Service == "" && svc.Pod == "" {
			return fmt.Errorf("service[%d] (%s): must set either 'service' or 'pod'", i, svc.Name)
		}
		if svc.Service != "" && svc.Pod != "" {
			return fmt.Errorf("service[%d] (%s): set 'service' or 'pod', not both", i, svc.Name)
		}

		if svc.IsMultiPort() {
			// Multi-port mode: RemotePort and LocalPort must be zero
			if svc.RemotePort != 0 {
				return fmt.Errorf("service[%d] (%s): remote_port must not be set in multi-port mode", i, svc.Name)
			}
			if svc.LocalPort != 0 {
				return fmt.Errorf("service[%d] (%s): local_port must not be set in multi-port mode", i, svc.Name)
			}
			if len(svc.ExcludePorts) > 0 && !svc.Ports.All {
				return fmt.Errorf("service[%d] (%s): exclude_ports can only be used with ports: all", i, svc.Name)
			}
			if svc.LocalPortOffset < 0 {
				return fmt.Errorf("service[%d] (%s): local_port_offset must be non-negative", i, svc.Name)
			}
			// Skip local port duplicate checking for multi-port (actual ports unknown until resolution)
		} else {
			if len(svc.ExcludePorts) > 0 {
				return fmt.Errorf("service[%d] (%s): exclude_ports requires multi-port mode", i, svc.Name)
			}
			if svc.LocalPortOffset != 0 {
				return fmt.Errorf("service[%d] (%s): local_port_offset requires multi-port mode", i, svc.Name)
			}
			// local_port 0 means dynamic port assignment by the OS
			if svc.LocalPort < 0 || svc.LocalPort > 65535 {
				return fmt.Errorf("service[%d] (%s): invalid local_port %d", i, svc.Name, svc.LocalPort)
			}
			if svc.RemotePort <= 0 || svc.RemotePort > 65535 {
				return fmt.Errorf("service[%d] (%s): invalid remote_port %d", i, svc.Name, svc.RemotePort)
			}
			// Skip duplicate check for dynamic ports (0)
			if svc.LocalPort != 0 {
				if prev, ok := seen[svc.LocalPort]; ok {
					return fmt.Errorf("service[%d] (%s): local_port %d already used by %s", i, svc.Name, svc.LocalPort, prev)
				}
				seen[svc.LocalPort] = svc.Name
			}
		}
	}

	// Validate listen address
	if c.Listen != "" {
		switch {
		case strings.HasPrefix(c.Listen, "sock://"):
			path, _ := strings.CutPrefix(c.Listen, "sock://")
			if path == "" {
				return fmt.Errorf("listen: empty path after sock://")
			}
		case strings.HasPrefix(c.Listen, "tcp://"):
			addr, _ := strings.CutPrefix(c.Listen, "tcp://")
			if _, _, err := net.SplitHostPort(addr); err != nil {
				return fmt.Errorf("listen: invalid tcp address %q: %w", addr, err)
			}
			if c.APIKey == "" {
				return fmt.Errorf("listen: api_key is required when using tcp:// listener")
			}
		default:
			return fmt.Errorf("listen: unsupported scheme; must start with sock:// or tcp://")
		}
	}

	// Validate hooks
	for i, h := range c.Hooks {
		if err := validateHook(i, h); err != nil {
			return err
		}
	}

	// Validate supervisor config durations
	if err := c.Supervisor.validate(); err != nil {
		return fmt.Errorf("supervisor: %w", err)
	}

	return nil
}

// validateHook checks structural fields of a hook config.
// Type-specific and event-name validation is deferred to hook.BuildFromConfig
// to avoid duplicating the hook package's validation logic.
func validateHook(idx int, h HookConfig) error {
	prefix := fmt.Sprintf("hook[%d] (%s)", idx, h.Name)
	if h.Name == "" {
		return fmt.Errorf("hook[%d]: name is required", idx)
	}
	if h.Type == "" {
		return fmt.Errorf("%s: type is required", prefix)
	}
	if h.Timeout != "" {
		if _, err := time.ParseDuration(h.Timeout); err != nil {
			return fmt.Errorf("%s: invalid timeout %q: %w", prefix, h.Timeout, err)
		}
	}
	if h.FailMode != "" && h.FailMode != "open" && h.FailMode != "closed" {
		return fmt.Errorf("%s: invalid fail_mode %q (use \"open\" or \"closed\")", prefix, h.FailMode)
	}
	return nil
}

func (s SupervisorConfig) validate() error {
	for _, pair := range []struct{ name, val string }{
		{"health_check_interval", s.HealthCheckInterval},
		{"ready_timeout", s.ReadyTimeout},
		{"backoff_initial", s.BackoffInitial},
		{"backoff_max", s.BackoffMax},
	} {
		if pair.val != "" {
			if _, err := time.ParseDuration(pair.val); err != nil {
				return fmt.Errorf("invalid %s %q: %w", pair.name, pair.val, err)
			}
		}
	}
	return nil
}

// ParsedSupervisorConfig holds supervisor config with defaults applied and durations parsed.
type ParsedSupervisorConfig struct {
	MaxRestarts          int
	HealthCheckThreshold int
	HealthCheckInterval  time.Duration
	ReadyTimeout         time.Duration
	BackoffInitial       time.Duration
	BackoffMax           time.Duration
}

// ParsedSupervisor returns supervisor config with defaults applied.
func (s SupervisorConfig) ParsedSupervisor() ParsedSupervisorConfig {
	threshold := s.HealthCheckThreshold
	if threshold <= 0 {
		threshold = 3
	}
	return ParsedSupervisorConfig{
		MaxRestarts:          s.MaxRestarts,
		HealthCheckThreshold: threshold,
		HealthCheckInterval:  parseDurationOr(s.HealthCheckInterval, 10*time.Second),
		ReadyTimeout:         parseDurationOr(s.ReadyTimeout, 15*time.Second),
		BackoffInitial:       parseDurationOr(s.BackoffInitial, 1*time.Second),
		BackoffMax:           parseDurationOr(s.BackoffMax, 30*time.Second),
	}
}

// LoadServices reads a YAML/TOML file and returns only the services list.
// It validates each service individually. Other config fields (context, namespace,
// hooks, supervisor) in the file are ignored — this is designed for overlay files.
func LoadServices(path string) ([]ServiceConfig, error) {
	cfg, err := loadRaw(path)
	if err != nil {
		return nil, err
	}

	for i, svc := range cfg.Services {
		if err := ValidateService(svc); err != nil {
			return nil, fmt.Errorf("service[%d]: %w", i, err)
		}
	}

	return cfg.Services, nil
}

func parseDurationOr(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

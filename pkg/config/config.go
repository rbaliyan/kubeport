// Package config provides configuration loading for kubeport.
package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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
	ErrNoConfig        = errors.New("no config file found")
	ErrNoServices      = errors.New("no services defined")
	ErrServiceExists   = errors.New("service already exists")
	ErrServiceNotFound = errors.New("service not found")
	ErrConfigExists    = errors.New("config file already exists")
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
func (p PortsConfig) MarshalYAML() (any, error) {
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

// parsePortsFromRaw converts a raw TOML-decoded value into a PortsConfig.
// Used by the TOML decoding path since go-toml/v2 does not support the
// UnmarshalTOML(any) interface.
func parsePortsFromRaw(data any) (PortsConfig, error) {
	var p PortsConfig
	switch v := data.(type) {
	case string:
		if v == "all" {
			p.All = true
			return p, nil
		}
		return p, fmt.Errorf("invalid ports value %q: expected \"all\" or a list", v)
	case []any:
		for _, item := range v {
			switch elem := item.(type) {
			case string:
				if elem == "all" {
					p.All = true
					return p, nil
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
				return p, fmt.Errorf("invalid port selector: expected string or object")
			}
		}
		return p, nil
	default:
		return p, fmt.Errorf("ports must be \"all\" or a list")
	}
}

// ServiceConfig defines a Kubernetes service or pod to port-forward.
type ServiceConfig struct {
	Name            string        `yaml:"name" toml:"name"`
	Service         string        `yaml:"service,omitempty" toml:"service,omitempty"`
	Pod             string        `yaml:"pod,omitempty" toml:"pod,omitempty"`
	LocalPort       int           `yaml:"local_port,omitempty" toml:"local_port,omitempty"`
	RemotePort      int           `yaml:"remote_port,omitempty" toml:"remote_port,omitempty"`
	Namespace       string        `yaml:"namespace,omitempty" toml:"namespace,omitempty"`
	Ports           PortsConfig   `yaml:"ports,omitempty" toml:"ports,omitempty"`
	ExcludePorts    []string      `yaml:"exclude_ports,omitempty" toml:"exclude_ports,omitempty"`
	LocalPortOffset int           `yaml:"local_port_offset,omitempty" toml:"local_port_offset,omitempty"`
	Network         NetworkConfig `yaml:"network,omitempty" toml:"network,omitempty"`
	Chaos           ChaosConfig   `yaml:"chaos,omitempty" toml:"chaos,omitempty"`
	ParentName      string        `yaml:"-" toml:"-"` // set at runtime for expanded multi-port forwards
	PortName        string        `yaml:"-" toml:"-"` // set at runtime for expanded multi-port forwards
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
	MaxConnectionAge     string `yaml:"max_connection_age,omitempty" toml:"max_connection_age,omitempty"`         // e.g., "30m"; 0 = disabled
}

// ProxyServerConfig holds optional proxy server configuration shared by SOCKS5
// and HTTP proxy modes.
type ProxyServerConfig struct {
	Enabled    bool   `yaml:"enabled,omitempty" toml:"enabled,omitempty"`         // auto-start with daemon
	Listen     string `yaml:"listen,omitempty" toml:"listen,omitempty"`           // listen address
	Username   string `yaml:"username,omitempty" toml:"username,omitempty"`       // auth username
	Password   string `yaml:"password,omitempty" toml:"password,omitempty"`       // auth password
	FuzzyMatch *bool  `yaml:"fuzzy_match,omitempty" toml:"fuzzy_match,omitempty"` // nil=true; headless FQDN resolution
}

// NetworkConfig holds optional network simulation settings for latency injection
// and bandwidth throttling. A zero value means simulation is disabled.
type NetworkConfig struct {
	Latency   string `yaml:"latency,omitempty" toml:"latency,omitempty"`     // Go duration, e.g. "50ms"
	Jitter    string `yaml:"jitter,omitempty" toml:"jitter,omitempty"`       // Go duration, e.g. "10ms"
	Bandwidth string `yaml:"bandwidth,omitempty" toml:"bandwidth,omitempty"` // e.g. "5mbps", "500kbps", "1gbps"
}

// IsSet returns true if any network simulation is configured.
func (n NetworkConfig) IsSet() bool {
	return n.Latency != "" || n.Jitter != "" || n.Bandwidth != ""
}

// ParsedNetworkConfig holds parsed, ready-to-use network simulation settings.
type ParsedNetworkConfig struct {
	Latency     time.Duration // 0 = no latency injection
	Jitter      time.Duration // 0 = no jitter
	BytesPerSec int64         // 0 = no bandwidth cap
}

// IsEnabled returns true if any network simulation is active.
func (p ParsedNetworkConfig) IsEnabled() bool {
	return p.Latency > 0 || p.Jitter > 0 || p.BytesPerSec > 0
}

// Parse validates and parses the NetworkConfig into a ParsedNetworkConfig.
func (n NetworkConfig) Parse() (ParsedNetworkConfig, error) {
	var p ParsedNetworkConfig
	var err error

	if n.Latency != "" {
		p.Latency, err = time.ParseDuration(n.Latency)
		if err != nil {
			return p, fmt.Errorf("invalid latency %q: %w", n.Latency, err)
		}
		if p.Latency < 0 {
			return p, fmt.Errorf("latency must be non-negative, got %s", n.Latency)
		}
	}

	if n.Jitter != "" {
		p.Jitter, err = time.ParseDuration(n.Jitter)
		if err != nil {
			return p, fmt.Errorf("invalid jitter %q: %w", n.Jitter, err)
		}
		if p.Jitter < 0 {
			return p, fmt.Errorf("jitter must be non-negative, got %s", n.Jitter)
		}
	}

	if p.Jitter > 0 && p.Latency == 0 {
		return p, fmt.Errorf("jitter requires latency to be set")
	}
	if p.Jitter > p.Latency && p.Latency > 0 {
		return p, fmt.Errorf("jitter (%s) must not exceed latency (%s)", n.Jitter, n.Latency)
	}

	if n.Bandwidth != "" {
		p.BytesPerSec, err = parseBandwidth(n.Bandwidth)
		if err != nil {
			return p, fmt.Errorf("invalid bandwidth %q: %w", n.Bandwidth, err)
		}
	}

	return p, nil
}

// LatencySpikeConfig holds configuration for probabilistic latency spikes.
type LatencySpikeConfig struct {
	Probability float64 `yaml:"probability,omitempty" toml:"probability,omitempty"` // 0.0-1.0
	Duration    string  `yaml:"duration,omitempty" toml:"duration,omitempty"`       // Go duration, e.g. "5s"
}

// ChaosConfig holds optional chaos engineering settings for fault injection.
// A zero value (or Enabled=false) means chaos is disabled.
type ChaosConfig struct {
	Enabled      bool               `yaml:"enabled,omitempty" toml:"enabled,omitempty"`
	ErrorRate    float64            `yaml:"error_rate,omitempty" toml:"error_rate,omitempty"`         // 0.0-1.0, fraction of writes that fail
	LatencySpike LatencySpikeConfig `yaml:"latency_spike,omitempty" toml:"latency_spike,omitempty"`
}

// IsSet returns true if chaos injection is configured and enabled.
func (c ChaosConfig) IsSet() bool {
	return c.Enabled
}

// ParsedChaosConfig holds parsed, ready-to-use chaos engineering settings.
type ParsedChaosConfig struct {
	Enabled                 bool
	ErrorRate               float64       // 0.0-1.0
	LatencySpikeProbability float64       // 0.0-1.0
	LatencySpikeDuration    time.Duration // 0 = no spikes
}

// IsEnabled returns true if any chaos injection is active.
func (p ParsedChaosConfig) IsEnabled() bool {
	return p.Enabled && (p.ErrorRate > 0 || p.LatencySpikeProbability > 0)
}

// Parse validates and parses the ChaosConfig into a ParsedChaosConfig.
func (c ChaosConfig) Parse() (ParsedChaosConfig, error) {
	var p ParsedChaosConfig
	if !c.Enabled {
		return p, nil
	}
	p.Enabled = true

	if c.ErrorRate < 0 || c.ErrorRate > 1 {
		return p, fmt.Errorf("error_rate must be between 0.0 and 1.0, got %v", c.ErrorRate)
	}
	p.ErrorRate = c.ErrorRate

	if c.LatencySpike.Probability < 0 || c.LatencySpike.Probability > 1 {
		return p, fmt.Errorf("latency_spike.probability must be between 0.0 and 1.0, got %v", c.LatencySpike.Probability)
	}
	p.LatencySpikeProbability = c.LatencySpike.Probability

	if c.LatencySpike.Duration != "" {
		d, err := time.ParseDuration(c.LatencySpike.Duration)
		if err != nil {
			return p, fmt.Errorf("invalid latency_spike.duration %q: %w", c.LatencySpike.Duration, err)
		}
		if d < 0 {
			return p, fmt.Errorf("latency_spike.duration must be non-negative, got %s", c.LatencySpike.Duration)
		}
		p.LatencySpikeDuration = d
	}

	if p.LatencySpikeProbability > 0 && p.LatencySpikeDuration == 0 {
		return p, fmt.Errorf("latency_spike.duration is required when probability is set")
	}

	return p, nil
}

// ResolveChaos merges global and per-service chaos configs.
// Per-service fully overrides global when enabled. The global enabled flag
// acts as a master switch.
func ResolveChaos(global, perService ChaosConfig) ChaosConfig {
	// If neither is enabled, return zero.
	if !global.Enabled && !perService.Enabled {
		return ChaosConfig{}
	}
	// Per-service fully overrides when set.
	if perService.Enabled {
		return perService
	}
	return global
}

// ResolveNetwork merges global and per-service network configs.
// Per-service fields override global when non-empty (field-by-field merge).
func ResolveNetwork(global, perService NetworkConfig) NetworkConfig {
	merged := global
	if perService.Latency != "" {
		merged.Latency = perService.Latency
	}
	if perService.Jitter != "" {
		merged.Jitter = perService.Jitter
	}
	if perService.Bandwidth != "" {
		merged.Bandwidth = perService.Bandwidth
	}
	return merged
}

// parseBandwidth parses a human-readable bandwidth string into bytes per second.
// Accepted suffixes (case-insensitive): gbps, mbps, kbps (bits); gbytes, mbytes, kbytes (bytes).
func parseBandwidth(s string) (int64, error) {
	lower := strings.ToLower(strings.TrimSpace(s))

	type suffix struct {
		name       string
		multiplier int64
	}
	// Order matters: check longer suffixes first to avoid partial matches.
	suffixes := []suffix{
		{"gbytes", 1_000_000_000},
		{"mbytes", 1_000_000},
		{"kbytes", 1_000},
		{"gbps", 1_000_000_000 / 8},
		{"mbps", 1_000_000 / 8},
		{"kbps", 1_000 / 8},
	}

	for _, sf := range suffixes {
		if strings.HasSuffix(lower, sf.name) {
			numStr := strings.TrimSpace(lower[:len(lower)-len(sf.name)])
			val, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid number %q", numStr)
			}
			if val <= 0 {
				return 0, fmt.Errorf("bandwidth must be positive")
			}
			return int64(val * float64(sf.multiplier)), nil
		}
	}

	return 0, fmt.Errorf("unknown bandwidth unit; use kbps, mbps, gbps, kbytes, mbytes, or gbytes")
}

// Config holds the full proxy configuration.
type Config struct {
	Context     string           `yaml:"context" toml:"context"`
	Namespace   string           `yaml:"namespace" toml:"namespace"`
	LogFilePath string           `yaml:"log_file,omitempty" toml:"log_file,omitempty"`
	Listen      string           `yaml:"listen,omitempty" toml:"listen,omitempty"`
	APIKey      string           `yaml:"api_key,omitempty" toml:"api_key,omitempty"`
	KeyID       string           `yaml:"key_id,omitempty" toml:"key_id,omitempty"`
	Host        string           `yaml:"host,omitempty" toml:"host,omitempty"`
	Services    []ServiceConfig   `yaml:"services" toml:"services"`
	Hooks       []HookConfig      `yaml:"hooks,omitempty" toml:"hooks,omitempty"`
	Supervisor  SupervisorConfig  `yaml:"supervisor,omitempty" toml:"supervisor,omitempty"`
	Network     NetworkConfig     `yaml:"network,omitempty" toml:"network,omitempty"`
	Chaos       ChaosConfig       `yaml:"chaos,omitempty" toml:"chaos,omitempty"`
	SOCKS       ProxyServerConfig `yaml:"socks,omitempty" toml:"socks,omitempty"`
	HTTPProxy   ProxyServerConfig `yaml:"http_proxy,omitempty" toml:"http_proxy,omitempty"`

	// Runtime fields (not serialized)
	filePath string
	format   Format
}

// NewInMemory creates a Config from CLI arguments without a file.
// PIDFile, LogFile, and SocketFile default to paths inside the central runtime
// directory (~/.config/kubeport/ or ~/.kubeport/) because filePath is empty and
// CentralDir("") falls back to that directory.
func NewInMemory(kubeContext, namespace string, services []ServiceConfig) *Config {
	return &Config{
		Context:   kubeContext,
		Namespace: namespace,
		Services:  services,
	}
}

// CentralDir returns the central directory used for runtime files (PID, socket, logs).
//
// Resolution order:
//  1. If cfgPath is inside ~/.config/kubeport/ or ~/.kubeport/, use that dir.
//  2. If ~/.config/kubeport/ already contains a root config, use it.
//  3. If ~/.kubeport/ already contains a root config, use it.
//  4. Default to ~/.config/kubeport/ (created if absent).
func CentralDir(cfgPath string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	xdg := filepath.Join(home, ".config", "kubeport")
	dot := filepath.Join(home, ".kubeport")

	if cfgPath != "" {
		abs, _ := filepath.Abs(cfgPath)
		if strings.HasPrefix(abs, xdg+string(os.PathSeparator)) || abs == xdg {
			_ = os.MkdirAll(xdg, 0700)
			return xdg
		}
		if strings.HasPrefix(abs, dot+string(os.PathSeparator)) || abs == dot {
			_ = os.MkdirAll(dot, 0700)
			return dot
		}
	}

	// Prefer whichever standard dir has a root config file.
	for _, name := range []string{"kubeport.yaml", "kubeport.yml", "kubeport.toml"} {
		if _, err := os.Stat(filepath.Join(xdg, name)); err == nil {
			_ = os.MkdirAll(xdg, 0700)
			return xdg
		}
	}
	for _, name := range []string{"kubeport.yaml", "kubeport.yml", "kubeport.toml"} {
		if _, err := os.Stat(filepath.Join(dot, name)); err == nil {
			_ = os.MkdirAll(dot, 0700)
			return dot
		}
	}

	_ = os.MkdirAll(xdg, 0700)
	return xdg
}

// FilePath returns the path the config was loaded from.
func (c *Config) FilePath() string {
	return c.filePath
}

// Format returns the config file format.
func (c *Config) FileFormat() Format {
	return c.format
}

// InstanceID returns a stable, filesystem-safe identifier for this config instance.
// It is derived from the absolute path of the config file so that multiple instances
// with different configs can coexist in the same central directory.
func (c *Config) InstanceID() string {
	path := c.filePath
	if path == "" {
		cwd, _ := os.Getwd()
		path = filepath.Join(cwd, "_inline")
	}
	abs, _ := filepath.Abs(path)
	sum := sha256.Sum256([]byte(abs))
	// Use the parent directory name as a human-readable prefix.
	base := filepath.Base(filepath.Dir(abs))
	if base == "." || base == "" || base == string(os.PathSeparator) {
		base = "kubeport"
	}
	// Sanitize: replace non-alphanumeric characters with hyphens.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '-'
	}, base)
	return safe + "-" + hex.EncodeToString(sum[:4])
}

// PIDFile returns the path for the PID file in the central runtime directory.
func (c *Config) PIDFile() string {
	return filepath.Join(CentralDir(c.filePath), c.InstanceID()+".pid")
}

// LogFile returns the path for the log file. If LogFilePath is set, it is used directly.
// Otherwise the log is placed in a logs/ subdirectory of the central directory;
// that subdirectory is created on first call (mode 0700).
func (c *Config) LogFile() string {
	if c.LogFilePath != "" {
		return c.LogFilePath
	}
	logsDir := filepath.Join(CentralDir(c.filePath), "logs")
	_ = os.MkdirAll(logsDir, 0700)
	return filepath.Join(logsDir, c.InstanceID()+".log")
}

// SocketFile returns the path for the Unix domain socket. An explicit "sock://" prefix
// in the Listen field takes precedence; otherwise the socket is placed in the central
// runtime directory.
func (c *Config) SocketFile() string {
	if path, ok := strings.CutPrefix(c.Listen, "sock://"); ok {
		return path
	}
	return filepath.Join(CentralDir(c.filePath), c.InstanceID()+".sock")
}

// ResolvedKeyID returns the identifier used to represent the configured API key
// in the instance registry and CLI output. If key_id is set in the config that
// value is returned as-is; otherwise a stable HMAC-SHA256 fingerprint is derived
// from the key material so that operators can correlate instances without exposing
// the raw key. Returns an empty string when no API key is configured.
//
// Note: SaveTo automatically generates and persists a UUID key_id when api_key is
// set and key_id is absent, so this fallback path is only hit for in-memory configs
// that have never been saved.
func (c *Config) ResolvedKeyID() string {
	if c.KeyID != "" {
		return c.KeyID
	}
	if c.APIKey == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte("kubeport-key-id"))
	_, _ = mac.Write([]byte(c.APIKey))
	return hex.EncodeToString(mac.Sum(nil)[:8]) // 16 hex chars — stable HMAC fingerprint
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
	data, err := os.ReadFile(path) // #nosec G304 -- config file path from user input
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{}
	format := detectFormat(path)

	switch format {
	case FormatTOML:
		if err := unmarshalTOML(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	default:
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	cfg.filePath = path
	cfg.format = format

	// Migrate legacy underscore event names to colon-based names.
	for i := range cfg.Hooks {
		migrateHookEventNames(&cfg.Hooks[i])
	}

	return cfg, nil
}

// legacyEventNames maps old underscore-based event names to colon-based names.
var legacyEventNames = map[string]string{
	"manager_starting":     "manager:starting",
	"manager_stopped":      "manager:stopped",
	"forward_connected":    "forward:connected",
	"forward_disconnected": "forward:disconnected",
	"forward_failed":       "forward:failed",
	"forward_stopped":      "forward:stopped",
	"health_check_failed":  "health:check_failed",
	"service_added":        "service:added",
	"service_removed":      "service:removed",
}

// migrateHookEventNames rewrites legacy underscore event names to colon-based
// names in both the Events list and the Shell command map keys.
func migrateHookEventNames(h *HookConfig) {
	for i, name := range h.Events {
		if newName, ok := legacyEventNames[name]; ok {
			h.Events[i] = newName
		}
	}
	if len(h.Shell) > 0 {
		migrated := make(map[string]string, len(h.Shell))
		for name, cmd := range h.Shell {
			if newName, ok := legacyEventNames[name]; ok {
				migrated[newName] = cmd
			} else {
				migrated[name] = cmd
			}
		}
		h.Shell = migrated
	}
}

// serviceConfigTOML is a TOML-specific intermediate struct where Ports is decoded
// as a raw value (string or array) since go-toml/v2 does not support custom
// UnmarshalTOML(any) interfaces.
type serviceConfigTOML struct {
	Name            string        `toml:"name"`
	Service         string        `toml:"service,omitempty"`
	Pod             string        `toml:"pod,omitempty"`
	LocalPort       int           `toml:"local_port,omitempty"`
	RemotePort      int           `toml:"remote_port,omitempty"`
	Namespace       string        `toml:"namespace,omitempty"`
	Ports           any           `toml:"ports,omitempty"`
	ExcludePorts    []string      `toml:"exclude_ports,omitempty"`
	LocalPortOffset int           `toml:"local_port_offset,omitempty"`
	Network         NetworkConfig `toml:"network,omitempty"`
	Chaos           ChaosConfig   `toml:"chaos,omitempty"`
}

// configTOML is a TOML-specific intermediate struct for decoding.
type configTOML struct {
	Context     string              `toml:"context"`
	Namespace   string              `toml:"namespace"`
	LogFilePath string              `toml:"log_file,omitempty"`
	Listen      string              `toml:"listen,omitempty"`
	APIKey      string              `toml:"api_key,omitempty"`
	KeyID       string              `toml:"key_id,omitempty"`
	Host        string              `toml:"host,omitempty"`
	Services    []serviceConfigTOML `toml:"services"`
	Hooks       []HookConfig        `toml:"hooks,omitempty"`
	Supervisor  SupervisorConfig    `toml:"supervisor,omitempty"`
	Network     NetworkConfig       `toml:"network,omitempty"`
	Chaos       ChaosConfig         `toml:"chaos,omitempty"`
	SOCKS       ProxyServerConfig   `toml:"socks,omitempty"`
	HTTPProxy   ProxyServerConfig   `toml:"http_proxy,omitempty"`
}

// unmarshalTOML decodes TOML data into a Config, handling the polymorphic ports field.
func unmarshalTOML(data []byte, cfg *Config) error {
	var raw configTOML
	if err := toml.Unmarshal(data, &raw); err != nil {
		return err
	}

	cfg.Context = raw.Context
	cfg.Namespace = raw.Namespace
	cfg.LogFilePath = raw.LogFilePath
	cfg.Listen = raw.Listen
	cfg.APIKey = raw.APIKey
	cfg.KeyID = raw.KeyID
	cfg.Host = raw.Host
	cfg.Hooks = raw.Hooks
	cfg.Supervisor = raw.Supervisor
	cfg.Network = raw.Network
	cfg.Chaos = raw.Chaos
	cfg.SOCKS = raw.SOCKS
	cfg.HTTPProxy = raw.HTTPProxy

	cfg.Services = make([]ServiceConfig, len(raw.Services))
	for i, rs := range raw.Services {
		cfg.Services[i] = ServiceConfig{
			Name:            rs.Name,
			Service:         rs.Service,
			Pod:             rs.Pod,
			LocalPort:       rs.LocalPort,
			RemotePort:      rs.RemotePort,
			Namespace:       rs.Namespace,
			ExcludePorts:    rs.ExcludePorts,
			LocalPortOffset: rs.LocalPortOffset,
			Network:         rs.Network,
			Chaos:           rs.Chaos,
		}
		if rs.Ports != nil {
			ports, err := parsePortsFromRaw(rs.Ports)
			if err != nil {
				return fmt.Errorf("service %q: %w", rs.Name, err)
			}
			cfg.Services[i].Ports = ports
		}
	}
	return nil
}

// marshalTOML converts a Config to TOML bytes, handling the polymorphic ports field.
func marshalTOML(c *Config) ([]byte, error) {
	raw := configTOML{
		Context:     c.Context,
		Namespace:   c.Namespace,
		LogFilePath: c.LogFilePath,
		Listen:      c.Listen,
		APIKey:      c.APIKey,
		KeyID:       c.KeyID,
		Host:        c.Host,
		Hooks:       c.Hooks,
		Supervisor:  c.Supervisor,
		Network:     c.Network,
		Chaos:       c.Chaos,
		SOCKS:       c.SOCKS,
		HTTPProxy:   c.HTTPProxy,
		Services:    make([]serviceConfigTOML, len(c.Services)),
	}
	for i, svc := range c.Services {
		raw.Services[i] = serviceConfigTOML{
			Name:            svc.Name,
			Service:         svc.Service,
			Pod:             svc.Pod,
			LocalPort:       svc.LocalPort,
			RemotePort:      svc.RemotePort,
			Namespace:       svc.Namespace,
			ExcludePorts:    svc.ExcludePorts,
			LocalPortOffset: svc.LocalPortOffset,
			Network:         svc.Network,
			Chaos:           svc.Chaos,
		}
		if svc.Ports.All {
			raw.Services[i].Ports = "all"
		} else if len(svc.Ports.Selectors) > 0 {
			// Check if all selectors are simple names
			allSimple := true
			for _, s := range svc.Ports.Selectors {
				if s.Port != 0 || s.LocalPort != 0 {
					allSimple = false
					break
				}
			}
			if allSimple {
				names := make([]string, len(svc.Ports.Selectors))
				for j, s := range svc.Ports.Selectors {
					names[j] = s.Name
				}
				raw.Services[i].Ports = names
			} else {
				raw.Services[i].Ports = svc.Ports.Selectors
			}
		}
	}
	return toml.Marshal(raw) // #nosec G117 -- APIKey is intentionally saved in config file
}

// Save writes the config back to the file it was loaded from, in the same format.
func (c *Config) Save() error {
	if c.filePath == "" {
		return fmt.Errorf("no file path set; use SaveTo instead")
	}
	return c.SaveTo(c.filePath, c.format)
}

// SaveTo writes the config to the given path in the specified format.
// If api_key is set but key_id is not, a random UUID key_id is generated and
// persisted so operators have a stable, human-friendly identifier for the key.
func (c *Config) SaveTo(path string, format Format) error {
	if c.APIKey != "" && c.KeyID == "" {
		c.KeyID = uuid.NewString()
	}

	var data []byte
	var err error

	switch format {
	case FormatTOML:
		data, err = marshalTOML(c)
	default:
		data, err = yaml.Marshal(c) // #nosec G117 -- APIKey is intentionally saved in config file
	}
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
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
		// Validate individual port selectors
		for j, sel := range svc.Ports.Selectors {
			if sel.Name == "" && sel.Port == 0 {
				return fmt.Errorf("service %q: port selector [%d] must have name or port", svc.Name, j)
			}
			if sel.Port < 0 || sel.Port > 65535 {
				return fmt.Errorf("service %q: port selector [%d] invalid port %d", svc.Name, j, sel.Port)
			}
			if sel.LocalPort < 0 || sel.LocalPort > 65535 {
				return fmt.Errorf("service %q: port selector [%d] invalid local_port %d", svc.Name, j, sel.LocalPort)
			}
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

	// Validate per-service network config
	if svc.Network.IsSet() {
		if _, err := svc.Network.Parse(); err != nil {
			return fmt.Errorf("service %q: network: %w", svc.Name, err)
		}
	}

	// Validate per-service chaos config
	if svc.Chaos.IsSet() {
		if _, err := svc.Chaos.Parse(); err != nil {
			return fmt.Errorf("service %q: chaos: %w", svc.Name, err)
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
		if err := ValidateService(svc); err != nil {
			return fmt.Errorf("service[%d]: %w", i, err)
		}
		// Cross-service duplicate local port check (only for legacy single-port)
		if !svc.IsMultiPort() && svc.LocalPort != 0 {
			if prev, ok := seen[svc.LocalPort]; ok {
				return fmt.Errorf("service[%d] (%s): local_port %d already used by %s", i, svc.Name, svc.LocalPort, prev)
			}
			seen[svc.LocalPort] = svc.Name
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
			cleaned := filepath.Clean(path)
			if strings.Contains(cleaned, "..") {
				return fmt.Errorf("listen: sock:// path must not contain '..'")
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

	// Build service name set for hook filter validation.
	svcNames := make(map[string]struct{}, len(c.Services))
	for _, svc := range c.Services {
		svcNames[svc.Name] = struct{}{}
	}

	// Validate hooks
	for i, h := range c.Hooks {
		if err := validateHook(i, h, svcNames); err != nil {
			return err
		}
	}

	// Validate supervisor config durations
	if err := c.Supervisor.validate(); err != nil {
		return fmt.Errorf("supervisor: %w", err)
	}

	// Validate global network config
	if c.Network.IsSet() {
		if _, err := c.Network.Parse(); err != nil {
			return fmt.Errorf("network: %w", err)
		}
	}

	// Validate global chaos config
	if c.Chaos.IsSet() {
		if _, err := c.Chaos.Parse(); err != nil {
			return fmt.Errorf("chaos: %w", err)
		}
	}

	return nil
}

// validateHook checks structural fields of a hook config.
// Type-specific and event-name validation is deferred to hook.BuildFromConfig
// to avoid duplicating the hook package's validation logic.
func validateHook(idx int, h HookConfig, svcNames map[string]struct{}) error {
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
	for _, name := range h.FilterServices {
		if _, ok := svcNames[name]; !ok {
			return fmt.Errorf("%s: filter_services: unknown service %q", prefix, name)
		}
	}
	return nil
}

func (s SupervisorConfig) validate() error {
	for _, pair := range []struct{ name, val string }{
		{"health_check_interval", s.HealthCheckInterval},
		{"ready_timeout", s.ReadyTimeout},
		{"backoff_initial", s.BackoffInitial},
		{"backoff_max", s.BackoffMax},
		{"max_connection_age", s.MaxConnectionAge},
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
	MaxConnectionAge     time.Duration
}

// ParsedSupervisor returns supervisor config with defaults applied.
// Returns an error if any duration field is set to an invalid value.
// Call Validate() before ParsedSupervisor() to catch errors early.
func (s SupervisorConfig) ParsedSupervisor() (ParsedSupervisorConfig, error) {
	threshold := s.HealthCheckThreshold
	if threshold <= 0 {
		threshold = 3
	}
	p := ParsedSupervisorConfig{
		MaxRestarts:          s.MaxRestarts,
		HealthCheckThreshold: threshold,
	}
	var err error
	if p.HealthCheckInterval, err = parseDuration(s.HealthCheckInterval, 10*time.Second); err != nil {
		return p, fmt.Errorf("health_check_interval: %w", err)
	}
	if p.ReadyTimeout, err = parseDuration(s.ReadyTimeout, 15*time.Second); err != nil {
		return p, fmt.Errorf("ready_timeout: %w", err)
	}
	if p.BackoffInitial, err = parseDuration(s.BackoffInitial, 1*time.Second); err != nil {
		return p, fmt.Errorf("backoff_initial: %w", err)
	}
	if p.BackoffMax, err = parseDuration(s.BackoffMax, 30*time.Second); err != nil {
		return p, fmt.Errorf("backoff_max: %w", err)
	}
	if p.MaxConnectionAge, err = parseDuration(s.MaxConnectionAge, 0); err != nil {
		return p, fmt.Errorf("max_connection_age: %w", err)
	}
	return p, nil
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

func parseDuration(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}

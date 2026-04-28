// Merge helpers for the extends inheritance chain.
// mergeConfigs is the entry point called by loadWithInheritance after the
// parent config is loaded; the other functions handle sub-sections.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mergeConfigs returns a new Config where base provides fallback values and
// override wins for every non-zero field. The merged result retains the
// runtime filePath/format of override (the child config).
func mergeConfigs(base, override *Config) *Config {
	result := *override

	if result.Context == "" {
		result.Context = base.Context
	}
	if result.Namespace == "" {
		result.Namespace = base.Namespace
	}
	if result.Listen == "" {
		result.Listen = base.Listen
	}
	if result.APIKey == "" {
		result.APIKey = base.APIKey
	}
	if result.KeyID == "" {
		result.KeyID = base.KeyID
	}
	if result.Host == "" {
		result.Host = base.Host
	}
	if result.LogFilePath == "" {
		result.LogFilePath = base.LogFilePath
	}

	result.Supervisor = mergeSupervisor(base.Supervisor, override.Supervisor)
	result.Network = ResolveNetwork(base.Network, override.Network)

	// Chaos: if override did not set Enabled (nil = not set), inherit base.
	if override.Chaos.Enabled == nil {
		result.Chaos = base.Chaos
	}

	// Proxy servers: if override did not set Enabled (nil = not set), inherit base.
	if override.SOCKS.Enabled == nil {
		result.SOCKS = base.SOCKS
	}
	if override.HTTPProxy.Enabled == nil {
		result.HTTPProxy = base.HTTPProxy
	}

	result.Services = mergeServices(base.Services, override.Services)
	result.Hooks = mergeHooks(base.Hooks, override.Hooks)

	// The extends chain is fully resolved; clear it from the merged result.
	result.Extends = ""

	return &result
}

// mergeSupervisor returns a SupervisorConfig where base provides fallback
// values and override wins for every non-zero field.
func mergeSupervisor(base, override SupervisorConfig) SupervisorConfig {
	result := base
	if override.MaxRestarts != 0 {
		result.MaxRestarts = override.MaxRestarts
	}
	if override.HealthCheckInterval != "" {
		result.HealthCheckInterval = override.HealthCheckInterval
	}
	if override.HealthCheckThreshold != 0 {
		result.HealthCheckThreshold = override.HealthCheckThreshold
	}
	if override.ReadyTimeout != "" {
		result.ReadyTimeout = override.ReadyTimeout
	}
	if override.BackoffInitial != "" {
		result.BackoffInitial = override.BackoffInitial
	}
	if override.BackoffMax != "" {
		result.BackoffMax = override.BackoffMax
	}
	if override.MaxConnectionAge != "" {
		result.MaxConnectionAge = override.MaxConnectionAge
	}
	return result
}

// mergeServices merges two service lists. For services with the same name,
// override's definition replaces base's. Services present only in base or only
// in override are both included; base-only services appear first.
func mergeServices(base, override []ServiceConfig) []ServiceConfig {
	overrideByName := make(map[string]ServiceConfig, len(override))
	for _, s := range override {
		overrideByName[s.Name] = s
	}

	result := make([]ServiceConfig, 0, len(base)+len(override))
	seen := make(map[string]bool, len(base))

	for _, s := range base {
		if ov, ok := overrideByName[s.Name]; ok {
			result = append(result, ov)
		} else {
			result = append(result, s)
		}
		seen[s.Name] = true
	}

	for _, s := range override {
		if !seen[s.Name] {
			result = append(result, s)
		}
	}

	return result
}

// mergeHooks merges two hook lists by name. Override hooks replace base hooks
// with the same name; all others are included. Base-order hooks appear first,
// followed by override-only hooks. Unnamed hooks are always appended.
func mergeHooks(base, override []HookConfig) []HookConfig {
	overrideByName := make(map[string]HookConfig, len(override))
	for _, h := range override {
		if h.Name != "" {
			overrideByName[h.Name] = h
		}
	}

	result := make([]HookConfig, 0, len(base)+len(override))
	seen := make(map[string]bool, len(base))

	for _, h := range base {
		if h.Name != "" {
			if ov, ok := overrideByName[h.Name]; ok {
				result = append(result, ov)
			} else {
				result = append(result, h)
			}
			seen[h.Name] = true
		} else {
			result = append(result, h)
		}
	}

	for _, h := range override {
		if h.Name == "" || !seen[h.Name] {
			result = append(result, h)
		}
	}

	return result
}

// resolveExtendsPath resolves an extends path relative to the config file that
// contains it. Supports ~ expansion and relative paths.
func resolveExtendsPath(extends, configFilePath string) (string, error) {
	if strings.HasPrefix(extends, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		extends = filepath.Join(home, extends[2:])
	}

	if !filepath.IsAbs(extends) {
		extends = filepath.Join(filepath.Dir(configFilePath), extends)
	}

	return filepath.Clean(extends), nil
}

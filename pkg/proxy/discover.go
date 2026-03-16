package proxy

import (
	"fmt"
	"os"
	"path/filepath"

	pkgconfig "github.com/rbaliyan/kubeport/pkg/config"
)

// discoveredTarget holds the connection details discovered from kubeport config.
type discoveredTarget struct {
	mode    pkgconfig.ListenMode
	address string
	apiKey  string
}

// discoverTarget finds the kubeport daemon by:
// 1. Discovering and loading the kubeport config file (same search order as the CLI)
// 2. Reading the listen address (Unix socket or TCP) and API key from config
// 3. Falling back to checking standard socket locations if no config is found
func discoverTarget() (*discoveredTarget, error) {
	cfgPath, err := pkgconfig.Discover()
	if err == nil {
		cfg, loadErr := pkgconfig.Load(cfgPath)
		if loadErr == nil {
			lc := cfg.ListenAddress()
			if lc.Mode == pkgconfig.ListenTCP {
				return &discoveredTarget{
					mode:    pkgconfig.ListenTCP,
					address: lc.Address,
					apiKey:  cfg.APIKey,
				}, nil
			}
			// Unix socket — verify it exists
			if _, err := os.Stat(lc.Address); err == nil {
				return &discoveredTarget{
					mode:    pkgconfig.ListenUnix,
					address: lc.Address,
				}, nil
			}
		}

		// Try the default socket next to the config file regardless of whether
		// the config loaded successfully (socket may exist from a prior run).
		sock := filepath.Join(filepath.Dir(cfgPath), ".kubeport.sock")
		if _, err := os.Stat(sock); err == nil {
			return &discoveredTarget{mode: pkgconfig.ListenUnix, address: sock}, nil
		}

		// Config file exists but failed to load and no socket found — surface the
		// load error so the caller can diagnose a corrupted or invalid config.
		if loadErr != nil {
			return nil, fmt.Errorf("load kubeport config %s: %w", cfgPath, loadErr)
		}
	}

	// Fallback: check common socket locations directly
	cwd, _ := os.Getwd()
	if cwd != "" {
		sock := filepath.Join(cwd, ".kubeport.sock")
		if _, err := os.Stat(sock); err == nil {
			return &discoveredTarget{mode: pkgconfig.ListenUnix, address: sock}, nil
		}
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		for _, dir := range []string{
			filepath.Join(home, ".config", "kubeport"),
			filepath.Join(home, ".kubeport"),
		} {
			sock := filepath.Join(dir, ".kubeport.sock")
			if _, err := os.Stat(sock); err == nil {
				return &discoveredTarget{mode: pkgconfig.ListenUnix, address: sock}, nil
			}
		}
	}

	return nil, fmt.Errorf("no kubeport daemon found; is it running?")
}

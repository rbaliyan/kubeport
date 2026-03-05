package proxy

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rbaliyan/kubeport/internal/config"
)

// discoveredTarget holds the connection details discovered from kubeport config.
type discoveredTarget struct {
	mode    config.ListenMode
	address string
	apiKey  string
}

// discoverTarget finds the kubeport daemon by:
// 1. Discovering and loading the kubeport config file (same search order as the CLI)
// 2. Reading the listen address (Unix socket or TCP) and API key from config
// 3. Falling back to checking standard socket locations if no config is found
func discoverTarget() (*discoveredTarget, error) {
	cfgPath, err := config.Discover()
	if err == nil {
		cfg, loadErr := config.Load(cfgPath)
		if loadErr == nil {
			lc := cfg.ListenAddress()
			if lc.Mode == config.ListenTCP {
				return &discoveredTarget{
					mode:    config.ListenTCP,
					address: lc.Address,
					apiKey:  cfg.APIKey,
				}, nil
			}
			// Unix socket — verify it exists
			if _, err := os.Stat(lc.Address); err == nil {
				return &discoveredTarget{
					mode:    config.ListenUnix,
					address: lc.Address,
				}, nil
			}
		}

		// Config loaded but no custom listen — try default socket next to config
		sock := filepath.Join(filepath.Dir(cfgPath), ".kubeport.sock")
		if _, err := os.Stat(sock); err == nil {
			return &discoveredTarget{mode: config.ListenUnix, address: sock}, nil
		}
	}

	// Fallback: check common socket locations directly
	cwd, _ := os.Getwd()
	if cwd != "" {
		sock := filepath.Join(cwd, ".kubeport.sock")
		if _, err := os.Stat(sock); err == nil {
			return &discoveredTarget{mode: config.ListenUnix, address: sock}, nil
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
				return &discoveredTarget{mode: config.ListenUnix, address: sock}, nil
			}
		}
	}

	return nil, fmt.Errorf("no kubeport daemon found; is it running?")
}

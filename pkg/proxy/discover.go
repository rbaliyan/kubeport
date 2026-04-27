package proxy

import (
	"fmt"
	"os"
	"path/filepath"

	instanceregistry "github.com/rbaliyan/kubeport/internal/registry"
	pkgconfig "github.com/rbaliyan/kubeport/pkg/config"
)

// discoveredTarget holds the connection details discovered from kubeport config.
type discoveredTarget struct {
	mode     pkgconfig.ListenMode
	address  string
	apiKey   string
	certPath string // path to TLS cert file for TCP connections (empty = system CA)
}

// discoverTarget finds the kubeport daemon by:
// 1. Discovering and loading the kubeport config file (same search order as the CLI)
// 2. Reading the listen address (Unix socket or TCP) and API key from config
// 3. Consulting the central instance registry (~/.config/kubeport/instances.json) for
//    new-style per-instance sockets (<instance-id>.sock)
// 4. Falling back to checking standard socket locations for old-style .kubeport.sock files
func discoverTarget() (*discoveredTarget, error) {
	cfgPath, err := pkgconfig.Discover()
	if err == nil {
		cfg, loadErr := pkgconfig.Load(cfgPath)
		if loadErr == nil {
			lc := cfg.ListenAddress()
			if lc.Mode == pkgconfig.ListenTCP {
				return &discoveredTarget{
					mode:     pkgconfig.ListenTCP,
					address:  lc.Address,
					apiKey:   cfg.APIKey,
					certPath: filepath.Join(filepath.Dir(cfgPath), ".kubeport-tls.crt"),
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

		// Config file exists but failed to load — surface the error so the caller
		// can diagnose a corrupted or invalid config.
		if loadErr != nil {
			return nil, fmt.Errorf("load kubeport config %s: %w", cfgPath, loadErr)
		}
	}

	// Consult the central instance registry for new-style per-instance sockets.
	// The registry lives in ~/.config/kubeport/ (or ~/.kubeport/ if that is the
	// central dir) and tracks running daemons by their InstanceID-derived socket.
	centralDir := pkgconfig.CentralDir("")
	if reg, openErr := instanceregistry.Open(centralDir); openErr == nil {
		if entries, listErr := reg.List(); listErr == nil {
			for _, e := range entries {
				if e.Socket != "" {
					if _, statErr := os.Stat(e.Socket); statErr == nil {
						return &discoveredTarget{mode: pkgconfig.ListenUnix, address: e.Socket}, nil
					}
				}
			}
		}
	}

	// Fallback: check old-style .kubeport.sock locations for backward compatibility.
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

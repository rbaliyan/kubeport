package proxy

import "github.com/rbaliyan/kubeport/pkg/config"

// RemoteForward is a transport-neutral description of a single port-forward
// owned by another running kubeport instance. The CLI (and any other caller)
// translates whatever it learns over gRPC into these values so the conflict
// matching logic can live here in the daemon-owned package rather than in the
// CLI.
type RemoteForward struct {
	Instance   string // socket or TCP endpoint of the owning daemon
	PID        int
	ConfigFile string
	ServiceName string // service name on the owning instance ("" if unknown)
	ActualPort  int    // actual local port on the owning instance (0 if unknown)
	LocalPort   int    // configured local port on the owning instance (0 if dynamic)
}

// DetectExternalConflicts matches the services in cfg against the forwards
// owned by other live instances and returns an ExternalForward for every
// service that is already managed elsewhere — matched by service name first,
// then by static local port. Each local service is reported at most once.
//
// This is the pure decision logic shared by the CLI start/reload paths and the
// daemon's own Reload. Discovering the remote forwards (registry lookup, gRPC
// dialing) is left to the caller, which supplies the results as []RemoteForward.
func DetectExternalConflicts(cfg *config.Config, remote []RemoteForward) []ExternalForward {
	if cfg == nil || len(cfg.Services) == 0 || len(remote) == 0 {
		return nil
	}

	type owner struct {
		instance   string
		pid        int
		configFile string
		actualPort int
	}
	byName := make(map[string]owner)
	byPort := make(map[int]owner)

	for _, fw := range remote {
		o := owner{
			instance:   fw.Instance,
			pid:        fw.PID,
			configFile: fw.ConfigFile,
			actualPort: fw.ActualPort,
		}
		if fw.ServiceName != "" {
			byName[fw.ServiceName] = o
		}
		port := fw.ActualPort
		if port == 0 {
			port = fw.LocalPort
		}
		if port > 0 {
			byPort[port] = o
		}
	}

	seen := make(map[string]bool)
	var externals []ExternalForward
	for _, svc := range cfg.Services {
		if seen[svc.Name] {
			continue
		}
		if o, ok := byName[svc.Name]; ok {
			seen[svc.Name] = true
			externals = append(externals, ExternalForward{
				Service: svc, Instance: o.instance, PID: o.pid,
				ConfigFile: o.configFile, ActualPort: o.actualPort,
			})
			continue
		}
		if svc.LocalPort != 0 {
			if o, ok := byPort[svc.LocalPort]; ok {
				seen[svc.Name] = true
				externals = append(externals, ExternalForward{
					Service: svc, Instance: o.instance, PID: o.pid,
					ConfigFile: o.configFile, ActualPort: o.actualPort,
				})
			}
		}
	}
	return externals
}

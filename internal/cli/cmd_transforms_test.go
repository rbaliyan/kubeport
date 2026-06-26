package cli

import (
	"testing"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/registry"
	"github.com/rbaliyan/kubeport/pkg/config"
)

func TestServiceConfigToProto(t *testing.T) {
	tests := []struct {
		name      string
		svc       config.ServiceConfig
		wantPorts bool
		wantAll   bool
		wantNames []string
	}{
		{
			name: "single port service",
			svc: config.ServiceConfig{
				Name:       "web",
				Service:    "nginx",
				LocalPort:  8080,
				RemotePort: 80,
				Namespace:  "default",
			},
		},
		{
			name: "all ports",
			svc: config.ServiceConfig{
				Name:            "api",
				Service:         "api-svc",
				Ports:           config.PortsConfig{All: true},
				LocalPortOffset: 10000,
			},
			wantPorts: true,
			wantAll:   true,
		},
		{
			name: "named port selectors",
			svc: config.ServiceConfig{
				Name:    "multi",
				Service: "multi-svc",
				Ports: config.PortsConfig{
					Selectors: []config.PortSelector{{Name: "http"}, {Name: "grpc"}},
				},
			},
			wantPorts: true,
			wantNames: []string{"http", "grpc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := serviceConfigToProto(tt.svc)
			if req.Service.Name != tt.svc.Name {
				t.Errorf("Name = %q, want %q", req.Service.Name, tt.svc.Name)
			}
			if req.Service.Service != tt.svc.Service {
				t.Errorf("Service = %q, want %q", req.Service.Service, tt.svc.Service)
			}
			if int(req.Service.LocalPort) != tt.svc.LocalPort {
				t.Errorf("LocalPort = %d, want %d", req.Service.LocalPort, tt.svc.LocalPort)
			}
			if tt.wantPorts {
				if req.Ports == nil {
					t.Fatal("expected Ports to be set")
				}
				if req.Ports.All != tt.wantAll {
					t.Errorf("Ports.All = %v, want %v", req.Ports.All, tt.wantAll)
				}
				if tt.wantNames != nil {
					if len(req.Ports.PortNames) != len(tt.wantNames) {
						t.Fatalf("PortNames = %v, want %v", req.Ports.PortNames, tt.wantNames)
					}
					for i, n := range tt.wantNames {
						if req.Ports.PortNames[i] != n {
							t.Errorf("PortNames[%d] = %q, want %q", i, req.Ports.PortNames[i], n)
						}
					}
				}
			} else if req.Ports != nil {
				t.Errorf("expected nil Ports, got %+v", req.Ports)
			}
		})
	}
}

func TestForwardFromProto(t *testing.T) {
	fw := &kubeportv1.ForwardStatusProto{
		Service: &kubeportv1.ServiceInfo{
			Name:       "web",
			Service:    "nginx",
			RemotePort: 80,
			Namespace:  "default",
		},
		State:            kubeportv1.ForwardState_FORWARD_STATE_RUNNING,
		ActualPort:       8080,
		Restarts:         3,
		BytesIn:          100,
		BytesOut:         200,
		Lazy:             true,
		ConnectionMode:   "mux",
		ExternalInstance: "primary.sock",
		ExternalPid:      42,
	}

	out := forwardFromProto(fw)
	if out.Name != "web" {
		t.Errorf("Name = %q, want web", out.Name)
	}
	if out.State != "running" {
		t.Errorf("State = %q, want running", out.State)
	}
	if out.LocalPort != 8080 {
		t.Errorf("LocalPort = %d, want 8080", out.LocalPort)
	}
	if out.RemotePort != 80 {
		t.Errorf("RemotePort = %d, want 80", out.RemotePort)
	}
	if out.Target != "nginx" {
		t.Errorf("Target = %q, want nginx", out.Target)
	}
	if out.Restarts != 3 {
		t.Errorf("Restarts = %d, want 3", out.Restarts)
	}
	if !out.Lazy {
		t.Error("Lazy = false, want true")
	}
	if out.ExternalInstance != "primary.sock" {
		t.Errorf("ExternalInstance = %q, want primary.sock", out.ExternalInstance)
	}
	if out.ExternalPID != 42 {
		t.Errorf("ExternalPID = %d, want 42", out.ExternalPID)
	}
}

func TestForwardFromProto_PodTargetFallback(t *testing.T) {
	fw := &kubeportv1.ForwardStatusProto{
		Service: &kubeportv1.ServiceInfo{
			Name: "dbg",
			Pod:  "debug-pod",
		},
		State: kubeportv1.ForwardState_FORWARD_STATE_STARTING,
	}
	out := forwardFromProto(fw)
	if out.Target != "debug-pod" {
		t.Errorf("Target = %q, want debug-pod (pod fallback)", out.Target)
	}
	if out.State != "starting" {
		t.Errorf("State = %q, want starting", out.State)
	}
}

func TestProxyStatusFromConfig(t *testing.T) {
	enabled := true
	tests := []struct {
		name          string
		cfg           *config.Config
		wantSocks     bool
		wantSocksAddr string
		wantHTTP      bool
		wantHTTPAddr  string
	}{
		{
			name: "nil config",
			cfg:  nil,
		},
		{
			name: "neither configured",
			cfg:  &config.Config{},
		},
		{
			name: "socks enabled with default addr",
			cfg: &config.Config{
				SOCKS: config.ProxyServerConfig{Enabled: &enabled},
			},
			wantSocks:     true,
			wantSocksAddr: "127.0.0.1:1080",
		},
		{
			name: "http with explicit listen but not enabled",
			cfg: &config.Config{
				HTTPProxy: config.ProxyServerConfig{Listen: "127.0.0.1:9999"},
			},
			wantHTTP:     true,
			wantHTTPAddr: "127.0.0.1:9999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := &app{cfg: tt.cfg}
			socks, httpProxy := a.proxyStatusFromConfig()
			if tt.wantSocks {
				if socks == nil {
					t.Fatal("expected socks status")
				}
				if socks.Listen != tt.wantSocksAddr {
					t.Errorf("socks.Listen = %q, want %q", socks.Listen, tt.wantSocksAddr)
				}
			} else if socks != nil {
				t.Errorf("expected nil socks, got %+v", socks)
			}
			if tt.wantHTTP {
				if httpProxy == nil {
					t.Fatal("expected http status")
				}
				if httpProxy.Listen != tt.wantHTTPAddr {
					t.Errorf("http.Listen = %q, want %q", httpProxy.Listen, tt.wantHTTPAddr)
				}
			} else if httpProxy != nil {
				t.Errorf("expected nil http, got %+v", httpProxy)
			}
		})
	}
}

func TestParseChaosTargets(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantServices []string
		wantAll      bool
	}{
		{"empty", nil, nil, false},
		{"services only", []string{"db", "redis"}, []string{"db", "redis"}, false},
		{"all flag", []string{"--all"}, nil, true},
		{"services and all", []string{"db", "--all", "redis"}, []string{"db", "redis"}, true},
		// Dash-prefixed args are dropped; bare args (including flag values) are
		// treated as service names — callers strip flag values before calling.
		{"dash-prefixed flags dropped", []string{"db", "--error-rate"}, []string{"db"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			services, all := parseChaosTargets(tt.args)
			if all != tt.wantAll {
				t.Errorf("all = %v, want %v", all, tt.wantAll)
			}
			if len(services) != len(tt.wantServices) {
				t.Fatalf("services = %v, want %v", services, tt.wantServices)
			}
			for i, s := range tt.wantServices {
				if services[i] != s {
					t.Errorf("services[%d] = %q, want %q", i, services[i], s)
				}
			}
		})
	}
}

func TestParseProxyFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want proxyFlags
	}{
		{
			name: "long forms",
			args: []string{"--listen", "127.0.0.1:2080", "--cluster-domain", "k8s.local", "--username", "u", "--password", "p"},
			want: proxyFlags{listenAddr: "127.0.0.1:2080", clusterDomain: "k8s.local", username: "u", password: "p"},
		},
		{
			name: "short forms",
			args: []string{"-l", "0.0.0.0:1080", "-u", "admin", "-p", "secret"},
			want: proxyFlags{listenAddr: "0.0.0.0:1080", username: "admin", password: "secret"},
		},
		{
			name: "missing value at end is ignored",
			args: []string{"--listen"},
			want: proxyFlags{},
		},
		{
			name: "empty",
			args: nil,
			want: proxyFlags{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseProxyFlags(tt.args)
			if got != tt.want {
				t.Errorf("parseProxyFlags = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestApplyConfigDefaults(t *testing.T) {
	t.Run("fills empty fields from config", func(t *testing.T) {
		t.Parallel()
		f := proxyFlags{}
		f.applyConfigDefaults(config.ProxyServerConfig{
			Listen:   "127.0.0.1:1080",
			Username: "cfguser",
			Password: "cfgpass",
		})
		if f.listenAddr != "127.0.0.1:1080" || f.username != "cfguser" || f.password != "cfgpass" {
			t.Errorf("defaults not applied: %+v", f)
		}
	})

	t.Run("does not override existing flag values", func(t *testing.T) {
		t.Parallel()
		f := proxyFlags{listenAddr: "flagaddr", username: "flaguser", password: "flagpass"}
		f.applyConfigDefaults(config.ProxyServerConfig{
			Listen:   "cfgaddr",
			Username: "cfguser",
			Password: "cfgpass",
		})
		if f.listenAddr != "flagaddr" || f.username != "flaguser" || f.password != "flagpass" {
			t.Errorf("config overrode flag values: %+v", f)
		}
	})
}

func TestResolveConfigPath(t *testing.T) {
	t.Run("explicit config flag", func(t *testing.T) {
		t.Parallel()
		a := &app{configFile: "/etc/kubeport.yaml"}
		if got := a.resolveConfigPath(); got != "/etc/kubeport.yaml" {
			t.Errorf("resolveConfigPath = %q, want /etc/kubeport.yaml", got)
		}
	})
}

func TestValueOrDefault(t *testing.T) {
	tests := []struct {
		name string
		s    string
		def  string
		want string
	}{
		{"non-empty returns value", "value", "default", "value"},
		{"empty returns default", "", "default", "default"},
		{"both empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := valueOrDefault(tt.s, tt.def); got != tt.want {
				t.Errorf("valueOrDefault(%q, %q) = %q, want %q", tt.s, tt.def, got, tt.want)
			}
		})
	}
}

func TestBuildConfigFromCLI(t *testing.T) {
	t.Run("no services errors", func(t *testing.T) {
		t.Parallel()
		a := &app{}
		if err := a.buildConfigFromCLI(nil); err == nil {
			t.Fatal("expected error for empty services")
		}
	})

	t.Run("builds config with defaults", func(t *testing.T) {
		t.Parallel()
		a := &app{cliContext: "my-ctx"}
		svcs := []config.ServiceConfig{{Name: "web", Service: "nginx", RemotePort: 80, LocalPort: 8080}}
		if err := a.buildConfigFromCLI(svcs); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if a.cfg == nil {
			t.Fatal("cfg not set")
		}
		if a.cfg.Namespace != "default" {
			t.Errorf("Namespace = %q, want default", a.cfg.Namespace)
		}
		if a.cfg.Context != "my-ctx" {
			t.Errorf("Context = %q, want my-ctx", a.cfg.Context)
		}
	})
}

func TestParseCLIServices(t *testing.T) {
	t.Run("empty returns nil", func(t *testing.T) {
		t.Parallel()
		a := &app{}
		svcs, err := a.parseCLIServices()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if svcs != nil {
			t.Errorf("expected nil, got %v", svcs)
		}
	})

	t.Run("parses valid svc flags", func(t *testing.T) {
		t.Parallel()
		a := &app{cliServices: []string{"web:svc/nginx:80:8080"}}
		svcs, err := a.parseCLIServices()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(svcs) != 1 || svcs[0].Name != "web" || svcs[0].Service != "nginx" {
			t.Errorf("unexpected services: %+v", svcs)
		}
	})

	t.Run("invalid svc flag errors", func(t *testing.T) {
		t.Parallel()
		a := &app{cliServices: []string{"garbage"}}
		if _, err := a.parseCLIServices(); err == nil {
			t.Fatal("expected error for invalid svc flag")
		}
	})
}

func TestSocketPath(t *testing.T) {
	t.Run("no config returns default", func(t *testing.T) {
		t.Parallel()
		a := &app{}
		if got := a.socketPath(); got != ".kubeport.sock" {
			t.Errorf("socketPath = %q, want .kubeport.sock", got)
		}
	})
}

func TestResolveCertFile(t *testing.T) {
	t.Run("no config returns empty", func(t *testing.T) {
		t.Parallel()
		a := &app{}
		if got := a.resolveCertFile(); got != "" {
			t.Errorf("resolveCertFile = %q, want empty", got)
		}
	})

	t.Run("derives from config file dir", func(t *testing.T) {
		t.Parallel()
		cfg := config.NewInMemory("ctx", "ns", nil)
		dir := t.TempDir()
		if err := cfg.SaveTo(dir+"/kubeport.yaml", config.FormatYAML); err != nil {
			t.Fatalf("save: %v", err)
		}
		a := &app{cfg: cfg}
		want := dir + "/.kubeport-tls.crt"
		if got := a.resolveCertFile(); got != want {
			t.Errorf("resolveCertFile = %q, want %q", got, want)
		}
	})
}

func TestBuildConflictReport(t *testing.T) {
	live := []liveInstance{
		{
			entry: registry.Entry{PID: 1234, ConfigFile: "/owner.yaml"},
			forwards: []*kubeportv1.ForwardStatusProto{
				{
					Service:    &kubeportv1.ServiceInfo{Name: "owner-web", LocalPort: 8080},
					ActualPort: 8080,
				},
			},
		},
	}

	tests := []struct {
		name          string
		newServices   []config.ServiceConfig
		wantConflicts int
		wantClean     int
	}{
		{
			name:          "conflicting static port",
			newServices:   []config.ServiceConfig{{Name: "web", LocalPort: 8080}},
			wantConflicts: 1,
			wantClean:     0,
		},
		{
			name:          "non-conflicting static port",
			newServices:   []config.ServiceConfig{{Name: "api", LocalPort: 9090}},
			wantConflicts: 0,
			wantClean:     1,
		},
		{
			name:          "dynamic port never conflicts",
			newServices:   []config.ServiceConfig{{Name: "dyn", LocalPort: 0}},
			wantConflicts: 0,
			wantClean:     1,
		},
		{
			name: "mixed",
			newServices: []config.ServiceConfig{
				{Name: "web", LocalPort: 8080},
				{Name: "api", LocalPort: 9090},
			},
			wantConflicts: 1,
			wantClean:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conflicts, clean := buildConflictReport(tt.newServices, live)
			if len(conflicts) != tt.wantConflicts {
				t.Errorf("conflicts = %d, want %d (%+v)", len(conflicts), tt.wantConflicts, conflicts)
			}
			if len(clean) != tt.wantClean {
				t.Errorf("clean = %d, want %d (%+v)", len(clean), tt.wantClean, clean)
			}
			if tt.wantConflicts > 0 && len(conflicts) > 0 {
				if conflicts[0].OwnedBy != "owner-web" {
					t.Errorf("OwnedBy = %q, want owner-web", conflicts[0].OwnedBy)
				}
				if conflicts[0].OwnerPID != 1234 {
					t.Errorf("OwnerPID = %d, want 1234", conflicts[0].OwnerPID)
				}
			}
		})
	}
}

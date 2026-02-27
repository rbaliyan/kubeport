package cli

import (
	"os"
	"testing"
)

func TestParseArgs_HostFlag(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantHost string
	}{
		{"--host with space", []string{"--host", "localhost:9090", "status"}, "localhost:9090"},
		{"--host= form", []string{"--host=10.0.0.1:9090", "status"}, "10.0.0.1:9090"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{}
			cmd, _ := a.parseArgs(tt.args)
			if a.remoteHost != tt.wantHost {
				t.Errorf("remoteHost = %q, want %q", a.remoteHost, tt.wantHost)
			}
			if cmd != "status" {
				t.Errorf("command = %q, want 'status'", cmd)
			}
		})
	}
}

func TestParseArgs_APIKeyFlag(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantKey string
	}{
		{"--api-key with space", []string{"--api-key", "my-secret", "status"}, "my-secret"},
		{"--api-key= form", []string{"--api-key=my-secret", "status"}, "my-secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &app{}
			a.parseArgs(tt.args)
			if a.apiKey != tt.wantKey {
				t.Errorf("apiKey = %q, want %q", a.apiKey, tt.wantKey)
			}
		})
	}
}

func TestResolveAPIKey_Priority(t *testing.T) {
	// Flag takes priority
	a := &app{apiKey: "from-flag"}
	t.Setenv("KUBEPORT_API_KEY", "from-env")
	if got := a.resolveAPIKey(); got != "from-flag" {
		t.Fatalf("expected from-flag, got %s", got)
	}

	// Env next
	a2 := &app{}
	t.Setenv("KUBEPORT_API_KEY", "from-env")
	if got := a2.resolveAPIKey(); got != "from-env" {
		t.Fatalf("expected from-env, got %s", got)
	}

	// Config last
	os.Unsetenv("KUBEPORT_API_KEY")
	a3 := &app{}
	// resolveAPIKey without config returns empty
	if got := a3.resolveAPIKey(); got != "" {
		t.Fatalf("expected empty, got %s", got)
	}
}

func TestResolveHost_Priority(t *testing.T) {
	a := &app{remoteHost: "from-flag"}
	if got := a.resolveHost(); got != "from-flag" {
		t.Fatalf("expected from-flag, got %s", got)
	}

	a2 := &app{}
	if got := a2.resolveHost(); got != "" {
		t.Fatalf("expected empty, got %s", got)
	}
}

func TestParseSvcFlag(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantName   string
		wantSvc    string
		wantPod    string
		wantRemote int
		wantLocal  int
		wantNS     string
		wantErr    bool
	}{
		{
			name:       "service basic",
			input:      "Atlas:svc/atlas-app-dev:80:8061",
			wantName:   "Atlas",
			wantSvc:    "atlas-app-dev",
			wantRemote: 80,
			wantLocal:  8061,
		},
		{
			name:       "pod basic",
			input:      "Redis:pod/redis-node-0:6379:6380",
			wantName:   "Redis",
			wantPod:    "redis-node-0",
			wantRemote: 6379,
			wantLocal:  6380,
		},
		{
			name:       "with namespace",
			input:      "Vault:svc/vault:8200:8200:vault",
			wantName:   "Vault",
			wantSvc:    "vault",
			wantRemote: 8200,
			wantLocal:  8200,
			wantNS:     "vault",
		},
		{
			name:       "service keyword",
			input:      "API:service/my-api:3000:3000",
			wantName:   "API",
			wantSvc:    "my-api",
			wantRemote: 3000,
			wantLocal:  3000,
		},
		{
			name:       "dynamic local port",
			input:      "Debug:svc/debug:9090:0",
			wantName:   "Debug",
			wantSvc:    "debug",
			wantRemote: 9090,
			wantLocal:  0,
		},
		{
			name:    "too few parts",
			input:   "Atlas:svc/atlas",
			wantErr: true,
		},
		{
			name:    "bad remote port",
			input:   "Atlas:svc/atlas:abc:8061",
			wantErr: true,
		},
		{
			name:    "bad local port",
			input:   "Atlas:svc/atlas:80:xyz",
			wantErr: true,
		},
		{
			name:    "missing target name",
			input:   "Atlas:svc/:80:8061",
			wantErr: true,
		},
		{
			name:    "invalid type",
			input:   "Atlas:deploy/my-deploy:80:8061",
			wantErr: true,
		},
		{
			name:    "no slash separator",
			input:   "Atlas:atlas-app:80:8061",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, err := parseSvcFlag(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if svc.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", svc.Name, tt.wantName)
			}
			if svc.Service != tt.wantSvc {
				t.Errorf("Service = %q, want %q", svc.Service, tt.wantSvc)
			}
			if svc.Pod != tt.wantPod {
				t.Errorf("Pod = %q, want %q", svc.Pod, tt.wantPod)
			}
			if svc.RemotePort != tt.wantRemote {
				t.Errorf("RemotePort = %d, want %d", svc.RemotePort, tt.wantRemote)
			}
			if svc.LocalPort != tt.wantLocal {
				t.Errorf("LocalPort = %d, want %d", svc.LocalPort, tt.wantLocal)
			}
			if svc.Namespace != tt.wantNS {
				t.Errorf("Namespace = %q, want %q", svc.Namespace, tt.wantNS)
			}
		})
	}
}

func TestParseSvcFlag_MultiPort(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantName   string
		wantSvc    string
		wantAll    bool
		wantPorts  []string // expected selector names
		wantOffset int
		wantNS     string
		wantErr    bool
	}{
		{
			name:     "all ports",
			input:    "api:svc/my-api:all",
			wantName: "api",
			wantSvc:  "my-api",
			wantAll:  true,
		},
		{
			name:      "named ports",
			input:     "api:svc/my-api:http,grpc",
			wantName:  "api",
			wantSvc:   "my-api",
			wantPorts: []string{"http", "grpc"},
		},
		{
			name:       "all with offset",
			input:      "api:svc/my-api:all:+10000",
			wantName:   "api",
			wantSvc:    "my-api",
			wantAll:    true,
			wantOffset: 10000,
		},
		{
			name:       "all with offset and namespace",
			input:      "api:svc/my-api:all:+10000:prod",
			wantName:   "api",
			wantSvc:    "my-api",
			wantAll:    true,
			wantOffset: 10000,
			wantNS:     "prod",
		},
		{
			name:     "all with namespace no offset",
			input:    "api:svc/my-api:all:prod",
			wantName: "api",
			wantSvc:  "my-api",
			wantAll:  true,
			wantNS:   "prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, err := parseSvcFlag(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if svc.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", svc.Name, tt.wantName)
			}
			if svc.Service != tt.wantSvc {
				t.Errorf("Service = %q, want %q", svc.Service, tt.wantSvc)
			}
			if svc.Ports.All != tt.wantAll {
				t.Errorf("Ports.All = %v, want %v", svc.Ports.All, tt.wantAll)
			}
			if tt.wantPorts != nil {
				if len(svc.Ports.Selectors) != len(tt.wantPorts) {
					t.Fatalf("Selectors count = %d, want %d", len(svc.Ports.Selectors), len(tt.wantPorts))
				}
				for i, want := range tt.wantPorts {
					if svc.Ports.Selectors[i].Name != want {
						t.Errorf("Selector[%d].Name = %q, want %q", i, svc.Ports.Selectors[i].Name, want)
					}
				}
			}
			if svc.LocalPortOffset != tt.wantOffset {
				t.Errorf("LocalPortOffset = %d, want %d", svc.LocalPortOffset, tt.wantOffset)
			}
			if svc.Namespace != tt.wantNS {
				t.Errorf("Namespace = %q, want %q", svc.Namespace, tt.wantNS)
			}
		})
	}
}

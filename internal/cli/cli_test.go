package cli

import (
	"testing"
)

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
			input:   "Atlas:svc/atlas:80",
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

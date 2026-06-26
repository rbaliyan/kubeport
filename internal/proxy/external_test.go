package proxy

import (
	"testing"

	"github.com/rbaliyan/kubeport/pkg/config"
)

func TestDetectExternalConflicts(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *config.Config
		remote []RemoteForward
		want   []ExternalForward
	}{
		{
			name:   "nil config returns nil",
			cfg:    nil,
			remote: []RemoteForward{{ServiceName: "web", Instance: "sock", PID: 1}},
			want:   nil,
		},
		{
			name:   "empty remote returns nil",
			cfg:    &config.Config{Services: []config.ServiceConfig{{Name: "web", LocalPort: 8080}}},
			remote: nil,
			want:   nil,
		},
		{
			name: "match by service name",
			cfg:  &config.Config{Services: []config.ServiceConfig{{Name: "web", LocalPort: 8080}}},
			remote: []RemoteForward{
				{ServiceName: "web", Instance: "primary.sock", PID: 42, ConfigFile: "/a.yaml", ActualPort: 9090},
			},
			want: []ExternalForward{
				{
					Service:    config.ServiceConfig{Name: "web", LocalPort: 8080},
					Instance:   "primary.sock",
					PID:        42,
					ConfigFile: "/a.yaml",
					ActualPort: 9090,
				},
			},
		},
		{
			name: "no name match falls back to local port via actual port",
			cfg:  &config.Config{Services: []config.ServiceConfig{{Name: "web", LocalPort: 8080}}},
			remote: []RemoteForward{
				{ServiceName: "other", Instance: "primary.sock", PID: 7, ConfigFile: "/b.yaml", ActualPort: 8080},
			},
			want: []ExternalForward{
				{
					Service:    config.ServiceConfig{Name: "web", LocalPort: 8080},
					Instance:   "primary.sock",
					PID:        7,
					ConfigFile: "/b.yaml",
					ActualPort: 8080,
				},
			},
		},
		{
			name: "actual port zero falls back to local port in byPort map",
			cfg:  &config.Config{Services: []config.ServiceConfig{{Name: "web", LocalPort: 8080}}},
			remote: []RemoteForward{
				{ServiceName: "other", Instance: "primary.sock", PID: 9, ConfigFile: "/c.yaml", ActualPort: 0, LocalPort: 8080},
			},
			want: []ExternalForward{
				{
					Service:    config.ServiceConfig{Name: "web", LocalPort: 8080},
					Instance:   "primary.sock",
					PID:        9,
					ConfigFile: "/c.yaml",
					ActualPort: 0,
				},
			},
		},
		{
			name: "duplicate service name reported once",
			cfg: &config.Config{Services: []config.ServiceConfig{
				{Name: "web", LocalPort: 8080},
				{Name: "web", LocalPort: 8080},
			}},
			remote: []RemoteForward{
				{ServiceName: "web", Instance: "primary.sock", PID: 11, ConfigFile: "/d.yaml", ActualPort: 8080},
			},
			want: []ExternalForward{
				{
					Service:    config.ServiceConfig{Name: "web", LocalPort: 8080},
					Instance:   "primary.sock",
					PID:        11,
					ConfigFile: "/d.yaml",
					ActualPort: 8080,
				},
			},
		},
		{
			name: "no match returns nil",
			cfg:  &config.Config{Services: []config.ServiceConfig{{Name: "web", LocalPort: 8080}}},
			remote: []RemoteForward{
				{ServiceName: "other", Instance: "primary.sock", PID: 1, ActualPort: 9999},
			},
			want: nil,
		},
		{
			name: "dynamic local port without name match is not reported",
			cfg:  &config.Config{Services: []config.ServiceConfig{{Name: "web", LocalPort: 0}}},
			remote: []RemoteForward{
				{ServiceName: "other", Instance: "primary.sock", PID: 1, ActualPort: 8080},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DetectExternalConflicts(tt.cfg, tt.remote)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d externals, want %d (%+v)", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i].Service.Name != tt.want[i].Service.Name {
					t.Errorf("[%d] Service.Name = %q, want %q", i, got[i].Service.Name, tt.want[i].Service.Name)
				}
				if got[i].Instance != tt.want[i].Instance {
					t.Errorf("[%d] Instance = %q, want %q", i, got[i].Instance, tt.want[i].Instance)
				}
				if got[i].PID != tt.want[i].PID {
					t.Errorf("[%d] PID = %d, want %d", i, got[i].PID, tt.want[i].PID)
				}
				if got[i].ConfigFile != tt.want[i].ConfigFile {
					t.Errorf("[%d] ConfigFile = %q, want %q", i, got[i].ConfigFile, tt.want[i].ConfigFile)
				}
				if got[i].ActualPort != tt.want[i].ActualPort {
					t.Errorf("[%d] ActualPort = %d, want %d", i, got[i].ActualPort, tt.want[i].ActualPort)
				}
			}
		})
	}
}

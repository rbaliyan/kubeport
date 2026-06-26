package daemon

import (
	"context"
	"testing"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"github.com/rbaliyan/kubeport/pkg/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestServer_Mappings(t *testing.T) {
	mgr := &mockSupervisor{
		mappings: []proxy.AddressMapping{
			{InternalAddr: "web.demo.svc.cluster.local:80", LocalAddr: "localhost:8080", ServiceName: "web"},
			{InternalAddr: "api.demo.svc.cluster.local:3000", LocalAddr: "localhost:9090", ServiceName: "api"},
		},
	}
	cfg := &config.Config{Context: "map-ctx", Namespace: "map-ns"}
	srv := &Server{mgr: mgr, cfg: cfg}

	resp, err := srv.Mappings(context.Background(), &kubeportv1.MappingsRequest{ClusterDomain: "cluster.local"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Context != "map-ctx" {
		t.Errorf("Context = %q, want map-ctx", resp.Context)
	}
	if resp.Namespace != "map-ns" {
		t.Errorf("Namespace = %q, want map-ns", resp.Namespace)
	}
	if len(resp.Mappings) != 2 {
		t.Fatalf("Mappings count = %d, want 2", len(resp.Mappings))
	}
	if resp.Addrs["web.demo.svc.cluster.local:80"] != "localhost:8080" {
		t.Errorf("Addrs[web] = %q, want localhost:8080", resp.Addrs["web.demo.svc.cluster.local:80"])
	}
	if resp.Addrs["api.demo.svc.cluster.local:3000"] != "localhost:9090" {
		t.Errorf("Addrs[api] = %q, want localhost:9090", resp.Addrs["api.demo.svc.cluster.local:3000"])
	}
}

func TestServer_UpdateChaos(t *testing.T) {
	t.Run("inline params resolve and pass through", func(t *testing.T) {
		mgr := &mockSupervisor{chaosUpdated: []string{"db"}}
		srv := &Server{mgr: mgr, cfg: &config.Config{}}

		resp, err := srv.UpdateChaos(context.Background(), &kubeportv1.UpdateChaosRequest{
			Services:         []string{"db"},
			Enabled:          true,
			ErrorRate:        0.2,
			SpikeProbability: 0.1,
			SpikeDurationMs:  500,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Updated) != 1 || resp.Updated[0] != "db" {
			t.Errorf("Updated = %v, want [db]", resp.Updated)
		}
		if !mgr.lastChaosCfg.Enabled || mgr.lastChaosCfg.ErrorRate != 0.2 {
			t.Errorf("forwarded cfg = %+v, want enabled with error rate 0.2", mgr.lastChaosCfg)
		}
		if mgr.lastChaosCfg.LatencySpikeDuration != 500*time.Millisecond {
			t.Errorf("spike duration = %v, want 500ms", mgr.lastChaosCfg.LatencySpikeDuration)
		}
	})

	t.Run("reset path calls ResetChaos", func(t *testing.T) {
		mgr := &mockSupervisor{}
		srv := &Server{mgr: mgr, cfg: &config.Config{}}

		resp, err := srv.UpdateChaos(context.Background(), &kubeportv1.UpdateChaosRequest{
			Services: []string{"db"},
			Reset_:   true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// ResetChaos in the mock returns (nil, nil); UpdateChaos must not have been called.
		if mgr.lastChaosSvcs != nil {
			t.Errorf("UpdateChaos should not run on reset, got svcs %v", mgr.lastChaosSvcs)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}
	})

	t.Run("preset resolves to slow-network", func(t *testing.T) {
		mgr := &mockSupervisor{}
		srv := &Server{mgr: mgr, cfg: &config.Config{}}

		_, err := srv.UpdateChaos(context.Background(), &kubeportv1.UpdateChaosRequest{
			Services: []string{"db"},
			Preset:   kubeportv1.ChaosPreset_CHAOS_PRESET_SLOW_NETWORK,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := proxy.ChaosPresets["slow-network"]
		if mgr.lastChaosCfg != want {
			t.Errorf("preset cfg = %+v, want %+v", mgr.lastChaosCfg, want)
		}
	})

	t.Run("out of range error rate is rejected", func(t *testing.T) {
		mgr := &mockSupervisor{}
		srv := &Server{mgr: mgr, cfg: &config.Config{}}

		_, err := srv.UpdateChaos(context.Background(), &kubeportv1.UpdateChaosRequest{
			Services:  []string{"db"},
			ErrorRate: 1.5,
		})
		if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", err)
		}
	})
}

func TestChaosParamsFromProto(t *testing.T) {
	tests := []struct {
		name    string
		req     *kubeportv1.UpdateChaosRequest
		wantErr bool
	}{
		{
			name: "valid inline params",
			req: &kubeportv1.UpdateChaosRequest{
				Enabled:          true,
				ErrorRate:        0.5,
				SpikeProbability: 0.2,
				SpikeDurationMs:  100,
			},
		},
		{
			name: "error rate below zero",
			req:  &kubeportv1.UpdateChaosRequest{ErrorRate: -0.1},
			wantErr: true,
		},
		{
			name: "error rate above one",
			req:  &kubeportv1.UpdateChaosRequest{ErrorRate: 1.1},
			wantErr: true,
		},
		{
			name: "spike probability above one",
			req:  &kubeportv1.UpdateChaosRequest{SpikeProbability: 1.5, SpikeDurationMs: 100},
			wantErr: true,
		},
		{
			name: "spike probability without duration",
			req:  &kubeportv1.UpdateChaosRequest{SpikeProbability: 0.5, SpikeDurationMs: 0},
			wantErr: true,
		},
		{
			name: "unstable-cluster preset bypasses validation",
			req:  &kubeportv1.UpdateChaosRequest{Preset: kubeportv1.ChaosPreset_CHAOS_PRESET_UNSTABLE_CLUSTER},
		},
		{
			name: "packet-loss preset bypasses validation",
			req:  &kubeportv1.UpdateChaosRequest{Preset: kubeportv1.ChaosPreset_CHAOS_PRESET_PACKET_LOSS},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := chaosParamsFromProto(tt.req)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPortSpecToConfig(t *testing.T) {
	tests := []struct {
		name        string
		ps          *kubeportv1.PortSpec
		wantAll     bool
		wantNames   []string
		wantExclude []string
		wantOffset  int
	}{
		{
			name: "nil returns zero values",
			ps:   nil,
		},
		{
			name:    "all true",
			ps:      &kubeportv1.PortSpec{All: true, LocalPortOffset: 10000},
			wantAll: true,
			wantOffset: 10000,
		},
		{
			name:      "port names become selectors",
			ps:        &kubeportv1.PortSpec{PortNames: []string{"http", "grpc"}},
			wantNames: []string{"http", "grpc"},
		},
		{
			name:        "exclude ports and offset passthrough",
			ps:          &kubeportv1.PortSpec{ExcludePorts: []string{"metrics"}, LocalPortOffset: 5},
			wantExclude: []string{"metrics"},
			wantOffset:  5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pc, exclude, offset := portSpecToConfig(tt.ps)
			if pc.All != tt.wantAll {
				t.Errorf("All = %v, want %v", pc.All, tt.wantAll)
			}
			if len(pc.Selectors) != len(tt.wantNames) {
				t.Fatalf("selectors = %d, want %d", len(pc.Selectors), len(tt.wantNames))
			}
			for i, n := range tt.wantNames {
				if pc.Selectors[i].Name != n {
					t.Errorf("Selectors[%d].Name = %q, want %q", i, pc.Selectors[i].Name, n)
				}
			}
			if len(exclude) != len(tt.wantExclude) {
				t.Fatalf("exclude = %v, want %v", exclude, tt.wantExclude)
			}
			for i, e := range tt.wantExclude {
				if exclude[i] != e {
					t.Errorf("exclude[%d] = %q, want %q", i, exclude[i], e)
				}
			}
			if offset != tt.wantOffset {
				t.Errorf("offset = %d, want %d", offset, tt.wantOffset)
			}
		})
	}
}

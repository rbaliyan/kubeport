package proxy

import "testing"

func TestClientOptions_Setters(t *testing.T) {
	t.Run("WithSocketPath", func(t *testing.T) {
		t.Parallel()
		o := &options{}
		WithSocketPath("/run/kubeport.sock")(o)
		if o.socketPath != "/run/kubeport.sock" {
			t.Errorf("socketPath = %q, want /run/kubeport.sock", o.socketPath)
		}
	})

	t.Run("WithClusterDomain", func(t *testing.T) {
		t.Parallel()
		o := &options{}
		WithClusterDomain("svc.example.internal")(o)
		if o.clusterDomain != "svc.example.internal" {
			t.Errorf("clusterDomain = %q, want svc.example.internal", o.clusterDomain)
		}
	})

	t.Run("WithFuzzyMatch true", func(t *testing.T) {
		t.Parallel()
		o := &options{}
		WithFuzzyMatch(true)(o)
		if o.fuzzyMatch == nil || !*o.fuzzyMatch {
			t.Errorf("fuzzyMatch = %v, want &true", o.fuzzyMatch)
		}
	})

	t.Run("WithFuzzyMatch false", func(t *testing.T) {
		t.Parallel()
		o := &options{}
		WithFuzzyMatch(false)(o)
		if o.fuzzyMatch == nil || *o.fuzzyMatch {
			t.Errorf("fuzzyMatch = %v, want &false", o.fuzzyMatch)
		}
	})
}

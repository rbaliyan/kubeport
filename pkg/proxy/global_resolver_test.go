package proxy

import (
	"testing"
)

func TestRegistry_MergeAndTranslate(t *testing.T) {
	r := &registry{
		addrs:     make(map[string]string),
		resolvers: make(map[*globalResolver]struct{}),
	}

	r.merge(map[string]string{
		"svc-a:80":  "localhost:8080",
		"pod-0:379": "localhost:6380",
	})

	tests := []struct {
		input string
		want  string
	}{
		{"svc-a:80", "localhost:8080"},
		{"pod-0:379", "localhost:6380"},
		{"unknown:80", "unknown:80"},
	}
	for _, tt := range tests {
		if got := r.translate(tt.input); got != tt.want {
			t.Errorf("translate(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRegistry_MergeFromMultipleClients(t *testing.T) {
	r := &registry{
		addrs:     make(map[string]string),
		resolvers: make(map[*globalResolver]struct{}),
	}

	// Client 1
	r.merge(map[string]string{"svc-a:80": "localhost:8080"})
	// Client 2
	r.merge(map[string]string{"svc-b:443": "localhost:8443"})

	if got := r.translate("svc-a:80"); got != "localhost:8080" {
		t.Errorf("svc-a: got %q", got)
	}
	if got := r.translate("svc-b:443"); got != "localhost:8443" {
		t.Errorf("svc-b: got %q", got)
	}
}

func TestRegistry_RemoveOnlyMatchingValues(t *testing.T) {
	r := &registry{
		addrs:     make(map[string]string),
		resolvers: make(map[*globalResolver]struct{}),
	}

	// Client 1 registers
	r.merge(map[string]string{"svc:80": "localhost:8080"})
	// Client 2 overwrites with different local port
	r.merge(map[string]string{"svc:80": "localhost:9090"})

	// Client 1 closes — should NOT remove because value changed
	r.remove(map[string]string{"svc:80": "localhost:8080"})

	if got := r.translate("svc:80"); got != "localhost:9090" {
		t.Errorf("after remove, got %q, want localhost:9090", got)
	}
}

func TestRegistry_RemoveMatchingValues(t *testing.T) {
	r := &registry{
		addrs:     make(map[string]string),
		resolvers: make(map[*globalResolver]struct{}),
	}

	r.merge(map[string]string{"svc:80": "localhost:8080"})
	r.remove(map[string]string{"svc:80": "localhost:8080"})

	if got := r.translate("svc:80"); got != "svc:80" {
		t.Errorf("after remove, got %q, want passthrough", got)
	}
}

func TestRegistry_TranslateFuzzyFQDN(t *testing.T) {
	r := &registry{
		addrs:     make(map[string]string),
		resolvers: make(map[*globalResolver]struct{}),
	}

	r.merge(map[string]string{"pod-0:6379": "localhost:6380"})

	got := r.translate("pod-0.headless-svc.ns.svc.cluster.local:6379")
	if got != "localhost:6380" {
		t.Errorf("fuzzy FQDN translate = %q, want localhost:6380", got)
	}
}

func TestRegisterGlobalResolver_Idempotent(t *testing.T) {
	// Just verify it doesn't panic when called multiple times.
	RegisterGlobalResolver()
	RegisterGlobalResolver()
}

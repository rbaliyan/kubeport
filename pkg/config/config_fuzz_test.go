package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// exerciseServices drives the per-service field-merge and duration-parse paths
// reached after a successful decode (ResolveNetwork/ResolveChaos + Parse). The
// oracle is no-panic: these resolvers must tolerate any decoded config.
func exerciseServices(cfg *Config) {
	for _, svc := range cfg.Services {
		_, _ = ResolveNetwork(cfg.Network, svc.Network).Parse()
		_, _ = ResolveChaos(cfg.Chaos, svc.Chaos).Parse()
		_ = ResolveConnectionMode(cfg.Supervisor, svc)
		_ = svc.IsMultiPort()
		_ = svc.Target()
	}
	_, _ = cfg.Supervisor.ParsedSupervisor()
}

func FuzzUnmarshalYAML(f *testing.F) {
	f.Add([]byte(`context: dev
namespace: default
services:
  - name: api
    service: api-svc
    remote_port: 8080
`))
	f.Add([]byte(`services:
  - name: web
    pod: web-0
    ports: all
`))
	f.Add([]byte(`services:
  - name: web
    service: web-svc
    ports:
      - http
      - grpc
`))
	f.Add([]byte(`services:
  - name: web
    service: web-svc
    ports:
      - name: http
        local_port: 9090
`))
	f.Add([]byte(`hooks:
  - name: vpn
    type: shell
    timeout: 30s
    fail_mode: closed
    events: [manager:starting]
    shell:
      manager:starting: echo starting
`))
	f.Add([]byte(`supervisor:
  max_restarts: 5
  health_check_interval: 10s
  ready_timeout: 15s
  backoff_initial: 1s
  backoff_max: 30s
`))
	f.Add([]byte(`extends: base.yaml
context: dev
services:
  - name: redis
    service: redis-svc
    remote_port: 6379
    connection_mode: isolated
network:
  latency: 50ms
  jitter: 10ms
chaos:
  enabled: true
  error_rate: 0.1
`))
	f.Add([]byte(""))
	f.Add([]byte("{{{{"))
	f.Add([]byte("services: null"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return
		}

		// Deepen: drive the field-merge and duration-parse paths fed by the decode.
		exerciseServices(&cfg)

		// Round-trip idempotence oracle. A config that parsed cleanly must
		// marshal back, re-parse without error, and then reach a fixed point on
		// a second marshal/parse. We compare the *second* and *third* parses
		// rather than the first and second because the first marshal normalizes
		// representational-but-semantically-equal differences (a nil slice/map
		// becomes an empty one once emitted as YAML and decoded back). Comparing
		// post-normalization parses isolates genuine lossy/non-idempotent
		// behavior in the custom PortsConfig codec from that benign noise.
		out1, err := yaml.Marshal(&cfg)
		if err != nil {
			t.Fatalf("re-marshal of a successfully parsed config failed: %v (cfg=%#v)", err, cfg)
		}
		var cfg2 Config
		if err := yaml.Unmarshal(out1, &cfg2); err != nil {
			t.Fatalf("re-parse of marshaled config failed: %v\nmarshaled:\n%s", err, out1)
		}
		out2, err := yaml.Marshal(&cfg2)
		if err != nil {
			t.Fatalf("second marshal failed: %v (cfg=%#v)", err, cfg2)
		}
		var cfg3 Config
		if err := yaml.Unmarshal(out2, &cfg3); err != nil {
			t.Fatalf("third parse failed: %v\nmarshaled:\n%s", err, out2)
		}
		if !reflect.DeepEqual(cfg2, cfg3) {
			t.Fatalf("YAML round-trip not idempotent:\nsecond: %#v\nthird:  %#v\nmarshaled:\n%s", cfg2, cfg3, out2)
		}
	})
}

func FuzzUnmarshalTOML(f *testing.F) {
	f.Add([]byte(`context = "dev"
namespace = "default"

[[services]]
name = "api"
service = "api-svc"
remote_port = 8080
`))
	f.Add([]byte(`[[services]]
name = "web"
pod = "web-0"
ports = "all"
`))
	f.Add([]byte(`[[services]]
name = "web"
service = "web-svc"
ports = ["http", "grpc"]
`))
	f.Add([]byte(`[[hooks]]
name = "notify"
type = "webhook"
timeout = "5s"
fail_mode = "open"
`))
	f.Add([]byte(`[supervisor]
max_restarts = 5
health_check_interval = "10s"
`))
	f.Add([]byte(`extends = "base.toml"
context = "dev"

[[services]]
name = "redis"
service = "redis-svc"
remote_port = 6379
connection_mode = "isolated"

[network]
latency = "50ms"

[chaos]
enabled = true
error_rate = 0.1
`))
	f.Add([]byte(""))
	f.Add([]byte("{{{{"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var cfg Config
		if err := unmarshalTOML(data, &cfg); err != nil {
			// Still exercise the raw toml.Unmarshal path for configTOML.
			var raw configTOML
			_ = toml.Unmarshal(data, &raw)
			return
		}

		// Deepen: drive the field-merge and duration-parse paths fed by the decode.
		exerciseServices(&cfg)

		// Round-trip idempotence oracle (see FuzzUnmarshalYAML for why the
		// second and third parses are compared rather than the first and
		// second). Guards parsePortsFromRaw and marshalTOML.
		out1, err := marshalTOML(&cfg)
		if err != nil {
			t.Fatalf("re-marshal of a successfully parsed config failed: %v (cfg=%#v)", err, cfg)
		}
		var cfg2 Config
		if err := unmarshalTOML(out1, &cfg2); err != nil {
			t.Fatalf("re-parse of marshaled config failed: %v\nmarshaled:\n%s", err, out1)
		}
		out2, err := marshalTOML(&cfg2)
		if err != nil {
			t.Fatalf("second marshal failed: %v (cfg=%#v)", err, cfg2)
		}
		var cfg3 Config
		if err := unmarshalTOML(out2, &cfg3); err != nil {
			t.Fatalf("third parse failed: %v\nmarshaled:\n%s", err, out2)
		}
		if !reflect.DeepEqual(cfg2, cfg3) {
			t.Fatalf("TOML round-trip not idempotent:\nsecond: %#v\nthird:  %#v\nmarshaled:\n%s", cfg2, cfg3, out2)
		}
	})
}

// FuzzLoadWithInheritance writes the fuzzed bytes to a temp file and calls Load,
// exercising loadWithInheritance, mergeConfigs, and the extends cycle detection.
// Oracle: no panic. Input is bounded so the fuzzer does not waste time on huge
// payloads that exercise no new logic.
func FuzzLoadWithInheritance(f *testing.F) {
	f.Add([]byte(`context: dev
services:
  - name: api
    service: api-svc
    remote_port: 8080
`))
	f.Add([]byte(`extends: ./child.yaml
services:
  - name: web
    service: web-svc
    remote_port: 80
`))
	f.Add([]byte(`extends: /nonexistent/parent.yaml
context: dev
`))
	f.Add([]byte("{{{{"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			return // bound: large inputs add no coverage for the inheritance logic
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "kubeport.yaml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			// A temp-file write failure is an environment bug, not a property of
			// the fuzz input — fail loudly rather than silently skipping coverage.
			t.Fatalf("write temp config: %v", err)
		}
		// A self-referential extends ("./kubeport.yaml") must be caught by cycle
		// detection rather than recursing forever; Load must always return.
		_, _ = Load(path)

		fuzzMergeDeterminism(t, dir, data)
	})
}

// fuzzMergeDeterminism treats the fuzzed bytes as a parent config and loads a
// fixed, well-formed child that `extends` it. When the child loads successfully
// (the parent parsed and the chain merged), it asserts two guarantees that
// mergeConfigs actually makes:
//
//  1. Determinism: loading the child twice yields identical Configs. mergeConfigs
//     is a pure function of its inputs, so repeated loads of the same files must
//     not diverge (map iteration order, slice aliasing, etc.).
//  2. Child-wins: the child sets a sentinel Context that the parent cannot
//     contain (it is rejected by YAML quoting), so the merged Context must equal
//     the child's value — override beats base for a scalar field set in the
//     child.
//
// Anything that fails to load (malformed parent, cycle, missing field) is simply
// skipped; the no-panic guarantee for those paths is covered by the single-file
// Load call above.
func fuzzMergeDeterminism(t *testing.T, dir string, parentData []byte) {
	t.Helper()

	const childContext = "fuzz-child-ctx"

	parentPath := filepath.Join(dir, "parent.yaml")
	if err := os.WriteFile(parentPath, parentData, 0o600); err != nil {
		t.Fatalf("write parent config: %v", err)
	}

	// A fixed, well-formed child extending the fuzzed parent. The child sets its
	// own Context so we can assert override-wins after the merge.
	childData := "extends: parent.yaml\ncontext: " + childContext + "\n"
	childPath := filepath.Join(dir, "child.yaml")
	if err := os.WriteFile(childPath, []byte(childData), 0o600); err != nil {
		t.Fatalf("write child config: %v", err)
	}

	cfg1, err := Load(childPath)
	if err != nil {
		return // parent did not parse / merge cleanly — nothing to assert
	}

	// Determinism: a second identical load must produce an identical Config.
	cfg2, err := Load(childPath)
	if err != nil {
		t.Fatalf("second Load of an extends chain that loaded once failed: %v", err)
	}
	if !reflect.DeepEqual(cfg1, cfg2) {
		t.Fatalf("extends merge is not deterministic:\nfirst:  %#v\nsecond: %#v\nparent:\n%s", cfg1, cfg2, parentData)
	}

	// Child-wins: the child's Context must survive the merge regardless of what
	// the parent set. The env override path is not triggered here because
	// K8S_CONTEXT is unset under `go test` unless the caller exports it; the
	// child's literal value is the expected result.
	if os.Getenv("K8S_CONTEXT") == "" && cfg1.Context != childContext {
		t.Fatalf("child Context %q did not win the merge; got %q\nparent:\n%s", childContext, cfg1.Context, parentData)
	}
}

func FuzzValidateService(f *testing.F) {
	f.Add("api", "api-svc", "", 8080, 80, "", false, 0, 0)
	f.Add("web", "", "web-pod", 0, 3000, "staging", false, 0, 0)
	f.Add("multi", "multi-svc", "", 0, 0, "", true, 0, 0)
	f.Add("", "", "", 0, 0, "", false, 0, 0)
	f.Add("both", "svc", "pod", 99999, -1, "", false, 0, 0)
	f.Add("neg", "svc", "", -1, 70000, "", false, -5, 100)

	f.Fuzz(func(t *testing.T, name, service, pod string, localPort, remotePort int, namespace string, portsAll bool, localPortOffset, selectorPort int) {
		svc := ServiceConfig{
			Name:            name,
			Service:         service,
			Pod:             pod,
			LocalPort:       localPort,
			RemotePort:      remotePort,
			Namespace:       namespace,
			LocalPortOffset: localPortOffset,
		}
		if portsAll {
			svc.Ports = PortsConfig{All: true}
		} else if selectorPort != 0 {
			svc.Ports = PortsConfig{
				Selectors: []PortSelector{{Port: selectorPort}},
			}
		}
		_ = ValidateService(svc)
	})
}

package cli

import (
	"testing"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/pkg/config"
)

// FuzzParseSvcFlag fuzzes the --svc flag parser.
//
// parseSvcFlag is a syntactic parser: it owns the structural guarantees of the
// flag grammar (a valid type/target form, a coherent port-spec, mode
// consistency) and deliberately defers two semantic checks to
// config.ValidateService, which runs later on the assembled config:
//
//   - service name presence — parts[0] is copied verbatim, so ":svc/x:80:8080"
//     parses into a Name=="" config that ValidateService rejects.
//   - port range — strconv.Atoi accepts any integer, so an out-of-range port
//     such as 70000 parses cleanly and is range-checked only by ValidateService.
//
// Both gaps are by design (a single validation choke point downstream) and are
// harmless: the bad config cannot reach the daemon because Validate runs first.
// The oracle therefore asserts the property that actually matters: a successful
// parse must produce a config whose ONLY possible validation failures are those
// two deferred checks. Any other ValidateService failure — i.e. one the parser
// was structurally responsible for — is a genuine CLI/validation divergence and
// must be reported, not papered over.
func FuzzParseSvcFlag(f *testing.F) {
	f.Add("web:svc/nginx:80:8080")
	f.Add("api:svc/api-svc:8080:9090:staging")
	f.Add("db:pod/postgres-0:5432:5432")
	f.Add("multi:svc/multi-svc:all")
	f.Add("multi:svc/multi-svc:all:+10000")
	f.Add("multi:svc/multi-svc:all:+10000:prod")
	f.Add("named:svc/svc:http,grpc")
	f.Add("named:svc/svc:http,grpc:prod")
	f.Add("")
	f.Add(":::")
	f.Add("x:bad/y:1:2")
	f.Add("x:svc/:1:2")
	f.Add("x:svc/y:notaport:2")

	f.Fuzz(func(t *testing.T, s string) {
		svc, err := parseSvcFlag(s)
		if err != nil {
			return
		}

		if verr := config.ValidateService(svc); verr != nil {
			// A parse success that fails validation is acceptable only when the
			// failure is one of the two checks parseSvcFlag deliberately defers
			// (empty name, or out-of-range port). If the config is otherwise
			// valid once those are fixed up, the divergence is benign. Anything
			// else means the parser accepted something structurally bogus.
			if !onlyDeferredValidationFailure(svc) {
				t.Fatalf("parseSvcFlag(%q) succeeded but ValidateService rejected it for a non-deferred reason: %v\nsvc=%#v", s, verr, svc)
			}
		}
	})
}

// onlyDeferredValidationFailure reports whether svc fails ValidateService only
// because of the two checks parseSvcFlag intentionally leaves to it: a missing
// name, or a single-port value out of range. It does so by repairing those two
// fields on a copy and confirming the repaired config validates.
func onlyDeferredValidationFailure(svc config.ServiceConfig) bool {
	fixed := svc
	if fixed.Name == "" {
		fixed.Name = "n" // repair the deferred name-presence check
	}
	if !fixed.IsMultiPort() {
		// Repair the deferred port-range check.
		if fixed.RemotePort <= 0 || fixed.RemotePort > 65535 {
			fixed.RemotePort = 80
		}
		if fixed.LocalPort < 0 || fixed.LocalPort > 65535 {
			fixed.LocalPort = 0
		}
	}
	return config.ValidateService(fixed) == nil
}

// FuzzServiceConfigProtoRoundTrip round-trips a fuzzed ServiceConfig through the
// production serviceConfigToProto and back, asserting the semantically
// meaningful fields survive.
//
// Fields intentionally NOT carried by serviceConfigToProto (excluded from the
// equality, by design):
//   - ExcludePorts: the AddServiceRequest produced for offload does not populate
//     PortSpec.ExcludePorts (it is set separately on the daemon side).
//   - PortSelector.Port / PortSelector.LocalPort: only selector *names* are
//     serialized; a selector with no name is dropped entirely.
//   - Network / Chaos / Lazy / ConnectionMode / runtime fields: not part of the
//     AddServiceRequest wire shape.
func FuzzServiceConfigProtoRoundTrip(f *testing.F) {
	f.Add("web", "nginx", "", 80, 8080, "default", false, "", "")
	f.Add("db", "", "pg-0", 5432, 0, "prod", false, "", "")
	f.Add("multi", "multi-svc", "", 0, 0, "", true, "", "")
	f.Add("named", "svc", "", 0, 0, "ns", false, "http", "grpc")
	f.Add("", "", "", 0, 0, "", false, "", "")

	f.Fuzz(func(t *testing.T, name, service, pod string, localPort, remotePort int, namespace string, all bool, sel1, sel2 string) {
		svc := config.ServiceConfig{
			Name:       name,
			Service:    service,
			Pod:        pod,
			LocalPort:  localPort,
			RemotePort: remotePort,
			Namespace:  namespace,
		}
		switch {
		case all:
			svc.Ports = config.PortsConfig{All: true}
			svc.LocalPort = 0
			svc.RemotePort = 0
		case sel1 != "" || sel2 != "":
			if sel1 != "" {
				svc.Ports.Selectors = append(svc.Ports.Selectors, config.PortSelector{Name: sel1})
			}
			if sel2 != "" {
				svc.Ports.Selectors = append(svc.Ports.Selectors, config.PortSelector{Name: sel2})
			}
			svc.LocalPort = 0
			svc.RemotePort = 0
		}

		req := serviceConfigToProto(svc)
		got := protoToServiceConfig(req)

		if got.Name != svc.Name {
			t.Fatalf("Name: got %q want %q", got.Name, svc.Name)
		}
		if got.Service != svc.Service {
			t.Fatalf("Service: got %q want %q", got.Service, svc.Service)
		}
		if got.Pod != svc.Pod {
			t.Fatalf("Pod: got %q want %q", got.Pod, svc.Pod)
		}
		if got.Namespace != svc.Namespace {
			t.Fatalf("Namespace: got %q want %q", got.Namespace, svc.Namespace)
		}

		if svc.IsMultiPort() {
			if got.Ports.All != svc.Ports.All {
				t.Fatalf("Ports.All: got %v want %v", got.Ports.All, svc.Ports.All)
			}
			// Only named selectors survive; compare names.
			wantNames := nonEmptyNames(svc.Ports.Selectors)
			gotNames := nonEmptyNames(got.Ports.Selectors)
			if !equalStrings(wantNames, gotNames) {
				t.Fatalf("selector names: got %v want %v", gotNames, wantNames)
			}
		} else {
			if got.LocalPort != svc.LocalPort {
				t.Fatalf("LocalPort: got %d want %d", got.LocalPort, svc.LocalPort)
			}
			if got.RemotePort != svc.RemotePort {
				t.Fatalf("RemotePort: got %d want %d", got.RemotePort, svc.RemotePort)
			}
		}
	})
}

// protoToServiceConfig mirrors the daemon-side serviceInfoToConfig +
// portSpecToConfig reverse conversion (internal/daemon/convert.go) and the
// AddService handler's PortSpec application (server.go), which together turn an
// AddServiceRequest back into a ServiceConfig. It is replicated here because
// those functions are unexported in the daemon package and the cli package
// (where serviceConfigToProto lives) cannot import daemon without an import
// cycle.
func protoToServiceConfig(req *kubeportv1.AddServiceRequest) config.ServiceConfig {
	si := req.GetService()
	svc := config.ServiceConfig{
		Name:       si.GetName(),
		Service:    si.GetService(),
		Pod:        si.GetPod(),
		LocalPort:  int(si.GetLocalPort()),
		RemotePort: int(si.GetRemotePort()),
		Namespace:  si.GetNamespace(),
	}
	if ps := req.GetPorts(); ps != nil {
		svc.Ports = config.PortsConfig{All: ps.GetAll()}
		for _, name := range ps.GetPortNames() {
			svc.Ports.Selectors = append(svc.Ports.Selectors, config.PortSelector{Name: name})
		}
		svc.ExcludePorts = ps.GetExcludePorts()
		svc.LocalPortOffset = int(ps.GetLocalPortOffset())
		svc.RemotePort = 0
		svc.LocalPort = 0
	}
	return svc
}

func nonEmptyNames(sels []config.PortSelector) []string {
	var out []string
	for _, s := range sels {
		if s.Name != "" {
			out = append(out, s.Name)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

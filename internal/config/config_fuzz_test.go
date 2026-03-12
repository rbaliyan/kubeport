package config

import (
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

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
	f.Add([]byte(""))
	f.Add([]byte("{{{{"))
	f.Add([]byte("services: null"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var cfg Config
		_ = yaml.Unmarshal(data, &cfg)
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
	f.Add([]byte(""))
	f.Add([]byte("{{{{"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var cfg Config
		_ = unmarshalTOML(data, &cfg)

		// Also fuzz the raw toml.Unmarshal path for configTOML
		var raw configTOML
		_ = toml.Unmarshal(data, &raw)
	})
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

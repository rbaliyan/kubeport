package hook

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// shellMetachars are the characters sanitizeShellValue (template.go) strips from
// user-controlled string fields (Service, ParentName, PortName, PodName, Error)
// to prevent shell command injection. Keep this in sync with the switch in
// sanitizeShellValue; the FuzzExpandVars oracle asserts none of these survive a
// substitution that originates from a user field.
const shellMetachars = "`$(){}[]|&;<>\n\r\x00"

// FuzzExpandVars asserts the shell-injection-prevention invariant documented on
// ExpandVars/sanitizeShellValue: every shell metacharacter in shellMetachars is
// stripped from the user-controlled fields (Service, ParentName, PortName,
// PodName, Error) before substitution. The template itself is a fixed string
// containing only those user variables plus the internally-generated safe
// variables (EVENT, PORT, REMOTE_PORT, RESTARTS, TIME), so any metacharacter in
// the output must have originated from a user field — and therefore must have
// been stripped. A violation here is a real security bug: report it, do not
// relax the oracle.
func FuzzExpandVars(f *testing.F) {
	f.Add("my-svc", "my-pod", "http", 8080, 80, 0, "")
	f.Add("", "", "", 0, 0, 0, "connection refused")
	f.Add("svc`whoami`", "$(rm -rf /)", "a|b", 1, 2, 3, "err;reboot")
	f.Add("a&b", "c<d>e", "f{g}h", 3000, 80, 1, "x[y]z")
	f.Add("line\nbreak", "carriage\rreturn", "nul\x00byte", 443, 443, 0, "p|q&r;s")
	f.Add("ünïcödé", "pod-0", "grpc", 0, 0, 0, "")

	// Fixed template containing only user-controlled variables plus safe internal
	// ones. The literal text has no metacharacters, so the output's metacharacters
	// (if any) can only come from a substituted user field.
	const tmpl = "${SERVICE}|${PARENT_NAME}|${PORT_NAME}|${POD}|${ERROR}|${EVENT}|${PORT}|${REMOTE_PORT}|${RESTARTS}|${TIME}"

	f.Fuzz(func(t *testing.T, service, parent, portName string, localPort, remotePort, restarts int, errMsg string) {
		ev := Event{
			Type:       EventForwardConnected,
			Time:       time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			Service:    service,
			ParentName: parent,
			PortName:   portName,
			LocalPort:  localPort,
			RemotePort: remotePort,
			PodName:    "pod-placeholder",
			Restarts:   restarts,
		}
		if errMsg != "" {
			ev.Error = errors.New(errMsg)
		}

		out := ExpandVars(tmpl, ev)

		// The only literal metacharacter in tmpl is "|" (the field separator), so
		// strip those out before checking. Everything remaining must be free of
		// shell metacharacters because every user field was sanitized.
		body := strings.ReplaceAll(out, "|", "")
		for _, mc := range shellMetachars {
			if strings.ContainsRune(body, mc) {
				t.Fatalf("shell metacharacter %q survived sanitization in output %q (service=%q parent=%q port_name=%q error=%q)",
					string(mc), out, service, parent, portName, errMsg)
			}
		}
	})
}

// FuzzExpandVarsJSON asserts that ExpandVarsJSON produces output safe to embed
// inside JSON string literals: substituting fuzzed user values into a JSON body
// template must yield a document that json.Valid accepts. This guards the
// documented contract that ExpandVarsJSON JSON-encodes each value before
// substitution.
func FuzzExpandVarsJSON(f *testing.F) {
	f.Add("my-svc", "my-pod", "http", "")
	f.Add(`svc"with"quotes`, `pod\back\slash`, "p\tt", "err\nnewline")
	f.Add("ctrl\x01char", "tab\tpod", "a\"b", `{"injected":true}`)
	f.Add("ünïcödé", "", "", "")

	const tmpl = `{"service":"${SERVICE}","pod":"${POD}","port_name":"${PORT_NAME}","error":"${ERROR}","event":"${EVENT}","port":${PORT}}`

	f.Fuzz(func(t *testing.T, service, pod, portName, errMsg string) {
		ev := Event{
			Type:       EventForwardConnected,
			Time:       time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			Service:    service,
			ParentName: service,
			PortName:   portName,
			LocalPort:  8080,
			RemotePort: 80,
			PodName:    pod,
		}
		if errMsg != "" {
			ev.Error = errors.New(errMsg)
		}

		out := ExpandVarsJSON(tmpl, ev)
		if !json.Valid([]byte(out)) {
			t.Fatalf("ExpandVarsJSON produced invalid JSON %q (service=%q pod=%q port_name=%q error=%q)",
				out, service, pod, portName, errMsg)
		}
	})
}

func FuzzParseEventType(f *testing.F) {
	f.Add("manager:starting")
	f.Add("manager:stopped")
	f.Add("forward:connected")
	f.Add("forward:disconnected")
	f.Add("forward:failed")
	f.Add("forward:stopped")
	f.Add("health:check_failed")
	f.Add("service:added")
	f.Add("service:removed")
	// Legacy underscore names
	f.Add("manager_starting")
	f.Add("forward_connected")
	f.Add("")
	f.Add("unknown_event")
	f.Add("MANAGER_STARTING")

	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseEventType(s)
	})
}

func FuzzBuildFromConfig(f *testing.F) {
	f.Add("hook1", "shell", "manager:starting", "30s", "open", "echo hi", "", "")
	f.Add("hook2", "webhook", "forward:connected", "5s", "closed", "", "http://example.com", "")
	f.Add("hook3", "exec", "forward:failed", "", "", "", "", "echo,hello")
	f.Add("", "", "", "", "", "", "", "")
	f.Add("h", "unknown", "bad_event", "notaduration", "invalid", "", "", "")
	f.Add("h", "shell", "", "1h", "closed", "ls -la", "", "")

	f.Fuzz(func(t *testing.T, name, typ, event, timeout, failMode, shellCmd, webhookURL, execCmd string) {
		hc := config.HookConfig{
			Name:     name,
			Type:     typ,
			Timeout:  timeout,
			FailMode: failMode,
		}
		if event != "" {
			hc.Events = []string{event}
		}
		if shellCmd != "" {
			hc.Shell = map[string]string{}
			if event != "" {
				hc.Shell[event] = shellCmd
			} else {
				hc.Shell["forward:connected"] = shellCmd
			}
		}
		if webhookURL != "" {
			hc.Webhook = &config.WebhookConfig{URL: webhookURL}
		}
		if execCmd != "" {
			hc.Exec = &config.ExecConfig{Command: []string{execCmd}}
		}
		_, _ = BuildFromConfig(hc)
	})
}

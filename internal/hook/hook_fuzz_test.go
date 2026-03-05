package hook

import (
	"errors"
	"testing"
	"time"

	"github.com/rbaliyan/kubeport/internal/config"
)

func FuzzExpandVars(f *testing.F) {
	f.Add("Service ${SERVICE} on port ${PORT}", "my-svc", "my-pod", 8080, 80, "")
	f.Add("${EVENT}: ${ERROR}", "", "", 0, 0, "connection refused")
	f.Add("", "", "", 0, 0, "")
	f.Add("${SERVICE}${SERVICE}${SERVICE}", "x", "", 1, 2, "")
	f.Add("no vars here", "svc", "", 3000, 80, "")
	f.Add("${UNKNOWN} ${PORT} ${TIME}", "svc", "pod", 443, 443, "")
	f.Add("${PARENT_NAME}/${PORT_NAME}", "svc", "pod", 0, 0, "")

	f.Fuzz(func(t *testing.T, tmpl, service, pod string, localPort, remotePort int, errMsg string) {
		ev := Event{
			Type:       EventForwardConnected,
			Time:       time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			Service:    service,
			ParentName: service,
			PortName:   "http",
			LocalPort:  localPort,
			RemotePort: remotePort,
			PodName:    pod,
			Restarts:   0,
		}
		if errMsg != "" {
			ev.Error = errors.New(errMsg)
		}
		_ = ExpandVars(tmpl, ev)
	})
}

func FuzzParseEventType(f *testing.F) {
	f.Add("manager_starting")
	f.Add("manager_stopped")
	f.Add("forward_connected")
	f.Add("forward_disconnected")
	f.Add("forward_failed")
	f.Add("forward_stopped")
	f.Add("health_check_failed")
	f.Add("service_added")
	f.Add("service_removed")
	f.Add("")
	f.Add("unknown_event")
	f.Add("MANAGER_STARTING")

	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseEventType(s)
	})
}

func FuzzBuildFromConfig(f *testing.F) {
	f.Add("hook1", "shell", "manager_starting", "30s", "open", "echo hi", "", "")
	f.Add("hook2", "webhook", "forward_connected", "5s", "closed", "", "http://example.com", "")
	f.Add("hook3", "exec", "forward_failed", "", "", "", "", "echo,hello")
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
				hc.Shell["forward_connected"] = shellCmd
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

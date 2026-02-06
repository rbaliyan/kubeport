package hook

import (
	"fmt"
	"time"

	"github.com/rbaliyan/kubeport/internal/config"
)

// BuildFromConfig constructs a Hook and its metadata from a HookConfig.
func BuildFromConfig(hc config.HookConfig) (Hook, []EventType, FailMode, time.Duration, error) {
	// Parse fail mode
	fm := FailOpen
	if hc.FailMode == "closed" {
		fm = FailClosed
	}

	// Parse timeout
	timeout := 10 * time.Second
	if hc.Timeout != "" {
		d, err := time.ParseDuration(hc.Timeout)
		if err != nil {
			return nil, nil, 0, 0, fmt.Errorf("hook %q: invalid timeout %q: %w", hc.Name, hc.Timeout, err)
		}
		timeout = d
	}

	// Parse events
	var events []EventType
	for _, name := range hc.Events {
		et, ok := ParseEventType(name)
		if !ok {
			return nil, nil, 0, 0, fmt.Errorf("hook %q: unknown event %q", hc.Name, name)
		}
		events = append(events, et)
	}

	// Build hook by type
	var h Hook
	var err error

	switch hc.Type {
	case "shell":
		if len(hc.Shell) == 0 {
			return nil, nil, 0, 0, fmt.Errorf("hook %q: shell config with commands is required", hc.Name)
		}
		h, err = NewShellHook(hc.Name, hc.Shell, hc.FilterServices)
		if err != nil {
			return nil, nil, 0, 0, err
		}
		// If no explicit events, infer from shell command keys
		if len(events) == 0 {
			for eventName := range hc.Shell {
				et, ok := ParseEventType(eventName)
				if !ok {
					continue
				}
				events = append(events, et)
			}
		}
	case "webhook":
		if hc.Webhook == nil || hc.Webhook.URL == "" {
			return nil, nil, 0, 0, fmt.Errorf("hook %q: webhook.url is required", hc.Name)
		}
		h = NewWebhookHook(hc.Name, hc.Webhook.URL, hc.Webhook.Headers, hc.Webhook.BodyTemplate, hc.FilterServices)
	case "exec":
		if hc.Exec == nil || len(hc.Exec.Command) == 0 {
			return nil, nil, 0, 0, fmt.Errorf("hook %q: exec.command is required", hc.Name)
		}
		h, err = NewExecHook(hc.Name, hc.Exec.Command, hc.FilterServices)
		if err != nil {
			return nil, nil, 0, 0, err
		}
	default:
		return nil, nil, 0, 0, fmt.Errorf("hook %q: unknown type %q (use shell, webhook, or exec)", hc.Name, hc.Type)
	}

	return h, events, fm, timeout, nil
}

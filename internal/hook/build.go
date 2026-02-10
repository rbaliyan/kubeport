package hook

import (
	"fmt"
	"time"

	"github.com/rbaliyan/kubeport/internal/config"
)

// Registration holds a built hook with its metadata.
type Registration struct {
	Hook     Hook
	Events   []EventType
	FailMode FailMode
	Timeout  time.Duration
}

// BuildFromConfig constructs a Hook and its metadata from a HookConfig.
func BuildFromConfig(hc config.HookConfig) (Registration, error) {
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
			return Registration{}, fmt.Errorf("hook %q: invalid timeout %q: %w", hc.Name, hc.Timeout, err)
		}
		timeout = d
	}

	// Parse events
	var events []EventType
	for _, name := range hc.Events {
		et, ok := ParseEventType(name)
		if !ok {
			return Registration{}, fmt.Errorf("hook %q: unknown event %q", hc.Name, name)
		}
		events = append(events, et)
	}

	// Build hook by type
	var h Hook
	var err error

	switch hc.Type {
	case "shell":
		if len(hc.Shell) == 0 {
			return Registration{}, fmt.Errorf("hook %q: shell config with commands is required", hc.Name)
		}
		h, err = NewShellHook(hc.Name, hc.Shell, hc.FilterServices)
		if err != nil {
			return Registration{}, err
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
			return Registration{}, fmt.Errorf("hook %q: webhook.url is required", hc.Name)
		}
		h = NewWebhookHook(hc.Name, hc.Webhook.URL, hc.Webhook.Headers, hc.Webhook.BodyTemplate, hc.FilterServices)
	case "exec":
		if hc.Exec == nil || len(hc.Exec.Command) == 0 {
			return Registration{}, fmt.Errorf("hook %q: exec.command is required", hc.Name)
		}
		h, err = NewExecHook(hc.Name, hc.Exec.Command, hc.FilterServices)
		if err != nil {
			return Registration{}, err
		}
	default:
		return Registration{}, fmt.Errorf("hook %q: unknown type %q (use shell, webhook, or exec)", hc.Name, hc.Type)
	}

	return Registration{
		Hook:     h,
		Events:   events,
		FailMode: fm,
		Timeout:  timeout,
	}, nil
}

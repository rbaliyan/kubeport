package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var _ Hook = (*webhookHook)(nil)

// webhookHook POSTs a JSON payload to an HTTP endpoint on lifecycle events.
type webhookHook struct {
	name         string
	url          string
	headers      map[string]string
	bodyTemplate string // optional; uses ${VAR} expansion like ExecHook
	client       *http.Client
	filter       map[string]bool
}

// newWebhookHook creates a webhook hook.
func newWebhookHook(name, url string, headers map[string]string, bodyTemplate string, filterServices []string) *webhookHook {
	return &webhookHook{
		name:         name,
		url:          url,
		headers:      headers,
		bodyTemplate: bodyTemplate,
		client:       &http.Client{Timeout: 30 * time.Second},
		filter:       buildFilter(filterServices),
	}
}

func (h *webhookHook) Name() string { return h.name }

func (h *webhookHook) OnEvent(ctx context.Context, event Event) error {
	if !matchesFilter(h.filter, event) {
		return nil
	}

	var body []byte
	var err error

	if h.bodyTemplate != "" {
		body = []byte(ExpandVars(h.bodyTemplate, event))
	} else {
		payload := map[string]any{
			"event":       event.Type.String(),
			"service":     event.Service,
			"parent_name": event.ParentName,
			"port_name":   event.PortName,
			"local_port":  event.LocalPort,
			"remote_port": event.RemotePort,
			"pod":         event.PodName,
			"restarts":    event.Restarts,
			"time":        event.Time.Format(time.RFC3339),
		}
		if event.Error != nil {
			payload["error"] = event.Error.Error()
		}
		body, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal webhook payload: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}

	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook POST %s: %w", h.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook %s returned HTTP %d", h.url, resp.StatusCode)
	}
	return nil
}

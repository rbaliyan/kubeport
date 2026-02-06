package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WebhookHook POSTs a JSON payload to an HTTP endpoint on lifecycle events.
type WebhookHook struct {
	name         string
	url          string
	headers      map[string]string
	bodyTemplate string // optional; uses ${VAR} expansion like ExecHook
	client       *http.Client
	filter       map[string]bool
}

// NewWebhookHook creates a webhook hook.
func NewWebhookHook(name, url string, headers map[string]string, bodyTemplate string, filterServices []string) *WebhookHook {
	var filter map[string]bool
	if len(filterServices) > 0 {
		filter = make(map[string]bool, len(filterServices))
		for _, s := range filterServices {
			filter[s] = true
		}
	}
	return &WebhookHook{
		name:         name,
		url:          url,
		headers:      headers,
		bodyTemplate: bodyTemplate,
		client:       &http.Client{Timeout: 30 * time.Second},
		filter:       filter,
	}
}

func (h *WebhookHook) Name() string { return h.name }

func (h *WebhookHook) OnEvent(ctx context.Context, event Event) error {
	if h.filter != nil && event.Service != "" && !h.filter[event.Service] {
		return nil
	}

	var body []byte
	var err error

	if h.bodyTemplate != "" {
		body = []byte(expandWebhookVars(h.bodyTemplate, event))
	} else {
		payload := map[string]any{
			"event":       event.Type.String(),
			"service":     event.Service,
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
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook %s returned HTTP %d", h.url, resp.StatusCode)
	}
	return nil
}

// expandWebhookVars expands ${VAR} template variables in a string.
func expandWebhookVars(s string, e Event) string {
	var errStr string
	if e.Error != nil {
		errStr = e.Error.Error()
	}
	replacements := []struct{ old, new string }{
		{"${EVENT}", e.Type.String()},
		{"${SERVICE}", e.Service},
		{"${PORT}", strconv.Itoa(e.LocalPort)},
		{"${REMOTE_PORT}", strconv.Itoa(e.RemotePort)},
		{"${POD}", e.PodName},
		{"${RESTARTS}", strconv.Itoa(e.Restarts)},
		{"${ERROR}", errStr},
		{"${TIME}", e.Time.Format(time.RFC3339)},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	return s
}

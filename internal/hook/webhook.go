package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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
func newWebhookHook(name, rawURL string, headers map[string]string, bodyTemplate string, filterServices []string) *webhookHook {
	return &webhookHook{
		name:         name,
		url:          rawURL,
		headers:      headers,
		bodyTemplate: bodyTemplate,
		client:       &http.Client{Timeout: 30 * time.Second},
		filter:       buildFilter(filterServices),
	}
}

// validateWebhookURL checks that the webhook URL uses http or https scheme
// and does not target cloud metadata service IP ranges (169.254.0.0/16 and
// the AWS EC2 instance metadata address). Localhost is allowed since kubeport
// is a local development tool.
func validateWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("webhook URL scheme %q is not allowed; use http or https", u.Scheme)
	}

	// Resolve hostname to IP(s) and check for metadata service ranges.
	host := u.Hostname()
	ips, err := net.LookupHost(host)
	if err != nil {
		// Unresolvable host — allow it; the HTTP client will fail later.
		return nil
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isMetadataIP(ip) {
			return fmt.Errorf("webhook URL resolves to a cloud metadata service address (%s); request blocked", ipStr)
		}
	}
	return nil
}

// metadataRanges contains CIDR blocks used by cloud metadata services.
var metadataRanges = func() []*net.IPNet {
	cidrs := []string{
		"169.254.0.0/16", // link-local; includes 169.254.169.254 (AWS/GCP/Azure metadata)
		"100.64.0.0/10",  // shared address space; used by some cloud metadata endpoints
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			nets = append(nets, ipNet)
		}
	}
	return nets
}()

// isMetadataIP returns true if ip falls within a cloud metadata service range.
func isMetadataIP(ip net.IP) bool {
	for _, r := range metadataRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

func (h *webhookHook) Name() string { return h.name }

func (h *webhookHook) OnEvent(ctx context.Context, event Event) error {
	if !matchesFilter(h.filter, event) {
		return nil
	}

	if err := validateWebhookURL(h.url); err != nil {
		return fmt.Errorf("webhook %q: %w", h.name, err)
	}

	var body []byte
	var err error

	if h.bodyTemplate != "" {
		body = []byte(ExpandVarsJSON(h.bodyTemplate, event))
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

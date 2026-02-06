package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookHook_DefaultPayload(t *testing.T) {
	var received map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %s", ct)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := NewWebhookHook("test-wh", srv.URL, nil, "", nil)
	event := Event{
		Type:       EventForwardConnected,
		Time:       time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		Service:    "api",
		LocalPort:  8080,
		RemotePort: 80,
		PodName:    "api-pod-abc",
		Restarts:   2,
	}

	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if received["event"] != "forward_connected" {
		t.Errorf("event = %v, want forward_connected", received["event"])
	}
	if received["service"] != "api" {
		t.Errorf("service = %v, want api", received["service"])
	}
	if received["pod"] != "api-pod-abc" {
		t.Errorf("pod = %v, want api-pod-abc", received["pod"])
	}
	if received["local_port"] != float64(8080) {
		t.Errorf("local_port = %v, want 8080", received["local_port"])
	}
	if received["remote_port"] != float64(80) {
		t.Errorf("remote_port = %v, want 80", received["remote_port"])
	}
	if received["restarts"] != float64(2) {
		t.Errorf("restarts = %v, want 2", received["restarts"])
	}
	if _, ok := received["error"]; ok {
		t.Errorf("error should not be present when nil")
	}
}

func TestWebhookHook_ErrorInPayload(t *testing.T) {
	var received map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := NewWebhookHook("test-wh", srv.URL, nil, "", nil)
	event := Event{
		Type:  EventForwardFailed,
		Time:  time.Now(),
		Error: fmt.Errorf("connection refused"),
	}

	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if received["error"] != "connection refused" {
		t.Errorf("error = %v, want 'connection refused'", received["error"])
	}
}

func TestWebhookHook_BodyTemplate(t *testing.T) {
	var body string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tmpl := `{"svc":"${SERVICE}","port":${PORT}}`
	h := NewWebhookHook("tmpl-wh", srv.URL, nil, tmpl, nil)

	event := Event{
		Type:      EventForwardConnected,
		Time:      time.Now(),
		Service:   "redis",
		LocalPort: 6379,
	}

	if err := h.OnEvent(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := `{"svc":"redis","port":6379}`
	if body != expected {
		t.Errorf("body = %q, want %q", body, expected)
	}
}

func TestWebhookHook_CustomHeaders(t *testing.T) {
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	headers := map[string]string{"Authorization": "Bearer test-token"}
	h := NewWebhookHook("auth-wh", srv.URL, headers, "", nil)

	if err := h.OnEvent(context.Background(), Event{Type: EventManagerStopped, Time: time.Now()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want 'Bearer test-token'", gotAuth)
	}
}

func TestWebhookHook_ServiceFilter(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := NewWebhookHook("filtered-wh", srv.URL, nil, "", []string{"allowed"})

	// Non-matching service — should not call webhook
	if err := h.OnEvent(context.Background(), Event{Type: EventForwardConnected, Service: "other"}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("webhook should not be called for filtered service")
	}

	// Matching service — should call webhook
	if err := h.OnEvent(context.Background(), Event{Type: EventForwardConnected, Service: "allowed", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("webhook should be called for allowed service")
	}
}

func TestWebhookHook_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	h := NewWebhookHook("err-wh", srv.URL, nil, "", nil)
	err := h.OnEvent(context.Background(), Event{Type: EventManagerStopped, Time: time.Now()})
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

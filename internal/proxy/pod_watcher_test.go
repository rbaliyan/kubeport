package proxy

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPodWatcher_TerminatingEmitsEvent(t *testing.T) {
	client := fake.NewSimpleClientset()
	fw := watch.NewFake()

	client.PrependWatchReactor("pods", k8stesting.DefaultWatchReactor(fw, nil))

	w := newPodWatcher(client, "default", "app=web", testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.run(ctx)

	// Send a modified event with DeletionTimestamp set (terminating pod).
	now := metav1.Now()
	fw.Modify(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "web-abc123",
			Namespace:         "default",
			DeletionTimestamp: &now,
			ResourceVersion:   "100",
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})

	select {
	case evt := <-w.events():
		if evt.podName != "web-abc123" {
			t.Fatalf("got pod name %q, want %q", evt.podName, "web-abc123")
		}
		if evt.eventType != podTerminating {
			t.Fatalf("got event type %d, want podTerminating", evt.eventType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for terminating event")
	}
}

func TestPodWatcher_NonTerminatingIgnored(t *testing.T) {
	client := fake.NewSimpleClientset()
	fw := watch.NewFake()

	client.PrependWatchReactor("pods", k8stesting.DefaultWatchReactor(fw, nil))

	w := newPodWatcher(client, "default", "app=web", testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.run(ctx)

	// Send a modified event without DeletionTimestamp (not terminating).
	fw.Modify(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "web-abc123",
			Namespace:       "default",
			ResourceVersion: "100",
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})

	select {
	case evt := <-w.events():
		t.Fatalf("unexpected event: %+v", evt)
	case <-time.After(200 * time.Millisecond):
		// Expected — no event emitted.
	}
}

func TestPodWatcher_NonRunningTerminatingIgnored(t *testing.T) {
	client := fake.NewSimpleClientset()
	fw := watch.NewFake()

	client.PrependWatchReactor("pods", k8stesting.DefaultWatchReactor(fw, nil))

	w := newPodWatcher(client, "default", "app=web", testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.run(ctx)

	// A pod that is Pending with DeletionTimestamp should not emit.
	now := metav1.Now()
	fw.Modify(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "web-abc123",
			Namespace:         "default",
			DeletionTimestamp: &now,
			ResourceVersion:   "100",
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	})

	select {
	case evt := <-w.events():
		t.Fatalf("unexpected event for non-running pod: %+v", evt)
	case <-time.After(200 * time.Millisecond):
		// Expected.
	}
}

func TestPodWatcher_CancelStops(t *testing.T) {
	client := fake.NewSimpleClientset()
	fw := watch.NewFake()

	client.PrependWatchReactor("pods", k8stesting.DefaultWatchReactor(fw, nil))

	w := newPodWatcher(client, "default", "app=web", testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	go w.run(ctx)

	cancel()

	// eventCh should be closed after run exits.
	select {
	case _, ok := <-w.events():
		if ok {
			t.Fatal("expected channel to be closed after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event channel to close")
	}
}

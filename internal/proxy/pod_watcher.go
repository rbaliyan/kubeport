package proxy

import (
	"context"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// podEventType identifies a pod lifecycle change.
type podEventType int

const (
	podTerminating podEventType = iota // DeletionTimestamp set on a running pod
)

// podEvent represents a pod lifecycle change detected by the watcher.
type podEvent struct {
	podName   string
	namespace string
	eventType podEventType
}

// podWatcher watches pods matching a label selector and emits lifecycle events.
type podWatcher struct {
	clientset kubernetes.Interface
	namespace string
	selector  string
	eventCh   chan podEvent
	logger    *slog.Logger
}

func newPodWatcher(clientset kubernetes.Interface, namespace, selector string, logger *slog.Logger) *podWatcher {
	return &podWatcher{
		clientset: clientset,
		namespace: namespace,
		selector:  selector,
		eventCh:   make(chan podEvent, 8),
		logger:    logger,
	}
}

// events returns the channel to receive pod lifecycle events.
func (w *podWatcher) events() <-chan podEvent {
	return w.eventCh
}

// run watches pods until ctx is cancelled. It automatically re-establishes the
// watch on channel close (API server timeout) using the last resource version.
func (w *podWatcher) run(ctx context.Context) {
	defer close(w.eventCh)

	var resourceVersion string
	for {
		if ctx.Err() != nil {
			return
		}

		watcher, err := w.clientset.CoreV1().Pods(w.namespace).Watch(ctx, metav1.ListOptions{
			LabelSelector:  w.selector,
			ResourceVersion: resourceVersion,
		})
		if err != nil {
			w.logger.Debug("pod watch failed, retrying",
				"namespace", w.namespace,
				"selector", w.selector,
				"error", err,
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		resourceVersion = w.drain(ctx, watcher)
		watcher.Stop()
	}
}

// drain reads events from the watcher until the channel closes or ctx is done.
// Returns the last seen resource version for resumption.
func (w *podWatcher) drain(ctx context.Context, watcher watch.Interface) string {
	var rv string
	for {
		select {
		case <-ctx.Done():
			return rv
		case evt, ok := <-watcher.ResultChan():
			if !ok {
				return rv // watch channel closed, will re-establish
			}

			pod, ok := evt.Object.(*corev1.Pod)
			if !ok {
				continue
			}
			rv = pod.ResourceVersion

			if evt.Type == watch.Modified && pod.DeletionTimestamp != nil && pod.Status.Phase == corev1.PodRunning {
				select {
				case w.eventCh <- podEvent{
					podName:   pod.Name,
					namespace: pod.Namespace,
					eventType: podTerminating,
				}:
				default:
					// Drop event if consumer is slow.
				}
			}
		}
	}
}

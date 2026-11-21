package watcher

import (
	"io"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func crashing(restarts int32) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns", UID: "u1"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: restarts,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}}
}

func TestDedupeAndCooldown(t *testing.T) {
	w := New(Config{Cooldown: time.Hour}, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.Observe(crashing(1))
	w.Observe(crashing(1)) // same restart: duplicate update
	w.Observe(crashing(2)) // new restart, but within cooldown
	if got := len(w.queue); got != 1 {
		t.Fatalf("queued %d jobs, want 1", got)
	}

	w2 := New(Config{Cooldown: 0}, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w2.Observe(crashing(1))
	w2.Observe(crashing(1))
	w2.Observe(crashing(2))
	if got := len(w2.queue); got != 2 {
		t.Fatalf("queued %d jobs, want 2", got)
	}
}

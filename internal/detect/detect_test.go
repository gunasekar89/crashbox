package detect

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var now = time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)

func pod(statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: statuses}}
}

func terminated(reason string, code int32, ago time.Duration) *corev1.ContainerStateTerminated {
	return &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code, FinishedAt: metav1.NewTime(now.Add(-ago))}
}

func TestDetect(t *testing.T) {
	opts := Options{MaxAge: 10 * time.Minute, Now: func() time.Time { return now }}
	cases := []struct {
		name   string
		cs     corev1.ContainerStatus
		reason string // "" means no trigger
	}{
		{"healthy", corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}, ""},
		{"crashloop", corev1.ContainerStatus{Name: "app", RestartCount: 4,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 40s"}},
			LastTerminationState: corev1.ContainerState{Terminated: terminated("Error", 1, time.Minute)}}, ReasonCrashLoopBackOff},
		{"oom then restarted", corev1.ContainerStatus{Name: "app", RestartCount: 1,
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			LastTerminationState: corev1.ContainerState{Terminated: terminated("OOMKilled", 137, time.Minute)}}, ReasonOOMKilled},
		{"oom and crashloop prefers oom", corev1.ContainerStatus{Name: "app", RestartCount: 6,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: terminated("OOMKilled", 137, 2*time.Hour)}}, ReasonOOMKilled},
		{"stale oom ignored", corev1.ContainerStatus{Name: "app", RestartCount: 1,
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			LastTerminationState: corev1.ContainerState{Terminated: terminated("OOMKilled", 137, time.Hour)}}, ""},
		{"oom currently terminated", corev1.ContainerStatus{Name: "job",
			State: corev1.ContainerState{Terminated: terminated("OOMKilled", 137, time.Second)}}, ReasonOOMKilled},
		{"image pull is not a crash", corev1.ContainerStatus{Name: "app",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(pod(tc.cs), opts)
			if tc.reason == "" {
				if len(got) != 0 {
					t.Fatalf("expected no trigger, got %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Reason != tc.reason {
				t.Fatalf("expected %s, got %+v", tc.reason, got)
			}
			if got[0].Container != tc.cs.Name || got[0].RestartCount != tc.cs.RestartCount {
				t.Fatalf("wrong container info: %+v", got[0])
			}
		})
	}
}

func TestDetectInitContainers(t *testing.T) {
	p := &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
		Name:  "migrate",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}}}}
	got := Detect(p, Options{})
	if len(got) != 1 || !got[0].Init {
		t.Fatalf("expected init trigger, got %+v", got)
	}
}

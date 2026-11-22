package collect

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gunasekar89/crashbox/internal/bundle"
	"github.com/gunasekar89/crashbox/internal/detect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCollect(t *testing.T) {
	isController := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-7d9f-x2", Namespace: "shop", UID: "aaaa-bbbb-cccc",
			Labels:          map[string]string{"app": "api", "pod-template-hash": "7d9f"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-7d9f", Controller: &isController}}},
		Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "app", Image: "api:1.2",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", RestartCount: 3,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
		}}},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Status: corev1.NodeStatus{
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue}}}}
	ev := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e1", Namespace: "shop"},
		InvolvedObject: corev1.ObjectReference{UID: pod.UID}, Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container"}

	c := &Collector{Client: fake.NewSimpleClientset(pod, node, ev), Cluster: "prod", CurrentLogs: true,
		Now: func() time.Time { return time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC) }}
	trig := detect.Detect(pod, detect.Options{})
	b := c.Collect(context.Background(), pod, trig)

	m := b.Manifest
	if len(m.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", m.Errors)
	}
	if m.Pod.Workload == nil || m.Pod.Workload.Kind != "Deployment" || m.Pod.Workload.Name != "api" {
		t.Fatalf("workload not inferred: %+v", m.Pod.Workload)
	}
	if len(m.Triggers) != 1 || m.Triggers[0].Reason != detect.ReasonOOMKilled {
		t.Fatalf("triggers: %+v", m.Triggers)
	}
	if m.Containers[0].Limits["memory"] != "128Mi" {
		t.Fatalf("limits not captured: %+v", m.Containers[0])
	}
	types := map[string]bool{}
	for _, f := range m.Files {
		types[f.Type] = true
	}
	for _, want := range []string{bundle.TypePod, bundle.TypeDescribe, bundle.TypeEvents, bundle.TypeLogsPrevious, bundle.TypeLogsCurrent, bundle.TypeNode} {
		if !types[want] {
			t.Errorf("missing file type %s (have %v)", want, types)
		}
	}
	desc, _ := b.File("describe.txt")
	for _, want := range []string{"api-7d9f-x2", "CrashLoopBackOff", "OOMKilled", "Back-off restarting"} {
		if !strings.Contains(string(desc), want) {
			t.Errorf("describe.txt missing %q:\n%s", want, desc)
		}
	}
	var evs []corev1.Event
	raw, _ := b.File("events.json")
	if err := json.Unmarshal(raw, &evs); err != nil || len(evs) != 1 {
		t.Fatalf("events.json: %v %d", err, len(evs))
	}
}

func TestCollectNotYetRestarted(t *testing.T) {
	// A container that just died and hasn't been restarted: its logs are the
	// kubelet's "current" logs, but they belong in previous.log.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "ns", UID: "u"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}}}},
	}
	c := &Collector{Client: fake.NewSimpleClientset(pod), CurrentLogs: true}
	b := c.Collect(context.Background(), pod, detect.Detect(pod, detect.Options{}))
	var logs []string
	for _, f := range b.Manifest.Files {
		if f.Container == "app" {
			logs = append(logs, f.Path)
		}
	}
	if len(logs) != 1 || logs[0] != "logs/app/previous.log" {
		t.Fatalf("want only logs/app/previous.log, got %v", logs)
	}
}

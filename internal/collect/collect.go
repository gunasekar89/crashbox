// Package collect gathers the evidence for a dying pod into a bundle.
package collect

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/gunasekar89/crashbox/internal/bundle"
	"github.com/gunasekar89/crashbox/internal/detect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Version is stamped into every manifest; set at build time.
var Version = "dev"

type Collector struct {
	Client  kubernetes.Interface
	Metrics metricsclient.Interface // optional
	Cluster string
	// LogTailLines limits each log to its last N lines (0 = no line limit).
	LogTailLines int64
	// LogLimitBytes caps each log (0 = no byte limit).
	LogLimitBytes int64
	// CurrentLogs also captures the logs of the restarted container.
	CurrentLogs bool
	Now         func() time.Time
}

// Collect builds a bundle for pod. It never fails outright on a single
// collector error; those are recorded in the manifest instead.
func (c *Collector) Collect(ctx context.Context, pod *corev1.Pod, triggers []detect.Trigger) *bundle.Bundle {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	ts := now().UTC()

	// Re-read the pod so the bundle reflects the latest status, falling back
	// to the informer's copy if the pod is already gone.
	if fresh, err := c.Client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil && fresh.UID == pod.UID {
		pod = fresh
	}

	b := bundle.New(bundle.Manifest{
		ID:         bundle.NewID(ts, pod.Name, string(pod.UID)),
		CapturedAt: ts,
		Cluster:    c.Cluster,
		Collector:  bundle.CollectorInfo{Name: "crashbox", Version: Version},
		Pod:        podInfo(pod),
		Triggers:   triggers,
		Containers: containerInfo(pod),
	})

	clean := pod.DeepCopy()
	clean.ManagedFields = nil
	if err := b.AddJSON("pod.json", bundle.TypePod, "", clean); err != nil {
		b.Errorf("pod", "%v", err)
	}

	events := c.collectEvents(ctx, b, pod)
	b.Add("describe.txt", bundle.TypeDescribe, "", []byte(Describe(pod, events)), false)
	c.collectLogs(ctx, b, pod, triggers)
	c.collectMetrics(ctx, b, pod)
	c.collectNode(ctx, b, pod)
	return b
}

func (c *Collector) collectEvents(ctx context.Context, b *bundle.Bundle, pod *corev1.Pod) []corev1.Event {
	sel := fields.Set{"involvedObject.uid": string(pod.UID)}.AsSelector().String()
	list, err := c.Client.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{FieldSelector: sel})
	if err != nil {
		b.Errorf("events", "%v", err)
		return nil
	}
	events := list.Items
	sort.Slice(events, func(i, j int) bool { return eventTime(events[i]).Before(eventTime(events[j])) })
	for i := range events {
		events[i].ManagedFields = nil
	}
	if err := b.AddJSON("events.json", bundle.TypeEvents, "", events); err != nil {
		b.Errorf("events", "%v", err)
	}
	return events
}

func eventTime(e corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	default:
		return e.CreationTimestamp.Time
	}
}

func (c *Collector) collectLogs(ctx context.Context, b *bundle.Bundle, pod *corev1.Pod, triggers []detect.Trigger) {
	for _, t := range triggers {
		// The previous container's logs are the evidence that vanishes on the
		// next restart, so they come first.
		c.fetchLog(ctx, b, pod, t.Container, true)
		if c.CurrentLogs {
			c.fetchLog(ctx, b, pod, t.Container, false)
		}
	}
}

func (c *Collector) fetchLog(ctx context.Context, b *bundle.Bundle, pod *corev1.Pod, container string, previous bool) {
	opts := &corev1.PodLogOptions{Container: container, Previous: previous, Timestamps: true}
	if c.LogTailLines > 0 {
		opts.TailLines = &c.LogTailLines
	}
	if c.LogLimitBytes > 0 {
		opts.LimitBytes = &c.LogLimitBytes
	}
	name, typ := "current", bundle.TypeLogsCurrent
	if previous {
		name, typ = "previous", bundle.TypeLogsPrevious
	}
	collector := "logs/" + name + "/" + container
	stream, err := c.Client.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Stream(ctx)
	if err != nil {
		b.Errorf(collector, "%v", err)
		return
	}
	defer stream.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		b.Errorf(collector, "partial read: %v", err)
	}
	truncated := c.LogLimitBytes > 0 && int64(buf.Len()) >= c.LogLimitBytes
	b.Add(fmt.Sprintf("logs/%s/%s.log", container, name), typ, container, buf.Bytes(), truncated)
}

func (c *Collector) collectMetrics(ctx context.Context, b *bundle.Bundle, pod *corev1.Pod) {
	if c.Metrics == nil {
		return
	}
	pm, err := c.Metrics.MetricsV1beta1().PodMetricses(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		b.Errorf("metrics/pod", "%v", err)
		return
	}
	pm.ManagedFields = nil
	if err := b.AddJSON("metrics/pod.json", bundle.TypePodMetrics, "", pm); err != nil {
		b.Errorf("metrics/pod", "%v", err)
	}
	usage := map[string]map[string]string{}
	for _, cm := range pm.Containers {
		u := map[string]string{}
		for k, v := range cm.Usage {
			u[string(k)] = v.String()
		}
		usage[cm.Name] = u
	}
	for i := range b.Manifest.Containers {
		b.Manifest.Containers[i].Usage = usage[b.Manifest.Containers[i].Name]
	}
}

// NodeSummary is the slice of the node that matters for a crash: pressure
// conditions and how much room there was.
type NodeSummary struct {
	Name        string                 `json:"name"`
	Labels      map[string]string      `json:"labels,omitempty"`
	Conditions  []corev1.NodeCondition `json:"conditions"`
	Capacity    corev1.ResourceList    `json:"capacity"`
	Allocatable corev1.ResourceList    `json:"allocatable"`
	NodeInfo    corev1.NodeSystemInfo  `json:"nodeInfo"`
	Usage       corev1.ResourceList    `json:"usage,omitempty"`
}

func (c *Collector) collectNode(ctx context.Context, b *bundle.Bundle, pod *corev1.Pod) {
	if pod.Spec.NodeName == "" {
		return
	}
	n, err := c.Client.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		b.Errorf("node", "%v", err)
		return
	}
	s := NodeSummary{Name: n.Name, Labels: n.Labels, Conditions: n.Status.Conditions,
		Capacity: n.Status.Capacity, Allocatable: n.Status.Allocatable, NodeInfo: n.Status.NodeInfo}
	if c.Metrics != nil {
		if nm, err := c.Metrics.MetricsV1beta1().NodeMetricses().Get(ctx, n.Name, metav1.GetOptions{}); err == nil {
			s.Usage = nm.Usage
		} else {
			b.Errorf("metrics/node", "%v", err)
		}
	}
	if err := b.AddJSON("node.json", bundle.TypeNode, "", s); err != nil {
		b.Errorf("node", "%v", err)
	}
}

func podInfo(p *corev1.Pod) bundle.PodInfo {
	info := bundle.PodInfo{Namespace: p.Namespace, Name: p.Name, UID: string(p.UID), Node: p.Spec.NodeName,
		Phase: string(p.Status.Phase), QOSClass: string(p.Status.QOSClass), Labels: p.Labels}
	if ref := metav1.GetControllerOf(p); ref != nil {
		info.Owner = &bundle.OwnerRef{Kind: ref.Kind, Name: ref.Name}
		info.Workload = info.Owner
		// A ReplicaSet named <deploy>-<pod-template-hash> belongs to a
		// Deployment. Inferring this avoids needing RBAC on ReplicaSets.
		if h := p.Labels["pod-template-hash"]; ref.Kind == "ReplicaSet" && h != "" && strings.HasSuffix(ref.Name, "-"+h) {
			info.Workload = &bundle.OwnerRef{Kind: "Deployment", Name: strings.TrimSuffix(ref.Name, "-"+h)}
		}
	}
	return info
}

func containerInfo(p *corev1.Pod) []bundle.ContainerInfo {
	statuses := map[string]corev1.ContainerStatus{}
	for _, s := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		statuses[s.Name] = s
	}
	var out []bundle.ContainerInfo
	add := func(cs []corev1.Container, init bool) {
		for _, c := range cs {
			s := statuses[c.Name]
			out = append(out, bundle.ContainerInfo{
				Name: c.Name, Init: init, Image: c.Image, ImageID: s.ImageID, Ready: s.Ready,
				RestartCount: s.RestartCount, State: stateString(s.State), LastState: stateString(s.LastTerminationState),
				Requests: resources(c.Resources.Requests), Limits: resources(c.Resources.Limits),
			})
		}
	}
	add(p.Spec.InitContainers, true)
	add(p.Spec.Containers, false)
	return out
}

func resources(rl corev1.ResourceList) map[string]string {
	if len(rl) == 0 {
		return nil
	}
	m := map[string]string{}
	for k, v := range rl {
		m[string(k)] = v.String()
	}
	return m
}

func stateString(s corev1.ContainerState) string {
	switch {
	case s.Waiting != nil:
		return "Waiting: " + s.Waiting.Reason
	case s.Running != nil:
		return "Running"
	case s.Terminated != nil:
		return fmt.Sprintf("Terminated: %s (exit %d)", s.Terminated.Reason, s.Terminated.ExitCode)
	}
	return ""
}

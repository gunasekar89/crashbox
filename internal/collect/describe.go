package collect

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Describe renders a kubectl-describe-style summary of the pod. It is built
// from the captured objects rather than by shelling out to kubectl, so the
// image needs no kubectl binary and the output matches the JSON exactly.
func Describe(p *corev1.Pod, events []corev1.Event) string {
	var sb strings.Builder
	w := tabwriter.NewWriter(&sb, 0, 4, 2, ' ', 0)
	kv := func(k, v string) { fmt.Fprintf(w, "%s:\t%s\n", k, v) }

	kv("Name", p.Name)
	kv("Namespace", p.Namespace)
	kv("Node", p.Spec.NodeName)
	if p.Status.StartTime != nil {
		kv("Start Time", p.Status.StartTime.UTC().Format(time.RFC3339))
	}
	kv("Labels", mapString(p.Labels))
	kv("Status", string(p.Status.Phase))
	if p.Status.Reason != "" {
		kv("Reason", p.Status.Reason)
	}
	kv("IP", p.Status.PodIP)
	kv("QoS Class", string(p.Status.QOSClass))
	for _, ref := range p.OwnerReferences {
		kv("Controlled By", ref.Kind+"/"+ref.Name)
	}
	w.Flush()

	statuses := map[string]corev1.ContainerStatus{}
	for _, s := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		statuses[s.Name] = s
	}
	section := func(title string, cs []corev1.Container) {
		if len(cs) == 0 {
			return
		}
		fmt.Fprintf(&sb, "%s:\n", title)
		for _, c := range cs {
			s := statuses[c.Name]
			fmt.Fprintf(&sb, "  %s:\n", c.Name)
			fmt.Fprintf(&sb, "    Image:          %s\n", c.Image)
			fmt.Fprintf(&sb, "    State:          %s\n", stateDetail(s.State))
			if last := stateDetail(s.LastTerminationState); last != "" {
				fmt.Fprintf(&sb, "    Last State:     %s\n", last)
			}
			fmt.Fprintf(&sb, "    Ready:          %t\n", s.Ready)
			fmt.Fprintf(&sb, "    Restart Count:  %d\n", s.RestartCount)
			if l := c.Resources.Limits; len(l) > 0 {
				fmt.Fprintf(&sb, "    Limits:         %s\n", resourceString(l))
			}
			if r := c.Resources.Requests; len(r) > 0 {
				fmt.Fprintf(&sb, "    Requests:       %s\n", resourceString(r))
			}
		}
	}
	section("Init Containers", p.Spec.InitContainers)
	section("Containers", p.Spec.Containers)

	if len(p.Status.Conditions) > 0 {
		sb.WriteString("Conditions:\n")
		w = tabwriter.NewWriter(&sb, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "  Type\tStatus")
		for _, c := range p.Status.Conditions {
			fmt.Fprintf(w, "  %s\t%s\n", c.Type, c.Status)
		}
		w.Flush()
	}

	sb.WriteString("Events:\n")
	if len(events) == 0 {
		sb.WriteString("  <none>\n")
		return sb.String()
	}
	w = tabwriter.NewWriter(&sb, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  Type\tReason\tLast Seen\tCount\tFrom\tMessage")
	for _, e := range events {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%s\t%s\n", e.Type, e.Reason, eventTime(e).UTC().Format(time.RFC3339),
			max(e.Count, 1), e.Source.Component, strings.TrimSpace(e.Message))
	}
	w.Flush()
	return sb.String()
}

func stateDetail(s corev1.ContainerState) string {
	switch {
	case s.Waiting != nil:
		return strings.TrimSpace("Waiting (" + s.Waiting.Reason + ") " + s.Waiting.Message)
	case s.Running != nil:
		return "Running since " + s.Running.StartedAt.UTC().Format(time.RFC3339)
	case s.Terminated != nil:
		t := s.Terminated
		out := fmt.Sprintf("Terminated (%s) exit=%d", t.Reason, t.ExitCode)
		if t.Signal != 0 {
			out += fmt.Sprintf(" signal=%d", t.Signal)
		}
		if !t.FinishedAt.IsZero() {
			out += " finished=" + t.FinishedAt.UTC().Format(time.RFC3339)
		}
		return out
	}
	return ""
}

func mapString(m map[string]string) string {
	if len(m) == 0 {
		return "<none>"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, ",")
}

func resourceString(rl corev1.ResourceList) string {
	m := map[string]string{}
	for k, v := range rl {
		m[string(k)] = v.String()
	}
	return mapString(m)
}

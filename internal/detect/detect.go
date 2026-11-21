// Package detect decides whether a pod is in a state worth recording.
package detect

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Reasons Crashbox reacts to.
const (
	ReasonCrashLoopBackOff = "CrashLoopBackOff"
	ReasonOOMKilled        = "OOMKilled"
)

// Trigger describes one container that has just died in an interesting way.
type Trigger struct {
	Reason       string     `json:"reason"`
	Container    string     `json:"container"`
	Init         bool       `json:"init"`
	RestartCount int32      `json:"restartCount"`
	ExitCode     *int32     `json:"exitCode,omitempty"`
	Signal       *int32     `json:"signal,omitempty"`
	TermReason   string     `json:"terminationReason,omitempty"`
	TerminatedAt *time.Time `json:"terminatedAt,omitempty"`
	Message      string     `json:"message,omitempty"`
}

// Options tunes detection.
type Options struct {
	// MaxAge ignores terminations that finished longer ago than this. It stops
	// a freshly started watcher from capturing every historical OOM in the
	// cluster. Zero disables the check.
	MaxAge time.Duration
	// Now is injectable for tests.
	Now func() time.Time
}

// Detect returns a trigger for every container in the pod that is in
// CrashLoopBackOff or whose most recent termination was OOMKilled.
func Detect(pod *corev1.Pod, opts Options) []Trigger {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	var out []Trigger
	check := func(statuses []corev1.ContainerStatus, init bool) {
		for _, cs := range statuses {
			if t, ok := detectContainer(cs, init, opts.MaxAge, now()); ok {
				out = append(out, t)
			}
		}
	}
	check(pod.Status.InitContainerStatuses, true)
	check(pod.Status.ContainerStatuses, false)
	return out
}

func detectContainer(cs corev1.ContainerStatus, init bool, maxAge time.Duration, now time.Time) (Trigger, bool) {
	// The termination we care about is the current one if the container is
	// still terminated, otherwise the last one before the restart.
	term := cs.State.Terminated
	if term == nil {
		term = cs.LastTerminationState.Terminated
	}
	crashLoop := cs.State.Waiting != nil && cs.State.Waiting.Reason == ReasonCrashLoopBackOff
	oom := term != nil && term.Reason == ReasonOOMKilled

	if !crashLoop && !oom {
		return Trigger{}, false
	}
	// OOM-only detections on old terminations are stale. CrashLoopBackOff is
	// a live state, so it is always current.
	if !crashLoop && maxAge > 0 && term != nil && !term.FinishedAt.IsZero() && now.Sub(term.FinishedAt.Time) > maxAge {
		return Trigger{}, false
	}

	t := Trigger{Container: cs.Name, Init: init, RestartCount: cs.RestartCount}
	// OOMKilled is the more specific diagnosis, so it wins when both apply.
	if oom {
		t.Reason = ReasonOOMKilled
	} else {
		t.Reason = ReasonCrashLoopBackOff
	}
	if crashLoop {
		t.Message = cs.State.Waiting.Message
	}
	if term != nil {
		ec, sig := term.ExitCode, term.Signal
		t.ExitCode = &ec
		if sig != 0 {
			t.Signal = &sig
		}
		t.TermReason = term.Reason
		if !term.FinishedAt.IsZero() {
			ft := term.FinishedAt.Time.UTC()
			t.TerminatedAt = &ft
		}
		if t.Message == "" {
			t.Message = term.Message
		}
	}
	return t, true
}

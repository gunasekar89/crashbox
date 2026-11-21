// Package watcher turns pod updates into captured bundles.
package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gunasekar89/crashbox/internal/bundle"
	"github.com/gunasekar89/crashbox/internal/detect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// Collector and Store are the narrow interfaces the watcher needs.
type Collector interface {
	Collect(ctx context.Context, pod *corev1.Pod, triggers []detect.Trigger) *bundle.Bundle
}
type Store func(ctx context.Context, b *bundle.Bundle) error

type Config struct {
	Namespace     string // "" = all namespaces
	LabelSelector string
	Workers       int
	// Cooldown is the minimum gap between bundles for the same container.
	// A pod in CrashLoopBackOff for hours would otherwise produce a bundle
	// every few minutes.
	Cooldown       time.Duration
	CaptureTimeout time.Duration
	Detect         detect.Options
	Resync         time.Duration
}

type Watcher struct {
	cfg     Config
	client  kubernetes.Interface
	collect Collector
	store   Store
	log     *slog.Logger
	queue   chan job

	mu   sync.Mutex
	seen map[string]seenEntry // podUID/container -> last capture
}

type job struct {
	pod      *corev1.Pod
	triggers []detect.Trigger
}

type seenEntry struct {
	restartCount int32
	at           time.Time
}

func New(cfg Config, client kubernetes.Interface, c Collector, s Store, log *slog.Logger) *Watcher {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.CaptureTimeout <= 0 {
		cfg.CaptureTimeout = 60 * time.Second
	}
	return &Watcher{cfg: cfg, client: client, collect: c, store: s, log: log,
		queue: make(chan job, 256), seen: map[string]seenEntry{}}
}

// Run blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	factory := informers.NewSharedInformerFactoryWithOptions(w.client, w.cfg.Resync,
		informers.WithNamespace(w.cfg.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = w.cfg.LabelSelector }))
	inf := factory.Core().V1().Pods().Informer()
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { w.Observe(obj) },
		UpdateFunc: func(_, obj any) { w.Observe(obj) },
	}); err != nil {
		return err
	}

	var wg sync.WaitGroup
	for i := 0; i < w.cfg.Workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.work(ctx) }()
	}
	go w.gc(ctx)

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), inf.HasSynced) {
		return fmt.Errorf("pod informer never synced")
	}
	w.log.Info("watching pods", "namespace", nsLabel(w.cfg.Namespace), "selector", w.cfg.LabelSelector)
	<-ctx.Done()
	wg.Wait()
	return nil
}

// Observe checks one pod and queues a capture if needed. Exported for tests.
func (w *Watcher) Observe(obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	triggers := w.fresh(pod, detect.Detect(pod, w.cfg.Detect))
	if len(triggers) == 0 {
		return
	}
	select {
	case w.queue <- job{pod: pod.DeepCopy(), triggers: triggers}:
	default:
		// Never block the informer. Dropping is better than stalling every
		// other pod's detection.
		w.log.Warn("capture queue full, dropping", "pod", pod.Namespace+"/"+pod.Name)
	}
}

// fresh filters out triggers already captured for this restart, or within
// the cooldown, and marks the rest as seen.
func (w *Watcher) fresh(pod *corev1.Pod, ts []detect.Trigger) []detect.Trigger {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	var out []detect.Trigger
	for _, t := range ts {
		key := string(pod.UID) + "/" + t.Container
		if s, ok := w.seen[key]; ok && (s.restartCount == t.RestartCount || now.Sub(s.at) < w.cfg.Cooldown) {
			continue
		}
		w.seen[key] = seenEntry{restartCount: t.RestartCount, at: now}
		out = append(out, t)
	}
	return out
}

func (w *Watcher) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-w.queue:
			w.capture(ctx, j)
		}
	}
}

func (w *Watcher) capture(ctx context.Context, j job) {
	ctx, cancel := context.WithTimeout(ctx, w.cfg.CaptureTimeout)
	defer cancel()
	start := time.Now()
	b := w.collect.Collect(ctx, j.pod, j.triggers)
	reasons := make([]string, len(j.triggers))
	for i, t := range j.triggers {
		reasons[i] = t.Reason + ":" + t.Container
	}
	log := w.log.With("pod", j.pod.Namespace+"/"+j.pod.Name, "triggers", reasons, "bundle", b.Manifest.ID)
	if err := w.store(ctx, b); err != nil {
		log.Error("store bundle failed", "err", err)
		return
	}
	log.Info("captured bundle", "files", len(b.Manifest.Files), "collectErrors", len(b.Manifest.Errors), "took", time.Since(start).Round(time.Millisecond))
}

// gc forgets old entries so the seen map doesn't grow forever.
func (w *Watcher) gc(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-max(w.cfg.Cooldown, time.Hour) * 6)
			w.mu.Lock()
			for k, s := range w.seen {
				if s.at.Before(cutoff) {
					delete(w.seen, k)
				}
			}
			w.mu.Unlock()
		}
	}
}

func nsLabel(ns string) string {
	if ns == "" {
		return "<all>"
	}
	return ns
}

// Command crashbox is a flight recorder for dying pods.
//
//	crashbox watch   --sink s3://bucket/crashbox       # run in-cluster
//	crashbox capture -n shop api-7d9f-x2 --sink ./out   # one-off, from a laptop
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gunasekar89/crashbox/internal/bundle"
	"github.com/gunasekar89/crashbox/internal/collect"
	"github.com/gunasekar89/crashbox/internal/detect"
	"github.com/gunasekar89/crashbox/internal/sink"
	"github.com/gunasekar89/crashbox/internal/watcher"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

type common struct {
	kubeconfig, cluster string
	sinks               multiFlag
	tailLines, maxBytes int64
	currentLogs         bool
	noMetrics           bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.kubeconfig, "kubeconfig", "", "path to kubeconfig (default: in-cluster, then $KUBECONFIG / ~/.kube/config)")
	fs.StringVar(&c.cluster, "cluster", envOr("CRASHBOX_CLUSTER", ""), "cluster name stamped into bundles and storage keys")
	fs.Var(&c.sinks, "sink", "where to store bundles: a path, file:///dir, s3://bucket/prefix or gs://bucket/prefix (repeatable; default $CRASHBOX_SINK or ./crashbox-bundles)")
	fs.Int64Var(&c.tailLines, "log-tail-lines", 5000, "keep the last N lines of each log (0 = all)")
	fs.Int64Var(&c.maxBytes, "log-max-bytes", 10<<20, "cap each log at this many bytes (0 = no cap)")
	fs.BoolVar(&c.currentLogs, "current-logs", true, "also capture logs of the restarted container")
	fs.BoolVar(&c.noMetrics, "no-metrics", false, "skip metrics.k8s.io resource usage")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "watch":
		err = runWatch(ctx, log, os.Args[2:])
	case "capture":
		err = runCapture(ctx, log, os.Args[2:])
	case "version":
		fmt.Println(collect.Version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("crashbox failed", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: crashbox <watch|capture|version> [flags]\n  watch    watch pods and capture bundles for CrashLoopBackOff / OOMKilled\n  capture  capture one pod now: crashbox capture -n <namespace> <pod>")
}

func runWatch(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	var c common
	c.register(fs)
	ns := fs.String("namespace", envOr("CRASHBOX_NAMESPACE", ""), "only watch this namespace (default: all)")
	sel := fs.String("selector", "", "only watch pods matching this label selector")
	workers := fs.Int("workers", 4, "concurrent captures")
	cooldown := fs.Duration("cooldown", 15*time.Minute, "minimum gap between bundles for the same container")
	maxAge := fs.Duration("max-age", 10*time.Minute, "ignore OOM terminations older than this (avoids capturing history on startup)")
	timeout := fs.Duration("capture-timeout", 60*time.Second, "time limit for one capture")
	addr := fs.String("health-addr", envOr("CRASHBOX_HEALTH_ADDR", ":9808"), "address for /healthz (empty disables)")
	if pos := parseInterspersed(fs, args); len(pos) > 0 {
		return fmt.Errorf("watch takes no arguments, got %v", pos)
	}

	client, col, store, err := setup(ctx, &c)
	if err != nil {
		return err
	}
	if *addr != "" {
		go serveHealth(*addr, log)
	}
	w := watcher.New(watcher.Config{
		Namespace: *ns, LabelSelector: *sel, Workers: *workers, Cooldown: *cooldown,
		CaptureTimeout: *timeout, Detect: detect.Options{MaxAge: *maxAge},
	}, client, col, store, log)
	return w.Run(ctx)
}

func runCapture(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	var c common
	c.register(fs)
	ns := fs.String("n", "default", "namespace")
	pos := parseInterspersed(fs, args)
	if len(pos) != 1 {
		return fmt.Errorf("capture needs exactly one pod name, got %d: %v", len(pos), pos)
	}
	client, col, store, err := setup(ctx, &c)
	if err != nil {
		return err
	}
	pod, err := client.CoreV1().Pods(*ns).Get(ctx, pos[0], metav1.GetOptions{})
	if err != nil {
		return err
	}
	triggers := detect.Detect(pod, detect.Options{})
	if len(triggers) == 0 {
		// A manual capture is still useful for a pod that is merely flapping,
		// so record every container's previous logs.
		for _, cs := range pod.Status.ContainerStatuses {
			triggers = append(triggers, detect.Trigger{Reason: "Manual", Container: cs.Name, RestartCount: cs.RestartCount})
		}
	}
	b := col.Collect(ctx, pod, triggers)
	if err := store(ctx, b); err != nil {
		return err
	}
	log.Info("captured bundle", "bundle", b.Key(), "files", len(b.Manifest.Files), "collectErrors", len(b.Manifest.Errors))
	return nil
}

// parseInterspersed parses flags that may come before or after positional
// arguments, like kubectl does: "capture mypod -n shop" and
// "capture -n shop mypod" both work. Go's flag package stops at the first
// positional argument on its own.
func parseInterspersed(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		if args[0] == "--" {
			return append(pos, args[1:]...)
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func setup(ctx context.Context, c *common) (kubernetes.Interface, *collect.Collector, watcher.Store, error) {
	cfg, err := restConfig(c.kubeconfig)
	if err != nil {
		return nil, nil, nil, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	col := &collect.Collector{Client: client, Cluster: c.cluster, LogTailLines: c.tailLines,
		LogLimitBytes: c.maxBytes, CurrentLogs: c.currentLogs}
	if !c.noMetrics {
		if mc, err := metricsclient.NewForConfig(cfg); err == nil {
			col.Metrics = mc
		}
	}

	urls := []string(c.sinks)
	if len(urls) == 0 {
		urls = strings.Split(envOr("CRASHBOX_SINK", "./crashbox-bundles"), ",")
	}
	var sinks []sink.Sink
	for _, u := range urls {
		s, err := sink.Open(ctx, strings.TrimSpace(u))
		if err != nil {
			return nil, nil, nil, err
		}
		sinks = append(sinks, s)
	}
	store := func(ctx context.Context, b *bundle.Bundle) error {
		var errs []error
		for _, s := range sinks {
			if _, err := sink.Write(ctx, s, b); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	return client, col, store, nil
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil).ClientConfig()
}

func serveHealth(addr string, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Warn("health server not running; capture continues", "err", err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

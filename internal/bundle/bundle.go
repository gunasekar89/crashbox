// Package bundle defines the Crashbox bundle format: a manifest.json that
// indexes everything captured, plus the raw evidence files, packed as a
// gzipped tarball. The manifest is the contract consumers such as K-Agent read.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/gunasekar89/crashbox/internal/detect"
)

const (
	APIVersion   = "crashbox.dev/v1alpha1"
	Kind         = "CrashBundle"
	ManifestFile = "manifest.json"
)

// File types, so consumers can find evidence without parsing paths.
const (
	TypePod          = "pod"
	TypeDescribe     = "describe"
	TypeEvents       = "events"
	TypeLogsPrevious = "logs/previous"
	TypeLogsCurrent  = "logs/current"
	TypePodMetrics   = "metrics/pod"
	TypeNode         = "node"
)

// Manifest is the machine-readable index of a bundle.
type Manifest struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	ID         string           `json:"id"`
	CapturedAt time.Time        `json:"capturedAt"`
	Cluster    string           `json:"cluster,omitempty"`
	Collector  CollectorInfo    `json:"collector"`
	Pod        PodInfo          `json:"pod"`
	Triggers   []detect.Trigger `json:"triggers"`
	Containers []ContainerInfo  `json:"containers"`
	Files      []FileEntry      `json:"files"`
	Errors     []CollectError   `json:"errors,omitempty"`
}

type CollectorInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type PodInfo struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	UID       string            `json:"uid"`
	Node      string            `json:"node,omitempty"`
	Phase     string            `json:"phase,omitempty"`
	QOSClass  string            `json:"qosClass,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Owner     *OwnerRef         `json:"owner,omitempty"`
	// Workload is the top-level controller, e.g. the Deployment behind a ReplicaSet.
	Workload *OwnerRef `json:"workload,omitempty"`
}

type OwnerRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type ContainerInfo struct {
	Name         string            `json:"name"`
	Init         bool              `json:"init"`
	Image        string            `json:"image"`
	ImageID      string            `json:"imageID,omitempty"`
	Ready        bool              `json:"ready"`
	RestartCount int32             `json:"restartCount"`
	State        string            `json:"state"`
	LastState    string            `json:"lastState,omitempty"`
	Requests     map[string]string `json:"requests,omitempty"`
	Limits       map[string]string `json:"limits,omitempty"`
	// Usage is a point-in-time sample from metrics.k8s.io, if available.
	Usage map[string]string `json:"usage,omitempty"`
}

type FileEntry struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	Container string `json:"container,omitempty"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
	Truncated bool   `json:"truncated,omitempty"`
}

// CollectError records a collector that failed without failing the bundle.
// Partial evidence beats no evidence.
type CollectError struct {
	Collector string `json:"collector"`
	Message   string `json:"message"`
}

// Bundle is a manifest plus file contents, assembled in memory.
type Bundle struct {
	Manifest Manifest
	files    map[string][]byte
}

func New(m Manifest) *Bundle {
	m.APIVersion, m.Kind = APIVersion, Kind
	return &Bundle{Manifest: m, files: map[string][]byte{}}
}

// Add stores a file and indexes it in the manifest.
func (b *Bundle) Add(p, typ, container string, data []byte, truncated bool) {
	sum := sha256.Sum256(data)
	b.files[p] = data
	b.Manifest.Files = append(b.Manifest.Files, FileEntry{
		Path: p, Type: typ, Container: container, Bytes: len(data),
		SHA256: hex.EncodeToString(sum[:]), Truncated: truncated,
	})
}

// AddJSON marshals v and adds it.
func (b *Bundle) AddJSON(p, typ, container string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b.Add(p, typ, container, data, false)
	return nil
}

func (b *Bundle) Errorf(collector, format string, args ...any) {
	b.Manifest.Errors = append(b.Manifest.Errors, CollectError{Collector: collector, Message: fmt.Sprintf(format, args...)})
}

func (b *Bundle) File(p string) ([]byte, bool) {
	d, ok := b.files[p]
	return d, ok
}

// ManifestJSON returns the manifest as indented JSON.
func (b *Bundle) ManifestJSON() ([]byte, error) {
	return json.MarshalIndent(b.Manifest, "", "  ")
}

// Key is the storage path for this bundle, without extension:
// <cluster>/<namespace>/<pod>/<id>. Grouping by pod keeps a crashlooping
// pod's history together for later analysis.
func (b *Bundle) Key() string {
	cluster := b.Manifest.Cluster
	if cluster == "" {
		cluster = "default"
	}
	return path.Join(cluster, b.Manifest.Pod.Namespace, b.Manifest.Pod.Name, b.Manifest.ID)
}

// WriteTarGz writes manifest.json first, then every file, under a top-level
// directory named after the bundle ID.
func (b *Bundle) WriteTarGz(w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	mt := b.Manifest.CapturedAt
	write := func(name string, data []byte) error {
		hdr := &tar.Header{Name: path.Join(b.Manifest.ID, name), Mode: 0o644, Size: int64(len(data)), ModTime: mt, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	mj, err := b.ManifestJSON()
	if err != nil {
		return err
	}
	if err := write(ManifestFile, mj); err != nil {
		return err
	}
	names := make([]string, 0, len(b.files))
	for n := range b.files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := write(n, b.files[n]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// TarGz returns the archive bytes.
func (b *Bundle) TarGz() ([]byte, error) {
	var buf bytes.Buffer
	err := b.WriteTarGz(&buf)
	return buf.Bytes(), err
}

// NewID builds a sortable, human-readable bundle ID.
func NewID(t time.Time, podName, uid string) string {
	short := strings.ReplaceAll(uid, "-", "")
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%s-%s-%s", t.UTC().Format("20060102T150405Z"), podName, short)
}

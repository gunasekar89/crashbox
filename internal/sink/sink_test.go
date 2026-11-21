package sink

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gunasekar89/crashbox/internal/bundle"
)

func TestLocalWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), "file://"+dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	b := bundle.New(bundle.Manifest{ID: "id1", CapturedAt: ts, Pod: bundle.PodInfo{Namespace: "ns", Name: "p"}})
	key, err := Write(context.Background(), s, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".tar.gz", ".manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, key+ext)); err != nil {
			t.Errorf("missing %s: %v", ext, err)
		}
	}
	if key != "default/ns/p/id1" {
		t.Errorf("key = %s", key)
	}
}

func TestOpenRejectsUnknownScheme(t *testing.T) {
	if _, err := Open(context.Background(), "ftp://x"); err == nil {
		t.Fatal("expected error")
	}
}

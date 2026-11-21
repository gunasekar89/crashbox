package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestTarGzRoundTrip(t *testing.T) {
	ts := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	b := New(Manifest{ID: NewID(ts, "api-7d9f", "1234abcd-5678"), CapturedAt: ts, Cluster: "prod",
		Pod: PodInfo{Namespace: "shop", Name: "api-7d9f", UID: "1234abcd-5678"}})
	b.Add("logs/app/previous.log", TypeLogsPrevious, "app", []byte("panic: boom\n"), false)

	if got, want := b.Key(), "prod/shop/api-7d9f/20261121T060000Z-api-7d9f-1234abcd"; got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
	data, err := b.TarGz()
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	var m Manifest
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if len(names) == 1 {
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(names) != 2 || names[0] != b.Manifest.ID+"/manifest.json" {
		t.Fatalf("unexpected entries %v", names)
	}
	if m.APIVersion != APIVersion || m.Kind != Kind || len(m.Files) != 1 || m.Files[0].SHA256 == "" {
		t.Fatalf("bad manifest %+v", m)
	}
}

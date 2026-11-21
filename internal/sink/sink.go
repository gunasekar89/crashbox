// Package sink stores finished bundles. Each bundle is written as two
// objects: <key>.tar.gz (the evidence) and <key>.manifest.json (the index),
// so consumers can list and filter crashes without downloading archives.
package sink

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/gunasekar89/crashbox/internal/bundle"
)

type Sink interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
	String() string
}

// Open parses a sink URL: file:///path (or a bare path), s3://bucket/prefix,
// gs://bucket/prefix.
func Open(ctx context.Context, raw string) (Sink, error) {
	if !strings.Contains(raw, "://") {
		return NewLocal(raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse sink %q: %w", raw, err)
	}
	prefix := strings.Trim(u.Path, "/")
	switch u.Scheme {
	case "file":
		return NewLocal(u.Path)
	case "s3":
		return NewS3(ctx, u.Host, prefix, u.Query())
	case "gs", "gcs":
		return NewGCS(ctx, u.Host, prefix)
	}
	return nil, fmt.Errorf("unsupported sink scheme %q (want file, s3 or gs)", u.Scheme)
}

// Write stores the archive, then the manifest. The manifest goes last so
// that its presence means the bundle is complete.
func Write(ctx context.Context, s Sink, b *bundle.Bundle) (string, error) {
	archive, err := b.TarGz()
	if err != nil {
		return "", err
	}
	manifest, err := b.ManifestJSON()
	if err != nil {
		return "", err
	}
	key := b.Key()
	if err := s.Put(ctx, key+".tar.gz", "application/gzip", archive); err != nil {
		return "", fmt.Errorf("%s: write archive: %w", s, err)
	}
	if err := s.Put(ctx, key+".manifest.json", "application/json", manifest); err != nil {
		return "", fmt.Errorf("%s: write manifest: %w", s, err)
	}
	return key, nil
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "/" + key
}

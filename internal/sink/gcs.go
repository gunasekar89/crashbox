package sink

import (
	"context"

	"cloud.google.com/go/storage"
)

// GCS writes bundles to a Google Cloud Storage bucket using Application
// Default Credentials, so GKE Workload Identity works unchanged.
type GCS struct {
	client         *storage.Client
	bucket, prefix string
}

func NewGCS(ctx context.Context, bucket, prefix string) (*GCS, error) {
	c, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return &GCS{client: c, bucket: bucket, prefix: prefix}, nil
}

func (g *GCS) Put(ctx context.Context, key, contentType string, data []byte) error {
	w := g.client.Bucket(g.bucket).Object(join(g.prefix, key)).NewWriter(ctx)
	w.ContentType = contentType
	if _, err := w.Write(data); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func (g *GCS) String() string { return "gs://" + join(g.bucket, g.prefix) }

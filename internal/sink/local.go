package sink

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Local writes bundles to a directory, e.g. a PersistentVolume.
type Local struct{ Dir string }

func NewLocal(dir string) (*Local, error) {
	if dir == "" {
		return nil, fmt.Errorf("local sink needs a directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Local{Dir: dir}, nil
}

func (l *Local) Put(_ context.Context, key, _ string, data []byte) error {
	p := filepath.Join(l.Dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Write to a temp file and rename, so readers never see half a bundle.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (l *Local) String() string { return "file://" + l.Dir }

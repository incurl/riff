package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Local is the on-disk [Storage] backend. It mirrors PocketBase's
// own storage layout under `pb_data/storage/<record_id>/<filename>`.
//
// When the bridge hook is configured to use this backend (the default
// and DATABASE), Put/Get/Delete are all no-ops with respect to
// PocketBase's own files: the bytes are already on disk and served
// directly. The hook is expected to detect this case and skip the
// remote mirror entirely. The methods here are still implemented so
// the bridge can call Put if it needs to copy a file into a different
// key (e.g. when migrating an attachment from one record to another).
type Local struct {
	// Root is the directory under which all object keys are
	// resolved. Typically `app.DataDir() + "/storage"`.
	Root string
}

// NewLocal returns a [Local] rooted at rootDir. rootDir is created
// if it does not exist.
func NewLocal(rootDir string) *Local {
	return &Local{Root: rootDir}
}

// Put writes the data from r to the local file at Root/key. Returns
// a relative URL of the form "/storage/<key>" which the existing
// PocketBase static handler will serve.
func (l *Local) Put(_ context.Context, key string, r io.Reader, _ string) (string, error) {
	if l == nil {
		return "", errors.New("local: nil receiver")
	}
	if key == "" {
		return "", errors.New("local: key is required")
	}
	full := filepath.Join(l.Root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("local: mkdir for %s: %w", full, err)
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("local: open %s: %w", full, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(full)
		return "", fmt.Errorf("local: write %s: %w", full, err)
	}
	return l.URL(key), nil
}

// Get opens the file for reading. Returns os.ErrNotExist for missing
// keys so callers can map that to a 404.
func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if l == nil {
		return nil, errors.New("local: nil receiver")
	}
	full := filepath.Join(l.Root, filepath.FromSlash(key))
	f, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("local: open %s: %w", full, err)
	}
	return f, nil
}

// Delete removes the file. Missing files are not an error.
func (l *Local) Delete(_ context.Context, key string) error {
	if l == nil {
		return nil
	}
	full := filepath.Join(l.Root, filepath.FromSlash(key))
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("local: remove %s: %w", full, err)
	}
	return nil
}

// URL returns the public path under which the file is served by
// PocketBase's static handler.
func (l *Local) URL(key string) string {
	if l == nil || key == "" {
		return ""
	}
	clean := strings.TrimLeft(filepath.ToSlash(key), "/")
	return "/storage/" + clean
}

// Stat returns the file's size. Missing files return (nil, nil).
func (l *Local) Stat(_ context.Context, key string) (*Object, error) {
	if l == nil {
		return nil, errors.New("local: nil receiver")
	}
	full := filepath.Join(l.Root, filepath.FromSlash(key))
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("local: stat %s: %w", full, err)
	}
	return &Object{Key: key, Size: info.Size()}, nil
}

// ErrNotFound is the canonical "object does not exist" error.
var ErrNotFound = fmt.Errorf("storage: not found")

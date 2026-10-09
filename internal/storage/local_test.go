package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalPutGetDelete(t *testing.T) {
	dir := t.TempDir()
	l := NewLocal(dir)

	// Put
	url, err := l.Put(context.Background(), "abc/patch.bin", bytes.NewReader([]byte("hello")), "application/octet-stream")
	require.NoError(t, err)
	require.Equal(t, "/storage/abc/patch.bin", url)

	// File landed on disk
	body, err := os.ReadFile(filepath.Join(dir, "abc", "patch.bin"))
	require.NoError(t, err)
	require.Equal(t, "hello", string(body))

	// Get round-trips
	r, err := l.Get(context.Background(), "abc/patch.bin")
	require.NoError(t, err)
	defer r.Close()
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))

	// Stat reports size
	obj, err := l.Stat(context.Background(), "abc/patch.bin")
	require.NoError(t, err)
	require.NotNil(t, obj)
	require.Equal(t, int64(5), obj.Size)

	// Delete is idempotent
	require.NoError(t, l.Delete(context.Background(), "abc/patch.bin"))
	require.NoError(t, l.Delete(context.Background(), "abc/patch.bin")) // missing is fine

	// Stat now reports nil for missing
	obj, err = l.Stat(context.Background(), "abc/patch.bin")
	require.NoError(t, err)
	require.Nil(t, obj)

	// Get returns ErrNotFound
	_, err = l.Get(context.Background(), "abc/patch.bin")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLocalURLPreservesSlashes(t *testing.T) {
	l := NewLocal(t.TempDir())
	// Slashes in the key are part of the path structure; they
	// must NOT be percent-encoded. The PocketBase static handler
	// serves any file under /storage/...
	require.Equal(t, "/storage/a/b.bin", l.URL("a/b.bin"))
	require.Equal(t, "/storage/x.bin", l.URL("x.bin"))
}

func TestNewDefaultsLocalOrDatabase(t *testing.T) {
	dir := t.TempDir()
	for _, b := range []string{"", BackendLocal, BackendDatabase} {
		backend, err := New(dir, Config{Backend: b})
		require.NoError(t, err)
		_, ok := backend.(*Local)
		require.True(t, ok, "backend %q should be Local", b)
	}
}

func TestNewRejectsUnknown(t *testing.T) {
	_, err := New(t.TempDir(), Config{Backend: "FLOPPY"})
	require.ErrorIs(t, err, ErrNotConfigured)
}

func TestNewS3Validation(t *testing.T) {
	base := S3Config{Endpoint: "https://s3.example.com", Bucket: "b", AccessKeyID: "id", SecretAccessKey: "sk"}
	for _, mutate := range []func(*S3Config){
		func(c *S3Config) { c.Endpoint = "" },
		func(c *S3Config) { c.Bucket = "" },
		func(c *S3Config) { c.AccessKeyID = "" },
		func(c *S3Config) { c.SecretAccessKey = "" },
	} {
		cfg := base
		mutate(&cfg)
		_, err := NewS3(cfg)
		require.Error(t, err, "should reject %+v", cfg)
		require.True(t, strings.Contains(err.Error(), "s3:"))
	}
}

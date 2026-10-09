// Package storage interface and shared types.
package storage

import (
	"context"
	"errors"
	"io"
)

// Backend names match the legacy Memos proto enum so that
// `InstanceSetting.StorageSetting.StorageType` round-trips through this
// package without re-mapping.
const (
	BackendLocal    = "LOCAL"
	BackendDatabase = "DATABASE"
	BackendS3       = "S3"
)

// ErrNotConfigured is returned by [New] when the active instance has
// no storage setting, or the S3 settings are missing required fields.
var ErrNotConfigured = errors.New("storage backend is not configured")

// Object describes the metadata of a stored object returned by
// [Storage.Stat]. Sizes are in bytes.
type Object struct {
	Key         string
	Size        int64
	ContentType string
}

// Storage is the pluggable backend interface. Implementations must be
// safe for concurrent use; the bridge hook fires from a goroutine
// pool when attachments are saved.
//
// Implementations: [Local], [S3]. The legacy DATABASE storage type is
// not a real backend in riff; PocketBase stores FileField bytes on
// disk by default and we delegate to [Local].
type Storage interface {
	// Put uploads data under key. The returned URL is what the bridge
	// hook writes into the record's `remote_url` column. contentType
	// may be empty; backends should default to application/octet-stream
	// in that case.
	Put(ctx context.Context, key string, r io.Reader, contentType string) (url string, err error)

	// Get returns the bytes for key. io.EOF is treated as "not found".
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Delete removes key. Missing keys are not an error.
	Delete(ctx context.Context, key string) error

	// URL returns the public URL for key without contacting the
	// backend (no I/O). For S3 this is the canonical bucket URL; for
	// Local this is the same path PocketBase would have served.
	URL(key string) string

	// Stat returns the object metadata. Missing keys are not an error
	// (returns nil, nil).
	Stat(ctx context.Context, key string) (*Object, error)
}

// Config is the bridge-hook-facing view of an instance's storage
// setting. The bridge hook resolves this from
// `InstanceSetting.StorageSetting` once per write, so it is cheap to
// re-construct.
type Config struct {
	// Backend is one of BackendLocal, BackendDatabase, BackendS3.
	Backend string

	// S3 holds the S3-only fields. Ignored unless Backend == BackendS3.
	S3 S3Config
}

// S3Config mirrors the legacy Memos
// `InstanceSetting.StorageSetting.S3Config` proto. The bridge hook
// reads the persisted JSON column and constructs this.
type S3Config struct {
	// Endpoint is the S3-compatible endpoint URL, including scheme
	// (e.g. "https://s3.amazonaws.com" or
	// "https://accountid.r2.cloudflarestorage.com"). Required.
	Endpoint string

	// Region is the AWS region (or "auto" for Cloudflare R2). Required.
	Region string

	// Bucket is the target bucket name. Required.
	Bucket string

	// AccessKeyID and SecretAccessKey authenticate the request.
	// Both required.
	AccessKeyID     string
	SecretAccessKey string

	// UsePathStyle forces path-style addressing
	// ("/bucket/key") instead of the default virtual-hosted style
	// ("bucket.endpoint/key"). Required for MinIO and most
	// non-AWS S3-compatible stores.
	UsePathStyle bool

	// InsecureSkipTLSVerify disables TLS certificate verification
	// when connecting to the S3 endpoint. Only enable for trusted
	// endpoints that use a self-signed certificate.
	InsecureSkipTLSVerify bool
}

// New constructs a [Storage] from cfg. The Local backend is the
// default when cfg is empty (Backend == "" or Backend == "DATABASE").
//
// The Local backend's filesystem root is the PocketBase data dir;
// callers should resolve it from `app.DataDir()` before calling.
func New(rootDir string, cfg Config) (Storage, error) {
	switch cfg.Backend {
	case "", BackendLocal, BackendDatabase:
		return NewLocal(rootDir), nil
	case BackendS3:
		return NewS3(cfg.S3)
	default:
		return nil, ErrNotConfigured
	}
}

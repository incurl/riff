// S3 backend for the storage package.
//
// S3 is a thin wrapper around PocketBase's own
// `pocketbase/tools/filesystem` package, which already ships a
// battle-tested S3 client (see discussion
// https://github.com/pocketbase/pocketbase/discussions/6562 for the
// reason riff does not depend on the official aws-sdk-go-v2). The
// riff-shaped `Storage` interface is implemented on top of
// `filesystem.System` so the bridge hook in `pb/hooks/` can call
// it without learning the lower-level API.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/pocketbase/pocketbase/tools/filesystem/blob"
)

// S3 is the S3-compatible [Storage] backend. It is safe for
// concurrent use; one underlying `*filesystem.System` is shared.
type S3 struct {
	sys  *filesystem.System
	cfg  S3Config
	root string // base URL prefix used by [S3.URL]
}

// NewS3 constructs an S3 backend. The cfg fields are validated
// lightly; an empty cfg returns an error.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("s3: endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: bucket is required")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("s3: access key and secret are required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	sys, err := filesystem.NewS3(
		cfg.Bucket,
		region,
		cfg.Endpoint,
		cfg.AccessKeyID,
		cfg.SecretAccessKey,
		cfg.UsePathStyle,
	)
	if err != nil {
		return nil, fmt.Errorf("s3: new filesystem: %w", err)
	}
	root := strings.TrimRight(cfg.Endpoint, "/")
	if cfg.UsePathStyle {
		root += "/" + cfg.Bucket
	} else {
		root = strings.Replace(root, "://", "://"+cfg.Bucket+".", 1)
	}
	return &S3{sys: sys, cfg: cfg, root: root}, nil
}

// Put uploads data to the bucket under key. The returned URL is
// what the bridge hook writes into the record's `remote_url`
// column.
func (s *S3) Put(_ context.Context, key string, r io.Reader, contentType string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("s3: nil receiver")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w, err := s.sys.NewWriter(key, &blob.WriterOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("s3: new writer: %w", err)
	}
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("s3: copy: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("s3: close: %w", err)
	}
	return s.URL(key), nil
}

// Get opens the object body for reading.
func (s *S3) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if s == nil {
		return nil, fmt.Errorf("s3: nil receiver")
	}
	r, err := s.sys.GetReader(key)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("s3: get: %w", err)
	}
	return r, nil
}

// Delete removes the object. Missing keys are not an error.
func (s *S3) Delete(_ context.Context, key string) error {
	if s == nil {
		return nil
	}
	if err := s.sys.Delete(key); err != nil && !errors.Is(err, blob.ErrNotFound) && err != filesystem.ErrNotFound {
		return fmt.Errorf("s3: delete: %w", err)
	}
	return nil
}

// URL returns the public URL of the object without making a
// request. For path-style endpoints this is
// "<endpoint>/<bucket>/<key>"; for virtual-hosted style it is
// "<bucket>.<endpoint-host>/<key>". The key is split on "/" and
// each segment is percent-encoded so the URL is unambiguous to the
// receiving server while preserving the path structure.
func (s *S3) URL(key string) string {
	if s == nil || key == "" {
		return ""
	}
	return s.root + "/" + encodeS3Key(key)
}

// Stat returns the object size. Missing objects return (nil, nil).
// The PB filesystem API does not expose a HEAD call, so we
// approximate by opening the reader and asking for its size.
func (s *S3) Stat(_ context.Context, key string) (*Object, error) {
	if s == nil {
		return nil, fmt.Errorf("s3: nil receiver")
	}
	r, err := s.sys.GetReader(key)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("s3: head: %w", err)
	}
	defer r.Close()
	return &Object{Key: key, Size: r.Size(), ContentType: r.ContentType()}, nil
}

// Close releases the underlying filesystem. Safe to call multiple
// times.
func (s *S3) Close() error {
	if s == nil || s.sys == nil {
		return nil
	}
	return s.sys.Close()
}

// encodeS3Key percent-encodes each path segment of key. Slashes
// are preserved as path separators, but spaces, question marks, and
// other reserved characters are escaped.
func encodeS3Key(key string) string {
	segments := strings.Split(key, "/")
	for i, seg := range segments {
		segments[i] = segmentEscape(seg)
	}
	return strings.Join(segments, "/")
}

// segmentEscape is a minimal percent-encoder for a single path
// segment. It encodes spaces and the small set of characters that
// are unsafe in URL path segments; the unreserved set (alnum +
// "-._~") is preserved.
func segmentEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
		case c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

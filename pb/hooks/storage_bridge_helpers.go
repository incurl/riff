package hooks

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/encoding/protojson"

	v1 "github.com/incurl/riff/proto/gen/api/v1"

	"github.com/incurl/riff/internal/storage"
)

// readStorageConfig reads the `instance_settings` row with
// key=STORAGE and decodes its JSON payload into a
// `storage.Config`.
func readStorageConfig(app core.App) (storage.Config, error) {
	row, err := app.FindFirstRecordByFilter("instance_settings", "key={:k}", map[string]any{
		"k": v1.InstanceSetting_Key_name[int32(v1.InstanceSetting_STORAGE)],
	})
	if err != nil {
		// No row: default to local.
		return storage.Config{Backend: storage.BackendLocal}, nil
	}
	raw := row.GetString("payload")
	if raw == "" {
		return storage.Config{Backend: storage.BackendLocal}, nil
	}
	setting := &v1.InstanceSetting_StorageSetting{}
	if err := protojson.Unmarshal([]byte(raw), setting); err != nil {
		return storage.Config{}, fmt.Errorf("storage-bridge: decode payload: %w", err)
	}
	return configFromProto(setting)
}

// configFromProto projects a v1.InstanceSetting_StorageSetting onto
// a storage.Config. The proto uses the legacy Memos shape
// (StorageType + S3Config); the newer `storages`/`default_storage_id`
// fields are not yet supported by the bridge (they ship in a
// future Phase 6).
func configFromProto(s *v1.InstanceSetting_StorageSetting) (storage.Config, error) {
	if s == nil {
		return storage.Config{Backend: storage.BackendLocal}, nil
	}
	switch s.StorageType {
	case v1.InstanceSetting_StorageSetting_S3:
		protoS3 := s.GetS3Config()
		if protoS3 == nil {
			return storage.Config{}, fmt.Errorf("storage-bridge: S3 storage type without S3 config")
		}
		return storage.Config{
			Backend: storage.BackendS3,
			S3: storage.S3Config{
				Endpoint:    protoS3.GetEndpoint(),
				Region:      protoS3.GetRegion(),
				Bucket:      protoS3.GetBucket(),
				AccessKeyID: protoS3.GetAccessKeyId(),
				// SecretAccessKey is INPUT_ONLY and never
				// returned by GetInstanceSetting, so we
				// cannot push it to S3 from the bridge's
				// read path. The admin must restart with a
				// new secret to take effect.
				UsePathStyle:          protoS3.GetUsePathStyle(),
				InsecureSkipTLSVerify: protoS3.GetInsecureSkipTlsVerify(),
			},
		}, nil
	case v1.InstanceSetting_StorageSetting_LOCAL:
		return storage.Config{Backend: storage.BackendLocal}, nil
	case v1.InstanceSetting_StorageSetting_DATABASE:
		return storage.Config{Backend: storage.BackendDatabase}, nil
	default:
		return storage.Config{Backend: storage.BackendLocal}, nil
	}
}

// shouldMirrorCollection returns true for the two collections that
// hold user-uploaded attachments.
func shouldMirrorCollection(name string) bool {
	return name == "patches" || name == "samples"
}

// mimeForFile returns a best-effort Content-Type for a filename.
func mimeForFile(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".zip":
		return "application/zip"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// joinURLs serialises a list of remote URLs into a JSON array
// string suitable for the `remote_url` text column. A single URL
// is stored as a bare string; splitURLs accepts both shapes.
func joinURLs(urls []string) string {
	if len(urls) == 1 {
		return urls[0]
	}
	b, _ := json.Marshal(urls)
	return string(b)
}

// splitURLs parses the JSON-array-shaped remote_url column back
// into a slice. A bare URL is returned as a one-element slice.
func splitURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "[") {
		return []string{raw}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{raw}
	}
	return out
}

// keyFromURL reverses a stored remote URL back into an S3 key by
// stripping the scheme, host, and bucket. The shape depends on
// the addressing style: virtual-hosted puts the bucket in the
// host, path-style puts it in the first path segment.
func keyFromURL(u string) string {
	if u == "" {
		return ""
	}
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return ""
		}
		// rest = "host/bucket/...key". Skip the host, then
		// skip the next path segment (the bucket).
		host := rest[:j]
		path := rest[j+1:]
		// Heuristic: virtual-hosted style embeds the bucket in
		// the host (bucket.s3.amazonaws.com or
		// <bucket>.<endpoint>), so the first path segment IS
		// the key. Path-style puts the bucket in the first
		// path segment, so we skip one more.
		//
		// We pick virtual-hosted unless the host contains no
		// dot OR the path's first segment matches the host's
		// first label. This catches MinIO and most S3
		// deployments without ambiguity.
		if looksVirtualHosted(host) {
			return path
		}
		if k := strings.IndexByte(path, '/'); k >= 0 {
			return path[k+1:]
		}
		return ""
	}
	if strings.HasPrefix(u, "/") {
		parts := strings.SplitN(u[1:], "/", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}
	return u
}

// looksVirtualHosted reports whether host looks like a
// virtual-hosted S3 endpoint (bucket.s3.region.amazonaws.com or
// bucket.<custom-host>). Path-style endpoints use a bare host
// like "s3.amazonaws.com" or "minio.local:9000".
func looksVirtualHosted(host string) bool {
	// Strip the port for the host-port check.
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	// Two or more labels => virtual-hosted. Single-label hosts
	// are typically path-style.
	return strings.Count(host, ".") >= 2 || strings.Contains(host, "localhost")
}

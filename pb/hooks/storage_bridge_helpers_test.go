package hooks

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"

	"github.com/incurl/riff/internal/storage"
)

func TestConfigFromProtoDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *v1.InstanceSetting_StorageSetting
		want storage.Config
	}{
		{"nil", nil, storage.Config{Backend: storage.BackendLocal}},
		{"unset", &v1.InstanceSetting_StorageSetting{}, storage.Config{Backend: storage.BackendLocal}},
		{"local", &v1.InstanceSetting_StorageSetting{StorageType: v1.InstanceSetting_StorageSetting_LOCAL}, storage.Config{Backend: storage.BackendLocal}},
		{"database", &v1.InstanceSetting_StorageSetting{StorageType: v1.InstanceSetting_StorageSetting_DATABASE}, storage.Config{Backend: storage.BackendDatabase}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := configFromProto(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestConfigFromProtoS3(t *testing.T) {
	in := &v1.InstanceSetting_StorageSetting{
		StorageType: v1.InstanceSetting_StorageSetting_S3,
		S3Config: &v1.InstanceSetting_StorageSetting_S3Config{
			AccessKeyId:           "AKIA",
			Endpoint:              "https://s3.amazonaws.com",
			Region:                "us-east-1",
			Bucket:                "patches",
			UsePathStyle:          true,
			InsecureSkipTlsVerify: true,
		},
	}
	got, err := configFromProto(in)
	require.NoError(t, err)
	require.Equal(t, storage.BackendS3, got.Backend)
	require.Equal(t, "AKIA", got.S3.AccessKeyID)
	require.Equal(t, "patches", got.S3.Bucket)
	require.True(t, got.S3.UsePathStyle)
	require.True(t, got.S3.InsecureSkipTLSVerify)
}

func TestConfigFromProtoS3Missing(t *testing.T) {
	in := &v1.InstanceSetting_StorageSetting{StorageType: v1.InstanceSetting_StorageSetting_S3}
	_, err := configFromProto(in)
	require.Error(t, err)
}

func TestJoinSplitURLs(t *testing.T) {
	// Single URL is stored as bare string.
	require.Equal(t, "https://r2.example/x", joinURLs([]string{"https://r2.example/x"}))
	// Multi is JSON array.
	joined := joinURLs([]string{"https://a", "https://b"})
	require.Equal(t, `["https://a","https://b"]`, joined)
	require.Equal(t, []string{"https://a", "https://b"}, splitURLs(joined))
	// Single round-trips.
	require.Equal(t, []string{"https://r2.example/x"}, splitURLs("https://r2.example/x"))
	// Empty.
	require.Nil(t, splitURLs(""))
	require.Nil(t, splitURLs("  "))
}

func TestKeyFromURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"virtual hosted", "https://patches.s3.amazonaws.com/a/b.wav", "a/b.wav"},
		{"path style", "https://minio.local:9000/patches/a/b.wav", "a/b.wav"},
		{"with query", "https://patches.s3.amazonaws.com/a/b.wav?X-Amz-Signature=x", "a/b.wav"},
		{"no scheme path style", "/patches/a/b.wav", "a/b.wav"},
		{"just key", "a/b.wav", "a/b.wav"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, keyFromURL(tc.in))
		})
	}
}

func TestMimeForFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"x.wav", "audio/wav"},
		{"Y.MP3", "audio/mpeg"},
		{"patch.zip", "application/zip"},
		{"photo.JPG", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"x.png", "image/png"},
		{"x.webp", "image/webp"},
		{"x.bin", "application/octet-stream"},
		{"noext", "application/octet-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, mimeForFile(tc.name))
		})
	}
}

func TestShouldMirrorCollection(t *testing.T) {
	require.True(t, shouldMirrorCollection("patches"))
	require.True(t, shouldMirrorCollection("samples"))
	require.False(t, shouldMirrorCollection("memos"))
	require.False(t, shouldMirrorCollection("users"))
}

package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeS3 is a tiny S3-compatible HTTP server that records the
// requests it sees and serves PUT/GET/HEAD/DELETE against an
// in-memory key/value store. We do not validate signatures — the
// only thing we test is that the wrapper passes the right URL,
// method, headers, and body to the PB filesystem client.
type fakeS3 struct {
	hits int64
	keys map[string][]byte
}

func newFakeS3() *fakeS3 { return &fakeS3{keys: map[string][]byte{}} }

func (f *fakeS3) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&f.hits, 1)
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			f.keys[r.URL.Path] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodGet, http.MethodHead:
			body, ok := f.keys[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			if r.Method == http.MethodGet {
				_, _ = w.Write(body)
			}
		case http.MethodDelete:
			delete(f.keys, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func TestS3URL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		endpoint  string
		bucket    string
		pathStyle bool
		key       string
		want      string
	}{
		{"path-style minio", "http://minio.local:9000", "patches", true, "a/b.wav", "http://minio.local:9000/patches/a/b.wav"},
		{"virtual-hosted s3", "https://s3.amazonaws.com", "patches", false, "a/b.wav", "https://patches.s3.amazonaws.com/a/b.wav"},
		{"path-style trailing slash", "https://s3.example.com/", "p", true, "k", "https://s3.example.com/p/k"},
		{"space in segment", "https://s3.example.com", "p", true, "has space.wav", "https://s3.example.com/p/has%20space.wav"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewS3(S3Config{
				Endpoint:        tc.endpoint,
				Region:          "us-east-1",
				Bucket:          tc.bucket,
				AccessKeyID:     "id",
				SecretAccessKey: "sk",
				UsePathStyle:    tc.pathStyle,
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, store.URL(tc.key))
		})
	}
}

func TestS3PutGetDeleteRoundTrip(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	store, err := NewS3(S3Config{
		Endpoint:        srv.URL,
		Region:          "us-east-1",
		Bucket:          "p",
		AccessKeyID:     "id",
		SecretAccessKey: "sk",
		UsePathStyle:    true,
	})
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	url, err := store.Put(ctx, "samples/x.wav", strings.NewReader("RIFFwave"), "audio/wav")
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/p/samples/x.wav", url)
	require.Greater(t, atomic.LoadInt64(&fake.hits), int64(0))

	r, err := store.Get(ctx, "samples/x.wav")
	require.NoError(t, err)
	defer r.Close()
	body, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "RIFFwave", string(body))

	require.NoError(t, store.Delete(ctx, "samples/x.wav"))
	_, err = store.Get(ctx, "samples/x.wav")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestS3StatMissing(t *testing.T) {
	srv := httptest.NewServer(newFakeS3().handler())
	defer srv.Close()
	store, err := NewS3(S3Config{
		Endpoint:        srv.URL,
		Region:          "us-east-1",
		Bucket:          "p",
		AccessKeyID:     "id",
		SecretAccessKey: "sk",
		UsePathStyle:    true,
	})
	require.NoError(t, err)
	defer store.Close()

	obj, err := store.Stat(context.Background(), "nope")
	require.NoError(t, err)
	require.Nil(t, obj)
}

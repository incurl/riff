package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAIEngine_HappyPath stands up a fake OpenAI
// endpoint, sends a small audio blob through the
// engine, and asserts the upstream sees the right
// multipart fields and the engine unwraps the
// response's `text` field correctly.
func TestOpenAIEngine_HappyPath(t *testing.T) {
	var gotFields map[string]string
	var gotFilename string
	var gotBytes int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/audio/transcriptions", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		require.NoError(t, err)
		require.Equal(t, "multipart/form-data", mediaType)

		mr := multipart.NewReader(r.Body, params["boundary"])
		gotFields = map[string]string{}
		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			require.NoError(t, perr)
			if part.FileName() != "" {
				gotFilename = part.FileName()
				raw, _ := io.ReadAll(part)
				gotBytes = len(raw)
				continue
			}
			name := part.FormName()
			val, _ := io.ReadAll(part)
			gotFields[name] = string(val)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "hello world"})
	}))
	defer srv.Close()

	engine, err := NewOpenAIEngine(Config{Type: ProviderOpenAI, Endpoint: srv.URL, APIKey: "test-key"})
	require.NoError(t, err)

	got, err := engine.Transcribe(context.Background(), bytes.NewReader([]byte("RIFFfake-audio")), "hello.wav", "audio/wav", TranscribeOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello world", got)

	assert.Equal(t, "whisper-1", gotFields["model"], "empty TranscribeOptions must pick the default model")
	assert.Equal(t, "hello.wav", gotFilename)
	assert.Equal(t, 14, gotBytes, "the engine must forward the exact audio bytes")
}

// TestOpenAIEngine_CustomModelAndLanguage confirms the
// TranscribeOptions fields are forwarded to the
// upstream multipart form.
func TestOpenAIEngine_CustomModelAndLanguage(t *testing.T) {
	gotFields := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		require.NoError(t, err)
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			require.NoError(t, perr)
			if part.FileName() != "" {
				_, _ = io.Copy(io.Discard, part)
				continue
			}
			name := part.FormName()
			val, _ := io.ReadAll(part)
			gotFields[name] = string(val)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "ok"})
	}))
	defer srv.Close()

	engine, err := NewOpenAIEngine(Config{Type: ProviderOpenAI, Endpoint: srv.URL, APIKey: "k"})
	require.NoError(t, err)

	_, err = engine.Transcribe(context.Background(), bytes.NewReader([]byte("x")), "a.wav", "audio/wav", TranscribeOptions{
		Model:    "whisper-large-v3",
		Language: "en",
		Prompt:   "PO-33, sampler",
	})
	require.NoError(t, err)
	assert.Equal(t, "whisper-large-v3", gotFields["model"])
	assert.Equal(t, "en", gotFields["language"])
	assert.Equal(t, "PO-33, sampler", gotFields["prompt"])
}

// TestOpenAIEngine_UpstreamError confirms the engine
// surfaces the upstream HTTP status and body so the
// operator's logs can pinpoint a misconfigured key
// without a second round-trip.
func TestOpenAIEngine_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "incorrect api key")
	}))
	defer srv.Close()

	engine, err := NewOpenAIEngine(Config{Type: ProviderOpenAI, Endpoint: srv.URL, APIKey: "k"})
	require.NoError(t, err)

	_, err = engine.Transcribe(context.Background(), bytes.NewReader([]byte("x")), "a.wav", "audio/wav", TranscribeOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "incorrect api key")
}

// TestOpenAIEngine_RequiresAPIKey pins the
// not-configured contract: an empty API key is
// rejected at construction so the Connect handler
// does not have to special-case the empty-key path
// in two places.
func TestOpenAIEngine_RequiresAPIKey(t *testing.T) {
	_, err := NewOpenAIEngine(Config{Type: ProviderOpenAI, APIKey: ""})
	require.Error(t, err)
	_, ok := err.(ErrNotConfigured)
	assert.True(t, ok, "want ErrNotConfigured, got %T: %v", err, err)
}

// TestNewEngine_RoutesByType is a smoke test: the
// dispatcher returns the right engine type for each
// ProviderType.
func TestNewEngine_RoutesByType(t *testing.T) {
	_, err := NewEngine(Config{Type: ProviderOpenAI, APIKey: "k"})
	require.NoError(t, err)
	_, err = NewEngine(Config{Type: ProviderGemini, APIKey: "k"})
	require.NoError(t, err)
	_, err = NewEngine(Config{Type: ProviderUnspecified, APIKey: "k"})
	require.Error(t, err)
	_, ok := err.(ErrUnknownType)
	assert.True(t, ok)
	assert.True(t, strings.Contains(err.Error(), "0"))
}

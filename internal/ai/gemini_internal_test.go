package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGeminiEngine_HappyPath stands up a fake
// Generative Language API endpoint, sends a small
// audio blob through the engine, and asserts the
// request shape (model, mime_type, base64 data) and
// the response shape (candidates[0].content.parts
// concatenation) work end to end.
func TestGeminiEngine_HappyPath(t *testing.T) {
	var gotBody map[string]any
	var gotURL string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))

		// Mirror the real Generative Language API
		// response shape so the engine's parser is
		// exercised against a faithful body.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{
							{"text": "hello "},
							{"text": "world"},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	engine, err := NewGeminiEngine(Config{Type: ProviderGemini, Endpoint: srv.URL, APIKey: "test-key"})
	require.NoError(t, err)

	got, err := engine.Transcribe(context.Background(), bytes.NewReader([]byte("RIFFfake-audio")), "hello.wav", "audio/wav", TranscribeOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello world", got)

	assert.Contains(t, gotURL, "models/gemini-2.5-flash:generateContent")
	assert.Contains(t, gotURL, "key=test-key")
	contents, ok := gotBody["contents"].([]any)
	require.True(t, ok)
	require.Len(t, contents, 1)
	parts, ok := contents[0].(map[string]any)["parts"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 2)
	inline, ok := parts[1].(map[string]any)["inline_data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "audio/wav", inline["mime_type"])
	// The data is the base64 of the audio bytes we sent.
	decoded, derr := base64.StdEncoding.DecodeString(inline["data"].(string))
	require.NoError(t, derr)
	assert.Equal(t, "RIFFfake-audio", string(decoded))
}

// TestGeminiEngine_UpstreamError pins the error
// surfacing contract: the upstream status and body
// must reach the operator's logs.
func TestGeminiEngine_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "permission denied")
	}))
	defer srv.Close()

	engine, err := NewGeminiEngine(Config{Type: ProviderGemini, Endpoint: srv.URL, APIKey: "k"})
	require.NoError(t, err)

	_, err = engine.Transcribe(context.Background(), bytes.NewReader([]byte("x")), "a.wav", "audio/wav", TranscribeOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
	assert.Contains(t, err.Error(), "permission denied")
}

// TestGeminiEngine_RequiresAPIKey is the not-configured
// contract.
func TestGeminiEngine_RequiresAPIKey(t *testing.T) {
	_, err := NewGeminiEngine(Config{Type: ProviderGemini, APIKey: ""})
	require.Error(t, err)
	_, ok := err.(ErrNotConfigured)
	assert.True(t, ok)
}

package connect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// writeAISettingRow writes a single AI setting row to
// the `instance_settings` collection. The proto's
// `api_key` is INPUT_ONLY in the wire shape, but the
// server-side instanceAISetting read uses the raw JSON
// payload, so the api_key round-trips through protojson
// and is visible to the trusted Transcribe handler.
func writeAISettingRow(t *testing.T, app core.App, providers []*v1.InstanceSetting_AIProviderConfig, transcription *v1.InstanceSetting_TranscriptionConfig) {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("instance_settings")
	require.NoError(t, err)
	row := core.NewRecord(col)
	row.Set("key", "AI")
	setting := &v1.InstanceSetting{
		Name: "instance/settings/AI",
		Value: &v1.InstanceSetting_AiSetting{
			AiSetting: &v1.InstanceSetting_AISetting{
				Providers:     providers,
				Transcription: transcription,
			},
		},
	}
	payload, err := protojson.Marshal(setting.GetAiSetting())
	require.NoError(t, err)
	row.Set("payload", string(payload))
	require.NoError(t, app.Save(row))
}

// TestTranscribe_HappyPath: stand up a fake OpenAI
// endpoint, persist an AI setting row pointing at it,
// call Transcribe, and assert the response text.
func TestTranscribe_HappyPath(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "transcribed"})
	}))
	defer srv.Close()

	writeAISettingRow(t, app,
		[]*v1.InstanceSetting_AIProviderConfig{
			{
				Id:       "openai-main",
				Title:    "OpenAI",
				Type:     v1.InstanceSetting_OPENAI,
				Endpoint: srv.URL,
				ApiKey:   "sk-test",
			},
		},
		&v1.InstanceSetting_TranscriptionConfig{
			ProviderId: "openai-main",
		},
	)

	srv2 := NewAIServer(app)
	out, err := srv2.Transcribe(withViewer(context.Background(), alice), &v1.TranscribeRequest{
		Audio: &v1.TranscriptionAudio{
			Source:      &v1.TranscriptionAudio_Content{Content: []byte("RIFFfake-audio")},
			Filename:    "recording.wav",
			ContentType: "audio/wav",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "transcribed", out.GetText())
}

// TestTranscribe_NotConfigured pins the
// "transcription is not configured on this instance"
// path: no AI setting row, or a row without a
// transcription block, must return FailedPrecondition
// rather than 500.
func TestTranscribe_NotConfigured(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewAIServer(app)

	// No setting row at all.
	_, err := srv.Transcribe(withViewer(context.Background(), alice), &v1.TranscribeRequest{
		Audio: &v1.TranscriptionAudio{
			Source: &v1.TranscriptionAudio_Content{Content: []byte("x")},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// A row with providers but no transcription block.
	writeAISettingRow(t, app,
		[]*v1.InstanceSetting_AIProviderConfig{
			{Id: "openai-main", Type: v1.InstanceSetting_OPENAI, ApiKey: "k"},
		},
		nil,
	)
	_, err = srv.Transcribe(withViewer(context.Background(), alice), &v1.TranscribeRequest{
		Audio: &v1.TranscriptionAudio{
			Source: &v1.TranscriptionAudio_Content{Content: []byte("x")},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestTranscribe_ProviderMissingAPIKey is the
// "operator configured the provider but forgot the key"
// path: ErrNotConfigured from the engine maps to
// FailedPrecondition so the SPA can render a precise
// hint.
func TestTranscribe_ProviderMissingAPIKey(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")

	writeAISettingRow(t, app,
		[]*v1.InstanceSetting_AIProviderConfig{
			{Id: "openai-main", Type: v1.InstanceSetting_OPENAI},
		},
		&v1.InstanceSetting_TranscriptionConfig{ProviderId: "openai-main"},
	)

	srv := NewAIServer(app)
	_, err := srv.Transcribe(withViewer(context.Background(), alice), &v1.TranscribeRequest{
		Audio: &v1.TranscriptionAudio{
			Source: &v1.TranscriptionAudio_Content{Content: []byte("x")},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestTranscribe_RequiresAuth is the anonymous-caller
// guard.
func TestTranscribe_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewAIServer(app)
	_, err := srv.Transcribe(context.Background(), &v1.TranscribeRequest{
		Audio: &v1.TranscriptionAudio{
			Source: &v1.TranscriptionAudio_Content{Content: []byte("x")},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestTranscribe_EmptyAudio is the request-validation
// guard: a request with no bytes and no uri is
// InvalidArgument, not 500.
func TestTranscribe_EmptyAudio(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewAIServer(app)
	_, err := srv.Transcribe(withViewer(context.Background(), alice), &v1.TranscribeRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestTranscribe_GeminiProvider confirms the dispatch
// table routes the GEMINI type to the Gemini engine;
// the test does not need to mock the upstream (we
// only verify the engine construction picks the right
// path), but it does verify the api_key reaches the
// engine by rejecting the empty-key case.
func TestTranscribe_GeminiProvider(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")

	// A Gemini provider with no API key must surface
	// the ErrNotConfigured path (FailedPrecondition),
	// not the OpenAI one.
	writeAISettingRow(t, app,
		[]*v1.InstanceSetting_AIProviderConfig{
			{Id: "gemini-main", Type: v1.InstanceSetting_GEMINI},
		},
		&v1.InstanceSetting_TranscriptionConfig{ProviderId: "gemini-main"},
	)

	srv := NewAIServer(app)
	_, err := srv.Transcribe(withViewer(context.Background(), alice), &v1.TranscribeRequest{
		Audio: &v1.TranscriptionAudio{
			Source: &v1.TranscriptionAudio_Content{Content: []byte("x")},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestTranscribe_GeminiProvider_UpstreamBody confirms
// the wire shape: the Gemini engine receives a JSON
// body whose inline_data.data field is the base64 of
// the input bytes. A fake upstream that echoes the
// first text part back lets us assert the dispatch
// table landed on the Gemini engine without
// duplicating the OpenAI test.
func TestTranscribe_GeminiProvider_UpstreamBody(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"parts": []map[string]any{{"text": "ok"}}}},
			},
		})
	}))
	defer srv.Close()

	writeAISettingRow(t, app,
		[]*v1.InstanceSetting_AIProviderConfig{
			{Id: "gemini-main", Type: v1.InstanceSetting_GEMINI, Endpoint: srv.URL, ApiKey: "k"},
		},
		&v1.InstanceSetting_TranscriptionConfig{ProviderId: "gemini-main"},
	)

	aiSrv := NewAIServer(app)
	out, err := aiSrv.Transcribe(withViewer(context.Background(), alice), &v1.TranscribeRequest{
		Audio: &v1.TranscriptionAudio{
			Source:      &v1.TranscriptionAudio_Content{Content: []byte("hello")},
			Filename:    "x.wav",
			ContentType: "audio/wav",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", out.GetText())

	contents, _ := got["contents"].([]any)
	require.NotEmpty(t, contents)
	parts, _ := contents[0].(map[string]any)["parts"].([]any)
	require.NotEmpty(t, parts)
	inline, _ := parts[1].(map[string]any)["inline_data"].(map[string]any)
	require.NotNil(t, inline)
	decoded, derr := base64.StdEncoding.DecodeString(inline["data"].(string))
	require.NoError(t, derr)
	assert.Equal(t, "hello", string(decoded))
}

// TestTranscribe_UpstreamError pins the
// CodeUnavailable mapping for an upstream HTTP error;
// the operator's log shows the upstream body so the
// misconfiguration is debuggable from a single
// request.
func TestTranscribe_UpstreamError(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "upstream rejected the request")
	}))
	defer srv.Close()

	writeAISettingRow(t, app,
		[]*v1.InstanceSetting_AIProviderConfig{
			{Id: "openai-main", Type: v1.InstanceSetting_OPENAI, Endpoint: srv.URL, ApiKey: "k"},
		},
		&v1.InstanceSetting_TranscriptionConfig{ProviderId: "openai-main"},
	)

	aiSrv := NewAIServer(app)
	_, err := aiSrv.Transcribe(withViewer(context.Background(), alice), &v1.TranscribeRequest{
		Audio: &v1.TranscriptionAudio{
			Source: &v1.TranscriptionAudio_Content{Content: []byte("x")},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "upstream rejected the request")
}

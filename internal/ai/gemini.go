package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// geminiDefaultEndpoint is the Generative Language API
// base URL. The Memos upstream also uses this; we
// mirror it for parity. The model id is appended at
// request time so the operator can pick a custom model
// without rebuilding the URL.
const geminiDefaultEndpoint = "https://generativelanguage.googleapis.com/v1beta"

// geminiDefaultModel is the upstream model when the
// proto's TranscriptionConfig.model is empty. The
// proto comment for `model` mentions
// `gemini-2.5-flash for GEMINI providers`; we match.
const geminiDefaultModel = "gemini-2.5-flash"

// geminiMaxBodyBytes caps the upload size the engine
// will buffer into memory. The SPA enforces a 25 MiB
// hard limit (mirroring the upstream `MAX_AUDIO_SIZE`
// in web/src/components/MemoEditor/services/
// transcriptionService.ts); the engine uses the same
// limit so a misbehaving client cannot OOM the server.
// 25 MiB is well within Generative Language API's
// inline-data limit.
const geminiMaxBodyBytes = 25 * 1024 * 1024

// GeminiEngine is an Engine that calls the Google
// Generative Language API's `generateContent` endpoint
// with inline audio bytes. The response is a JSON
// object with a `candidates[0].content.parts[*].text`
// field; the engine walks the parts to extract the
// best-effort text.
type GeminiEngine struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

// NewGeminiEngine constructs a GeminiEngine. An empty
// apiKey is rejected with ErrNotConfigured. An empty
// endpoint falls back to the Generative Language API
// default.
func NewGeminiEngine(cfg Config) (*GeminiEngine, error) {
	if cfg.APIKey == "" {
		return nil, ErrNotConfigured{Field: "api_key"}
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = geminiDefaultEndpoint
	}
	return &GeminiEngine{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   cfg.APIKey,
		client:   sharedHTTPClient,
	}, nil
}

// Transcribe reads up to geminiMaxBodyBytes from r and
// posts the bytes inline to the upstream
// generateContent endpoint. The MIME type falls back
// to application/octet-stream when the SPA sends
// none; Gemini's MIME sniffer is lenient and a
// correctly-named file part still resolves to the
// right decoder.
func (e *GeminiEngine) Transcribe(ctx context.Context, r io.Reader, filename, contentType string, opts TranscribeOptions) (string, error) {
	body, err := io.ReadAll(io.LimitReader(r, geminiMaxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("ai: read audio: %w", err)
	}
	if len(body) > geminiMaxBodyBytes {
		return "", fmt.Errorf("ai: audio exceeds %d-byte limit", geminiMaxBodyBytes)
	}

	model := opts.Model
	if model == "" {
		model = geminiDefaultModel
	}
	if contentType == "" {
		contentType = guessMimeFromFilename(filename)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// The proto TranscribeAudio oneof carries either
	// bytes (inline) or a URI. We only support inline,
	// which the Memos SPA always sends. The mime_type
	// field is the wire format the upstream API uses
	// to pick an audio decoder.
	prompt := "Transcribe the audio. Output only the transcribed text, with no preamble or commentary."
	if opts.Prompt != "" {
		prompt = opts.Prompt
	}
	reqBody := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": prompt},
					{
						"inline_data": map[string]any{
							"mime_type": contentType,
							"data":      base64StdEncoding(body),
						},
					},
				},
			},
		},
	}
	if opts.Language != "" {
		// Gemini does not have a language hint on
		// generateContent, so we fold it into the
		// prompt. The empty-string case means
		// "auto-detect", which is the default.
		reqBody["contents"].([]map[string]any)[0]["parts"].([]map[string]any)[0]["text"] =
			fmt.Sprintf("Transcribe the audio in %s. Output only the transcribed text, with no preamble or commentary.", opts.Language)
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("ai: marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", e.endpoint, model, e.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("ai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ai: call upstream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("ai: upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("ai: decode upstream response: %w", err)
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("ai: upstream returned no candidates")
	}
	var sb strings.Builder
	for _, p := range out.Candidates[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	return strings.TrimSpace(sb.String()), nil
}

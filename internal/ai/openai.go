package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// openAIDefaultEndpoint is the upstream API base URL.
// The Memos upstream also uses this; we mirror it for
// parity. Operators can override it via the IdP row's
// `endpoint` field to point at a self-hosted
// OpenAI-compatible server (LocalAI, Groq, etc).
const openAIDefaultEndpoint = "https://api.openai.com/v1"

// openAIDefaultModel is the upstream model when the
// proto's TranscriptionConfig.model is empty. The
// proto comment for `model` mentions
// `whisper-1 for OPENAI providers`; we match.
const openAIDefaultModel = "whisper-1"

// OpenAIEngine is an Engine that calls the OpenAI
// /v1/audio/transcriptions multipart endpoint. The
// multipart shape is the one every OpenAI-compatible
// provider accepts, so this engine also drives
// LocalAI, Groq, and similar self-hosted / proxied
// endpoints — the only thing the operator changes is
// the `endpoint` field on the IdP row.
type OpenAIEngine struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

// NewOpenAIEngine constructs an OpenAIEngine. An empty
// apiKey is rejected with ErrNotConfigured so the
// Connect handler can surface a precise
// FailedPrecondition. An empty endpoint falls back to
// the OpenAI default.
func NewOpenAIEngine(cfg Config) (*OpenAIEngine, error) {
	if cfg.APIKey == "" {
		return nil, ErrNotConfigured{Field: "api_key"}
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = openAIDefaultEndpoint
	}
	return &OpenAIEngine{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   cfg.APIKey,
		client:   sharedHTTPClient,
	}, nil
}

// Transcribe POSTs the audio bytes as a multipart form
// to the upstream endpoint. The response is a JSON
// object with a `text` field; any other shape is a
// Internal error so the handler can surface a useful
// "AI provider returned unexpected body" hint.
func (e *OpenAIEngine) Transcribe(ctx context.Context, r io.Reader, filename, contentType string, opts TranscribeOptions) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	model := opts.Model
	if model == "" {
		model = openAIDefaultModel
	}
	if err := mw.WriteField("model", model); err != nil {
		return "", fmt.Errorf("ai: write model field: %w", err)
	}
	if opts.Language != "" {
		if err := mw.WriteField("language", opts.Language); err != nil {
			return "", fmt.Errorf("ai: write language field: %w", err)
		}
	}
	if opts.Prompt != "" {
		if err := mw.WriteField("prompt", opts.Prompt); err != nil {
			return "", fmt.Errorf("ai: write prompt field: %w", err)
		}
	}

	// The file part must be named "file" per the OpenAI
	// API; the on-the-wire filename is the original
	// upload's filename so the upstream MIME sniffer
	// sees the right extension. contentType is best-
	// effort: an empty string is acceptable and tells
	// the sniffer to fall back to the filename.
	fw, err := mw.CreateFormFile("file", filenameOrDefault(filename))
	if err != nil {
		return "", fmt.Errorf("ai: create file part: %w", err)
	}
	if _, err := io.Copy(fw, r); err != nil {
		return "", fmt.Errorf("ai: copy file part: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("ai: close multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint+"/audio/transcriptions", &body)
	if err != nil {
		return "", fmt.Errorf("ai: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ai: call upstream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		// Surface the upstream body as the error message
		// so the operator's logs can pinpoint a
		// misconfigured key without a second round-trip.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("ai: upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("ai: decode upstream response: %w", err)
	}
	return strings.TrimSpace(out.Text), nil
}

// filenameOrDefault returns the original filename when
// non-empty, or a generic "audio" placeholder. The
// OpenAI API only inspects the extension, so the
// placeholder keeps multipart happy when the SPA
// sends a raw blob without a name.
func filenameOrDefault(name string) string {
	if name != "" {
		return name
	}
	return "audio"
}

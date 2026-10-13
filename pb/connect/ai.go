package connect

import (
	"bytes"
	"context"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"

	"github.com/incurl/riff/internal/ai"
)

// AIServer is the Connect handler for memos.api.v1.AIService.
// Today it serves exactly one RPC, Transcribe, but the
// type is the single point through which the rest of the
// AI surface (embeddings, completions, etc.) will land
// when the design demands them.
type AIServer struct {
	apiv1connect.UnimplementedAIServiceHandler
	app core.App
}

// NewAIServer constructs the AIService implementation.
func NewAIServer(app core.App) *AIServer { return &AIServer{app: app} }

// Transcribe dispatches the supplied audio bytes to the
// configured upstream speech-to-text provider and
// returns the best-effort text. The configuration lives
// in the `instance_settings` row whose `key == "AI"`;
// the proto's `transcription.provider_id` selects
// which entry of `providers[]` to use, and the empty
// provider_id is a clean "transcription disabled"
// signal.
//
// The handler is the only place the upstream API key
// leaves the server: it is read from the trusted
// server-side read of the `instance_settings` payload
// (the `api_key` field is INPUT_ONLY in the wire
// shape, so the SPA never sees it) and passed straight
// to the engine constructor. The engine's NewEngine
// call never persists the key.
//
// Source selection: the proto's `TranscriptionAudio`
// oneof is `content` (inline bytes) or `uri` (a
// reserved future). We support only `content`; a `uri`
// is rejected with InvalidArgument so a future wire
// shape can land without an undefined behaviour.
func (s *AIServer) Transcribe(ctx context.Context, req *v1.TranscribeRequest) (*v1.TranscribeResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}

	audio := req.GetAudio()
	if audio == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "audio is required")
	}
	content := audio.GetContent()
	if len(content) == 0 {
		// uri is the other oneof arm. Reserved for
		// future use per the proto; we surface a
		// precise InvalidArgument so a future wire
		// shape lands as a 400, not a silent no-op.
		if audio.GetUri() != "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "uri-based audio is not supported yet")
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, "audio.content is required")
	}

	// Resolve the AI instance setting. A missing or
	// empty setting is a clean "transcription not
	// configured" — the SPA hides the button by
	// default, so a real call here means the operator
	// has the row but the transcription block is
	// missing.
	setting := instanceAISetting(s.app)
	transcription := setting.GetTranscription()
	if transcription == nil || transcription.GetProviderId() == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "AI transcription is not configured on this instance")
	}

	// Find the provider row.
	var provider *v1.InstanceSetting_AIProviderConfig
	for _, p := range setting.GetProviders() {
		if p.GetId() == transcription.GetProviderId() {
			provider = p
			break
		}
	}
	if provider == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "AI provider not found: "+transcription.GetProviderId())
	}

	// Translate the proto type enum to the ai package's
	// local enum. The depguard rule on `**/internal/**`
	// keeps the ai package from importing the generated
	// protos, so the mapping is a one-line switch.
	engineCfg := ai.Config{
		Type:     aiTypeFromProto(provider.GetType()),
		Endpoint: provider.GetEndpoint(),
		APIKey:   provider.GetApiKey(),
	}
	engine, err := ai.NewEngine(engineCfg)
	if err != nil {
		return nil, aiConfigErrorToConnect(err)
	}

	opts := ai.TranscribeOptions{
		Model:    transcription.GetModel(),
		Language: transcription.GetLanguage(),
		Prompt:   transcription.GetPrompt(),
	}
	text, err := engine.Transcribe(ctx, bytes.NewReader(content), audio.GetFilename(), audio.GetContentType(), opts)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "transcription failed: "+err.Error())
	}
	return &v1.TranscribeResponse{Text: text}, nil
}

// aiTypeFromProto maps the proto's AIProviderType enum
// to the ai package's local ProviderType. The proto
// names are stable, so the mapping is a single switch.
func aiTypeFromProto(t v1.InstanceSetting_AIProviderType) ai.ProviderType {
	switch t {
	case v1.InstanceSetting_OPENAI:
		return ai.ProviderOpenAI
	case v1.InstanceSetting_GEMINI:
		return ai.ProviderGemini
	default:
		return ai.ProviderUnspecified
	}
}

// aiConfigErrorToConnect translates the engine
// constructor's typed errors into Connect errors the
// SPA can act on. ErrNotConfigured becomes
// FailedPrecondition (the operator has not set an
// API key); ErrUnknownType becomes InvalidArgument
// (the row is corrupt).
func aiConfigErrorToConnect(err error) error {
	switch err.(type) {
	case ai.ErrNotConfigured:
		return connect.NewError(connect.CodeFailedPrecondition, err.Error())
	case ai.ErrUnknownType:
		return connect.NewError(connect.CodeInvalidArgument, err.Error())
	default:
		return connect.NewError(connect.CodeInternal, err.Error())
	}
}

// _ silences the linter on the types import; it is
// the API for the on-disk `DateTime` shape, which the
// instance_settings JSON payload decodes into. A future
// transcription "completed_at" field would import this
// to populate the proto's created_at, but the v1 RPC
// has no such field.
var _ = types.DateTime{}

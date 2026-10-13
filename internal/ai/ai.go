// Package ai provides the speech-to-text engine surface
// used by the AIService.Transcribe Connect handler.
//
// The package is intentionally small. Two layers:
//
//  1. Engine is the abstraction the Connect handler
//     depends on. Every provider implements it.
//
//  2. The package-level NewEngine constructor maps the
//     proto provider-type enum to a concrete engine.
//     The Connect handler calls NewEngine once per
//     request and discards it; engines are stateless
//     from the caller's perspective (any per-request
//     state lives on the engine struct itself, not on
//     a singleton).
//
// The package does not import the generated proto
// types: that would pull the rest of riff into a leaf
// package. Instead, EngineConfig is a plain Go struct
// the Connect layer fills in. The depguard rule on
// `**/internal/**` keeps this invariant: only
// `internal/testutil` may import the generated protos.
package ai

import (
	"context"
	"fmt"
	"io"
)

// Engine is the contract every speech-to-text provider
// implements. The Connect handler reads the bytes from
// the proto request, wraps them in a Reader, and
// dispatches.
//
// Implementations must be safe to construct per-request:
// the Connect handler builds a fresh engine on every
// Transcribe call. Long-lived HTTP clients are
// acceptable on the engine struct (the OpenAI / Gemini
// providers reuse a single http.Client), but no
// goroutine-safe mutable state is required.
type Engine interface {
	// Transcribe reads the audio bytes from r and
	// returns the engine's best-effort text
	// transcription. The filename and contentType
	// hints are the original upload's metadata, so the
	// engine can pass the right MIME to the upstream
	// API. An empty contentType is a valid "let the
	// engine sniff" hint.
	Transcribe(ctx context.Context, r io.Reader, filename, contentType string, opts TranscribeOptions) (string, error)
}

// TranscribeOptions carries the per-request knobs the
// engine can honour. The proto's TranscriptionConfig
// has more fields (language, prompt); the engine is
// free to use or ignore any of them, with the OpenAI /
// Gemini provider implementations passing through what
// their respective APIs accept.
type TranscribeOptions struct {
	// Model is the upstream model identifier
	// (e.g. "whisper-1", "gemini-2.5-flash"). An empty
	// string tells the engine to pick its own
	// default.
	Model string

	// Language is the ISO 639-1 hint sent to the
	// provider. An empty string means "auto-detect".
	Language string

	// Prompt is an optional vocabulary hint the engine
	// passes to the provider as a "spelling" or
	// "context" cue.
	Prompt string
}

// Config is what NewEngine needs to know about a
// provider. The Connect handler fills it in from the
// `InstanceSetting.AI.providers[]` row matching the
// transcription.provider_id.
type Config struct {
	// Type selects the concrete engine. The proto's
	// AIProviderType enum values are translated by
	// NewEngine; anything unknown returns ErrUnknownType.
	Type ProviderType

	// Endpoint is the upstream API base URL. For
	// OPENAI the default is https://api.openai.com/v1;
	// for GEMINI the default is the Google Generative
	// Language API endpoint. Custom endpoints (e.g. a
	// self-hosted LocalAI) are honoured.
	Endpoint string

	// APIKey is the upstream bearer credential. The
	// Connect handler reads it from the
	// `api_key` column of the providers[] row.
	APIKey string
}

// ProviderType mirrors the proto's AIProviderType enum
// but in a form the internal package can use without
// importing the generated code.
type ProviderType int

const (
	ProviderUnspecified ProviderType = 0
	ProviderOpenAI      ProviderType = 1
	ProviderGemini      ProviderType = 2
)

// ErrUnknownType is returned by NewEngine when the
// config's Type is not one of the supported providers.
type ErrUnknownType struct{ Type ProviderType }

func (e ErrUnknownType) Error() string {
	return fmt.Sprintf("ai: unknown provider type %d", e.Type)
}

// ErrNotConfigured is returned by NewEngine when the
// config's APIKey is empty. The Connect handler
// translates this to a FailedPrecondition so the SPA
// can render a "set an API key in the instance
// settings" hint without leaking the underlying reason.
type ErrNotConfigured struct {
	Field string
}

func (e ErrNotConfigured) Error() string {
	return "ai: " + e.Field + " is not configured"
}

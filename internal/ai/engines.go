package ai

import (
	"net/http"
	"time"
)

// NewEngine constructs the right Engine for cfg. The
// returned engine is fresh per call (it owns a
// process-wide *http.Client underneath, but no mutable
// state), so the caller can drop it on the floor after
// the request finishes.
//
// An empty cfg.APIKey is rejected at the OpenAI /
// Gemini level with an ErrNotConfigured; the Connect
// handler translates that to a FailedPrecondition so
// the SPA can show an actionable error.
func NewEngine(cfg Config) (Engine, error) {
	switch cfg.Type {
	case ProviderOpenAI:
		return NewOpenAIEngine(cfg)
	case ProviderGemini:
		return NewGeminiEngine(cfg)
	default:
		return nil, ErrUnknownType{Type: cfg.Type}
	}
}

// sharedHTTPClient is the package-wide http.Client. The
// OpenAI / Gemini providers reuse it so the per-request
// engine can allocate cheaply. The 60-second timeout is
// large enough for a 25 MiB upload (the SPA's hard
// limit) on a slow connection; smaller uploads finish
// in well under a second.
var sharedHTTPClient = &http.Client{Timeout: 60 * time.Second}

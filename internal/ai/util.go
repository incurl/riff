package ai

import (
	"encoding/base64"
	"strings"
)

// base64StdEncoding wraps encoding/base64 so the
// gemini.go multipart body builder does not have to
// import the package directly. We use the standard
// (not URL-safe) alphabet because that's what the
// Generative Language API expects.
func base64StdEncoding(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// guessMimeFromFilename is a tiny extension-to-MIME
// table for the audio types the SPA can hand to
// Transcribe. The full table lives in pb/connect/
// attachment.go (guessMime); the ai package cannot
// import that file (it's a pb-level package), so this
// table is the minimal subset the Gemini engine
// needs to fall back on when the SPA does not send
// contentType.
func guessMimeFromFilename(name string) string {
	idx := strings.LastIndex(name, ".")
	if idx < 0 {
		return ""
	}
	switch strings.ToLower(name[idx+1:]) {
	case "wav", "wave":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "m4a":
		return "audio/mp4"
	case "ogg", "oga":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "webm":
		return "audio/webm"
	case "mp4":
		return "audio/mp4"
	case "aac":
		return "audio/aac"
	default:
		return ""
	}
}

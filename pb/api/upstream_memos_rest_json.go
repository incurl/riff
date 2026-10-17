package api

import "encoding/json"

// jsonStdUnmarshal is a thin alias around
// encoding/json.Unmarshal that lets the upstream-memos
// REST files reference the stdlib without importing it
// directly. Keeping the import centralised here also
// makes it easy to swap to a different JSON library in
// a future slice (the Memos frontend uses stdlib, so
// stdlib is the right pick for now).
func jsonStdUnmarshal(body []byte, v any) error {
	return json.Unmarshal(body, v)
}

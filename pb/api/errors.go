// Package api contains the custom HTTP routes, error helpers, and
// request/response shapes for the riff PocketBase backend.
//
// Custom routes under /api/po33/* return errors in the Memos
// frontend-expected envelope:
//
//	{ "code": <int>, "message": <string>, "data": {} }
//
// We implement this by returning *router.ApiError with a custom
// "code" field added to the Data map. PocketBase's router renders
// the response with our Message and Status as-is.
package api

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// NewAPIError constructs a *router.ApiError that carries a
// Memos-shape envelope. The "code" field rides in the Data map
// because router.ApiError doesn't have a top-level Code field.
//
// The React client in Phase 4 will normalize the response shape;
// for now, the data map exposes code + message clearly.
func NewAPIError(status int, message string) *router.ApiError {
	return &router.ApiError{
		Status:  status,
		Message: message,
		Data: map[string]any{
			"code":    status,
			"message": message,
		},
	}
}

// writeJSON is a small wrapper for successful responses.
func writeJSON(e *core.RequestEvent, status int, body any) {
	e.JSON(status, body)
}

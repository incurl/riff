package api

import (
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
)

// ContentType is the JSON:API media type.
//
// The frontend's fetch calls do not set an Accept header, so the type is
// only asserted on the response; it is what the specification requires and
// what upstream Flarum sends.
const ContentType = "application/vnd.api+json"

// ErrorObject is one JSON:API error object.
//
// Upstream Flarum's formatter emits `status` and `code` always, and
// `detail` when debug mode is on. The detail is always populated here:
// the forum's errors are produced by its own code, so the message is
// never a stack trace and never leaks a secret.
type ErrorObject struct {
	// Status is the HTTP status code, as a string.
	Status string `json:"status"`
	// Code is the machine-readable error code.
	Code string `json:"code"`
	// Detail is the human-readable message.
	Detail string `json:"detail,omitempty"`
	// Source names the request parameter the error is about.
	Source map[string]any `json:"source,omitempty"`
}

// ErrorDocument is a JSON:API error response body.
type ErrorDocument struct {
	// Errors holds the error objects.
	Errors []ErrorObject `json:"errors"`
}

// Error codes used by the forum.
//
// They are stable strings so a client can branch on them.
const (
	// CodeValidationFailed is a request that failed validation.
	CodeValidationFailed = "validation_failed"
	// CodeNotAuthenticated is a request without a valid session.
	CodeNotAuthenticated = "not_authenticated"
	// CodePermissionDenied is a request the actor may not perform.
	CodePermissionDenied = "permission_denied"
	// CodeNotFound is a missing resource.
	CodeNotFound = "not_found"
	// CodeConflict is a request that conflicts with existing state.
	CodeConflict = "conflict"
	// CodeInternal is an unexpected server-side failure.
	CodeInternal = "internal_error"
)

// WriteError renders a JSON:API error document.
//
// It is exported so the module's own routes (the session routes, which
// live outside the API prefix) answer in the same shape as the API.
//
// The response body is the document itself; the status code is the one
// carried by the error object.
func WriteError(e *core.RequestEvent, status int, code, detail string) error {
	doc := ErrorDocument{Errors: []ErrorObject{{
		Status: http.StatusText(status),
		Code:   code,
		Detail: detail,
	}}}
	// The JSON:API specification wants the HTTP status in the `status`
	// member as a number-as-string; upstream Flarum renders the HTTP
	// status text, which the frontend does not read. Keep the numeric
	// status so a client can branch on it.
	doc.Errors[0].Status = statusName(status)
	e.Response.Header().Set("Content-Type", ContentType)
	e.Response.WriteHeader(status)
	return e.JSON(status, doc)
}

// statusName renders an HTTP status as the string JSON:API expects.
//
// The JSON:API specification wants the numeric status; upstream Flarum
// renders the HTTP status TEXT there, which no client reads.
func statusName(status int) string {
	return strconv.Itoa(status)
}

// writeValidationError renders a validation failure.
func writeValidationError(e *core.RequestEvent, detail string) error {
	return WriteError(e, http.StatusUnprocessableEntity, CodeValidationFailed, detail)
}

// writeNotFound renders a missing resource.
//
// A missing resource and a hidden one are indistinguishable on purpose: a
// 403 would confirm that the resource exists.
func writeNotFound(e *core.RequestEvent, detail string) error {
	return WriteError(e, http.StatusNotFound, CodeNotFound, detail)
}

// writeUnauthenticated renders a request without a valid session.
func writeUnauthenticated(e *core.RequestEvent, detail string) error {
	return WriteError(e, http.StatusUnauthorized, CodeNotAuthenticated, detail)
}

// writeForbidden renders a request the actor may not perform.
func writeForbidden(e *core.RequestEvent, detail string) error {
	return WriteError(e, http.StatusForbidden, CodePermissionDenied, detail)
}

// writeConflict renders a request that conflicts with existing state.
func writeConflict(e *core.RequestEvent, detail string) error {
	return WriteError(e, http.StatusConflict, CodeConflict, detail)
}

// writeInternal renders an unexpected failure.
//
// The detail is deliberately generic: the underlying error is logged, not
// returned, so an internal message never reaches a client.
func writeInternal(e *core.RequestEvent, detail string) error {
	return WriteError(e, http.StatusInternalServerError, CodeInternal, detail)
}

// apiError is a service-layer error carrying the HTTP status and the
// JSON:API code.
//
// Services return it instead of writing a response, so the handler layer
// stays the only place that decides how a failure is rendered.
type apiError struct {
	// status is the HTTP status code.
	status int
	// code is the JSON:API error code.
	code string
	// detail is the human-readable message.
	detail string
	// err is the underlying error, for logging.
	err error
}

// Error implements the error interface.
func (e *apiError) Error() string {
	if e.err != nil {
		return e.detail + ": " + e.err.Error()
	}
	return e.detail
}

// Unwrap exposes the underlying error.
func (e *apiError) Unwrap() error { return e.err }

// newAPIError builds an apiError.
func newAPIError(status int, code, detail string, err error) *apiError {
	return &apiError{status: status, code: code, detail: detail, err: err}
}

// fail renders a service error as a JSON:API error document.
//
// An error that is not an apiError is a bug in the service layer, so it
// is reported as an internal error rather than leaking its text.
func fail(e *core.RequestEvent, err error) error {
	apiErr, ok := err.(*apiError)
	if !ok {
		return writeInternal(e, "The request could not be completed.")
	}
	switch apiErr.status {
	case http.StatusUnprocessableEntity:
		return writeValidationError(e, apiErr.detail)
	case http.StatusUnauthorized:
		return writeUnauthenticated(e, apiErr.detail)
	case http.StatusForbidden:
		return writeForbidden(e, apiErr.detail)
	case http.StatusNotFound:
		return writeNotFound(e, apiErr.detail)
	case http.StatusConflict:
		return writeConflict(e, apiErr.detail)
	default:
		return WriteError(e, apiErr.status, apiErr.code, apiErr.detail)
	}
}

// errNotFound builds a not-found apiError.
func errNotFound(detail string) *apiError {
	return newAPIError(http.StatusNotFound, CodeNotFound, detail, nil)
}

// errForbidden builds a permission-denied apiError.
func errForbidden(detail string) *apiError {
	return newAPIError(http.StatusForbidden, CodePermissionDenied, detail, nil)
}

// errUnauthenticated builds an unauthenticated apiError.
func errUnauthenticated(detail string) *apiError {
	return newAPIError(http.StatusUnauthorized, CodeNotAuthenticated, detail, nil)
}

// errValidation builds a validation apiError.
func errValidation(detail string) *apiError {
	return newAPIError(http.StatusUnprocessableEntity, CodeValidationFailed, detail, nil)
}

// This file holds the body-conversion helpers for the
// legacy Memos REST surface. The V1 (Memos 0.27-0.31)
// client sends the Google API HTTP wire format — every
// write has the body unwrapped (`body: "memo"` in the
// proto) and every read has the resource name in the URL
// path, not the body. The V0 (Memos 0.21) dialect is no
// longer supported; the route table no longer mounts
// the singular `/memo` or `/resource` paths.
//
// All helpers are pure functions of `(body []byte, path
// vars, query) (newBody []byte, error)`. They never
// touch the database, the rule engine, or the connect
// handler; the connect handler is called after the
// convert helper has produced a body it can parse.
//
// Naming convention: a helper named `v1*` converts a
// Memos-0.27-0.31 body into the proto shape. A
// `passthrough*` helper does no transformation and is
// here for table symmetry (so every row of the route
// table can name a wrapper, even when the wrapper is a
// no-op).
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// timeUnix is a tiny alias for time.Unix so the
// import block reads naturally. time.Unix(sec, nsec)
// returns the local-time Time; callers that want UTC
// invoke .UTC() on the result.
func timeUnix(sec, nsec int64) time.Time {
	return time.Unix(sec, nsec)
}

// bodyWrapper is the signature every entry in
// v1BodyWrappers has. The helper receives the original
// HTTP request body, the path variables PocketBase has
// already extracted (in their order on the route pattern),
// and the parsed query string. It returns the body to
// forward to the connect handler (always valid JSON) and
// an error if the input is unparseable. A nil error with
// a non-nil body means the request is well-formed and
// ready to dispatch; a non-nil error short-circuits the
// proxy with the HTTP status the helper returns from
// wrapError.
type bodyWrapper func(body []byte, pathVars map[string]string, query url.Values) ([]byte, error)

// v1BodyWrappers is the registry the route table looks
// up by name. The Builder column in the v1 route table
// is the key. Keep this map small; every entry is a
// promise that the matching route is wired.
var v1BodyWrappers = map[string]bodyWrapper{
	// V1 helpers.
	"v1_get_memo":              wrapV1GetMemoBody,
	"v1_create_memo":           wrapV1CreateMemoBody,
	"v1_update_memo":           wrapV1UpdateMemoBody,
	"v1_delete_memo":           wrapV1DeleteMemoBody,
	"v1_list_memo_comments":    wrapV1PathInjectBody("name", "memos/{memo}"),
	"v1_create_memo_comment":   wrapV1CreateMemoCommentBody,
	"v1_list_memo_reactions":   wrapV1PathInjectBody("name", "memos/{memo}"),
	"v1_upsert_memo_reaction":  wrapV1PathInjectBody("name", "memos/{memo}"),
	"v1_delete_memo_reaction":  wrapV1PathInjectBody("name", "memos/{memo}/reactions/{reaction}"),
	"v1_list_memo_relations":   wrapV1PathInjectBody("name", "memos/{memo}"),
	"v1_set_memo_relations":    wrapV1SetMemoRelationsBody,
	"v1_list_memo_attachments": wrapV1PathInjectBody("name", "memos/{memo}"),
	"v1_set_memo_attachments":  wrapV1SetMemoAttachmentsBody,
	"v1_get_attachment":        wrapV1PathInjectBody("name", "attachments/{attachment}"),
	"v1_delete_attachment":     wrapV1PathInjectBody("name", "attachments/{attachment}"),
	"v1_get_user":              wrapV1PathInjectBody("name", "users/{user}"),
	"v1_get_user_setting":      wrapV1PathInjectBody("name", "users/{user}/settings/{setting}"),
	"v1_update_user_setting":   wrapV1PathInjectBody("name", "users/{user}/settings/{setting}"),
	"v1_create_attachment":     wrapV1CreateAttachmentBody,
	"v1_list_attachments":      passthroughBody,
	"v1_get_user_stats":        passthroughBody, // handled in the custom builder, not here.
}

// passthroughBody is a no-op wrapper. It returns the
// input body unchanged (or `{}` if the input is empty).
// Used for endpoints that already match the proto shape
// or for endpoints where the path/query transformation
// happens in the handler that owns the body unwrap.
func passthroughBody(body []byte, _ map[string]string, _ url.Values) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return []byte("{}"), nil
	}
	return body, nil
}

// ============================================================
// V1 helpers
// ============================================================

// wrapV1GetMemoBody converts the empty `GET
// /api/v1/memos/{memo}` body into
// `GetMemoRequest{name: "memos/{memo}"}`. The connect
// handler reads `req.Name` to identify the memo.
func wrapV1GetMemoBody(_ []byte, path map[string]string, _ url.Values) ([]byte, error) {
	id, ok := path["memo"]
	if !ok || id == "" {
		return nil, newWrapErr(400, "missing path variable {memo}")
	}
	return jsonMarshal(map[string]any{"name": "memos/" + id})
}

// wrapV1CreateMemoBody wraps a V1 body that is the inner
// `Memo` shape (because the proto has `body: "memo"`) into
// `CreateMemoRequest{memo: <body>}`. The V1 client sends
// the inner memo fields directly.
func wrapV1CreateMemoBody(body []byte, _ map[string]string, _ url.Values) ([]byte, error) {
	inner, err := decodeFlatObject(body)
	if err != nil {
		return nil, err
	}
	return jsonMarshal(map[string]any{"memo": inner})
}

// wrapV1UpdateMemoBody unwraps the V1 PATCH body (inner
// `Memo`), injects the path variable `{memo}` as
// `memo.name`, and reads `updateMask` from the query
// string. The result is `UpdateMemoRequest{memo:
// {name,...}, update_mask}`.
func wrapV1UpdateMemoBody(body []byte, path map[string]string, query url.Values) ([]byte, error) {
	inner, err := decodeFlatObject(body)
	if err != nil {
		return nil, err
	}
	id, ok := path["memo"]
	if !ok || id == "" {
		return nil, newWrapErr(400, "missing path variable {memo}")
	}
	inner["name"] = "memos/" + id
	mask := query.Get("updateMask")
	if mask == "" {
		// The V1 client always sets updateMask; if
		// it's missing we fall back to "all fields
		// in the body".
		mask = "*"
	}
	return jsonMarshal(map[string]any{
		"memo":        inner,
		"update_mask": mask,
	})
}

// wrapV1DeleteMemoBody produces a `DeleteMemoRequest{name:
// "memos/{memo}"}` from the path variable. The V1
// client sends an empty body for DELETE.
func wrapV1DeleteMemoBody(_ []byte, path map[string]string, _ url.Values) ([]byte, error) {
	id, ok := path["memo"]
	if !ok || id == "" {
		return nil, newWrapErr(400, "missing path variable {memo}")
	}
	return jsonMarshal(map[string]any{"name": "memos/" + id})
}

// wrapV1PathInjectBody is a factory for routes that have
// a single path variable that maps to a `name` field in
// the proto request, with no body unwrap. The pattern
// argument is the resource-name format string, e.g.
// "memos/{memo}" — it is fed the path variables and the
// result is the value of the `name` field.
func wrapV1PathInjectBody(nameField, pattern string) bodyWrapper {
	return func(_ []byte, path map[string]string, _ url.Values) ([]byte, error) {
		// Expand the pattern: replace {var} tokens
		// with the value from `path`. Unknown tokens
		// become empty.
		name := pattern
		for k, v := range path {
			name = strings.ReplaceAll(name, "{"+k+"}", v)
		}
		// Bail if any token is still present (we
		// couldn't fill it in).
		if strings.Contains(name, "{") {
			return nil, newWrapErr(400, fmt.Sprintf("missing path variable for pattern %q", pattern))
		}
		return jsonMarshal(map[string]any{nameField: name})
	}
}

// wrapV1CreateMemoCommentBody wraps the V1 body
// (`{content: string}`) into
// `CreateMemoCommentRequest{name: "memos/{memo}",
// comment: {content}}` because the proto has
// `body: "comment"`.
func wrapV1CreateMemoCommentBody(body []byte, path map[string]string, _ url.Values) ([]byte, error) {
	inner, err := decodeFlatObject(body)
	if err != nil {
		return nil, err
	}
	id, ok := path["memo"]
	if !ok || id == "" {
		return nil, newWrapErr(400, "missing path variable {memo}")
	}
	return jsonMarshal(map[string]any{
		"name":    "memos/" + id,
		"comment": inner,
	})
}

// wrapV1SetMemoRelationsBody injects the path variable
// `{memo}` as `name` and copies the body through
// unchanged (the proto has `body: "*"` so the body IS
// the full request minus the path-extracted `name`).
func wrapV1SetMemoRelationsBody(body []byte, path map[string]string, _ url.Values) ([]byte, error) {
	inner, err := decodeFlatObject(body)
	if err != nil {
		return nil, err
	}
	id, ok := path["memo"]
	if !ok || id == "" {
		return nil, newWrapErr(400, "missing path variable {memo}")
	}
	inner["name"] = "memos/" + id
	return jsonMarshal(inner)
}

// wrapV1SetMemoAttachmentsBody is the same shape as
// SetMemoRelationsBody: inject `{memo}` as `name`,
// forward the rest of the body unchanged.
func wrapV1SetMemoAttachmentsBody(body []byte, path map[string]string, _ url.Values) ([]byte, error) {
	inner, err := decodeFlatObject(body)
	if err != nil {
		return nil, err
	}
	id, ok := path["memo"]
	if !ok || id == "" {
		return nil, newWrapErr(400, "missing path variable {memo}")
	}
	inner["name"] = "memos/" + id
	return jsonMarshal(inner)
}

// wrapV1CreateAttachmentBody wraps a V1 body that is
// the inner `Attachment` shape (because the proto has
// `body: "attachment"`) into
// `CreateAttachmentRequest{attachment: <body>}`.
func wrapV1CreateAttachmentBody(body []byte, _ map[string]string, _ url.Values) ([]byte, error) {
	inner, err := decodeFlatObject(body)
	if err != nil {
		return nil, err
	}
	return jsonMarshal(map[string]any{"attachment": inner})
}

// ============================================================
// Internal helpers
// ============================================================

// decodeFlatObject unmarshals a JSON object body into
// a map. Empty bodies return an empty map. A non-object
// payload returns an error.
func decodeFlatObject(body []byte) (map[string]any, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, newWrapErr(400, "invalid JSON body: "+err.Error())
	}
	return out, nil
}

// jsonMarshal is a thin wrapper so the imports stay
// readable. Errors here are programming errors
// (the inputs are always JSON-safe), so we panic
// rather than return them.
func jsonMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Match the protojson style: no HTML escaping,
	// no trailing newline. The connect handler's
	// protojson.Unmarshal accepts both.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Drop the trailing newline Encode adds.
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// coerceIntToString accepts the V0 int shapes
// (json.Number, float64, int, string) and returns a
// decimal string. A non-integer returns an error.
func coerceIntToString(v any) (string, error) {
	switch x := v.(type) {
	case float64:
		if x != float64(int64(x)) {
			return "", fmt.Errorf("not an integer: %v", v)
		}
		return strconv.FormatInt(int64(x), 10), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case json.Number:
		return x.String(), nil
	case string:
		return x, nil
	default:
		return "", fmt.Errorf("not an integer: %T", v)
	}
}

// unixTimestampProto converts a Unix-seconds float into
// the RFC 3339 string the proto timestamp field expects.
// Memos 0.21 used a flat seconds-since-epoch int; the
// proto wants a `google.protobuf.Timestamp` which
// protojson renders as an RFC 3339 string.
func unixTimestampProto(secs float64) string {
	whole := int64(secs)
	frac := secs - float64(whole)
	nanos := int64(frac * 1e9)
	if nanos < 0 {
		nanos = 0
	}
	return unixSecondsToRFC3339(whole, nanos)
}

// rowStatusToState maps the Memos 0.21 rowStatus enum
// string to the proto state enum string.
func rowStatusToState(rs string) string {
	switch strings.ToUpper(rs) {
	case "ARCHIVED":
		return "ARCHIVED"
	default:
		return "NORMAL"
	}
}

// v0VisibilityToProto was removed with the V0 surface;
// the V1 client already uses the proto enum names.

// wrapErr is a sentinel error wrapper that the proxy
// handler recognises and converts into an HTTP error
// response. The status code is preserved.
type wrapErr struct {
	status  int
	message string
}

func (e *wrapErr) Error() string { return e.message }

// newWrapErr constructs a wrapErr with the given
// status and message. The proxy handler unwraps the
// type and emits an HTTP error of the same status.
func newWrapErr(status int, msg string) error {
	return &wrapErr{status: status, message: msg}
}

// unixSecondsToRFC3339 formats a Unix-seconds
// timestamp as an RFC 3339 string. Kept as its own
// helper so the math is testable independently of the
// V0/V1 client shape.
func unixSecondsToRFC3339(secs, nanos int64) string {
	// Use stdlib time; the helper is small enough
	// that the time import is justified by the
	// complexity of the date math.
	return timeUnix(secs, nanos).UTC().Format(time.RFC3339Nano)
}

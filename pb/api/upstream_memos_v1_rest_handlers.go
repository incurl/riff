// This file holds the request-time handlers for the
// legacy Memos REST surface at `/api/v1/...`. There
// are two kinds:
//
//   - `connectProxyWrapHandler` — a thin extension of
//     `connectProxyHandler` that runs the body through
//     a `bodyWrapper` (see upstream_memos_v1_convert.go)
//     before forwarding it to the connect mux. This is
//     the V1 wire-shape adapter: every route that needs
//     a body conversion uses this handler.
//
//   - Custom handlers — for the routes that do not fit
//     the proxy-wrap shape:
//
//   - v1StatusHandler / v1PingHandler — version-probe
//     endpoints. The MoeMemos client calls
//     GET /api/v1/status first to decide which generation
//     of client to load; the V1 surface returns an
//     empty `.profile.version` so the V1 client is
//     selected.
//
//   - v1UserStatsHandler — the AIP-style custom verb
//     `:getStats` in the path. The path
//     /api/v1/users/{user}:getStats is a non-standard
//     GET that maps to UserService.GetUserStats; we
//     strip the verb and dispatch.
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// connectProxyWrapHandler is the wrap-flavoured cousin
// of `connectProxyHandler`. It performs the same
// request-rebuild + dispatch + response-copy dance, but
// before dispatching it runs the body through the
// supplied `bodyWrapper`. The wrapper has full access
// to the path variables PocketBase has already
// extracted (in the URL query string) and the original
// query string, so it can implement both body
// reshaping and path-variable-to-body injection.
//
// `pathVarsFor` reads PocketBase's stored path
// variables from the request; the framework exposes
// them via `e.Request.PathValue(name)` (Go 1.22+).
func connectProxyWrapHandler(app core.App, procedure string, wrap bodyWrapper) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		// Build the path-variable map PocketBase's
		// router has already extracted. We collect
		// every named segment on the route by
		// inspecting the registered route pattern,
		// but a more pragmatic shortcut: the route
		// table has a fixed set of {var} tokens per
		// pattern, and the wrap helper is given a
		// pattern-style format string. We cheat:
		// the wrap helper receives the path params
		// as a generic map; the v1 route table only
		// uses {memo}, {user}, {attachment},
		// {reaction}, {id}, {relatedId}, {type},
		// {setting} — all of which Go's PathValue
		// can read.
		//
		// We call PathValue for the names we know
		// exist on this route. Names that aren't
		// on the route just return "" and are
		// ignored by the wrap helper.
		pathVars := map[string]string{}
		for _, name := range []string{
			"memo", "user", "attachment", "reaction",
			"id", "relatedId", "type", "setting",
		} {
			if v := e.Request.PathValue(name); v != "" {
				pathVars[name] = v
			}
		}

		// Read the original body once so the wrap
		// helper can inspect it.
		var bodyBytes []byte
		if e.Request.Body != nil {
			b, err := io.ReadAll(e.Request.Body)
			if err != nil {
				return crudError(e, http.StatusBadRequest, "could not read request body")
			}
			bodyBytes = b
		}

		// Run the wrap helper. Errors are
		// wrapErr sentinels; the proxy returns
		// their status code.
		newBody, err := wrap(bodyBytes, pathVars, e.Request.URL.Query())
		if err != nil {
			if we, ok := err.(*wrapErr); ok {
				return crudError(e, we.status, we.message)
			}
			return crudError(e, http.StatusInternalServerError, "wrap failed: "+err.Error())
		}

		// Re-encode: copy the original body, swap
		// it for the wrapped body, then dispatch
		// through the connect mux the same way
		// connectProxyHandler does.
		mux := connectProxyMux()
		if mux == nil {
			e.Response.Header().Set("Content-Type", "application/json")
			e.Response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(e.Response, `{"code":13,"message":"connect proxy mux not installed"}`)
			return nil
		}
		upstream, err := http.NewRequestWithContext(
			e.Request.Context(),
			http.MethodPost,
			procedure,
			bytes.NewReader(newBody),
		)
		if err != nil {
			return errors.Wrap(err, "build upstream request")
		}
		upstream.Header = e.Request.Header.Clone()
		upstream.URL.RawQuery = e.Request.URL.RawQuery
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Accept", "application/json")
		if e.Auth != nil {
			upstream = upstream.WithContext(contextWithViewer(upstream.Context(), e.Auth))
		}

		resp := newConnectProxyResponseWriter()
		mux.ServeHTTP(resp, upstream)

		for k, vv := range resp.headers {
			for _, v := range vv {
				e.Response.Header().Add(k, v)
			}
		}
		status := resp.status
		if status == 0 {
			status = http.StatusOK
		}
		e.Response.WriteHeader(status)
		_, err = io.Copy(e.Response, &resp.body)
		return err
	}
}

// ============================================================
// Version-probe handlers (GET /api/v1/status, /api/v1/ping)
// ============================================================

// v1StatusHandler returns the V1-shape SystemStatus
// body. The MoeMemos (iOS + Android) boot path calls
// `GET /api/v1/status` first and inspects
// `profile.version`: a non-empty value routes the
// client to the V0 (Memos 0.21) surface, an empty
// value routes it to the V1 (Memos 0.27-0.31)
// surface. The default is "0.21.0" so the V0
// client is selected — the V0 surface is fully
// implemented (`pb/api/upstream_memos_v0_rest.go`)
// and the iOS-app-tested V1 fallback path
// (`/api/v1/workspace/profile`) is NOT served
// (a V1 REST route for that path would have to be
// added before the V1 fallback would be viable; see
// DECISIONS.md "V0 dispatch helper strips
// `Accept-Encoding: gzip` ..." for the wire-format
// rationale). Operators who have completed the V1
// surface can opt in by setting RIFF_MEMOS_VERSION
// to "0.31.0" (or whatever the current minimum is)
// — see pb/api.SetAdvertisedVersion and main.go.
//
// The shape is:
//
//	{ profile: { mode: "prod" | "dev" | "demo", version: "<advertised>" } }
func v1StatusHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		e.Response.Header().Set("Content-Type", "application/json")
		return e.JSON(http.StatusOK, map[string]any{
			"profile": map[string]any{
				"mode":    "prod",
				"version": advertisedMemosVersion,
			},
		})
	}
}

// v1PingHandler returns a trivial "pong" body. Memos
// 0.21 clients sometimes ping the server before the
// login flow; the V1 client does the same. The shape
// is `{ data: { ping: "ok" } }` per the Memos OpenAPI
// spec.
func v1PingHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, map[string]any{
			"data": map[string]any{"ping": "ok"},
		})
	}
}

// ============================================================
// V1 custom-verb (AIP-style) handler
// ============================================================

// v1UserStatsHandler dispatches
// GET /api/v1/users/{user}:getStats to the connect
// handler for UserService.GetUserStats. The path uses
// the AIP custom-verb pattern (`{name}:verb`); the
// V1 client emits it that way. Go's ServeMux rejects
// `{var}:` patterns, so the route is registered as
// `/api/v1/users/{user}/{rest...}` and we dispatch
// on the `rest` segment. Only `getStats` is supported
// today; other verbs produce a 404.
//
// Auth: the V1 client always sends a token.
// v1GetUserOrStatsHandler serves `GET /api/v1/users/{user}` for
// both the plain read and the AIP custom verb the client sends.
//
// The MoeMemos V1 client builds `GET /api/v1/users/{user}:getStats`
// — a colon, **no slash**, and a bare id in `{user}` (its
// `MemosV1Service.getId()` strips the resource name down before
// filling `path.user`). Go's `ServeMux` refuses to register a
// `{var}:` pattern (`panic: bad wildcard segment`), and the
// `/api/v1/users/{user}/{rest...}` route cannot match it either
// because `{user}` is the more specific single-segment pattern —
// so the colon form always lands here, `{user}` intact, and this
// handler splits the verb off before doing anything else.
//
// Splitting it here (rather than in the `v1_get_user` body
// wrapper) is what makes the call return `UserStats` instead of a
// `User`: the two procedures are different RPCs, so the routing
// decision has to happen before the connect dispatch, not after
// the request body is shaped.
func v1GetUserOrStatsHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		segment := e.Request.PathValue("user")
		if segment == "" {
			return crudError(e, http.StatusBadRequest, "missing path variable {user}")
		}
		user, verb := segment, ""
		if i := strings.LastIndex(segment, ":"); i >= 0 {
			user, verb = segment[:i], segment[i+1:]
		}
		if user == "" {
			return crudError(e, http.StatusBadRequest, "missing path variable {user}")
		}
		switch verb {
		case "":
			return v1DispatchAndEcho(e, memosConnectUserGetUserProcedure,
				map[string]any{"name": "users/" + user})
		case "getStats":
			return v1DispatchAndEcho(e, memosConnectUserGetUserStatsProcedure,
				map[string]any{"name": "users/" + user})
		default:
			return crudError(e, http.StatusNotFound, "unsupported custom verb: "+verb)
		}
	}
}

// v1DispatchAndEcho dispatches a literal request body to a connect
// procedure and writes the response onto the request event as JSON.
//
// `v0DispatchConnect` does NOT write the success body (it only
// writes the *error* body when the status is >= 400), so every
// caller must emit the payload itself — see the same contract noted
// on `v0ShapeSingleMemoAttachments` callers in the V0 handlers.
func v1DispatchAndEcho(e *core.RequestEvent, procedure string, body map[string]any) error {
	raw, status, err := v0DispatchConnect(e, procedure, mustJSON(body), nil, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		// v0DispatchConnect already wrote the connect error body.
		return nil
	}
	return e.JSON(http.StatusOK, json.RawMessage(raw))
}

// mustJSON is `jsonMarshal` for a literal request body the handler
// itself constructs. A `map[string]any` of strings cannot fail to
// marshal, so the error is discarded rather than threaded: there is
// no branch that could act on it.
func mustJSON(v any) []byte {
	b, err := jsonMarshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// v1UserStatsHandler serves the **slash-shaped** custom-verb URL
// `GET /api/v1/users/{user}/{rest...}` (the form a hand-written
// client or curl would use). The colon-shaped form the actual
// MoeMemos client sends cannot reach this route — Go's ServeMux
// panics on `{var}:`, and the more specific `/api/v1/users/{user}`
// single-segment pattern always wins the match — so that path is
// served by `v1GetUserOrStatsHandler` above. Both exist so either
// spelling resolves.
func v1UserStatsHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		name := e.Request.PathValue("user")
		rest := e.Request.PathValue("rest")
		if name == "" {
			return crudError(e, http.StatusBadRequest, "missing path variable {user}")
		}
		// Accept both `/…/getStats` and `/…/:getStats`.
		verb := rest
		if i := strings.LastIndex(verb, ":"); i >= 0 {
			verb = verb[i+1:]
		}
		if verb != "getStats" {
			return crudError(e, http.StatusNotFound, "unsupported custom verb: "+verb)
		}
		// Build a synthetic upstream request that
		// targets the connect procedure. The
		// GetUserStats RPC takes a name string,
		// so the body is `{"name": "users/<user>"}` — the
		// segment is the bare username/id the client sent, and
		// `GetUserStats` strips the prefix back off before its
		// `lookupUserByName`.
		body := []byte(`{"name":"users/` + name + `"}`)
		mux := connectProxyMux()
		if mux == nil {
			return crudError(e, http.StatusServiceUnavailable, "connect proxy mux not installed")
		}
		upstream, err := http.NewRequestWithContext(
			e.Request.Context(),
			http.MethodPost,
			memosConnectUserGetUserStatsProcedure,
			bytes.NewReader(body),
		)
		if err != nil {
			return errors.Wrap(err, "build upstream request")
		}
		upstream.Header = e.Request.Header.Clone()
		upstream.URL.RawQuery = e.Request.URL.RawQuery
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("Accept", "application/json")
		if e.Auth != nil {
			upstream = upstream.WithContext(contextWithViewer(upstream.Context(), e.Auth))
		}

		resp := newConnectProxyResponseWriter()
		mux.ServeHTTP(resp, upstream)
		for k, vv := range resp.headers {
			for _, v := range vv {
				e.Response.Header().Add(k, v)
			}
		}
		status := resp.status
		if status == 0 {
			status = http.StatusOK
		}
		e.Response.WriteHeader(status)
		_, err = io.Copy(e.Response, &resp.body)
		return err
	}
}

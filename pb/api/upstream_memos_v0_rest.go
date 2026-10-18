// This file declares the legacy Memos REST surface for the
// V0 (Memos 0.21) dialect. MoeMemos (iOS + Android) is a
// V0+V1 hybrid: the same APK ships both `MemosV0Api.kt` and
// `MemosV1Api.kt`, and the version-probe response
// (`GET /api/v1/status`) does NOT switch clients — it only
// tells the V0 client whether to attempt the upgrade. The
// V0 paths (`/api/v1/user/me`, `/api/v1/memo`,
// `/api/v1/resource`, `/api/v1/tag`, `/api/v1/memo/all`,
// `POST /api/v1/memo`, `POST /api/v1/memo/{id}/organizer`,
// `PATCH /api/v1/memo/{id}`, `DELETE /api/v1/memo/{id}`,
// `POST /api/v1/resource/blob`,
// `DELETE /api/v1/resource/{id}`) MUST be served, or every
// MoeMemos boot path falls into the SPA catch-all and the
// kotlinx.serialization codec surfaces "data couldn't be
// read" to the user.
//
// The V0 wire format is materially different from the V1
// surface served by `upstream_memos_v1_rest.go`:
//
//   - V0 memos use numeric `Long` IDs (`MemosV0Memo.id`)
//     rather than V1 resource names (`memos/abc`).
//   - V0 list responses are top-level arrays, not wrapped
//     in a `ListXResponse` envelope.
//   - V0 `MemosV0User` is flat (no resource `name` field;
//     the V0 `name` is the *display name*).
//   - V0 PATCH memo bodies echo the numeric `id` in the
//     body as well as the URL path.
//   - V0 uploads are `multipart/form-data` with a single
//     `file` part, not the V1 chunked `UploadAttachment`
//     procedure.
//
// Every V0 path goes through a `custom` handler that calls
// the connect mux (V1 procedures are the source of truth
// for business rules) and shapes the V1 response into the
// V0 wire format. The V0 request body is also shaped into
// the V1 wire format on the way in. This is the same
// `request-shape + dispatch + response-shape` pattern the
// V1 wrap flavor uses; the only difference is that the V0
// handlers do the shaping in the custom builder because
// both directions need a shaper, not just one.
//
// Order matters in this table: more specific patterns
// come before more general ones. PocketBase's
// `http.ServeMux` picks the first match.
package api

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/incurl/riff/internal/ratelimit"
)

// upstreamMemosV0RouteEntry mirrors upstreamMemosV1RouteEntry.
// Kept as a distinct type so the V0 and V1 route tables
// can evolve independently and the V0 code stays
// self-contained.
type upstreamMemosV0RouteEntry struct {
	Method string
	Path   string
	// Builder is the key into the custom-builder registry.
	// V0 routes always go through the `custom` flavor
	// because every V0 path needs a response shaper, not
	// just a request wrap.
	Builder string
	// The rate-limit scope the route-level middleware
	// applies. Empty inherits the package defaults.
	Scope ratelimit.Scope
}

// upstreamMemosV0Routes returns the V0 (Memos 0.21) REST
// route table. These paths complement the V1 surface in
// `upstream_memos_v1_rest.go`; the two tables together
// cover the full MoeMemos boot path.
func upstreamMemosV0Routes() []upstreamMemosV0RouteEntry {
	return []upstreamMemosV0RouteEntry{
		// ============================================================
		// V0 user (numeric Long id, flat shape)
		// ============================================================
		// `GET /api/v1/user/me` is the V0 equivalent of
		// V1's `GET /api/v1/auth/me`. The V0 client
		// treats it as the identity bootstrap: the
		// returned `id: Long` is the owner identity for
		// every other V0 call (list filters, resource
		// creatorId, etc.).
		{Method: http.MethodGet, Path: "/api/v1/user/me", Builder: "v0_user_me", Scope: ratelimit.ScopeAuthenticated},

		// ============================================================
		// V0 memo list / read / write
		// ============================================================
		// `GET /api/v1/memo` returns a top-level array
		// of MemosV0Memo (no envelope). The V1 client
		// uses `GET /api/v1/memos` (plural) with the
		// `ListMemosResponse{memos: [...]}` envelope;
		// both paths are distinct.
		{Method: http.MethodGet, Path: "/api/v1/memo", Builder: "v0_memo_list", Scope: ratelimit.ScopeAuthenticated},
		// `GET /api/v1/memo/all` is the admin /
		// directory view. Filters are query-string
		// (`pinned`, `tag`, `visibility`, `limit`,
		// `offset`) per the V0 OpenAPI.
		{Method: http.MethodGet, Path: "/api/v1/memo/all", Builder: "v0_memo_all_list", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/api/v1/memo", Builder: "v0_memo_create", Scope: ratelimit.ScopeWriteUser},
		// `POST /api/v1/memo/{id}/organizer` toggles
		// the `pinned` flag on a V0 memo. The V1
		// client uses `PATCH /api/v1/memos/{memo}`
		// with `update_mask = "pinned"` instead; the
		// V0 shape is its own verb.
		{Method: http.MethodPost, Path: "/api/v1/memo/{id}/organizer", Builder: "v0_memo_organizer", Scope: ratelimit.ScopeWriteUser},
		// `PATCH /api/v1/memo/{id}` updates a memo.
		// The V0 body shape echoes the `id` in the
		// body as well as the URL; the V0 handler
		// strips the redundant `id` and forwards the
		// inner fields as an `UpdateMemoRequest`.
		{Method: http.MethodPatch, Path: "/api/v1/memo/{id}", Builder: "v0_memo_patch", Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/api/v1/memo/{id}", Builder: "v0_memo_delete", Scope: ratelimit.ScopeWriteUser},

		// ============================================================
		// V0 resource (a.k.a. attachment) list / upload / delete
		// ============================================================
		// `GET /api/v1/resource` returns a top-level
		// array of MemosV0Resource (no envelope).
		{Method: http.MethodGet, Path: "/api/v1/resource", Builder: "v0_resource_list", Scope: ratelimit.ScopeAuthenticated},
		// `POST /api/v1/resource/blob` is a
		// `multipart/form-data` upload with a single
		// `file` part. The V1 client uses the chunked
		// `UploadAttachment` procedure; the V0 client
		// uses a single-shot multipart upload. The V0
		// handler reads the multipart part, persists
		// it via the same `patches` / `samples`
		// `CreateRule` the V1 path uses, and returns
		// the V0 `MemosV0Resource` shape.
		{Method: http.MethodPost, Path: "/api/v1/resource/blob", Builder: "v0_resource_upload", Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/api/v1/resource/{id}", Builder: "v0_resource_delete", Scope: ratelimit.ScopeWriteUser},

		// ============================================================
		// V0 tag list
		// ============================================================
		// `GET /api/v1/tag?creatorId=<long>` returns a
		// top-level array of strings (tag names) the
		// caller has used. Used by the V0 client's
		// tag-picker UI.
		{Method: http.MethodGet, Path: "/api/v1/tag", Builder: "v0_tag_list", Scope: ratelimit.ScopeAuthenticated},
	}
}

// upstreamMemosV0HandlerFactory picks the right custom
// handler for one row of upstreamMemosV0Routes. Every V0
// path is `custom`; the Builder key is the handler name.
func upstreamMemosV0HandlerFactory(app core.App, entry upstreamMemosV0RouteEntry) func(*core.RequestEvent) error {
	switch entry.Builder {
	case "v0_user_me":
		return v0UserMeHandler(app)
	case "v0_memo_list":
		return v0MemoListHandler(app)
	case "v0_memo_all_list":
		return v0MemoAllListHandler(app)
	case "v0_memo_create":
		return v0MemoCreateHandler(app)
	case "v0_memo_organizer":
		return v0MemoOrganizerHandler(app)
	case "v0_memo_patch":
		return v0MemoPatchHandler(app)
	case "v0_memo_delete":
		return v0MemoDeleteHandler(app)
	case "v0_resource_list":
		return v0ResourceListHandler(app)
	case "v0_resource_upload":
		return v0ResourceUploadHandler(app)
	case "v0_resource_delete":
		return v0ResourceDeleteHandler(app)
	case "v0_tag_list":
		return v0TagListHandler(app)
	default:
		return unknownFlavorHandler("v0:" + entry.Builder)
	}
}

// mountUpstreamMemosV0Routes registers every row of
// upstreamMemosV0Routes on the given router. The routes
// share the path space with the V1 table; a more
// specific V0 row is registered first in the same group
// to win against a V1 catch-all, but in practice the V0
// and V1 tables do not overlap (the V0 `/memo` is
// singular, the V1 `/memos` is plural). A nil limiter
// makes the rate-limit BindFunc a no-op.
func mountUpstreamMemosV0Routes(r *router.Router[*core.RequestEvent], app core.App, limiter ratelimit.Limiter) {
	for _, entry := range upstreamMemosV0Routes() {
		handler := upstreamMemosV0HandlerFactory(app, entry)
		route := matchMethod(r, entry.Method)(entry.Path, handler)
		if limiter != nil && entry.Scope != "" {
			route.BindFunc(restRateLimit(limiter, entry.Scope))
		}
	}
}

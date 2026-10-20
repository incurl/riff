// This file declares the legacy Memos REST surface that
// current MoeMemos (iOS + Android) clients expect to find
// at the upstream `/api/v1/...` prefix (no `/memos/api/...`).
//
// The surface is the V1 (Memos 0.27-0.31) OpenAPI definition
// emitted by `mudkipme/MoeMemos` — resource-name identity
// (`users/alice`, `memos/abc`, `attachments/xyz`), wrapped
// request bodies (`CreateMemoRequest{memo: Memo{...}}`),
// and the Google API HTTP wire format (path variables +
// `body: "memo"`-style unwraps). The V0 (Memos 0.21)
// dialect is not supported; the route table no longer
// mounts the singular `/memo`, `/resource`, or `/user/me`
// paths and the version-probe handler always returns
// the V1 shape.
//
// The connect handler is the single source of truth for
// every business rule; routes that need a small
// body-conversion reach it via the `connect_proxy_wrap`
// flavor, which applies a bodyWrapper
// (see upstream_memos_v1_convert.go) before forwarding.
// Routes that are already proto-shaped go through the
// existing `connect_proxy` flavor unchanged.
//
// The full per-endpoint table follows.
package api

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/incurl/riff/internal/ratelimit"
)

// upstreamMemosV1RouteEntry mirrors upstreamRouteEntry. The
// two types are kept distinct (rather than re-using the
// existing one) because the v1 surface has its own
// `Builder` semantics: `Builder` for a v1 entry is a key
// into `v1BodyWrappers` (body-conversion map) for the
// `connect_proxy_wrap` flavor, or a key into the custom
// builder registry for the `custom` flavor. Keeping the
// types separate makes the v1 code self-contained and
// avoids a cross-dependency between the two route tables.
type upstreamMemosV1RouteEntry struct {
	Method string
	Path   string
	Flavor string

	// For connect_proxy and connect_proxy_wrap this is the
	// Connect procedure path. For custom it is the builder
	// name. For pb_records_crud it is the PocketBase
	// collection name.
	Target string

	// For connect_proxy_wrap this is the key into
	// v1BodyWrappers (the body-conversion map).
	// For custom this is the builder key into the
	// customHandlers map. Empty for the other flavors.
	Builder string

	// The rate-limit scope the route-level middleware
	// applies. Empty inherits the package defaults.
	Scope ratelimit.Scope
}

// upstreamMemosV1Routes returns the legacy-Memos REST route
// table. The current client (MoeMemos 0.27-0.31) uses the
// V1 surface exclusively: resource-name identity
// (`/memos/{memo}`, `/attachments/{attachment}`, `/users/{user}`)
// and the Google API HTTP wire format. The V0 (Memos 0.21)
// dialect is not mounted; the singular `/memo`, `/resource`,
// `/user/me`, and `/tag` paths return 404 from the V1
// router.
//
// Order matters: the most specific patterns must come
// before the more general ones (e.g. `GET /memos/{memo}/reactions/{reaction}`
// before `GET /memos/{memo}/reactions`). PocketBase's
// `http.ServeMux` picks the first match.
func upstreamMemosV1Routes() []upstreamMemosV1RouteEntry {
	return []upstreamMemosV1RouteEntry{
		// ============================================================
		// Status / health probes
		// ============================================================
		{Method: http.MethodGet, Path: "/api/v1/status", Flavor: "custom", Builder: "v1_status", Scope: ratelimit.ScopeAnonymous},
		{Method: http.MethodGet, Path: "/api/v1/ping", Flavor: "custom", Builder: "v1_ping", Scope: ratelimit.ScopeAnonymous},

		// ============================================================
		// V1 instance / auth
		// ============================================================
		{Method: http.MethodGet, Path: "/api/v1/instance/profile", Flavor: "connect_proxy", Target: memosConnectInstanceGetInstanceProfileProcedure, Scope: ratelimit.ScopeAnonymous},
		// `POST /api/v1/auth/signin` is the V1 client
		// sign-in entry point. Without it, MoeMemos
		// falls through to the SPA catch-all and the
		// Connect codec surfaces the HTML body as
		// `data couldn't be read`. Mirrors the modern
		// SPA row at /memos/api/auth/signin.
		{Method: http.MethodPost, Path: "/api/v1/auth/signin", Flavor: "connect_proxy", Target: memosConnectAuthSignInProcedure, Scope: ratelimit.ScopeSignInIP},
		{Method: http.MethodGet, Path: "/api/v1/auth/me", Flavor: "connect_proxy", Target: memosConnectAuthGetCurrentUserProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/api/v1/auth/refresh", Flavor: "connect_proxy", Target: memosConnectAuthRefreshProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/api/v1/auth/signout", Flavor: "connect_proxy", Target: memosConnectAuthSignOutProcedure, Scope: ratelimit.ScopeAuthenticated},

		// ============================================================
		// V1 users (resource-name identity)
		// ============================================================
		{Method: http.MethodGet, Path: "/api/v1/users/{user}", Flavor: "connect_proxy_wrap", Builder: "v1_get_user", Target: memosConnectUserGetUserProcedure, Scope: ratelimit.ScopeAuthenticated},
		// AIP-style custom verb. The V1 client uses
		// `/api/v1/users/{user}:getStats`; Go's
		// ServeMux rejects `{var}` followed by `:`
		// in a pattern, so we register a free-form
		// `{rest...}` segment and let the custom
		// handler dispatch on the trailing verb.
		{Method: http.MethodGet, Path: "/api/v1/users/{user}/{rest...}", Flavor: "custom", Builder: "v1_user_stats", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/api/v1/users/{user}/settings/{setting}", Flavor: "connect_proxy_wrap", Builder: "v1_get_user_setting", Target: memosConnectUserGetUserSettingProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/api/v1/users/{user}/settings/{setting}", Flavor: "connect_proxy_wrap", Builder: "v1_update_user_setting", Target: memosConnectUserUpdateUserSettingProcedure, Scope: ratelimit.ScopeWriteUser},

		// ============================================================
		// V1 memos (proto-shaped already; just forward)
		// ============================================================
		{Method: http.MethodGet, Path: "/api/v1/memos", Flavor: "connect_proxy", Target: memosConnectMemoListMemosProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/api/v1/memos", Flavor: "connect_proxy_wrap", Builder: "v1_create_memo", Target: memosConnectMemoCreateMemoProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodGet, Path: "/api/v1/memos/{memo}", Flavor: "connect_proxy_wrap", Builder: "v1_get_memo", Target: memosConnectMemoGetMemoProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/api/v1/memos/{memo}", Flavor: "connect_proxy_wrap", Builder: "v1_update_memo", Target: memosConnectMemoUpdateMemoProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/api/v1/memos/{memo}", Flavor: "connect_proxy_wrap", Builder: "v1_delete_memo", Target: memosConnectMemoDeleteMemoProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- V1 memo comments ----
		{Method: http.MethodGet, Path: "/api/v1/memos/{memo}/comments", Flavor: "connect_proxy_wrap", Builder: "v1_list_memo_comments", Target: memosConnectMemoListMemoCommentsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/api/v1/memos/{memo}/comments", Flavor: "connect_proxy_wrap", Builder: "v1_create_memo_comment", Target: memosConnectMemoCreateMemoCommentProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- V1 memo reactions ----
		{Method: http.MethodGet, Path: "/api/v1/memos/{memo}/reactions", Flavor: "connect_proxy_wrap", Builder: "v1_list_memo_reactions", Target: memosConnectMemoListMemoReactionsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/api/v1/memos/{memo}/reactions", Flavor: "connect_proxy_wrap", Builder: "v1_upsert_memo_reaction", Target: memosConnectMemoUpsertMemoReactionProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/api/v1/memos/{memo}/reactions/{reaction}", Flavor: "connect_proxy_wrap", Builder: "v1_delete_memo_reaction", Target: memosConnectMemoDeleteMemoReactionProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- V1 memo relations ----
		{Method: http.MethodGet, Path: "/api/v1/memos/{memo}/relations", Flavor: "connect_proxy_wrap", Builder: "v1_list_memo_relations", Target: memosConnectMemoListMemoRelationsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/api/v1/memos/{memo}/relations", Flavor: "connect_proxy_wrap", Builder: "v1_set_memo_relations", Target: memosConnectMemoSetMemoRelationsProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- V1 memo attachments ----
		{Method: http.MethodGet, Path: "/api/v1/memos/{memo}/attachments", Flavor: "connect_proxy_wrap", Builder: "v1_list_memo_attachments", Target: memosConnectMemoListMemoAttachmentsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/api/v1/memos/{memo}/attachments", Flavor: "connect_proxy_wrap", Builder: "v1_set_memo_attachments", Target: memosConnectMemoSetMemoAttachmentsProcedure, Scope: ratelimit.ScopeWriteUser},

		// ============================================================
		// V1 attachments (resource-name identity)
		// ============================================================
		{Method: http.MethodGet, Path: "/api/v1/attachments", Flavor: "connect_proxy", Target: memosConnectAttachmentListAttachmentsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/api/v1/attachments", Flavor: "connect_proxy_wrap", Builder: "v1_create_attachment", Target: memosConnectAttachmentCreateAttachmentProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodGet, Path: "/api/v1/attachments/{attachment}", Flavor: "connect_proxy_wrap", Builder: "v1_get_attachment", Target: memosConnectAttachmentGetAttachmentProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/api/v1/attachments/{attachment}", Flavor: "connect_proxy_wrap", Builder: "v1_update_attachment", Target: memosConnectAttachmentUpdateAttachmentProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/api/v1/attachments/{attachment}", Flavor: "connect_proxy_wrap", Builder: "v1_delete_attachment", Target: memosConnectAttachmentDeleteAttachmentProcedure, Scope: ratelimit.ScopeWriteUser},
	}
}

// upstreamMemosV1HandlerFactory picks the right handler for
// one row of upstreamMemosV1Routes. The v1 surface uses
// four flavors: `connect_proxy` (plain proxy to the connect
// handler), `connect_proxy_wrap` (proxy after a body
// conversion), `pb_records_crud` (delegate to the in-process
// PocketBase record layer), and `custom` (hand-written
// handler).
func upstreamMemosV1HandlerFactory(app core.App, entry upstreamMemosV1RouteEntry) func(*core.RequestEvent) error {
	switch entry.Flavor {
	case "connect_proxy":
		return connectProxyHandler(app, entry.Target)
	case "connect_proxy_wrap":
		wrapper, ok := v1BodyWrappers[entry.Builder]
		if !ok {
			return unknownFlavorHandler("connect_proxy_wrap:" + entry.Builder)
		}
		return connectProxyWrapHandler(app, entry.Target, wrapper)
	case "pb_records_crud":
		return pbRecordsCRUDHandler(app, entry.Target)
	case "custom":
		return customBuilder(app, entry.Builder)
	default:
		return unknownFlavorHandler(entry.Flavor)
	}
}

// mountUpstreamMemosV1Routes registers every row of
// upstreamMemosV1Routes on the given router. Routes are
// registered in table order (the static /api/v1/... entries
// before their wildcard variants) so the PocketBase
// ServeMux dispatches the right pattern. A nil limiter
// makes the rate-limit BindFunc a no-op.
func mountUpstreamMemosV1Routes(r *router.Router[*core.RequestEvent], app core.App, limiter ratelimit.Limiter) {
	for _, entry := range upstreamMemosV1Routes() {
		handler := upstreamMemosV1HandlerFactory(app, entry)
		route := matchMethod(r, entry.Method)(entry.Path, handler)
		if limiter != nil && entry.Scope != "" {
			route.BindFunc(restRateLimit(limiter, entry.Scope))
		}
	}
}

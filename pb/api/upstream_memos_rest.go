package api

import (
	"net/http"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/incurl/riff/internal/ratelimit"
)

// upstreamRouteEntry is one row of the upstream-Memos REST
// surface that this package re-implements at /memos/api/....
//
// The table is the source of truth for both the route
// registration (upstreamMemosRoutes returns it, Register
// mounts it) and the OpenAPI fragment that pb/api/openapi.go
// emits. The proto-side `option (google.api.http)` annotations
// are kept at /api/v1/... for the MCP tool catalog and are
// deliberately separate — see DECISIONS.md 2026-10-17 for the
// supersession of the 2026-10-16 v1-prefix reservation.
//
// Flavor names:
//
//	connect_proxy   — the route forwards the request body to
//	                  a Connect procedure in pb/connect and
//	                  transcribes the response. Zero new
//	                  business logic; the connect handler
//	                  owns the rule engine. The procedure
//	                  path is "/memos.api.v1.<service>/<method>"
//	                  and the response is the JSON of the
//	                  reply message.
//	pb_records_crud — the route maps to a single PocketBase
//	                  collection. List/Get/Create/Update/Delete
//	                  hit the same paths PB's own
//	                  /api/collections/{name}/records serve.
//	                  The handler is collection-shaped and
//	                  reuses the same rule-engine check the
//	                  upstream Memos handlers did.
//	custom         — a hand-written handler that does not
//	                  fit either shape (auth, link metadata,
//	                  memos export/import, etc.). The
//	                  buildCustomHandler constructor maps a
//	                  flavor name to a registered builder.
//	alias          — a pure path-only rewrite: the
//	                  destination is another riff route that
//	                  already exists. The handler does an
//	                  http.ServeMux.ServeHTTP-style rewrite.
//	file_server    — the route serves a file blob. The
//	                  handler resolves the path parameter
//	                  to a record and writes the file bytes
//	                  from internal/storage. The 3
//	                  /file/... upstream routes all use
//	                  this flavor.
//	sse            — the route is the long-lived
//	                  Server-Sent Events stream. One route:
//	                  GET /memos/api/sse.
//
// Path parameters follow the PocketBase router syntax
// ({name} — not the {name=foo/*} proto syntax). The 2026-10-17
// design keeps the upstream route *shape* (a /api/{name=memos/*}
// upstream path becomes /memos/api/memos/{name} here) and the
// handler parses the wildcard into the proto resource-name
// before dispatching to the connect handler.
type upstreamRouteEntry struct {
	Method string
	Path   string
	Flavor string

	// For connect_proxy and pb_records_crud this is the Connect
	// procedure path or PocketBase collection name. For
	// custom/file_server/sse it is empty.
	Target string

	// For custom-flavor routes, the builder key into the
	// customHandlers map. Empty for the other flavors.
	Builder string

	// The PB rate-limit scope the route-level middleware
	// applies. Empty means the route inherits the
	// package-level defaults in the registration loop.
	Scope ratelimit.Scope
}

// upstreamMemosRoutes is the ordered list of every (method, path)
// that this package serves at /memos/api/....
//
// Ordering matters: PocketBase's ServeMux matches the first
// registered pattern. The static segments (e.g. /memos/api/memos)
// come before the wildcard segments (e.g. /memos/api/memos/{name})
// so the static path wins. The Register function appends the
// /memos/api prefix at registration time — the paths below are
// the values relative to the mount.
func upstreamMemosRoutes() []upstreamRouteEntry {
	return []upstreamRouteEntry{
		// ---- Auth (connect proxy; signin/signout/refresh live in
		// pb/connect/auth.go, signup is UserService.CreateUser in
		// pb/connect/user.go) ----
		{Method: http.MethodPost, Path: "/memos/api/auth/signin", Flavor: "connect_proxy", Target: memosConnectAuthSignInProcedure, Scope: ratelimit.ScopeSignInIP},
		// signup -> UserService.CreateUser. The auth/signup body
		// has {username,password,role}; the connect handler
		// recognises the unauthenticated bootstrap and the
		// self-service path from the request context.
		{Method: http.MethodPost, Path: "/memos/api/auth/signup", Flavor: "connect_proxy", Target: memosConnectUserCreateUserProcedure, Scope: ratelimit.ScopeSignupIP},
		{Method: http.MethodPost, Path: "/memos/api/auth/refresh", Flavor: "connect_proxy", Target: memosConnectAuthRefreshProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/auth/signout", Flavor: "connect_proxy", Target: memosConnectAuthSignOutProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/auth/me", Flavor: "connect_proxy", Target: memosConnectAuthGetCurrentUserProcedure, Scope: ratelimit.ScopeAuthenticated},

		// ---- Users (most are pb_records_crud, batchGet is connect_proxy) ----
		{Method: http.MethodGet, Path: "/memos/api/users", Flavor: "pb_records_crud", Target: "users", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/users/{name}", Flavor: "pb_records_crud", Target: "users", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/users", Flavor: "pb_records_crud", Target: "users", Scope: ratelimit.ScopeSignupIP},
		{Method: http.MethodPatch, Path: "/memos/api/users/{name}", Flavor: "pb_records_crud", Target: "users", Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/users/{name}", Flavor: "pb_records_crud", Target: "users", Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodPost, Path: "/memos/api/users/batchGet", Flavor: "connect_proxy", Target: memosConnectUserBatchGetUsersProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/users/{name}/getStats", Flavor: "connect_proxy_wrap", Target: memosConnectUserGetUserStatsProcedure, Scope: ratelimit.ScopeAuthenticated},
		// /users/{name}/usage: upstream Memos exposes a usage
		// report; riff has not modelled the same data. Return
		// 501 until a future slice adds the RPC. The custom
		// builder key resolves to a not-implemented handler.
		{Method: http.MethodGet, Path: "/memos/api/users/{name}/usage", Flavor: "custom", Builder: "not_implemented", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/users/{name}/exportMemos", Flavor: "connect_proxy_wrap", Target: memosConnectUserExportMemosProcedure, Scope: ratelimit.ScopeArchiveUser},
		{Method: http.MethodPost, Path: "/memos/api/users/{name}/importMemos", Flavor: "connect_proxy_wrap", Target: memosConnectUserImportMemosProcedure, Scope: ratelimit.ScopeArchiveUser},

		// ---- User settings (sub-collection) ----
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/settings", Flavor: "pb_records_crud", Target: "user_settings", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/settings/{name}", Flavor: "pb_records_crud", Target: "user_settings", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/memos/api/users/{parent}/settings/{name}", Flavor: "pb_records_crud", Target: "user_settings", Scope: ratelimit.ScopeWriteUser},

		// ---- User views ----
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/views", Flavor: "pb_records_crud", Target: "memo_views", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/views/{name}", Flavor: "pb_records_crud", Target: "memo_views", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/users/{parent}/views", Flavor: "pb_records_crud", Target: "memo_views", Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodPatch, Path: "/memos/api/users/{parent}/views/{name}", Flavor: "pb_records_crud", Target: "memo_views", Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/users/{parent}/views/{name}", Flavor: "pb_records_crud", Target: "memo_views", Scope: ratelimit.ScopeWriteUser},

		// ---- User notifications ----
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/notifications", Flavor: "connect_proxy_wrap", Target: memosConnectUserListUserNotificationsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/memos/api/users/{parent}/notifications/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectUserUpdateUserNotificationProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/users/{parent}/notifications/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectUserDeleteUserNotificationProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- User webhooks ----
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/webhooks", Flavor: "connect_proxy_wrap", Target: memosConnectUserListUserWebhooksProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/users/{parent}/webhooks", Flavor: "connect_proxy_wrap", Target: memosConnectUserCreateUserWebhookProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodPatch, Path: "/memos/api/users/{parent}/webhooks/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectUserUpdateUserWebhookProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/users/{parent}/webhooks/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectUserDeleteUserWebhookProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/webhooks/{name}/getSigningSecret", Flavor: "connect_proxy_wrap", Target: memosConnectUserGetUserWebhookSigningSecretProcedure, Scope: ratelimit.ScopeAuthenticated},

		// ---- User linked identities ----
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/linkedIdentities", Flavor: "connect_proxy_wrap", Target: memosConnectUserListLinkedIdentitiesProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/linkedIdentities/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectUserGetLinkedIdentityProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/users/{parent}/linkedIdentities", Flavor: "connect_proxy_wrap", Target: memosConnectUserCreateLinkedIdentityProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/users/{parent}/linkedIdentities/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectUserDeleteLinkedIdentityProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- User personal access tokens ----
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/personalAccessTokens", Flavor: "connect_proxy_wrap", Target: memosConnectUserListPersonalAccessTokensProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/users/{parent}/personalAccessTokens", Flavor: "connect_proxy_wrap", Target: memosConnectUserCreatePersonalAccessTokenProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/users/{parent}/personalAccessTokens/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectUserDeletePersonalAccessTokenProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- Memos (most flow through the MemoService Connect handler) ----
		{Method: http.MethodGet, Path: "/memos/api/memos", Flavor: "connect_proxy", Target: memosConnectMemoListMemosProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/memos/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectMemoGetMemoProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/memos", Flavor: "connect_proxy", Target: memosConnectMemoCreateMemoProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodPatch, Path: "/memos/api/memos/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectMemoUpdateMemoProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/memos/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectMemoDeleteMemoProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- Memo attachments / relations / comments / reactions / shares ----
		{Method: http.MethodGet, Path: "/memos/api/memos/{parent}/attachments", Flavor: "connect_proxy_wrap", Target: memosConnectMemoListMemoAttachmentsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/memos/api/memos/{name}/attachments", Flavor: "connect_proxy_wrap", Target: memosConnectMemoSetMemoAttachmentsProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodGet, Path: "/memos/api/memos/{parent}/relations", Flavor: "connect_proxy_wrap", Target: memosConnectMemoListMemoRelationsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/memos/api/memos/{name}/relations", Flavor: "connect_proxy_wrap", Target: memosConnectMemoSetMemoRelationsProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodGet, Path: "/memos/api/memos/{parent}/comments", Flavor: "connect_proxy_wrap", Target: memosConnectMemoListMemoCommentsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/memos/{parent}/comments", Flavor: "connect_proxy_wrap", Target: memosConnectMemoCreateMemoCommentProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodGet, Path: "/memos/api/memos/{parent}/reactions", Flavor: "connect_proxy_wrap", Target: memosConnectMemoListMemoReactionsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/memos/{parent}/reactions", Flavor: "connect_proxy_wrap", Target: memosConnectMemoUpsertMemoReactionProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/memos/{name}/reactions/{reaction_name}", Flavor: "connect_proxy_wrap", Target: memosConnectMemoDeleteMemoReactionProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodGet, Path: "/memos/api/memos/{parent}/shares", Flavor: "connect_proxy_wrap", Target: memosConnectMemoListMemoSharesProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/memos/{parent}/shares", Flavor: "connect_proxy_wrap", Target: memosConnectMemoCreateMemoShareProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/memos/{name}/shares/{share_name}", Flavor: "connect_proxy_wrap", Target: memosConnectMemoDeleteMemoShareProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodGet, Path: "/memos/api/shares/{code}/memo", Flavor: "connect_proxy_wrap", Target: memosConnectMemoGetSharedMemoProcedure, Scope: ratelimit.ScopeAnonymous},
		{Method: http.MethodGet, Path: "/memos/api/memos/-/linkMetadata", Flavor: "connect_proxy", Target: memosConnectMemoGetLinkMetadataProcedure, Scope: ratelimit.ScopeLinkMetadata},
		{Method: http.MethodPost, Path: "/memos/api/memos/-/linkMetadata/batchGet", Flavor: "connect_proxy", Target: memosConnectMemoBatchGetLinkMetadataProcedure, Scope: ratelimit.ScopeLinkMetadata},

		// ---- Attachments (unified `attachments` collection) ----
		{Method: http.MethodGet, Path: "/memos/api/attachments", Flavor: "pb_records_crud", Target: "attachments", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/attachments/{name}", Flavor: "pb_records_crud", Target: "attachments", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/attachments", Flavor: "connect_proxy", Target: memosConnectAttachmentCreateAttachmentProcedure, Scope: ratelimit.ScopeUploadUser},
		{Method: http.MethodPatch, Path: "/memos/api/attachments/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectAttachmentUpdateAttachmentProcedure, Scope: ratelimit.ScopeUploadUser},
		{Method: http.MethodDelete, Path: "/memos/api/attachments/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectAttachmentDeleteAttachmentProcedure, Scope: ratelimit.ScopeUploadUser},
		{Method: http.MethodPost, Path: "/memos/api/attachments/upload", Flavor: "connect_proxy", Target: memosConnectAttachmentUploadAttachmentProcedure, Scope: ratelimit.ScopeUploadUser},
		{Method: http.MethodPost, Path: "/memos/api/attachments/batchDelete", Flavor: "connect_proxy", Target: memosConnectAttachmentBatchDeleteAttachmentsProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- Spaces ----
		{Method: http.MethodGet, Path: "/memos/api/spaces", Flavor: "pb_records_crud", Target: "spaces", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/spaces/{name}", Flavor: "pb_records_crud", Target: "spaces", Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/spaces", Flavor: "connect_proxy", Target: memosConnectSpaceCreateSpaceProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodPatch, Path: "/memos/api/spaces/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceUpdateSpaceProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/spaces/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceDeleteSpaceProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- Space members ----
		{Method: http.MethodGet, Path: "/memos/api/spaces/{parent}/members", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceListSpaceMembersProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/spaces/{name}/members/{member_name}", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceGetSpaceMemberProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/memos/api/spaces/{name}/members/{member_name}", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceUpdateSpaceMemberProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/spaces/{name}/members/{member_name}", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceDeleteSpaceMemberProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- Space invitations ----
		{Method: http.MethodGet, Path: "/memos/api/spaces/{parent}/invitations", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceListSpaceInvitationsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/users/{parent}/spaceInvitations", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceListUserSpaceInvitationsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/spaces/{name}/invitations/{invitation_name}", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceGetSpaceInvitationProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/spaces/{parent}/invitations", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceCreateSpaceInvitationProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/spaces/{name}/invitations/{invitation_name}", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceDeleteSpaceInvitationProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodPost, Path: "/memos/api/spaces/{name}/invitations/{invitation_name}/accept", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceAcceptSpaceInvitationProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodPost, Path: "/memos/api/spaces/{name}/invitations/{invitation_name}/decline", Flavor: "connect_proxy_wrap", Target: memosConnectSpaceDeclineSpaceInvitationProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- Identity providers (admin only) ----
		{Method: http.MethodGet, Path: "/memos/api/identity-providers", Flavor: "connect_proxy", Target: memosConnectIdentityProviderListIdentityProvidersProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/identity-providers/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectIdentityProviderGetIdentityProviderProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/identity-providers", Flavor: "connect_proxy", Target: memosConnectIdentityProviderCreateIdentityProviderProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodPatch, Path: "/memos/api/identity-providers/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectIdentityProviderUpdateIdentityProviderProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodDelete, Path: "/memos/api/identity-providers/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectIdentityProviderDeleteIdentityProviderProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- Instance ----
		{Method: http.MethodGet, Path: "/memos/api/instance/profile", Flavor: "connect_proxy", Target: memosConnectInstanceGetInstanceProfileProcedure, Scope: ratelimit.ScopeAnonymous},
		{Method: http.MethodGet, Path: "/memos/api/instance/stats", Flavor: "connect_proxy", Target: memosConnectInstanceGetInstanceStatsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodGet, Path: "/memos/api/instance/settings/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectInstanceGetInstanceSettingProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPost, Path: "/memos/api/instance/settings/batchGet", Flavor: "connect_proxy", Target: memosConnectInstanceBatchGetInstanceSettingsProcedure, Scope: ratelimit.ScopeAuthenticated},
		{Method: http.MethodPatch, Path: "/memos/api/instance/settings/{name}", Flavor: "connect_proxy_wrap", Target: memosConnectInstanceUpdateInstanceSettingProcedure, Scope: ratelimit.ScopeWriteUser},
		{Method: http.MethodPost, Path: "/memos/api/instance/settings/notification/testEmail", Flavor: "connect_proxy", Target: memosConnectInstanceTestInstanceEmailSettingProcedure, Scope: ratelimit.ScopeWriteUser},

		// ---- AI ----
		{Method: http.MethodPost, Path: "/memos/api/ai/transcribe", Flavor: "connect_proxy", Target: memosConnectAITranscribeProcedure, Scope: ratelimit.ScopeTranscribeUser},

		// ---- File server (3 routes) ----
		{Method: http.MethodGet, Path: "/memos/api/file/attachments/{uid}", Flavor: "file_server", Builder: "attachment"},
		{Method: http.MethodGet, Path: "/memos/api/file/attachments/{uid}/{filename}", Flavor: "file_server", Builder: "attachment"},
		{Method: http.MethodGet, Path: "/memos/api/file/users/{identifier}/avatar", Flavor: "file_server", Builder: "user_avatar"},

		// ---- SSE (1 route) ----
		{Method: http.MethodGet, Path: "/memos/api/sse", Flavor: "sse", Scope: ratelimit.ScopeAuthenticated},
	}
}

// mountUpstreamMemosRoutes registers every row in upstreamMemosRoutes on the
// given router. The actual handler selection lives in
// upstreamMemosHandlerFactory. Routes are registered in table order
// (the static /memos/api/... entries before their wildcard
// variants) so the PocketBase ServeMux dispatches the right
// pattern. A nil limiter makes the rate-limit BindFunc a no-op,
// matching the connect-side behavior in pb/connect/ratelimit.go.
func mountUpstreamMemosRoutes(r *router.Router[*core.RequestEvent], app core.App, limiter ratelimit.Limiter) {
	for _, entry := range upstreamMemosRoutes() {
		handler := upstreamMemosHandlerFactory(app, entry)
		route := matchMethod(r, entry.Method)(entry.Path, handler)
		if limiter != nil && entry.Scope != "" {
			route.BindFunc(restRateLimit(limiter, entry.Scope))
		}
		if entry.Scope == ratelimit.ScopeAuthenticated {
			// A real auth gate.
			//
			// The rate-limit scope is a BUDGET, not a gate:
			// `restRateLimit` only decides whether the caller has
			// spent their allowance, and an anonymous caller has an
			// allowance too. The rule engine is the authority on
			// WHO may read a row, but a route whose collection is
			// public (the `users` profile list) still needs the
			// caller to be identified before the handler can
			// decide what to return.
			//
			// `apis.RequireAuth` is PocketBase's own middleware;
			// `e.Auth` is already populated because PB binds its
			// `loadAuthToken` middleware globally on the router
			// (apis/base.go).
			route.Bind(apis.RequireAuth())
		}
	}
}

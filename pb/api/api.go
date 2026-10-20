// Package api registers custom /api/po33/* REST routes and
// helpers for the riff PocketBase backend.
package api

import (
	"fmt"
	"strconv"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/ratelimit"
)

// packageLimiter is the rate limiter the REST
// routes in this file consult for per-route
// policy. Set once at OnServe time by
// Register; nil when the operator has not
// configured RIFF_RATE_LIMIT. The Connect
// service registration in pb/connect/connect.go
// owns its own per-procedure rate-limit table;
// the REST routes share this package-level
// limiter and apply per-route scopes.
var packageLimiter ratelimit.Limiter

// Register binds all riff custom routes on the given app. Call
// from main.go via app.OnServe().BindFunc.
//
// limiter is the rate limiter to consult for the
// per-route policy. A nil limiter keeps the
// previous unbounded behaviour; the rate-limit
// middleware is attached only when the limiter
// is non-nil. The Connect service registration
// in pb/connect/connect.go owns its own
// per-procedure rate-limit table; the REST
// routes in this file share the same limiter
// (when one is configured) and apply a
// coarse-grained IP throttle. Per-user
// throttling on the REST surface is a
// future-slice hardening.
func Register(app core.App, limiter ratelimit.Limiter) {
	// Capture the limiter at the package level
	// so handlers (which receive only the
	// RequestEvent) can consult it. The
	// Connect service in pb/connect/connect.go
	// does the same with its own procedureScope
	// table; the REST side does not have a
	// equivalent table, so a single package
	// variable is the simplest path.
	packageLimiter = limiter
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// Auth: PocketBase's built-in middleware handles
		// /api/* auth. Our routes live under /api/po33/* and
		// we require an authenticated user.

		e.Router.GET("/api/po33/feed", feedHandler)
		e.Router.GET("/api/po33/spaces/{id}/memos", spaceMemosHandler)
		e.Router.POST("/api/po33/spaces/{id}/invite", spaceInviteHandler)
		e.Router.POST("/api/po33/memos/{id}/move", memoMoveHandler)

		// PO-33 device pairing. The /pair/start,
		// /pair/{code}/qrcode, and /pair/{id} routes
		// are authenticated; the /pair/finish route
		// is intentionally not — the device has no
		// auth token until after it pairs, and the
		// Ed25519 signature over the canonical
		// request is the bearer credential.
		e.Router.POST("/api/po33/pair/start", pairStartHandler)
		e.Router.GET("/api/po33/pair/{code}/qrcode", pairQRCoreHandler)
		e.Router.POST("/api/po33/pair/finish", pairFinishHandler)
		e.Router.DELETE("/api/po33/pair/{id}", pairRevokeHandler)

		// PO-33 device sketch upload. The route is
		// not authenticated; the Ed25519 signature
		// in the multipart body is the bearer
		// credential, exactly the pattern the
		// /pair/finish route uses. The handler
		// creates a memo (state='DRAFT', with
		// the device metadata on the `sketch`
		// JSON column) plus the unified
		// `attachments` rows for the .p33 patch
		// blob, the per-slot .wav samples, and
		// the optional .bkp tarball. The 1840xxx
		// `sketch_versions` audit log that used
		// to be written here was dropped on
		// 2026-10-19 (see DECISIONS.md, "sketch
		// version log dropped"). See
		// po33_device_upload.go for the full
		// contract.
		e.Router.POST("/api/po33/sketches/upload", uploadSketchHandler)

		// PO-33 device-pull sketch push. The
		// flow is device-pulls (see
		// docs/design/po33-device-binding.md,
		// 2026-10-19 appendix): the SPA-facing
		// POST writes a `pending_pull`
		// envelope onto the device row with a
		// 15-minute signed URL; the device
		// polls GET /sketches/pending, fetches
		// /sketches/{memoId}/download with the
		// signed URL, and acks via
		// /sketches/pending/ack. The first two
		// routes are owner-facing (auth:
		// PocketBase session or memos_pat_*
		// token); the last three are
		// device-facing (auth: the device's
		// `X-Device-Fingerprint` header).
		e.Router.POST("/api/po33/devices/{deviceId}/push", pushSketchHandler)
		e.Router.GET("/api/po33/devices/{deviceId}/pushes/{pushId}", pushStatusHandler)
		e.Router.GET("/api/po33/sketches/pending", pendingPullHandler)
		e.Router.POST("/api/po33/sketches/pending/ack", ackPendingPullHandler)
		e.Router.GET("/api/po33/sketches/{memoId}/download", deviceSketchDownloadHandler)

		// The public file surface at /file/... . These are
		// the URLs the SPA, the markdown renderer, and the
		// share links emit (see web/src/utils/attachment.ts).
		// Before this was registered they fell through to the
		// SPA's catch-all route and answered 200 with
		// index.html — a broken image rather than a 404. The
		// handler enforces the attachment's memo visibility
		// and supports ?thumbnail=true, ?motion=true, and
		// ?share_token=…; see file_routes.go and file_authz.go.
		mountFileRoutes(e.Router, app)

		// Upstream-Memos REST surface at /memos/api/...
		// 97 routes (84 proto-annotated + 3 file server
		// + 1 SSE). The connect_proxy flavor dispatches
		// through the same connect.Server the public
		// /memos.api.v1.* routes use, so the two
		// surfaces produce byte-identical responses
		// without duplicating any business logic. The
		// pb_records_crud flavor rewrites the path to
		// PocketBase's native collection REST endpoints
		// (/api/collections/{name}/records) so the
		// built-in record handlers do the work. The
		// file_server flavor reads the record's file
		// blob and streams it. The sse flavor
		// subscribes to the realtime broker.
		mountUpstreamMemosRoutes(e.Router, app, limiter)

		// Legacy Memos REST surface at /api/v1/...
		// (the path prefix current MoeMemos clients
		// expect; no /memos/api/... prefix). The route
		// table is the V1 (Memos 0.27-0.31) OpenAPI
		// definition only; the V0 (Memos 0.21) dialect
		// is no longer supported. The V1 routes are
		// proto-shaped already and go through the
		// connect_proxy flavor. The custom flavor
		// covers the version-probe endpoints
		// (`v1_status`, `v1_ping`) and the V1
		// AIP-style custom-verb dispatcher
		// (`v1_user_stats`).
		mountUpstreamMemosV1Routes(e.Router, app, limiter)

		// Legacy Memos REST surface at /api/v1/...:
		// the V0 (Memos 0.21) dialect that the
		// MoeMemos client (a V0+V1 hybrid) still
		// uses for its primary memo + resource
		// CRUD. The V0 path is registered AFTER
		// the V1 table so V1 specific paths win
		// any overlap (in practice they don't
		// overlap — the V0 `/memo` is singular,
		// the V1 `/memos` is plural). The V0
		// handlers go through the `custom` flavor
		// and shape the V1 connect response into
		// the V0 wire format (numeric Long ids,
		// top-level array lists, `rowStatus` /
		// `visibility` strings). See
		// upstream_memos_v0_rest.go for the
		// table and upstream_memos_v0_convert.go
		// for the shape helpers.
		mountUpstreamMemosV0Routes(e.Router, app, limiter)

		_ = limiter // see the comment on Register

		return e.Next()
	})
}

// paginationFromQuery extracts page+perPage from the request with
// sensible defaults. Returns (page, perPage, err).
func paginationFromQuery(e *core.RequestEvent, defaultPerPage, maxPerPage int) (int, int, error) {
	page := 1
	perPage := defaultPerPage
	if v := e.Request.URL.Query().Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, 0, fmt.Errorf("invalid page parameter")
		}
		page = n
	}
	if v := e.Request.URL.Query().Get("per_page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, 0, fmt.Errorf("invalid per_page parameter")
		}
		if n > maxPerPage {
			n = maxPerPage
		}
		perPage = n
	}
	return page, perPage, nil
}

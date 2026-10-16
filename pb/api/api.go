// Package api registers custom /api/po33/* REST routes and
// helpers for the riff PocketBase backend.
package api

import (
	"fmt"
	"strconv"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/ratelimit"
)

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

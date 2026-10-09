// Package api registers custom /api/po33/* REST routes and
// helpers for the riff PocketBase backend.
package api

import (
	"fmt"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
)

// Register binds all riff custom routes on the given app. Call
// from main.go via app.OnServe().BindFunc.
func Register(app core.App) {
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// Auth: PocketBase's built-in middleware handles
		// /api/* auth. Our routes live under /api/po33/* and
		// we require an authenticated user.

		e.Router.GET("/api/po33/feed", feedHandler)
		e.Router.GET("/api/po33/spaces/{id}/memos", spaceMemosHandler)
		e.Router.POST("/api/po33/spaces/{id}/invite", spaceInviteHandler)
		e.Router.POST("/api/po33/memos/{id}/move", memoMoveHandler)

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

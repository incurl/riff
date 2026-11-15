package api

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Mount registers the JSON:API routes on a router.
//
// Every pattern carries an explicit method. Go's http.ServeMux (which
// this router wraps) panics when two overlapping patterns are registered
// without one being strictly more specific, and the riff SPA catch-all
// is a method-less "/{path...}". A method-less "/forum/api/{path...}"
// would be a sibling of that pattern, not a child, and the process would
// not start.
func (s *Server) Mount(r *router.Router[*core.RequestEvent]) {
	api := s.apiPath

	// --- the forum document -------------------------------------------
	// The frontend's boot sequence fetches this to learn the API URL,
	// the base path and the caller.
	r.GET(api, s.showForumHandler)
	r.GET(api+"/", s.showForumHandler)

	// --- discussions ---------------------------------------------------
	r.GET(api+"/discussions", s.listDiscussionsHandler)
	r.POST(api+"/discussions", s.createDiscussionHandler)
	r.GET(api+"/discussions/{id}", s.showDiscussionHandler)
	r.PATCH(api+"/discussions/{id}", s.updateDiscussionHandler)
	r.DELETE(api+"/discussions/{id}", s.deleteDiscussionHandler)

	// --- posts ---------------------------------------------------------
	r.GET(api+"/posts", s.listPostsHandler)
	r.POST(api+"/posts", s.createPostHandler)
	r.GET(api+"/posts/{id}", s.showPostHandler)
	r.PATCH(api+"/posts/{id}", s.updatePostHandler)
	r.DELETE(api+"/posts/{id}", s.deletePostHandler)

	// --- users ---------------------------------------------------------
	r.GET(api+"/users", s.listUsersHandler)
	r.GET(api+"/users/{id}", s.showUserHandler)
	r.PATCH(api+"/users/{id}", s.updateUserHandler)

	// --- groups --------------------------------------------------------
	r.GET(api+"/groups", s.listGroupsHandler)
	r.GET(api+"/groups/{id}", s.showGroupHandler)

	// --- notifications -------------------------------------------------
	r.GET(api+"/notifications", s.listNotificationsHandler)
	r.POST(api+"/notifications/read", s.readAllNotificationsHandler)
	r.PATCH(api+"/notifications/{id}", s.readNotificationHandler)

	// --- settings ------------------------------------------------------
	r.GET(api+"/settings", s.listSettingsHandler)
	r.POST(api+"/settings", s.saveSettingsHandler)
}

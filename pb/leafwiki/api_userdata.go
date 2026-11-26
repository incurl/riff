package leafwiki

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// The per-user metadata endpoints the wiki SPA calls on every boot.
//
// **AN UNROUTED PATH IS NOT A 404 — IT IS THE SPA's `index.html` WITH A
// 200.** The SPA catch-all is a method-less `GET {prefix}/{path...}` and
// the least specific route in the mux, so a request for a path the wiki
// does not serve is answered with the shell. The SPA's own request
// helper then parses that markup as JSON, fails, and returns `null` —
// and the caller dereferences it:
//
//	let t = await Ng();          // null, not an object
//	t.dateFormat ?? `locale`     // TypeError: reading 'dateFormat' of null
//
// That is a console error on EVERY page load, not a degraded feature.
// The two endpoints below are therefore served even though the port
// keeps no LeafWiki database: the preferences and favourites belong to
// an ACCOUNT, and one small JSON file beside the tree holds them.

// favoritePage is one entry of the favourites list.
//
// The SPA maps the list into a set of ids (`new Set(t.map(e=>e.id))`),
// so `id` is the load-bearing field; `title`, `path` and `kind` travel
// because a client that renders the list itself needs them and they cost
// nothing here.
type favoritePage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
}

// favoritesResponse is the payload of GET /api/favorites.
//
// It is an OBJECT with a `pages` key, not a bare array: the SPA reads
// `(await C('/api/favorites')).pages`.
type favoritesResponse struct {
	Pages []favoritePage `json:"pages"`
}

// userSettingsHandler serves GET and PUT /api/user-settings.
//
// A GET for an anonymous caller answers the DEFAULTS rather than a 401:
// the SPA loads the preferences on every mount, signed in or not, and a
// 401 would make it log the reader out before they did anything.
//
// A PUT requires an account — there is nobody to save the preferences
// for otherwise.
func (s *Server) userSettingsHandler(e *core.RequestEvent) error {
	if e.Request.Method == http.MethodPut {
		return s.saveUserSettingsHandler(e)
	}

	actor := s.currentUser(e)
	if actor == nil {
		return e.JSON(http.StatusOK, defaultUserSettings())
	}

	return e.JSON(http.StatusOK, s.userdata.Settings(actor.Id))
}

// saveUserSettingsHandler stores the caller's preferences.
func (s *Server) saveUserSettingsHandler(e *core.RequestEvent) error {
	actor := s.currentUser(e)
	if actor == nil {
		return e.UnauthorizedError("authentication is required to save settings", nil)
	}
	if !verifyCSRF(e) {
		return e.ForbiddenError("missing or invalid CSRF token", nil)
	}

	body := userSettings{}
	if err := decodeOptionalJSON(e, &body); err != nil {
		return e.BadRequestError("the settings could not be read", nil)
	}

	saved, err := s.userdata.SaveSettings(actor.Id, body)
	if err != nil {
		return s.fail(e, err)
	}

	return e.JSON(http.StatusOK, saved)
}

// favoritesHandler serves GET /api/favorites.
//
// An anonymous caller gets an EMPTY list rather than a 401, for the same
// reason the settings GET does: the SPA loads it on every mount.
func (s *Server) favoritesHandler(e *core.RequestEvent) error {
	actor := s.currentUser(e)
	if actor == nil {
		return e.JSON(http.StatusOK, favoritesResponse{Pages: []favoritePage{}})
	}

	ids := s.userdata.Favorites(actor.Id)
	pages := make([]favoritePage, 0, len(ids))

	for _, id := range ids {
		node, err := s.tree.Node(id)
		if err != nil || node == nil {
			// A favourite whose page was deleted is simply absent: the
			// SPA filters the list against the tree anyway, so a stale
			// id would render nothing.
			continue
		}
		pages = append(pages, favoritePage{
			ID:    node.ID,
			Title: node.Title,
			Path:  node.Path,
			Kind:  string(node.Kind),
		})
	}

	return e.JSON(http.StatusOK, favoritesResponse{Pages: pages})
}

// addFavoriteHandler serves PUT /api/pages/{id}/favorite.
func (s *Server) addFavoriteHandler(e *core.RequestEvent) error {
	actor, err := s.requireWriter(e)
	if err != nil {
		return err
	}
	if actor.ID == systemUserID {
		// Public-write mode has no account to keep favourites for.
		return e.JSON(http.StatusOK, favoritesResponse{Pages: []favoritePage{}})
	}

	pageID := e.Request.PathValue("id")
	if _, err := s.tree.Node(pageID); err != nil {
		return e.NotFoundError("page not found", nil)
	}

	if err := s.userdata.AddFavorite(actor.ID, pageID); err != nil {
		return s.fail(e, err)
	}

	return e.NoContent(http.StatusNoContent)
}

// deleteFavoriteHandler serves DELETE /api/pages/{id}/favorite.
func (s *Server) deleteFavoriteHandler(e *core.RequestEvent) error {
	actor, err := s.requireWriter(e)
	if err != nil {
		return err
	}
	if actor.ID == systemUserID {
		return e.NoContent(http.StatusNoContent)
	}

	if err := s.userdata.RemoveFavorite(actor.ID, e.Request.PathValue("id")); err != nil {
		return s.fail(e, err)
	}

	return e.NoContent(http.StatusNoContent)
}

// decodeOptionalJSON decodes a request body, tolerating an empty one.
//
// A zero-length body decodes to the zero value rather than an error:
// the settings PUT is the only caller, and the merge in
// `SaveSettings` is what fills the gaps.
func decodeOptionalJSON(e *core.RequestEvent, target any) error {
	payload, err := io.ReadAll(e.Request.Body)
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}

	return json.Unmarshal(payload, target)
}

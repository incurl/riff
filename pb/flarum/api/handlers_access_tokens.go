package api

import (
	"github.com/pocketbase/pocketbase/core"
)

// listAccessTokensHandler serves GET /api/access-tokens.
//
// The frontend's user security page lists the caller's access tokens.
// The route has to exist AND has to answer with a JSON:API ARRAY: the
// page fetches the session tokens and the developer tokens separately
// and joins the two answers with
// `Promise.all([...]).then(([sessions, developerTokens]) => …)`.
//
// An answer that is not an iterable fails the destructuring with "e is
// not iterable" — which is what happened while the path was unrouted
// and fell through to a route that answered with a single document.
//
// Upstream lists every access token of the account. This port cannot:
// a PocketBase auth token is a STATELESS JWT, so there is no stored
// token row to enumerate. What the caller can see is the session it is
// currently using, and that is what the list holds. The developer
// tokens are empty for the same reason — the forum does not mint
// developer tokens.
//
// The list is therefore a single session token for a signed-in caller
// and an empty list for an anonymous one. The upstream endpoint is
// authenticated, so an anonymous caller is refused.
func (s *Server) listAccessTokensHandler(e *core.RequestEvent) error {
	actor, err := s.requireAuth(e)
	if err != nil {
		return fail(e, err)
	}

	tokens := make([]Resource, 0, 1)

	// The caller's own session. It is a session token (not a developer
	// token) and it is the current one.
	tokens = append(tokens, Resource{
		Type: "access-tokens",
		ID:   actor.User.ID,
		Attributes: map[string]any{
			"token":          "",
			"userId":         actor.User.ID,
			"title":          "",
			"type":           "session",
			"createdAt":      nil,
			"lastActivityAt": nil,
			"lastIpAddress":  "",
			"device":         "",
			"isCurrent":      true,
			"isSessionToken": true,
		},
	})

	return s.writeDocument(e, Document{Data: tokens})
}

// notFoundHandler answers every API path that no route claims.
//
// The answer is a JSON:API 404, not the forum document and not the
// SPA's index.html: a caller that asked for a resource gets a
// resource-shaped error, and the frontend's request helper throws on
// the non-2xx status instead of parsing a page of markup as data.
func (*Server) notFoundHandler(e *core.RequestEvent) error {
	return fail(e, notFoundError("That resource does not exist."))
}

package leafwiki

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// loginRequest is the POST /api/auth/login body.
type loginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

// loginHandler authenticates a PocketBase user (or superuser) and stores the
// session in cookies.
//
// The identifier may be a username or an email. The response shape is
// LeafWiki's; the token itself is a PocketBase auth token, so the rest of the
// platform (PATs, the admin UI) keeps working on the same identity.
func (s *Server) loginHandler(e *core.RequestEvent) error {
	var request loginRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}

	record, err := s.findUserByIdentifier(request.Identifier)
	if err != nil {
		return e.UnauthorizedError("invalid credentials", nil)
	}
	if !record.ValidatePassword(request.Password) {
		return e.UnauthorizedError("invalid credentials", nil)
	}

	expiresAt, cookieErr := setSessionCookies(e, record)
	if cookieErr != nil {
		return e.InternalServerError(cookieErr.Error(), nil)
	}

	return e.JSON(http.StatusOK, authResponse{
		AccessTokenExpiresAt: expiresAt,
		Message:              "login successful",
		User:                 userFromRecord(record),
	})
}

// logoutHandler clears the session cookies.
func (*Server) logoutHandler(e *core.RequestEvent) error {
	clearSessionCookies(e)
	return e.JSON(http.StatusOK, map[string]string{"message": "logged out"})
}

// refreshTokenHandler re-mints the session cookie for an authenticated caller.
func (s *Server) refreshTokenHandler(e *core.RequestEvent) error {
	record := s.currentUser(e)
	if record == nil {
		return e.UnauthorizedError("no active session", nil)
	}

	expiresAt, err := setSessionCookies(e, record)
	if err != nil {
		return e.InternalServerError(err.Error(), nil)
	}

	return e.JSON(http.StatusOK, authResponse{
		AccessTokenExpiresAt: expiresAt,
		Message:              "session refreshed",
		User:                 userFromRecord(record),
	})
}

// meHandler reports the current user, or null for an anonymous caller.
//
// It answers 200 either way: the SPA treats a 4xx as "the server is broken",
// not as "logged out".
func (s *Server) meHandler(e *core.RequestEvent) error {
	record := s.currentUser(e)
	if record == nil {
		return e.JSON(http.StatusOK, nil)
	}
	return e.JSON(http.StatusOK, userFromRecord(record))
}

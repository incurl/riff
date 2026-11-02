package leafwiki

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

const (
	// accessCookieName carries the PocketBase auth token for browser
	// sessions. The SPA never reads it (it is HttpOnly); it only needs the
	// cookie to be sent with each request.
	accessCookieName = "leafwiki_access_token"

	// csrfCookieName is the double-submit CSRF token. It is deliberately
	// readable by JavaScript, because the SPA echoes it in X-CSRF-Token.
	csrfCookieName = "leafwiki_csrf"

	// csrfHeaderName is the header the SPA sends the CSRF token in.
	csrfHeaderName = "X-CSRF-Token"
)

// authUser is the user projection LeafWiki's SPA expects. The role names are
// LeafWiki's; they map onto PocketBase as:
//
//	admin  — a PocketBase superuser (_superusers)
//	editor — any authenticated record of the `users` collection
//	viewer — an anonymous reader
type authUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	TotpEnabled bool   `json:"totpEnabled"`
}

// authResponse is the login / refresh payload.
type authResponse struct {
	AccessTokenExpiresAt int64    `json:"accessTokenExpiresAt"`
	Message              string   `json:"message"`
	User                 authUser `json:"user"`
}

// userFromRecord projects a PocketBase auth record onto the SPA's user shape.
func userFromRecord(record *core.Record) authUser {
	role := "editor"
	if record.Collection().Name == core.CollectionNameSuperusers {
		role = "admin"
	}
	username := strings.TrimSpace(record.GetString("username"))
	if username == "" {
		username = record.Email()
	}
	return authUser{
		ID:       record.Id,
		Username: username,
		Email:    record.Email(),
		Role:     role,
	}
}

// findUserByIdentifier resolves a login identifier (username or email) to an
// auth record. Superusers are searched last: a regular user with the same
// email must win, otherwise a wiki editor could never sign in.
func (s *Server) findUserByIdentifier(identifier string) (*core.Record, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, errors.Wrap(ErrNotFound, "empty identifier")
	}

	users, err := s.app.FindCollectionByNameOrId("users")
	if err == nil {
		if record, emailErr := s.app.FindAuthRecordByEmail(users, identifier); emailErr == nil {
			return record, nil
		}
		byUsername := map[string]any{"identifier": identifier}
		if record, filterErr := s.app.FindFirstRecordByFilter("users", "username = {:identifier}", byUsername); filterErr == nil {
			return record, nil
		}
	}

	superusers, err := s.app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if err == nil {
		if record, emailErr := s.app.FindAuthRecordByEmail(superusers, identifier); emailErr == nil {
			return record, nil
		}
	}

	return nil, errors.Wrap(ErrNotFound, "user "+identifier)
}

// currentUser resolves the caller from a bearer token (the PocketBase auth
// token the framework's middleware may already have resolved into e.Auth), the
// session cookie this package sets at login, or nothing.
//
// The bearer token is parsed here as well rather than relying on e.Auth: the
// module must work on a router where PocketBase's auth middleware is not bound
// (that is exactly what the package's own tests build), and a client using
// `Authorization: Bearer <token>` should never depend on middleware ordering.
func (s *Server) currentUser(e *core.RequestEvent) *core.Record {
	if e.Auth != nil {
		return e.Auth
	}

	if token := bearerToken(e.Request); token != "" {
		record, err := s.app.FindAuthRecordByToken(token, core.TokenTypeAuth)
		if err != nil {
			return nil
		}
		return record
	}

	cookie, err := e.Request.Cookie(accessCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	record, err := s.app.FindAuthRecordByToken(cookie.Value, core.TokenTypeAuth)
	if err != nil {
		return nil
	}
	return record
}

// bearerToken extracts the token from an Authorization header.
func bearerToken(request *http.Request) string {
	header := request.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

// setSessionCookies mints a fresh auth token and stores it in the session
// cookie. It returns the token's expiry as a Unix timestamp, which the SPA
// uses to decide when to refresh.
func setSessionCookies(e *core.RequestEvent, record *core.Record) (int64, error) {
	token, err := record.NewAuthToken()
	if err != nil {
		return 0, errors.Wrap(err, "leafwiki: could not mint a session token")
	}

	duration := record.Collection().AuthToken.Duration
	if duration <= 0 {
		duration = 15 * 24 * 60 * 60
	}
	expiresAt := time.Now().Add(time.Duration(duration) * time.Second)

	e.SetCookie(&http.Cookie{
		Name:     accessCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(e),
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	})

	csrf, csrfErr := randomToken()
	if csrfErr != nil {
		return 0, csrfErr
	}
	e.SetCookie(&http.Cookie{
		Name:     csrfCookieName,
		Value:    csrf,
		Path:     "/",
		HttpOnly: false,
		Secure:   isSecureRequest(e),
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	})

	return expiresAt.Unix(), nil
}

// clearSessionCookies removes the session and CSRF cookies.
func clearSessionCookies(e *core.RequestEvent) {
	for _, name := range []string{accessCookieName, csrfCookieName} {
		e.SetCookie(&http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == accessCookieName,
			Secure:   isSecureRequest(e),
			SameSite: http.SameSiteLaxMode,
		})
	}
}

// isSecureRequest reports whether the browser reached the instance over TLS.
func isSecureRequest(e *core.RequestEvent) bool {
	if e.IsTLS() {
		return true
	}
	return strings.EqualFold(e.Request.Header.Get("X-Forwarded-Proto"), "https")
}

// randomToken returns a 32-byte hex token.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.Wrap(err, "leafwiki: could not generate a token")
	}
	return hex.EncodeToString(buf), nil
}

// verifyCSRF enforces the double-submit check for cookie-authenticated
// mutations. A bearer-token caller is exempt: it is not a browser session and
// cannot be tricked into sending a cookie.
func verifyCSRF(e *core.RequestEvent) bool {
	if e.Request.Header.Get("Authorization") != "" {
		return true
	}
	cookie, err := e.Request.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := e.Request.Header.Get(csrfHeaderName)
	if header == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) == 1
}

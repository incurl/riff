// Package session resolves the forum's caller against the shared
// PocketBase `users` collection.
//
// The forum has NO account store of its own: the host's PocketBase
// auth collection is the single source of truth, and the forum only
// reads the permitted public fields from it. Account lifecycle
// (registration, verification, password reset) stays with the host.
//
// A browser session is a PocketBase auth token in an HttpOnly cookie
// plus a double-submit CSRF token in a readable cookie, which is the
// contract the reused frontend expects (it echoes the CSRF token in the
// X-CSRF-Token header).
package session

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/bcrypt"

	"github.com/incurl/riff/internal/pattoken"
	"github.com/incurl/riff/pb/flarum/domain"
)

const (
	// AccessCookieName carries the PocketBase auth token for browser
	// sessions. It is HttpOnly: the SPA never reads it, it only needs the
	// browser to send it with each request.
	AccessCookieName = "forum_access_token"

	// CSRFCookieName is the double-submit CSRF token. It is deliberately
	// readable by JavaScript, because the SPA echoes it in X-CSRF-Token.
	CSRFCookieName = "forum_csrf"

	// CSRFHeaderName is the header the SPA sends the CSRF token in.
	CSRFHeaderName = "X-CSRF-Token"

	// csrfTokenBytes is the entropy of a CSRF token.
	csrfTokenBytes = 32

	// SessionCookieMaxAge is how long a session cookie lives, in
	// seconds. PocketBase's own auth tokens expire sooner; the cookie
	// only has to outlive them.
	//
	// It is exported because the admin payload reports the session
	// lifetime to the admin application, which shows it in minutes.
	SessionCookieMaxAge = 60 * 60 * 24 * 30
)

// Actor is the resolved caller of a request.
//
// An anonymous caller is an Actor with an empty ID; it is not nil, so a
// handler never has to distinguish "guest" from "not resolved".
type Actor struct {
	// User is the caller, nil for an anonymous request.
	User *domain.User
	// Record is the underlying PocketBase record, nil for an anonymous
	// request.
	Record *core.Record
	// CSRFToken is the request's CSRF token.
	CSRFToken string
}

// ID returns the actor's user id, empty for an anonymous caller.
func (a Actor) ID() string {
	if a.User == nil {
		return ""
	}
	return a.User.ID
}

// IsAuthenticated reports whether the caller is signed in.
func (a Actor) IsAuthenticated() bool { return a.User != nil }

// IsAnonymous reports whether the caller is not signed in.
func (a Actor) IsAnonymous() bool { return a.User == nil }

// IsAdmin reports whether the caller is a forum administrator.
func (a Actor) IsAdmin() bool {
	return a.User != nil && a.User.IsAdmin
}

// GroupLookup reports the groups an account belongs to.
//
// It is a seam rather than a direct dependency: this package must not
// import the repository (the dependency arrow points one way, from the
// repository to the domain), so the module supplies the lookup.
type GroupLookup func(userID string) []string

// Resolver resolves the caller of a request and manages the session
// cookies.
type Resolver struct {
	// app is the PocketBase app.
	app core.App
	// basePath is the module's mount path, used to scope the cookie
	// path so the session cookie is not sent to the host's other
	// routes.
	basePath string
	// secure marks the cookies as Secure. It follows the request's
	// scheme, so a plain-HTTP development instance still works.
	secure bool
	// groups reports the groups an account belongs to. It is optional:
	// without it an account's groups are unknown, and only a superuser
	// is an administrator.
	groups GroupLookup
}

// NewResolver returns a session resolver.
func NewResolver(app core.App, basePath string) *Resolver {
	return &Resolver{app: app, basePath: basePath}
}

// WithGroups returns a resolver that reads the caller's groups through
// the given lookup.
//
// The groups are what decide whether the caller administers the forum
// (see [Actor.IsAdmin]), so a resolver without this cannot tell a
// member of the administrator group from an ordinary member.
func (r *Resolver) WithGroups(lookup GroupLookup) *Resolver {
	r.groups = lookup
	return r
}

// Resolve determines the caller of a request.
//
// Three credentials are accepted, in order:
//
//  1. the PocketBase auth middleware's already-resolved record,
//  2. an `Authorization: Bearer <token>` header carrying a PocketBase
//     auth token,
//  3. the session cookie this package sets at login.
//
// The bearer token is parsed here rather than relying on the middleware,
// because the forum's routes are mounted on a router where the host may
// not have bound PocketBase's auth middleware, and a client that presents
// a bearer token should not depend on middleware ordering.
func (r *Resolver) Resolve(e *core.RequestEvent) (Actor, error) {
	actor := Actor{CSRFToken: r.csrfToken(e)}

	if record := r.recordFromRequest(e); record != nil {
		user, err := r.userFromRecord(record)
		if err != nil {
			return actor, err
		}
		actor.User = user
		actor.Record = record
		return actor, nil
	}

	return actor, nil
}

// recordFromRequest resolves the PocketBase record behind the request's
// credential, or nil when there is none.
func (r *Resolver) recordFromRequest(e *core.RequestEvent) *core.Record {
	if e.Auth != nil {
		return e.Auth
	}

	if token := BearerToken(e.Request); token != "" {
		if record, err := r.app.FindAuthRecordByToken(token, core.TokenTypeAuth); err == nil {
			return record
		}

		// A riff personal access token ("memos_pat_*"). It is the instance's
		// REVOCABLE credential, which is what an agent carries — an MCP tool
		// call forwards exactly this header and nothing else (see
		// `server/mcp`). Without this branch a PAT could post a memo and not
		// a forum reply.
		if record, err := pattoken.Verify(r.app, token); err == nil && record != nil {
			return record
		}

		return nil
	}

	cookie, err := e.Request.Cookie(AccessCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	record, err := r.app.FindAuthRecordByToken(cookie.Value, core.TokenTypeAuth)
	if err != nil {
		return nil
	}
	return record
}

// userFromRecord projects a PocketBase auth record onto the forum's
// User.
//
// Only the permitted public fields are read. The account's groups come
// from the forum's own membership table, not from a field on the shared
// collection.
//
// The administrator flag is the one thing that is NOT read from the
// record's collection alone. Upstream derives it from GROUP MEMBERSHIP
// (`User::isAdmin()` is `$this->groups->contains(Group::ADMINISTRATOR_ID)`),
// and the forum's administrator group is a real, assignable row
// (`forumgroupadmin`) — an operator grants it to somebody through the
// admin's own user editor, exactly as upstream's install writes a
// `group_user` row for the administrator group.
//
// Deriving the flag from the PocketBase collection alone therefore made
// every account an operator promoted into the administrator group a
// non-administrator: the admin panel answered 403, and the admin
// commands answered "You are not allowed to do that".
//
// A PocketBase superuser is still an administrator: the forum's
// superusers are the instance's operators, and upstream's bootstrap
// administrator is the same idea. So the flag is the union of the two
// sources.
func (r *Resolver) userFromRecord(record *core.Record) (*domain.User, error) {
	username := strings.TrimSpace(record.GetString("username"))
	if username == "" {
		// A PocketBase account without a username is still a valid
		// account; the email is the only label it has. The email is not
		// exposed through the API, it is only used to build a display
		// name here.
		username = record.Email()
	}

	user := &domain.User{
		ID:          record.Id,
		Username:    username,
		DisplayName: username,
		IsAdmin:     record.Collection().Name == core.CollectionNameSuperusers,
	}

	if avatar := record.GetString("avatar"); avatar != "" {
		user.AvatarURL = avatar
	}

	// The suspension columns, added by the Suspend plugin. They are read
	// HERE as well as in the repository because the permission layer asks
	// the session's projection, not the repository's: without this a
	// suspended account keeps every group permission and can still post.
	if until := record.GetDateTime("suspended_until"); !until.IsZero() {
		user.SuspendedUntil = until.Time()
	}

	// The administrator group, if the account is in it.
	if r.groups != nil {
		for _, groupID := range r.groups(record.Id) {
			if groupID == domain.GroupAdminID {
				user.IsAdmin = true
				break
			}
		}
	}

	return user, nil
}

// SetSessionCookie writes the session cookies for an authenticated
// caller.
//
// The auth token cookie is HttpOnly and the CSRF token cookie is
// readable, which is what the double-submit contract needs.
func (r *Resolver) SetSessionCookie(e *core.RequestEvent, token string, csrf string) {
	secure := r.secure || isSecureRequest(e.Request)
	http.SetCookie(e.Response, &http.Cookie{
		Name:     AccessCookieName,
		Value:    token,
		Path:     r.cookiePath(),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   SessionCookieMaxAge,
	})
	http.SetCookie(e.Response, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    csrf,
		Path:     r.cookiePath(),
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   SessionCookieMaxAge,
	})
}

// ClearSessionCookie removes the session cookies.
//
// The auth token itself is NOT revoked: PocketBase's auth tokens are
// stateless JWTs (there is no server-side token store to delete a row
// from), so a token that was captured stays valid until it expires —
// the `users` collection's authToken duration, five days by default.
//
// That is PocketBase's model, not the forum's: the wiki's logout has the
// same shape, and the host's revocable credential is the personal access
// token, not the session cookie. A caller that needs an immediately
// revocable credential should use a PAT.
func (r *Resolver) ClearSessionCookie(e *core.RequestEvent) {
	secure := r.secure || isSecureRequest(e.Request)
	for _, name := range []string{AccessCookieName, CSRFCookieName} {
		http.SetCookie(e.Response, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     r.cookiePath(),
			HttpOnly: name == AccessCookieName,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
	}
}

// cookiePath scopes the cookies to the module's mount path.
//
// A cookie scoped to "/" would be sent to every host route, which would
// leak the forum's session into the host application's other modules.
func (r *Resolver) cookiePath() string {
	path := strings.TrimSuffix(r.basePath, "/")
	if path == "" {
		return "/"
	}
	return path
}

// csrfToken returns the request's CSRF token.
//
// It comes from the X-CSRF-Token header when present, and from the
// readable cookie otherwise.
func (*Resolver) csrfToken(e *core.RequestEvent) string {
	if token := strings.TrimSpace(e.Request.Header.Get(CSRFHeaderName)); token != "" {
		return token
	}
	cookie, err := e.Request.Cookie(CSRFCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

// VerifyCSRF checks a mutating request's CSRF token.
//
// The comparison is constant-time. A request without a token, or with a
// token that does not match the cookie, is rejected.
//
// The token is compared against the COOKIE, not against a server-side
// session: that is the double-submit pattern, and it is what upstream
// Flarum's frontend expects (it echoes the cookie's value in the
// header).
func VerifyCSRF(e *core.RequestEvent) error {
	switch e.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}

	// A request that authenticates with a BEARER TOKEN needs no double
	// submit: the token travels in an explicit header, and a cross-site
	// page cannot set one on the caller's behalf — so there is no ambient
	// credential to forge. The cookie is the ambient one, and the cookie
	// path still requires the token.
	//
	// It is not a nicety. The MCP service executes a tool by replaying the
	// call in-process with the caller's `Authorization` header (see
	// `server/mcp`), which carries no cookie jar: without this branch every
	// forum write over MCP answered `missing CSRF cookie`, while the same
	// write over the HTTP API worked. Upstream's framework has the same
	// rule — its CSRF middleware skips token-authenticated requests.
	if BearerToken(e.Request) != "" {
		return nil
	}

	cookie, err := e.Request.Cookie(CSRFCookieName)
	if err != nil || cookie.Value == "" {
		return errors.New("forum: missing CSRF cookie")
	}

	provided := strings.TrimSpace(e.Request.Header.Get(CSRFHeaderName))
	if provided == "" {
		return errors.New("forum: missing CSRF token")
	}

	if subtle.ConstantTimeCompare([]byte(provided), []byte(cookie.Value)) != 1 {
		return errors.New("forum: CSRF token mismatch")
	}
	return nil
}

// NewCSRFToken returns a fresh CSRF token.
func NewCSRFToken() (string, error) {
	buf := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.Wrap(err, "forum: could not generate a CSRF token")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// BearerToken returns the bearer token of a request, empty when there is
// none.
func BearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// isSecureRequest reports whether the request arrived over HTTPS.
//
// The X-Forwarded-Proto header is honoured, because the forum is mounted
// behind a TLS-terminating proxy in the riff deployment.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// LoginRequest is the body of a login request.
//
// It mirrors the frontend's Session.login payload.
type LoginRequest struct {
	// Identification is the username or email.
	Identification string `json:"identification"`
	// Password is the account password.
	Password string `json:"password"`
	// Remember asks for a long-lived session.
	Remember bool `json:"remember"`
}

// Login authenticates a caller against the shared users collection.
//
// The account is resolved by username or email, exactly like upstream
// Flarum's login form. Superusers are searched last, so a regular
// account with the same email wins.
func (r *Resolver) Login(_ *core.RequestEvent, req LoginRequest) (*domain.User, string, error) {
	identification := strings.TrimSpace(req.Identification)
	if identification == "" || req.Password == "" {
		return nil, "", errors.New("forum: identification and password are required")
	}

	record, err := r.findByIdentifier(identification)
	if err != nil {
		// An unknown identifier must cost the same as a wrong
		// password. Without this the bcrypt work is skipped for an
		// account that does not exist, and the response TIME tells an
		// attacker which identifiers are real: the login answers in
		// about a millisecond for an unknown account and about fifty
		// for a known one.
		//
		// The dummy hash is a real bcrypt digest at the same cost, so
		// the comparison does the same work.
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(req.Password))

		return nil, "", errors.New("forum: invalid credentials")
	}

	if !record.ValidatePassword(req.Password) {
		return nil, "", errors.New("forum: invalid credentials")
	}

	token, err := record.NewAuthToken()
	if err != nil {
		return nil, "", errors.Wrap(err, "forum: could not mint an auth token")
	}

	user, err := r.userFromRecord(record)
	if err != nil {
		return nil, "", err
	}
	return user, token, nil
}

// dummyPasswordHash is a bcrypt digest of a value nobody knows.
//
// It exists so a login for an unknown identifier costs the same as a
// login for a known one with a wrong password. See [Resolver.Login].
var dummyPasswordHash = func() []byte {
	// A fixed, meaningless secret: the digest is only ever compared
	// against, never used to authenticate anything.
	hash, err := bcrypt.GenerateFromPassword([]byte("riff-forum-dummy-password"), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt only fails on a bad cost, and DefaultCost is valid.
		panic("forum: could not generate the dummy password hash: " + err.Error())
	}
	return hash
}()

// findByIdentifier resolves a login identifier to an auth record.
func (r *Resolver) findByIdentifier(identifier string) (*core.Record, error) {
	users, err := r.app.FindCollectionByNameOrId("users")
	if err == nil {
		if record, emailErr := r.app.FindAuthRecordByEmail(users, identifier); emailErr == nil {
			return record, nil
		}
		params := map[string]any{"identifier": identifier}
		if record, filterErr := r.app.FindFirstRecordByFilter(users, "username = {:identifier}", params); filterErr == nil {
			return record, nil
		}
	}

	superusers, err := r.app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if err == nil {
		if record, emailErr := r.app.FindAuthRecordByEmail(superusers, identifier); emailErr == nil {
			return record, nil
		}
	}

	return nil, errors.New("forum: no account matches that identifier")
}

// LogoutResponse is the body of a logout response.
type LogoutResponse struct {
	// Message confirms the logout.
	Message string `json:"message"`
}

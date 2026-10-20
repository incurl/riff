// file_authz.go decides who may read a given attachment blob.
//
// A file download is not a collection read. The `attachments`
// collection's ViewRule is the only thing standing between a
// private memo's file and the public internet, and a file server
// that ignores it turns every private attachment into a public
// URL. This file is the authority for the file surface; the
// collection rules are the authority for the API surface.
//
// Four credentials are accepted, in the upstream-Memos order:
//
//  1. the `share_token` query parameter — the bearer credential
//     for a shared memo. It grants access to the shared memo's
//     attachments only.
//  2. the `Authorization: Bearer …` header — a PocketBase auth
//     token (the SPA's session) or a `memos_pat_*` personal
//     access token.
//  3. the `riff_auth` cookie — the same PocketBase auth token the
//     SPA holds in localStorage.
//  4. nothing — an anonymous caller, who may only read files
//     belonging to a PUBLIC memo.
//
// The cookie exists because the SPA's auth token lives in
// localStorage, and localStorage is not reachable from an `<img>`
// src, a `<video> poster`, or a CSS `url()`. Those are the URLs
// that actually render a feed, so without a cookie the browser
// presents *no* credential on a file GET, `authorizeAttachment`
// falls through to the visibility rule, and every attachment on a
// PRIVATE memo — the SPA editor's default — answers 404. The
// SPA writes the cookie alongside the token in
// `web/src/auth-state.ts`, scoped to the SPA's own mount path so
// the token is never sent to the static site on the same host.
//
// The `share_token` is checked first because a share link is
// explicitly a capability: its holder is by definition not a
// member of the memo's space, so the normal visibility rules
// would reject them.
package api

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/connect"
)

// fileAccess is the outcome of an authorization check.
type fileAccess struct {
	// Allowed reports whether the caller may read the file.
	Allowed bool

	// Public reports whether the file belongs to a PUBLIC memo.
	// It only affects the Cache-Control header: a public file may
	// be cached by the browser and any intermediary, a private one
	// may not.
	Public bool
}

// fileViewer resolves the authenticated user behind the request,
// or nil for an anonymous caller.
//
// Three credentials are recognised:
//
//   - a PocketBase auth token (the `Authorization` header), which
//     is what the SPA's connect client sends;
//   - a `memos_pat_*` personal access token, which the SPA's
//     download links also use; and
//   - the `riff_auth` cookie, which carries the same PocketBase
//     auth token for the sub-resource requests that cannot set a
//     header (`<img>`, `<video>`, `<audio>`, CSS `url()`).
//
// `e.Auth` is checked first: PocketBase's own `loadAuthToken`
// middleware has already resolved a valid auth token by the time
// the handler runs. The header and the cookie are re-read as
// fallbacks so the file server also works when it is mounted
// without that middleware (the upstream-Memos REST family is
// mounted on its own router group, and the tests build a bare
// router).
//
// The cookie is a session token, so it is only accepted over a
// transport that may carry a session. A cookie accepted on any
// channel would let an active network attacker who can read one
// plain-HTTP response body also ride the session on every
// subsequent file GET. Local development over plain HTTP (`pnpm
// dev` against `http://localhost:8090`) is exempted by the
// loopback carve-out below, which mirrors what browsers do for
// `Secure` cookies on localhost.
func fileViewer(app core.App, e *core.RequestEvent) *core.Record {
	if e.Auth != nil {
		return e.Auth
	}
	if token := bearerHeader(e); token != "" {
		if rec := resolveFileToken(app, token); rec != nil {
			return rec
		}
	}
	if cookie, ok := fileAuthCookie(e); ok {
		if rec := resolveFileToken(app, cookie); rec != nil {
			return rec
		}
	}
	return nil
}

// fileAuthCookieName is the cookie the SPA writes so that a file
// GET can carry the caller's session.
const fileAuthCookieName = "riff_auth"

// fileAuthCookie reads the session cookie off the request.
//
// It reports false when no cookie is present or when the request
// did not arrive over a transport that may carry a session. The
// cookie value itself cannot say whether the browser sent it over
// http or https, so the transport check is what stands in for the
// `Secure` attribute the SPA sets on it.
func fileAuthCookie(e *core.RequestEvent) (string, bool) {
	c, err := e.Request.Cookie(fileAuthCookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	if !fileCookieTransportAllowed(e) {
		return "", false
	}
	return c.Value, true
}

// fileCookieTransportAllowed reports whether a request's transport
// may carry the session cookie.
//
// The rule is the one the Secure attribute implements, narrowed to
// the case that matters for a self-hosted instance: a loopback
// host is always allowed (that is `pnpm dev` against
// `localhost:8090`, and it is also what keeps a device bring-up
// over a plain-HTTP LAN address usable), and everything else must
// have arrived over TLS. `X-Forwarded-Proto` is honoured because
// every riff deployment terminates TLS at a reverse proxy (caddy
// in the container image) while the listener behind it is plain
// HTTP — without this check the cookie would be rejected on every
// production request.
//
// A forwarded-proto header is trusted because it is set by the
// proxy that fronts riff, and an untrusted client could only
// downgrade its own requests to anonymous — it cannot use the
// header to escalate, since a forged `https` still has to carry a
// token the caller already holds.
func fileCookieTransportAllowed(e *core.RequestEvent) bool {
	proto := e.Request.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = e.Request.Header.Get("X-Forwarded-Protocol")
	}
	if proto == "" && e.Request.TLS != nil {
		proto = "https"
	}
	if proto != "" {
		return strings.EqualFold(proto, "https")
	}
	return isLoopbackRequest(e.Request)
}

// isLoopbackRequest reports whether the request was addressed to
// the loopback interface, judged by the remote address when the
// router set one and by the Host header otherwise.
func isLoopbackRequest(r *http.Request) bool {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && isLoopbackHost(host) {
		return true
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return isLoopbackHost(host)
}

// isLoopbackHost reports whether a host string names the loopback
// interface.
func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// requestOriginFromEvent computes the user-facing origin (scheme
// + host[:port]) of the inbound request, honouring
// `X-Forwarded-Proto` from the proxy that fronts riff when the
// request reached the listener over plain HTTP. It returns the
// empty string when `Host` is not set on the request.
//
// This is the REST-side companion of
// `pb/connect.connect.requestOriginFromHTTP`. The connect path
// reads the headers through `connect.CallInfoForServerContext`;
// the REST path reads them straight off `e.Request`.
//
// Trusting the `X-Forwarded-Proto` header from a trusted
// proxy is the same posture as `fileCookieTransportAllowed`
// above: caddy is what fronts riff in every production
// deployment, and a forged `https` only lets the attacker
// downgrade their *own* requests to anonymous — they still
// need a token they hold to load any non-PUBLIC attachment,
// so the header cannot escalate access.
func requestOriginFromEvent(e *core.RequestEvent) string {
	if e == nil || e.Request == nil {
		return ""
	}
	hdr := e.Request.Header
	scheme := hdr.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = hdr.Get("X-Forwarded-Protocol")
	}
	if scheme == "" {
		// Plain HTTP from the loopback listener (development
		// or a transparent proxy that does not set the header)
		// — fall through to the request's own TLS state.
		if e.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := hdr.Get("Host")
	if host == "" {
		host = e.Request.Host
	}
	if host == "" {
		return ""
	}
	// Trim a path if the header somehow carries one (rare;
	// real listeners send only host[:port]).
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	return scheme + "://" + host
}

// resolveFileToken maps one token string onto the user record it
// authenticates, trying a PocketBase auth token first and then a
// riff personal access token. A nil result means the string is not
// a credential this instance accepts; the caller must not treat the
// request as authenticated on the strength of it.
func resolveFileToken(app core.App, token string) *core.Record {
	if token == "" {
		return nil
	}
	if rec, err := app.FindAuthRecordByToken(token, core.TokenTypeAuth); err == nil {
		return rec
	}
	rec, err := connect.ResolveFilePersonalAccessToken(app, token)
	if err != nil {
		return nil
	}
	return rec
}

// bearerHeader extracts the token from the Authorization header.
// An empty string means "no credential presented".
func bearerHeader(e *core.RequestEvent) string {
	header := e.Request.Header.Get("Authorization")
	if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return strings.TrimSpace(header)
}

// authorizeAttachment decides whether the caller may read the
// attachment's blob.
//
// The attachment is resolved first; an unknown attachment is a
// 404 (not a 403), so the file server never confirms that a given
// id exists to a caller who may not read it.
//
// The share token is checked first: a share link is a capability
// that stands on its own, and its holder is not a member of the
// memo's space.
func authorizeAttachment(app core.App, e *core.RequestEvent, rec *core.Record) fileAccess {
	shareToken := e.Request.URL.Query().Get("share_token")
	if shareToken != "" {
		if access, ok := authorizeViaShare(app, rec, shareToken); ok {
			return access
		}
	}

	viewer := fileViewer(app, e)

	// An attachment with no parent memo is an orphan: only its
	// uploader may read it. The `creator` column records the
	// uploader precisely for this case (see
	// pb/migrations/1890000000_attachment_creator_for_
	// orphan_binding.go — it is stamped by the upload handler
	// whenever the spec did not name a memo).
	//
	// This branch used to admit superusers only, on the grounds
	// that "the `attachments` collection has no `creator` column
	// of its own". That premise stopped being true when the column
	// was added, and the consequence was severe: the V0 upload
	// path deliberately lands rows unlinked (the MoeMemos client
	// uploads the file before it knows the memo id, and the
	// follow-up PATCH binds it), so **every file a V0 client
	// uploaded answered 404 on every read** until the PATCH
	// arrived — including the `externalLink` in the upload
	// response the client downloads from immediately. The uploader
	// holds a valid bearer token and is the only party entitled to
	// the bytes, so they are admitted.
	//
	// A row with no `creator` either predates the column or was
	// written by a superuser via the admin UI; it stays
	// superuser-only, which is the conservative answer for a row
	// whose owner cannot be established.
	memoID := rec.GetString("memo")
	if memoID == "" {
		if viewer == nil {
			return fileAccess{}
		}
		if viewer.IsSuperuser() {
			return fileAccess{Allowed: true}
		}
		if creator := rec.GetString("creator"); creator != "" && creator == viewer.Id {
			return fileAccess{Allowed: true}
		}
		return fileAccess{}
	}

	memo, err := app.FindRecordById("memos", memoID)
	if err != nil {
		return fileAccess{}
	}

	// The memo's own visibility rule is the authority. It encodes
	// PRIVATE (author only), PROTECTED (any signed-in user),
	// PUBLIC (anyone) and SPACE (members only).
	memos, err := app.FindCollectionByNameOrId("memos")
	if err != nil {
		return fileAccess{}
	}
	requestInfo := &core.RequestInfo{Method: http.MethodGet}
	if viewer != nil {
		requestInfo.Auth = viewer
	}
	ok, err := app.CanAccessRecord(memo, requestInfo, memos.ViewRule)
	if err != nil || !ok {
		return fileAccess{}
	}

	return fileAccess{
		Allowed: true,
		Public:  memo.GetString("visibility") == "PUBLIC",
	}
}

// authorizeViaShare resolves the share token and checks that it
// grants access to this attachment's memo.
//
// The token is the row's `code`; the row points at the shared
// memo. An expired share no longer grants anything. The second
// return value is false when the token does not apply (unknown,
// expired, or pointing at a different memo) so the caller can
// fall through to the normal visibility rules.
func authorizeViaShare(app core.App, rec *core.Record, shareToken string) (fileAccess, bool) {
	memoID := rec.GetString("memo")
	if memoID == "" {
		return fileAccess{}, false
	}

	rows, err := app.FindRecordsByFilter("memo_shares",
		"code={:c}", "", 1, 0,
		map[string]any{"c": shareToken},
	)
	if err != nil || len(rows) == 0 {
		return fileAccess{}, false
	}
	share := rows[0]
	if share.GetString("memo") != memoID {
		// The token is valid but points at a different memo. It
		// grants nothing here; fall through so the normal
		// visibility rules decide.
		return fileAccess{}, false
	}
	if dt := share.GetDateTime("expire_time"); !dt.IsZero() && dt.Time().Before(time.Now()) {
		return fileAccess{}, false
	}
	return fileAccess{Allowed: true}, true
}

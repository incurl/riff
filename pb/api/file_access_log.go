// file_access_log.go instruments the attachment download surface.
//
// # WHY THIS EXISTS
//
// A MoeMemos client that cannot render an attachment has no diagnostic
// surface at all: the file GET answers 404 (by design — a denial must not
// confirm the file exists), the client's UI shows a spinner, and the server
// log says nothing. Every question of the form "did the request arrive?",
// "was a credential presented?", "was the attachment found?", "was the
// caller allowed?" therefore had no answer.
//
// The lines below answer exactly those questions, and they are written to
// the STDLIB slog logger on purpose:
//
//   - PocketBase's own `app.Logger()` writes into the `logs.db` table, not
//     to stdout, so those lines never reach the Fly.io log stream. The
//     stdlib default logger writes to stderr, which supervisord forwards to
//     /dev/stdout, which is what Fly.io collects (`fly logs`). Every other
//     riff diagnostic (pb/connect, pb/hooks, pb/api) already uses
//     `slog.Default()` for the same reason.
//
// # PRIVACY
//
// The log records the *shape* of the request, never its contents: the
// attachment id, the route family, the outcome and whether a credential was
// presented. It never records the token, the share token, the file bytes or
// the memo content.
//
// The share token is redacted even though it is a credential: it arrives in
// the query string, and a query string is exactly the kind of thing that
// ends up in a proxy log. Only its PRESENCE is recorded.
//
// # COST
//
// The instrumentation is opt-in per instance. It is off by default because
// every attachment GET on a busy instance would otherwise add a log line.
// RIFF_FILE_ACCESS_LOG=1 turns it on.
//
// # The token
//
// `RIFF_FILE_ACCESS_LOG_TOKEN` is a shared secret that gates the
// instrumentation. When it is set, a request must present it — as the
// `X-Riff-Log-Token` header or the `?log_token=` query parameter — for the
// request to be logged at all. That makes the "log everything" mode usable
// on a public instance without flooding the log stream with lines nobody
// asked for, and it lets an operator open the surface to exactly one
// debugging client.
//
// The token is compared in constant time. It is never logged.
package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

const (
	// fileAccessLogEnabledEnv is the environment variable that enables the
	// attachment download logging.
	fileAccessLogEnabledEnv = "RIFF_FILE_ACCESS_LOG"

	// fileAccessLogSecretEnv holds the shared secret that gates the
	// instrumentation. It is empty when the surface is open (no token
	// required), which is only appropriate for a private instance.
	fileAccessLogSecretEnv = "RIFF_FILE_ACCESS_LOG_TOKEN"

	// fileAccessLogHeader is the header a client presents the gate token in.
	fileAccessLogHeader = "X-Riff-Log-Token"

	// fileAccessLogQuery is the query parameter form of the gate token. It
	// exists for clients that cannot set a header (an `<img>` src, for
	// instance).
	fileAccessLogQuery = "log_token"
)

// fileAccessLogEnabled is the operator's switch. It is read once at
// startup: toggling it requires a restart, which is deliberate — a
// runtime switch would make the log volume unpredictable.
var fileAccessLogEnabled = false

// fileAccessLogSecret is the gate token. An empty secret means "no token
// required".
var fileAccessLogSecret = ""

// SetFileAccessLog configures the attachment download logging.
//
// It is called once from main.go, before the routes are registered. The
// enabled flag turns the instrumentation on; the secret (when non-empty)
// gates it so that only requests presenting the token are logged.
func SetFileAccessLog(enabled bool, secret string) {
	fileAccessLogEnabled = enabled
	fileAccessLogSecret = strings.TrimSpace(secret)
}

// init reads the environment so a host that does not call
// [SetFileAccessLog] still gets the documented behaviour.
func init() {
	SetFileAccessLog(envBoolDefault(fileAccessLogEnabledEnv), os.Getenv(fileAccessLogSecretEnv))
}

// envBoolDefault reports whether the named variable holds a truthy value.
func envBoolDefault(name string) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		// "1" is the spelling the rest of riff's operator knobs use.
		return value == "1"
	}
	return parsed
}

// fileAccessLogAllowed reports whether this request may be logged.
//
// A request is logged when the instrumentation is on AND (no secret is
// configured OR the request presents the matching token).
func fileAccessLogAllowed(e *core.RequestEvent) bool {
	if !fileAccessLogEnabled {
		return false
	}
	if fileAccessLogSecret == "" {
		return true
	}
	presented := e.Request.Header.Get(fileAccessLogHeader)
	if presented == "" {
		presented = e.Request.URL.Query().Get(fileAccessLogQuery)
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(fileAccessLogSecret)) == 1
}

// fileAccessOutcome is the result of an attachment download, as logged.
type fileAccessOutcome struct {
	// Found reports whether the attachment row was resolved.
	Found bool
	// Allowed reports whether the caller was authorized to read it.
	Allowed bool
	// Status is the HTTP status the request answered with.
	Status int
}

// logFileAccess writes one line describing an attachment download.
//
// The line is deliberately flat and greppable: every line carries the
// `file_access` marker, so an operator can find the traffic with a single
// `fly logs` filter.
func logFileAccess(e *core.RequestEvent, uid string, outcome fileAccessOutcome, authKind string) {
	if !fileAccessLogAllowed(e) {
		return
	}

	attrs := []any{
		// The marker is a KEY with a value, not a bare string: slog pairs
		// the arguments two by two, so a lone string would swallow the
		// next key and shift every field.
		// The marker is a KEY with a value, not a bare string: slog pairs
		// the arguments two by two, so a lone string would swallow the
		// next key and shift every field.
		"marker", "file_access",
		"uid", uid,
		"route", fileAccessRoute(e),
		"status", outcome.Status,
		"found", outcome.Found,
		"allowed", outcome.Allowed,
		"auth", authKind,
		"ip", fileAccessClientIP(e),
		"method", e.Request.Method,

		// The credential's PRESENCE is diagnostic; its value is a secret
		// and never reaches the log.
		"has_bearer", bearerHeader(e) != "",
		"has_cookie", requestHasFileAuthCookie(e),
		"has_share", e.Request.URL.Query().Get("share_token") != "",
	}

	// A 404 is the interesting outcome: it means the client asked for
	// something it could not have. An allowed 200 is the boring one.
	if outcome.Allowed && outcome.Status == http.StatusOK {
		slog.Info("attachment served", attrs...)
		return
	}
	slog.Warn("attachment request denied", attrs...)
}

// fileAccessClientIP returns the caller's address for the log line.
//
// It falls back to the raw peer address when the event carries no app (the
// unit tests build a bare RequestEvent) or when the trusted-proxy settings
// cannot be consulted. The logging path must never panic the request it
// describes: a nil-app dereference inside a log call would turn a 404 into
// a 500.
func fileAccessClientIP(e *core.RequestEvent) string {
	if e == nil || e.Request == nil {
		return ""
	}
	if e.App == nil {
		return e.Request.RemoteAddr
	}
	return e.RealIP()
}

// fileAccessRoute names the route family the request used.
//
// The family matters more than the full path: the four surfaces have
// different client behaviours, and "the iOS client asked for /o/r/<uid>" is
// a different diagnosis from "the SPA asked for /file/attachments/<uid>".
func fileAccessRoute(e *core.RequestEvent) string {
	path := e.Request.URL.Path
	switch {
	case strings.HasPrefix(path, "/o/r/"):
		return "v0-o-r"
	case strings.HasPrefix(path, "/memos/api/file/"):
		return "upstream-memos-file"
	case strings.HasPrefix(path, "/file/attachments/"):
		return "file-attachments"
	case strings.HasPrefix(path, "/file/users/"):
		return "file-avatar"
	default:
		return "other"
	}
}

// requestHasFileAuthCookie reports whether the request carries the SPA's
// session cookie, without reading its value.
func requestHasFileAuthCookie(e *core.RequestEvent) bool {
	cookie, err := e.Request.Cookie(fileAuthCookieName)
	return err == nil && cookie.Value != ""
}

// fileAccessAuthKind names the credential the caller presented.
//
// It is the *kind*, never the value: "bearer" / "cookie" / "share" /
// "none". The kind is what distinguishes "the client sent no credential at
// all" (the iOS relative-URL bug) from "the client sent one and was
// denied" (a visibility rule).
func fileAccessAuthKind(e *core.RequestEvent) string {
	if e.Request.URL.Query().Get("share_token") != "" {
		return "share"
	}
	if bearerHeader(e) != "" {
		return "bearer"
	}
	if requestHasFileAuthCookie(e) {
		return "cookie"
	}
	if e.Auth != nil {
		return "middleware"
	}
	return "none"
}

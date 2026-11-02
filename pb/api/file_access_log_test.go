package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogs redirects the default slog logger into a buffer for the
// duration of the test and returns the buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// TestFileAccessLog_DisabledByDefault pins that the instrumentation is off
// unless the operator turns it on: a busy instance must not emit a log line
// per attachment GET.
func TestFileAccessLog_DisabledByDefault(t *testing.T) {
	SetFileAccessLog(false, "")
	t.Cleanup(func() { SetFileAccessLog(false, "") })

	buf := captureLogs(t)
	logFileAccess(nil, "uid-1", fileAccessOutcome{Found: true, Allowed: true, Status: 200}, "bearer")
	assert.Empty(t, buf.String(), "the file access log must be silent when disabled")
}

// TestFileAccessLog_TokenGate pins the shared-secret gate: with a secret
// configured, only a request presenting it is logged.
//
// This is what makes the "log everything" mode usable on a public instance:
// an operator can leave RIFF_FILE_ACCESS_LOG=1 on and still have the log
// stream contain only the traffic they asked about.
func TestFileAccessLog_TokenGate(t *testing.T) {
	SetFileAccessLog(true, "s3cret")
	t.Cleanup(func() { SetFileAccessLog(false, "") })

	cases := []struct {
		name    string
		header  string
		query   string
		allowed bool
	}{
		{name: "no token", allowed: false},
		{name: "wrong token", header: "nope", allowed: false},
		{name: "right token in header", header: "s3cret", allowed: true},
		{name: "right token in query", query: "s3cret", allowed: true},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			buf := captureLogs(t)
			target := "/file/attachments/uid-1"
			if test.query != "" {
				target += "?log_token=" + test.query
			}
			request := httptest.NewRequest(http.MethodGet, target, nil)
			if test.header != "" {
				request.Header.Set(fileAccessLogHeader, test.header)
			}
			e := &core.RequestEvent{Request: request}

			logFileAccess(e, "uid-1", fileAccessOutcome{Found: true, Allowed: true, Status: 200}, "bearer")

			out := buf.String()
			if test.allowed {
				assert.Contains(t, out, "file_access")
			} else {
				assert.Empty(t, out, "a request without the gate token must not be logged")
			}
		})
	}
}

// TestFileAccessLog_RecordsTheDecisionPoints pins the fields the log
// carries. They are the whole point of the instrumentation: a denied
// request must say whether the attachment existed, whether the caller was
// authorized, and which credential was presented.
func TestFileAccessLog_RecordsTheDecisionPoints(t *testing.T) {
	SetFileAccessLog(true, "")
	t.Cleanup(func() { SetFileAccessLog(false, "") })

	buf := captureLogs(t)
	request := httptest.NewRequest(http.MethodGet, "/o/r/uid-1", nil)
	request.Header.Set("Authorization", "Bearer secret-token-value")
	e := &core.RequestEvent{Request: request}

	logFileAccess(e, "uid-1", fileAccessOutcome{Found: true, Status: 404}, "bearer")

	out := buf.String()
	assert.Contains(t, out, "file_access", "the marker makes the line greppable")
	assert.Contains(t, out, "uid=uid-1")
	assert.Contains(t, out, "route=v0-o-r", "the route family distinguishes the client surfaces")
	assert.Contains(t, out, "status=404")
	assert.Contains(t, out, "found=true", "the attachment existed")
	assert.Contains(t, out, "allowed=false", "the caller was denied")
	assert.Contains(t, out, "auth=bearer")
	assert.Contains(t, out, "has_bearer=true")
	assert.NotContains(t, out, "secret-token-value", "the bearer must never be logged")
}

// TestFileAccessLog_NeverLogsSecrets pins that no credential value reaches
// the log — only the PRESENCE of a credential.
//
// The share token is the dangerous one: it arrives in the query string,
// which is exactly what ends up in a proxy's access log, so a line that
// echoed it would leak it a second time.
func TestFileAccessLog_NeverLogsSecrets(t *testing.T) {
	SetFileAccessLog(true, "")
	t.Cleanup(func() { SetFileAccessLog(false, "") })

	buf := captureLogs(t)
	request := httptest.NewRequest(http.MethodGet, "/file/attachments/uid-1?share_token=super-secret-share", nil)
	request.AddCookie(&http.Cookie{Name: fileAuthCookieName, Value: "cookie-secret-value"})
	e := &core.RequestEvent{Request: request}

	logFileAccess(e, "uid-1", fileAccessOutcome{Found: true, Allowed: true, Status: 200}, "share")

	out := buf.String()
	assert.NotContains(t, out, "super-secret-share", "the share token must never be logged")
	assert.NotContains(t, out, "cookie-secret-value", "the session cookie must never be logged")
	assert.Contains(t, out, "has_share=true", "the presence of a share token is recorded")
	assert.Contains(t, out, "has_cookie=true", "the presence of a cookie is recorded")
}

// TestFileAccessAuthKind pins the credential classification. The kind is
// what separates "the client sent no credential at all" (the iOS
// relative-URL bug) from "the client sent one and was denied".
func TestFileAccessAuthKind(t *testing.T) {
	newEvent := func(configure func(*http.Request)) *core.RequestEvent {
		request := httptest.NewRequest(http.MethodGet, "/file/attachments/uid-1", nil)
		if configure != nil {
			configure(request)
		}
		return &core.RequestEvent{Request: request}
	}

	assert.Equal(t, "none", fileAccessAuthKind(newEvent(nil)))
	assert.Equal(t, "bearer", fileAccessAuthKind(newEvent(func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer x")
	})))
	assert.Equal(t, "cookie", fileAccessAuthKind(newEvent(func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: fileAuthCookieName, Value: "x"})
	})))
	assert.Equal(t, "share", fileAccessAuthKind(newEvent(func(r *http.Request) {
		r.URL.RawQuery = "share_token=abc"
	})))
}

// TestFileAccessRoute pins the route classification. The four surfaces have
// different client behaviours, so "the iOS client asked for /o/r/<uid>" is
// a different diagnosis from "the SPA asked for /file/attachments/<uid>".
func TestFileAccessRoute(t *testing.T) {
	cases := map[string]string{
		"/o/r/uid-1":                        "v0-o-r",
		"/file/attachments/uid-1":           "file-attachments",
		"/file/attachments/uid-1/pic.png":   "file-attachments",
		"/memos/api/file/attachments/uid-1": "upstream-memos-file",
		"/file/users/alice/avatar":          "file-avatar",
		"/something/else":                   "other",
	}
	for path, want := range cases {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		e := &core.RequestEvent{Request: request}
		assert.Equal(t, want, fileAccessRoute(e), "path %s", path)
	}
}

// TestEnvBoolDefault pins the operator knob's spelling: "1" and "true" turn
// it on, anything else (including a typo) leaves it off.
func TestEnvBoolDefault(t *testing.T) {
	t.Setenv("RIFF_TEST_FILE_ACCESS_LOG", "1")
	assert.True(t, envBoolDefault("RIFF_TEST_FILE_ACCESS_LOG"))

	t.Setenv("RIFF_TEST_FILE_ACCESS_LOG", "true")
	assert.True(t, envBoolDefault("RIFF_TEST_FILE_ACCESS_LOG"))

	t.Setenv("RIFF_TEST_FILE_ACCESS_LOG", "yes")
	assert.False(t, envBoolDefault("RIFF_TEST_FILE_ACCESS_LOG"))

	t.Setenv("RIFF_TEST_FILE_ACCESS_LOG", "")
	assert.False(t, envBoolDefault("RIFF_TEST_FILE_ACCESS_LOG"))

	require.NotEmpty(t, fileAccessLogHeader)
}

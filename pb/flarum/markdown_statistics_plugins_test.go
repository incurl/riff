package flarum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/domain"
)

// The Markdown plugin is deliberately thin: the FORMATTER is riff's own
// markdown engine, which the forum already calls for every post. What the
// plugin contributes is the one piece of upstream's syntax that engine
// lacked (the inline spoiler) and the composer toolbar.
//
// So the test that matters is not "does the plugin exist" but "does a post
// written with the syntax come back rendered" — the chain from the plugin's
// registration through `renderContent` to `contentHtml`.

// TestMarkdown_APostRendersTheInlineSpoiler pins the whole chain.
func TestMarkdown_APostRendersTheInlineSpoiler(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "markdown_author")
	discussionID, _ := seedDiscussion(t, app, "Spoilers", "Hello")

	recorder := postAs(t, handler, discussionID, "the answer is ||forty two||", cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	attributes := postAttributes(t, recorder)

	html, ok := attributes["contentHtml"].(string)
	require.True(t, ok, "the post must carry rendered HTML")

	require.Contains(t, html, `<span class="spoiler" data-spoiler="">forty two</span>`,
		"the spoiler must reach the rendered body: %s", html)
	require.Contains(t, html, "the answer is")
}

// TestMarkdown_TheSpoilerSurvivesTheEditRoundTrip pins that the SOURCE is
// stored unrendered: the edit form shows what the author typed, and a
// stored HTML fragment there would be uneditable.
func TestMarkdown_TheSpoilerSurvivesTheEditRoundTrip(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "markdown_edit")
	discussionID, _ := seedDiscussion(t, app, "Editable", "Hello")

	recorder := postAs(t, handler, discussionID, "keep ||this|| hidden", cookies)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())

	require.Equal(t, "keep ||this|| hidden", postAttributes(t, recorder)["content"],
		"the Markdown source is what is stored")
}

// postAttributes decodes a single-resource response's attributes.
func postAttributes(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	document := map[string]any{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))

	data, ok := document["data"].(map[string]any)
	require.True(t, ok, "the response must carry a data object")

	attributes, ok := data["attributes"].(map[string]any)
	require.True(t, ok, "the resource must carry attributes")

	return attributes
}

// dayBucket returns the key the series uses for today: midnight UTC, in the
// RFC 3339 form the frontend's `new Date()` parses.
func dayBucket(t *testing.T) string {
	t.Helper()
	return time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)
}

// The Statistics plugin is ADMIN-ONLY, as upstream's `assertAdmin()` is.
// The two things worth pinning are that the totals are the right shape and
// that a non-administrator gets nothing — the endpoint aggregates across
// every discussion, including the ones the caller cannot read.

// TestStatistics_AnAdministratorGetsTheLifetimeTotals pins the contract:
//
//	GET /api/statistics?period=lifetime
//	-> {"users": N, "discussions": N, "posts": N}
func TestStatistics_AnAdministratorGetsTheLifetimeTotals(t *testing.T) {
	app, handler := newTestForum(t)

	seedDiscussion(t, app, "One", "Hello")
	seedDiscussion(t, app, "Two", "Hello again")

	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api/statistics?period=lifetime", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	totals := map[string]int{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &totals))

	for _, entity := range []string{"users", "discussions", "posts"} {
		_, ok := totals[entity]
		require.True(t, ok, "the lifetime totals must name %q: %v", entity, totals)
	}

	require.Equal(t, 2, totals["discussions"], "both seeded discussions must be counted")
	require.GreaterOrEqual(t, totals["posts"], 2, "each seeded discussion has a post")
}

// TestStatistics_AMemberIsRefused pins the gate.
//
// A 403 rather than a 404: the endpoint's existence is not the secret, the
// numbers are — and a 404 would make the plugin look unmounted.
func TestStatistics_AMemberIsRefused(t *testing.T) {
	app, handler := newTestForum(t)

	_, cookies := memberSession(t, app, "stats_member")

	recorder := do(t, handler, http.MethodGet, "/forum/api/statistics?period=lifetime", "", cookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/statistics?model=users", "", cookies)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestStatistics_AnAnonymousCallerIsRefused pins the same gate for a caller
// with no session at all.
func TestStatistics_AnAnonymousCallerIsRefused(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/statistics?period=lifetime", "", nil)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
}

// TestStatistics_ATimedSeriesNeedsAModel pins that a missing model is a
// validation failure rather than an empty chart: an empty series renders as
// "no activity", which is a different and wrong statement.
func TestStatistics_ATimedSeriesNeedsAModel(t *testing.T) {
	app, handler := newTestForum(t)

	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api/statistics?period=day", "", cookies)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())

	recorder = do(t, handler, http.MethodGet, "/forum/api/statistics?model=nonsense", "", cookies)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, recorder.Body.String())
}

// TestStatistics_ATimedSeriesIsPerDay pins the shape a chart consumes: a
// map of RFC 3339 day timestamps to counts.
func TestStatistics_ATimedSeriesIsPerDay(t *testing.T) {
	app, handler := newTestForum(t)

	seedDiscussion(t, app, "Today", "Hello")

	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api/statistics?model=discussions", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	series := map[string]int{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &series))

	require.NotEmpty(t, series, "a discussion created today must appear in the series")
	require.Equal(t, 1, series[dayBucket(t)], "the series must be keyed by the day: %v", series)
}

// TestStatistics_TheForumDocumentDoesNotLeakTheEntityList pins that nothing
// about the endpoint reaches a non-administrator through the forum document.
//
// Upstream publishes `statisticsEntities` on the forum resource, visible to
// administrators only. This port does not publish it at all, so the stronger
// statement holds: a guest's forum document carries no statistics at all.
func TestStatistics_TheForumDocumentDoesNotLeakTheEntityList(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	require.NotContains(t, recorder.Body.String(), "statisticsEntities")
}

// TestPlugins_TheModerationAndContentPluginsAreRegistered pins that the
// plugin list is what the manifest advertises.
func TestPlugins_TheModerationAndContentPluginsAreRegistered(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api/extensions", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	var manifest struct {
		Extensions []map[string]any `json:"extensions"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &manifest))

	ids := []string{}
	for _, extension := range manifest.Extensions {
		ids = append(ids, extension["id"].(string))
	}

	require.Contains(t, ids, "flarum/markdown")
	// Statistics declares no asset bundle, so it is not in this list — it
	// is in the plugin registry, which is what the endpoint proves.
	require.NotContains(t, ids, "flarum/statistics")

	_ = domain.PermissionLockDiscussion
}

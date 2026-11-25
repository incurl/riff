package flarum

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
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
// hourBucket is the bucket key for a row created just now.
//
// The last 25 hours are bucketed by the HOUR (upstream's `$hourlyFrom =
// new DateTime('-25 hours')`), so a row written by this test belongs to the
// hour bucket, not the day bucket.
func hourBucket(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(time.Now().UTC().Truncate(time.Hour).Unix(), 10)
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

// TestStatistics_ATimedSeriesIsKeyedByUnixSeconds pins the shape the
// chart consumes.
//
// The keys are UNIX SECONDS, not RFC 3339 strings. The widget sums the map
// over a window it computes in seconds
// (`StatisticsWidget::getPeriodCount`: `const time = parseInt(t)`), and
// `parseInt` of a date string is NaN — so an RFC 3339 key is silently
// skipped and every chart draws zero. That was the port's contract before
// the presentation half existed.
func TestStatistics_ATimedSeriesIsKeyedByUnixSeconds(t *testing.T) {
	app, handler := newTestForum(t)

	seedDiscussion(t, app, "Today", "Hello")

	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api/statistics?model=discussions&period=timed", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	series := map[string]int{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &series))

	require.NotEmpty(t, series, "a discussion created today must appear in the series")

	for key := range series {
		require.Regexp(t, `^[0-9]+$`, key, "every key must be unix seconds: %v", series)
	}

	require.Equal(t, 1, series[hourBucket(t)], "a row created now belongs to the hour bucket: %v", series)
}

// TestStatistics_TheForumDocumentDoesNotLeakTheEntityList pins that nothing
// about the endpoint reaches a non-administrator through the forum document.
//
// Upstream publishes `statisticsEntities` on the forum resource with
// `->visible(fn (...) => $context->getActor()->isAdmin())`, and an invisible
// field is ABSENT rather than null — a null still tells the reader the field
// exists.
func TestStatistics_TheForumDocumentDoesNotLeakTheEntityList(t *testing.T) {
	_, handler := newTestForum(t)

	recorder := do(t, handler, http.MethodGet, "/forum/api", "", nil)
	require.Equal(t, http.StatusOK, recorder.Code)

	require.NotContains(t, recorder.Body.String(), "statisticsEntities")
	require.NotContains(t, recorder.Body.String(), `"users","discussions","posts"`)
}

// TestStatistics_TheForumDocumentCarriesTheEntityListForAnAdministrator
// pins the visible half: the page lays its widgets out from this list before
// any number arrives.
//
// It has to come through the BOOT payload as well as `GET /api/`, because
// the SPA reads the forum document out of the boot payload — an attribute
// that only the API served would leave the page drawing its fallback
// layout.
func TestStatistics_TheForumDocumentCarriesTheEntityListForAnAdministrator(t *testing.T) {
	app, handler := newTestForum(t)

	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	require.Contains(t, recorder.Body.String(), "statisticsEntities")
	require.Contains(t, recorder.Body.String(), `"users","discussions","posts"`,
		"the entity list must be in upstream's registration order")

	// The same document is what the shell boots from.
	shell := do(t, handler, http.MethodGet, "/forum/", "", cookies)
	require.Equal(t, http.StatusOK, shell.Code)
	require.Contains(t, shell.Body.String(), "statisticsEntities")
}

// TestStatistics_EveryEntityAnswersATimedSeries pins that every entity the
// endpoint advertises actually answers.
//
// `users` is PocketBase's auth collection, whose system timestamp fields are
// `created` and `updated` — there is no `created_at`. The handler hardcoded
// `created_at`, so `?model=users` answered 500 ("The statistics could not be
// read") while `?model=discussions` worked, and the lifetime totals hid it
// because they only count rows and never filter. The entity list is the
// plugin's own, so a reader has no way to know one of them is a different
// shape of collection.
func TestStatistics_EveryEntityAnswersATimedSeries(t *testing.T) {
	app, handler := newTestForum(t)

	memberSession(t, app, "stats_series_member")

	cookies := testSuperuser(t, app)

	for _, model := range []string{"users", "discussions", "posts"} {
		recorder := do(t, handler, http.MethodGet, "/forum/api/statistics?model="+model, "", cookies)
		require.Equal(t, http.StatusOK, recorder.Code, "model=%s: %s", model, recorder.Body.String())

		series := map[string]int{}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &series), "model=%s", model)
	}
}

// TestStatistics_TheUsersSeriesReadsTheSharedCollectionsTimestamp pins the
// actual count, not just the status: a fix that grouped on a column the
// collection does not have, or read the wrong one, would answer 200 with an
// EMPTY series — which draws a chart reading "no activity", a different and
// wrong statement.
func TestStatistics_TheUsersSeriesReadsTheSharedCollectionsTimestamp(t *testing.T) {
	app, handler := newTestForum(t)

	memberSession(t, app, "stats_created_member")

	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api/statistics?model=users", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	series := map[string]int{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &series))

	require.Equal(t, 1, series[hourBucket(t)], "the member created now is in the hourly tail: %v", series)
}

// TestStatistics_OnlyCommentPostsAreCounted pins upstream's predicate.
//
// Upstream counts `Post::where('type', 'comment')`, so a post of another
// type does not inflate the number. The port writes `type = "comment"` on
// every post today, which is exactly why the filter needs a test: without
// one, a future post type silently changes the dashboard's total.
func TestStatistics_OnlyCommentPostsAreCounted(t *testing.T) {
	app, handler := newTestForum(t)

	discussionID, postID := seedDiscussion(t, app, "Counted", "Hello")

	collection, err := app.FindCollectionByNameOrId("forum_posts")
	require.NoError(t, err)

	// `user` is required on the collection, so reuse the seeded author.
	seeded, err := app.FindRecordById(collection, postID)
	require.NoError(t, err)

	// `discussionRenamed` is the collection's other selectable type, and the
	// one upstream also has — a rename event is stored as a post so the
	// stream can show it, but it is not a comment and must not count.
	other := core.NewRecord(collection)
	other.Set("discussion", discussionID)
	other.Set("user", seeded.GetString("user"))
	other.Set("content", "not a comment")
	other.Set("type", "discussionRenamed")
	other.Set("number", 99)
	require.NoError(t, app.Save(other))

	cookies := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/api/statistics?period=lifetime", "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	totals := map[string]int{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &totals))

	require.Equal(t, 1, totals["posts"], "only the comment post may be counted: %v", totals)
}

// TestStatistics_ACustomRangeIsServed pins upstream's `period=custom`.
//
// The range is widened BACKWARDS by its own length, so the chart can draw
// the previous period as a second series — a range that returned only its
// own points would draw a blank comparison line.
func TestStatistics_ACustomRangeIsServed(t *testing.T) {
	app, handler := newTestForum(t)

	seedDiscussion(t, app, "Custom", "Hello")

	cookies := testSuperuser(t, app)

	now := time.Now().UTC()
	startSec := now.Add(-2 * time.Hour).Unix()
	endSec := now.Add(time.Minute).Unix()

	path := "/forum/api/statistics?model=discussions&period=custom" +
		"&dateRange[start]=" + strconv.FormatInt(startSec, 10) +
		"&dateRange[end]=" + strconv.FormatInt(endSec, 10)

	recorder := do(t, handler, http.MethodGet, path, "", cookies)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	series := map[string]int{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &series))

	require.Equal(t, 1, series[hourBucket(t)], "the discussion must be in the custom range: %v", series)
}

// TestStatistics_ACustomRangeNeedsBothEnds pins the validation.
//
// Upstream throws `InvalidParameterException('A custom date range must be
// specified')`, and an empty series would draw as "no activity".
func TestStatistics_ACustomRangeNeedsBothEnds(t *testing.T) {
	app, handler := newTestForum(t)

	cookies := testSuperuser(t, app)

	for _, path := range []string{
		"/forum/api/statistics?model=discussions&period=custom",
		"/forum/api/statistics?model=discussions&period=custom&dateRange[start]=100",
		"/forum/api/statistics?model=discussions&period=custom&dateRange[start]=100&dateRange[end]=100",
	} {
		recorder := do(t, handler, http.MethodGet, path, "", cookies)
		require.Equal(t, http.StatusUnprocessableEntity, recorder.Code, "%s: %s", path, recorder.Body.String())
	}
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
	// Statistics ships an ADMIN bundle (its page and the dashboard widget),
	// so it is listed — with an `admin` entry and no `forum` entry, because
	// upstream's extension is admin-only.
	require.Contains(t, ids, "flarum/statistics")

	_ = domain.PermissionLockDiscussion
}

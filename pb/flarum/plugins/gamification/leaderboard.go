package gamification

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// The leaderboard's metrics.
//
// They are upstream's four, and the keys are upstream's: `posts`,
// `discussions`, `upvotes` and `votes`. A client that asks for one by an
// upstream name gets that metric.
//
// A metric registered by another extension is upstream's extension point,
// which this port does not have.
const (
	// MetricPosts counts comment posts written.
	MetricPosts = "posts"
	// MetricDiscussions counts discussions started.
	MetricDiscussions = "discussions"
	// MetricUpvotes counts upvotes received.
	MetricUpvotes = "upvotes"
	// MetricVotes counts votes received, up and down.
	MetricVotes = "votes"
)

// The leaderboard's periods.
//
// They are upstream's, and the semantics are CALENDAR periods rather than
// rolling windows: "this month" means the month we are in, which is what
// the words say. A rolling thirty days also makes the movement meaningless,
// because both windows slide on every page load.
const (
	// PeriodDay counts from the start of today.
	PeriodDay = "day"
	// PeriodWeek counts from the start of this week.
	PeriodWeek = "week"
	// PeriodMonth counts from the start of this month.
	PeriodMonth = "month"
	// PeriodYear counts from the start of this year.
	PeriodYear = "year"
	// PeriodAllTime counts everything.
	PeriodAllTime = "all"
)

// leaderboardHandler serves GET /api/leaderboard.
//
// It is upstream's `LeaderboardResource`: a computed ranking, not a stored
// table. The same people come back in a different order depending on the
// metric and the period, so there is no row to read.
//
// The contract:
//
//	GET /api/leaderboard
//	    -> {"data":[{"type":"leaderboard-entries","id":"<metric>-<period>-<position>",
//	                 "attributes":{"metric":…,"period":…,"position":…,"score":…},
//	                 "relationships":{"user":{"data":{…}}}}], …}
//
// The id COMPOSES the ranking, not just the place in it: a bare position
// means entry "11" of the yearly board and entry "11" of the monthly one
// are the same record to a client store that caches by type and id, so
// switching metric or period hands back whichever was fetched first and the
// new figures never appear.
func (p *Plugin) leaderboardHandler(e *core.RequestEvent) error {
	// The ranking page is gated by its own permission, upstream's
	// `fof.gamification.viewRankingPage` (`domain.PermissionViewRankingPage`
	// is that key's forum spelling).
	allowed, err := api.Can(e, domain.PermissionViewRankingPage, TypeLeaderboardEntry, "")
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The leaderboard could not be read.")
	}
	if !allowed {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to see the leaderboard.")
	}

	query := e.Request.URL.Query()

	metric := queryParam(query, "metric")
	if metric == "" {
		metric = settingString(e, SettingDefaultMetric, MetricPosts)
	}
	if !knownMetric(metric) {
		// A metric that does not exist is a validation failure rather than
		// an empty board: an empty board reads as "nobody has scored",
		// which is a different and wrong statement.
		//
		// Upstream FALLS BACK instead ("the value comes off a query
		// string, so it may be a stale link") — but a fallback answers a
		// question the caller did not ask, and the caller cannot tell.
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "That metric does not exist.")
	}

	period := queryParam(query, "period")
	if period == "" {
		period = settingString(e, SettingDefaultPeriod, PeriodYear)
	}
	if !knownPeriod(period) {
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "That period does not exist.")
	}

	scores, err := p.ranking(e, metric, period)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The leaderboard could not be read.")
	}

	limit, offset := pageParams(e)

	entries := make([]api.Resource, 0)
	for index, entry := range scores {
		if index < offset {
			continue
		}
		if len(entries) >= limit {
			break
		}

		position := index + 1

		entries = append(entries, api.Resource{
			Type: TypeLeaderboardEntry,
			ID:   leaderboardEntryID(metric, period, position),
			Attributes: map[string]any{
				"metric":   metric,
				"period":   period,
				"position": position,
				"score":    entry.score,
				// `movement` is upstream's "places gained since the
				// previous window", and it is NULL for this port: the
				// board is computed per request and no previous window
				// is kept. Null is what upstream itself sends for
				// all-time, and the frontend renders no arrow for it —
				// a zero would read as "no change", which is a claim.
				"movement": nil,
			},
			Relationships: map[string]api.Relationship{
				"user": {Data: api.Identifier{Type: api.TypeUser, ID: entry.userID}},
			},
		})
	}

	// The users the page names, so the client has names and avatars
	// without a second round trip — upstream's `defaultInclude(['user'])`.
	//
	// The walk is the core's own: it resolves the identifier through the
	// same account lookup every other response uses, so a private
	// account's fields are filtered exactly as they are elsewhere.
	included, err := api.IncludeRelations(e, entries, "user")
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The leaderboard could not be read.")
	}

	// `links.next` is the "load more" button: the frontend renders it only
	// when the link is present (`hasMore = !!payload.links?.next`), so a
	// page that ends without one stops the list there.
	links := &api.Links{}
	if offset+len(entries) < len(scores) {
		links.Next = leaderboardPageURL(e, metric, period, offset+len(entries), limit)
	}
	if offset > 0 {
		previous := max(offset-limit, 0)
		links.Prev = leaderboardPageURL(e, metric, period, previous, limit)
	}

	return api.WriteDocument(e, http.StatusOK, api.Document{
		Data:     entries,
		Included: included,
		Links:    links,
		Meta: &api.Meta{
			Page:    &api.PageMeta{Total: len(scores), Offset: offset, Limit: limit},
			PerPage: limit,
		},
	})
}

// queryParam reads a query parameter in either spelling.
//
// Upstream's resource has no path of its own, so its metric and period
// live where JSON:API reserves a resource's own parameters — under
// `filter`:
//
//	GET /api/leaderboard-entries?filter[metric]=posts&filter[period]=year
//
// The flat form is this port's older spelling and is still read, because
// a client that was written against it is not wrong, only older.
func queryParam(query url.Values, name string) string {
	if value := strings.TrimSpace(query.Get("filter[" + name + "]")); value != "" {
		return value
	}
	return strings.TrimSpace(query.Get(name))
}

// leaderboardPageURL composes a page link.
//
// It carries the ranking with it: a `next` that dropped the metric would
// hand the client the DEFAULT board's second page, which is a different
// ranking than the one it is paging through.
func leaderboardPageURL(e *core.RequestEvent, metric, period string, offset, limit int) string {
	return fmt.Sprintf("%s?filter[metric]=%s&filter[period]=%s&page[offset]=%d&page[limit]=%d",
		e.Request.URL.Path, url.QueryEscape(metric), url.QueryEscape(period), offset, limit)
}

// leaderboardScore is one account's score in one ranking.
type leaderboardScore struct {
	// userID is the account.
	userID string
	// score is what it scored.
	score int
}

// ranking computes the scores for a metric and a period.
//
// It is computed in Go over the plugin's own rows and the forum's posts,
// because PocketBase's query API cannot express the grouped aggregates the
// metrics are defined by, and a raw query would pin the plugin to one
// driver. The rows involved are bounded by the forum's own size, which is
// what upstream's own queries would scan too.
func (p *Plugin) ranking(e *core.RequestEvent, metric, period string) ([]leaderboardScore, error) {
	since, ok := periodStart(period)
	if !ok {
		return nil, errBadPeriod
	}

	votes, err := p.voteRows(e)
	if err != nil {
		return nil, err
	}

	posts, err := p.postRows(e)
	if err != nil {
		return nil, err
	}

	discussions, err := p.discussionRows(e)
	if err != nil {
		return nil, err
	}

	scores := map[string]int{}

	switch metric {
	case MetricPosts:
		for _, post := range posts {
			if !withinPeriod(post.createdAt, since) {
				continue
			}
			scores[post.userID]++
		}

	case MetricDiscussions:
		for _, discussion := range discussions {
			if !withinPeriod(discussion.createdAt, since) {
				continue
			}
			scores[discussion.userID]++
		}

	case MetricUpvotes:
		for _, vote := range votes {
			if vote.value <= 0 {
				continue
			}
			if !withinPeriod(vote.createdAt, since) {
				continue
			}
			post, ok := posts[vote.postID]
			if !ok {
				continue
			}
			scores[post.userID]++
		}

	case MetricVotes:
		for _, vote := range votes {
			if !withinPeriod(vote.createdAt, since) {
				continue
			}
			post, ok := posts[vote.postID]
			if !ok {
				continue
			}
			scores[post.userID] += vote.value
		}
	default:
		// Unreachable: the metric is validated before this point.
	}

	// A score of zero is not a ranking: upstream counts activity, so an
	// account that did nothing in the period is absent rather than last
	// with a zero.
	out := make([]leaderboardScore, 0, len(scores))
	for userID, score := range scores {
		if score <= 0 {
			continue
		}
		out = append(out, leaderboardScore{userID: userID, score: score})
	}

	// The order is the whole point of a ranking, so it is not the
	// client's to change.
	slices.SortFunc(out, func(a, b leaderboardScore) int {
		if a.score != b.score {
			// The higher score first.
			return b.score - a.score
		}
		// A stable tie-break, so the same forum always produces the same
		// board.
		return strings.Compare(a.userID, b.userID)
	})

	return out, nil
}

// voteRow is one vote.
type voteRow struct {
	postID    string
	userID    string
	value     int
	createdAt time.Time
}

// postRow is one post.
type postRow struct {
	userID    string
	createdAt time.Time
}

// discussionRow is one discussion.
type discussionRow struct {
	userID    string
	createdAt time.Time
}

// voteRows reads the plugin's votes.
func (p *Plugin) voteRows(e *core.RequestEvent) ([]voteRow, error) {
	collection, err := e.App.FindCollectionByNameOrId(p.physical[votesCollection])
	if err != nil {
		return nil, err
	}

	records, err := e.App.FindAllRecords(collection, nil)
	if err != nil {
		return nil, err
	}

	rows := make([]voteRow, 0, len(records))
	for _, record := range records {
		rows = append(rows, voteRow{
			postID:    record.GetString("post"),
			userID:    record.GetString("user"),
			value:     record.GetInt("value"),
			createdAt: record.GetDateTime("created_at").Time(),
		})
	}

	return rows, nil
}

// postRows reads the forum's posts.
func (p *Plugin) postRows(e *core.RequestEvent) (map[string]postRow, error) {
	collection, err := e.App.FindCollectionByNameOrId(p.postsPhysical)
	if err != nil {
		return nil, err
	}

	records, err := e.App.FindAllRecords(collection, nil)
	if err != nil {
		return nil, err
	}

	rows := make(map[string]postRow, len(records))
	for _, record := range records {
		// A hidden post is not a contribution: upstream's metrics all
		// exclude them.
		if !record.GetDateTime("hidden_at").IsZero() {
			continue
		}
		rows[record.Id] = postRow{
			userID:    record.GetString("user"),
			createdAt: record.GetDateTime("created_at").Time(),
		}
	}

	return rows, nil
}

// discussionRows reads the forum's discussions.
func (p *Plugin) discussionRows(e *core.RequestEvent) (map[string]discussionRow, error) {
	collection, err := e.App.FindCollectionByNameOrId(p.discussionsPhysical)
	if err != nil {
		return nil, err
	}

	records, err := e.App.FindAllRecords(collection, nil)
	if err != nil {
		return nil, err
	}

	rows := make(map[string]discussionRow, len(records))
	for _, record := range records {
		if !record.GetDateTime("hidden_at").IsZero() {
			continue
		}
		rows[record.Id] = discussionRow{
			userID:    record.GetString("user"),
			createdAt: record.GetDateTime("created_at").Time(),
		}
	}

	return rows, nil
}

// withinPeriod reports whether a timestamp falls in the period.
//
// A period with no start (all time) contains everything.
func withinPeriod(at time.Time, since time.Time) bool {
	if since.IsZero() {
		return true
	}
	return !at.Before(since)
}

// periodStart returns the start of a period.
//
// The zero time means "all time".
func periodStart(period string) (time.Time, bool) {
	now := time.Now().UTC()

	switch period {
	case PeriodDay:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), true
	case PeriodWeek:
		// The week starts on Monday, as ISO does.
		offset := (int(now.Weekday()) + 6) % 7
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -offset)
		return day, true
	case PeriodMonth:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), true
	case PeriodYear:
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC), true
	case PeriodAllTime:
		return time.Time{}, true
	default:
		return time.Time{}, false
	}
}

// knownMetric reports whether a metric exists.
func knownMetric(metric string) bool {
	switch metric {
	case MetricPosts, MetricDiscussions, MetricUpvotes, MetricVotes:
		return true
	default:
		return false
	}
}

// knownPeriod reports whether a period exists.
func knownPeriod(period string) bool {
	switch period {
	case PeriodDay, PeriodWeek, PeriodMonth, PeriodYear, PeriodAllTime:
		return true
	default:
		return false
	}
}

// leaderboardEntryID composes an entry's id.
//
// It names the RANKING, not just the place in it — see the handler. Upstream
// composes the same three parts with colons
// (`implode(':', [$metric, $period, $position])`), and a client that caches by
// type and id would otherwise hold two spellings of one entry.
func leaderboardEntryID(metric, period string, position int) string {
	return metric + ":" + period + ":" + strconv.Itoa(position)
}

// settingString reads a string forum setting.
//
// The value goes through `api.SettingString`, because the settings
// column is a JSON field: a STORED setting arrives as raw JSON bytes
// (`types.JSONRaw`), not as a Go string. A reader that switches only
// on `value.(string)` therefore never matches, the setting reads as
// unset, and the forum silently ignores what the operator saved —
// which is how "the setting is not read" happens.
func settingString(e *core.RequestEvent, key, fallback string) string {
	value, err := api.SettingValue(e, key, fallback)
	if err != nil {
		return fallback
	}

	text, ok := api.SettingString(value)
	if !ok || text == "" {
		return fallback
	}

	return text
}

// pageParams reads the offset pagination parameters.
//
// Upstream's resource is `->paginate(10, 50)`: ten entries a page, fifty
// at most. Both spellings are read — `page[limit]`/`page[offset]`, which
// is what `app.store.find` sends, and the flat `limit`/`offset` this port
// used before.
//
// The forum's own pagination is a page token; the leaderboard is a
// computed ranking, and a token over a computed set is meaningless, so the
// offset form upstream uses is the one that applies.
func pageParams(e *core.RequestEvent) (limit, offset int) {
	limit = defaultPageLimit
	offset = 0

	query := e.Request.URL.Query()

	if raw := pageParam(query, "limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = min(parsed, maxPageLimit)
		}
	}

	if raw := pageParam(query, "offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	return limit, offset
}

// pageParam reads a pagination parameter in either spelling.
//
// JSON:API's is `page[limit]`/`page[offset]` — that is what
// `app.store.find(type, { page: { offset, limit } })` sends — and the flat
// `limit`/`offset` is this port's older spelling.
func pageParam(query url.Values, name string) string {
	if value := strings.TrimSpace(query.Get("page[" + name + "]")); value != "" {
		return value
	}
	return strings.TrimSpace(query.Get(name))
}

// The page size upstream's resource declares (`paginate(10, 50)`).
const (
	// defaultPageLimit is the page size the client gets when it asks for
	// none.
	defaultPageLimit = 10
	// maxPageLimit is the largest page the resource will serve.
	maxPageLimit = 50
)

// errBadPeriod reports an unknown period.
var errBadPeriod = errors.New("gamification: the period is not known")

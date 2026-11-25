package statistics

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
)

// timedWindowYears is how far back the default time series reaches.
//
// Upstream returns two years of days plus the last 25 hours by the hour
// (`getTimedCounts`: `$startDate = new DateTime('-2 years')`), because its
// widest selector — "last 12 months" — is drawn from the same payload and
// the chart needs the PREVIOUS period's points too.
const timedWindowYears = 2

// hourlyWindow is how much of the tail is bucketed by the hour.
//
// Upstream: `$hourlyFrom = new DateTime('-25 hours')`. The extra hour
// overlaps the last daily bucket so "today" has a shape rather than a
// single point.
const hourlyWindow = 25 * time.Hour

// showStatisticsHandler serves GET /api/statistics.
//
// The contract is upstream's
// (`flarum-statistics/src/Api/Controller/ShowStatisticsData.php`):
//
//	?period=lifetime                 -> {"users": N, "discussions": N, "posts": N}
//	?model=users&period=timed        -> {"<unix seconds>": N, ...}
//	?model=users&period=custom
//	  &dateRange[start]=<unix sec>&dateRange[end]=<unix sec>
//	                                 -> {"<unix seconds>": N, ...}
//
// and it is ADMIN-ONLY. Upstream calls `assertAdmin()` before it reads the
// query at all, so a non-administrator learns nothing about the shape of
// the request — this does the same, and answers 403 rather than the 404 the
// rest of the forum uses for a hidden resource, because the endpoint's
// existence is not a secret but the numbers are.
//
// THE KEYS ARE UNIX SECONDS, NOT TIMESTAMPS. The admin page's widget sums
// the map over a window it computes in seconds
// (`StatisticsWidget::getPeriodCount`: `const time = parseInt(t); if (time
// >= period.start && time < period.end)`), so an RFC 3339 key is silently
// skipped and every chart reads zero.
func (p *Plugin) showStatisticsHandler(e *core.RequestEvent) error {
	actor, err := api.CurrentActor(e)
	if err != nil || actor.User == nil || !actor.User.IsAdmin {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You are not allowed to see the statistics.")
	}

	query := e.Request.URL.Query()
	period := query.Get("period")
	model := query.Get("model")

	if period == "lifetime" {
		return p.writeLifetime(e)
	}

	entity, ok := p.entities[model]
	if !ok {
		// A model is required for everything but the lifetime totals.
		// Answering with an empty series instead would draw an empty chart
		// and read as "no activity".
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A model must be specified.")
	}

	if period == "custom" {
		start, end, ok := customWindow(query)
		if !ok {
			return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A custom date range must be specified.")
		}

		// Upstream widens a custom range BACKWARDS by its own length
		// (`$startDate = (new Carbon($startDate))->subtract($diff)`), so
		// the chart can draw the previous period as a second series.
		return writeTimed(e, entity, start.Add(-end.Sub(start)), end)
	}

	now := time.Now().UTC()
	return writeTimed(e, entity, now.AddDate(-timedWindowYears, 0, 0), now)
}

// customWindow parses upstream's `dateRange[start]` / `dateRange[end]`.
//
// The values are UNIX SECONDS: the widget's date-range modal sends
// `VALUE.getTime() / 1000`. Go's query parser leaves the brackets in the
// key, so the lookup spells them out.
func customWindow(query url.Values) (start, end time.Time, ok bool) {
	startRaw := query.Get("dateRange[start]")
	endRaw := query.Get("dateRange[end]")

	startSeconds, err := strconv.ParseInt(startRaw, 10, 64)
	if err != nil || startSeconds <= 0 {
		return time.Time{}, time.Time{}, false
	}

	endSeconds, err := strconv.ParseInt(endRaw, 10, 64)
	if err != nil || endSeconds <= 0 {
		return time.Time{}, time.Time{}, false
	}

	start = time.Unix(startSeconds, 0).UTC()
	end = time.Unix(endSeconds, 0).UTC()

	if !end.After(start) {
		return time.Time{}, time.Time{}, false
	}

	return start, end, true
}

// writeLifetime answers the totals for every entity.
func (p *Plugin) writeLifetime(e *core.RequestEvent) error {
	totals := make(map[string]int, len(p.entities))

	for entity, spec := range p.entities {
		collection, err := e.App.FindCollectionByNameOrId(spec.collection)
		if err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The statistics could not be read.")
		}

		// The entity's predicate applies to the TOTAL too: upstream counts
		// `$this->query($model)->count()`, and that query carries the
		// `type = 'comment'` predicate. Counting every row here made the
		// dashboard's total for posts include rename events.
		total, err := e.App.CountRecords(collection, spec.exprs()...)
		if err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The statistics could not be read.")
		}

		totals[entity] = int(total)
	}

	e.Response.Header().Set("Content-Type", api.ContentType)
	return e.JSON(http.StatusOK, totals)
}

// writeTimed answers a per-bucket series for one entity, keyed by UNIX
// SECONDS.
//
// The grouping happens in Go rather than in SQL: PocketBase's own query API
// cannot express a `GROUP BY` over a formatted date, and a raw query would
// pin the plugin to one driver. Upstream groups in SQL and is therefore
// cheap over a large table; this reads the window's rows and buckets them,
// which is the trade this port already made and is bounded by the window.
//
// Two bucket sizes, as upstream has (`getTimedCounts`): everything older
// than the hourly tail is bucketed by the DAY, and the tail by the HOUR.
// Upstream runs it as two queries with an overlapping boundary and adds the
// overlap together; one pass with a single predicate is the same answer
// without the boundary arithmetic.
//
// A bucket with no records is ABSENT rather than zero, as upstream's is:
// the chart draws a line through the points it is given, and filling every
// gap would turn a quiet week into a flat line at zero.
func writeTimed(e *core.RequestEvent, entity statEntity, from, to time.Time) error {
	collection, err := e.App.FindCollectionByNameOrId(entity.collection)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The statistics could not be read.")
	}

	field := createdField(collection)

	filter := field + " >= {:from}"
	params := map[string]any{"from": from}
	if entity.filterColumn != "" {
		filter += " && " + entity.filterColumn + " = {:entityValue}"
		params["entityValue"] = entity.filterValue
	}

	records, err := e.App.FindRecordsByFilter(collection, filter, field, 0, 0, params)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The statistics could not be read.")
	}

	hourlyFrom := time.Now().UTC().Add(-hourlyWindow)

	counts := make(map[int64]int, len(records))

	for _, record := range records {
		created := record.GetDateTime(field)
		if created.IsZero() {
			continue
		}

		at := created.Time().UTC()
		if at.Before(from) || at.After(to) {
			continue
		}

		counts[bucketFor(at, hourlyFrom).Unix()]++
	}

	e.Response.Header().Set("Content-Type", api.ContentType)
	return e.JSON(http.StatusOK, counts)
}

// bucketFor returns the bucket a row created at `at` is counted in.
//
// The last 25 hours are bucketed by the HOUR and everything older by the
// DAY (`getTimedCounts`'s `$hourlyFrom`). It is a separate function because
// `created_at` on the forum's collections is a PocketBase AUTODATE field:
// the framework overwrites any value a test sets, so a row cannot be made
// old and the rule has to be exercised directly.
func bucketFor(at, hourlyFrom time.Time) time.Time {
	if at.After(hourlyFrom) {
		return at.Truncate(time.Hour)
	}
	return at.Truncate(24 * time.Hour)
}

// createdField resolves the column that carries a record's creation time.
//
// It is NOT always `created_at`. The forum's own collections declare that
// column explicitly (repository/mutations.go stamps it on every write), but
// the entity list also names the SHARED `users` collection, which is
// PocketBase's own auth collection — its system timestamp fields are
// `created` and `updated`, and there is no `created_at` at all.
//
// A hardcoded `created_at` therefore made `?model=users` a 500: PocketBase
// rejects a filter that names a column the collection does not have. The
// lifetime totals hid it, because they only count rows and never filter, and
// the test only exercised `model=discussions`.
func createdField(collection *core.Collection) string {
	if collection.Fields.GetByName("created_at") != nil {
		return "created_at"
	}
	return "created"
}

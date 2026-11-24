package statistics

import (
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
)

// timedWindowDays is how far back the time series reaches.
//
// Upstream reports two years of days plus the last 25 hours by the hour.
// One window is enough for the port's first cut: thirty days by day, which
// is what the chart's default view shows.
const timedWindowDays = 30

// showStatisticsHandler serves GET /api/statistics.
//
// The contract is upstream's
// (`flarum-statistics/src/Api/Controller/ShowStatisticsData.php`):
//
//	?period=lifetime          -> {"users": N, "discussions": N, "posts": N}
//	?model=users&period=<any> -> {"<RFC 3339 timestamp>": N, ...}
//
// and it is ADMIN-ONLY. Upstream calls `assertAdmin()` before it reads the
// query at all, so a non-administrator learns nothing about the shape of
// the request — this does the same, and answers 403 rather than the 404 the
// rest of the forum uses for a hidden resource, because the endpoint's
// existence is not a secret but the numbers are.
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

	if _, ok := p.entities[model]; !ok {
		// A model is required for everything but the lifetime totals.
		// Answering with an empty series instead would draw an empty chart
		// and read as "no activity".
		return api.WriteError(e, http.StatusUnprocessableEntity, api.CodeValidationFailed, "A model must be specified.")
	}

	return p.writeTimed(e, model)
}

// writeLifetime answers the totals for every entity.
func (p *Plugin) writeLifetime(e *core.RequestEvent) error {
	totals := make(map[string]int, len(p.entities))

	for entity, physical := range p.entities {
		collection, err := e.App.FindCollectionByNameOrId(physical)
		if err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The statistics could not be read.")
		}

		total, err := e.App.CountRecords(collection)
		if err != nil {
			return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The statistics could not be read.")
		}

		totals[entity] = int(total)
	}

	e.Response.Header().Set("Content-Type", api.ContentType)
	return e.JSON(http.StatusOK, totals)
}

// writeTimed answers a per-day series for one entity.
//
// The grouping happens in Go rather than in SQL: PocketBase's own query API
// cannot express a `GROUP BY` over a formatted date, and a raw query would
// pin the plugin to one driver. The cost is bounded by the window (thirty
// days), which is the same bound upstream puts on its chart.
//
// A day with no records is ABSENT rather than zero, as upstream's is: the
// chart draws a line through the points it is given, and filling every gap
// would turn a quiet week into a flat line at zero.
func (p *Plugin) writeTimed(e *core.RequestEvent, model string) error {
	collection, err := e.App.FindCollectionByNameOrId(p.entities[model])
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The statistics could not be read.")
	}

	from := time.Now().UTC().AddDate(0, 0, -timedWindowDays)

	records, err := e.App.FindRecordsByFilter(
		collection,
		"created_at >= {:from}",
		"created_at",
		0,
		0,
		map[string]any{"from": from},
	)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The statistics could not be read.")
	}

	counts := make(map[string]int, len(records))

	for _, record := range records {
		created := record.GetDateTime("created_at")
		if created.IsZero() {
			continue
		}

		// The key is the day, at midnight UTC, in the RFC 3339 form the
		// frontend's `new Date()` parses.
		day := created.Time().UTC().Truncate(24 * time.Hour)
		counts[day.Format(time.RFC3339)]++
	}

	e.Response.Header().Set("Content-Type", api.ContentType)
	return e.JSON(http.StatusOK, counts)
}

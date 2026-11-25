package statistics

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// urlValues turns a flat map into the url.Values the parser reads.
func urlValues(in map[string]string) url.Values {
	values := url.Values{}
	for key, value := range in {
		values.Set(key, value)
	}
	return values
}

// TestBucketFor_TheTailIsBucketedByTheHour pins the two bucket sizes.
//
// Upstream returns days for everything older than 25 hours and hours for the
// tail, in ONE payload (`getTimedCounts`: `$hourlyFrom = new DateTime('-25
// hours')`). A series that used the day bucket for the tail would collapse
// "today" to a single point, and the chart's today view has 24 points to
// draw.
//
// The rule is exercised DIRECTLY rather than through a backdated record:
// `created_at` on the forum's collections is a PocketBase AutodateField with
// `OnCreate: true`, so the framework overwrites any value a test sets and a
// row cannot be made old through the app.
func TestBucketFor_TheTailIsBucketedByTheHour(t *testing.T) {
	hourlyFrom := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	// Inside the tail: by the hour, so the minute and second are dropped.
	require.Equal(t,
		time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC),
		bucketFor(time.Date(2026, 11, 25, 14, 37, 12, 0, time.UTC), hourlyFrom),
	)

	// Older than the tail: by the day, so the time of day is dropped.
	require.Equal(t,
		time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		bucketFor(time.Date(2026, 11, 20, 17, 4, 9, 0, time.UTC), hourlyFrom),
	)

	// A row EXACTLY on the boundary belongs to the DAY bucket, not the tail:
	// upstream's daily pass runs `where($column, '>', $from)` up to
	// `min($end, $hourlyFrom)` INCLUSIVE, and its hourly pass starts strictly
	// after. Treating the boundary as hourly would count that row in both
	// passes, and upstream adds the overlap together.
	require.Equal(t,
		time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC),
		bucketFor(hourlyFrom, hourlyFrom),
	)
}

// TestBucketFor_AnHourBucketIsHourAligned pins what the widget actually
// sums: it compares `parseInt(key)` against second-based window edges, so a
// bucket that is not hour-aligned would straddle two windows.
func TestBucketFor_AnHourBucketIsHourAligned(t *testing.T) {
	now := time.Now().UTC()
	hourlyFrom := now.Add(-hourlyWindow)

	hour := bucketFor(now, hourlyFrom)
	require.Zero(t, hour.Unix()%3600, "an hour bucket must be hour-aligned: %v", hour)

	day := bucketFor(now.Add(-48*time.Hour), hourlyFrom)
	require.Zero(t, day.Unix()%86400, "a day bucket must be midnight-aligned: %v", day)
}

// TestCustomWindow pins the parser for upstream's `dateRange[start]` /
// `dateRange[end]`, which arrive as UNIX SECONDS under bracketed keys.
func TestCustomWindow(t *testing.T) {
	valid := urlValues(map[string]string{
		"dateRange[start]": "1700000000",
		"dateRange[end]":   "1700086400",
	})

	start, end, ok := customWindow(valid)
	require.True(t, ok, "a complete range must parse")
	require.Equal(t, int64(1700000000), start.Unix())
	require.Equal(t, int64(1700086400), end.Unix())

	for name, values := range map[string]map[string]string{
		"missing both":  {},
		"missing end":   {"dateRange[start]": "1700000000"},
		"missing start": {"dateRange[end]": "1700086400"},
		"zero start":    {"dateRange[start]": "0", "dateRange[end]": "1700086400"},
		"not a number":  {"dateRange[start]": "yesterday", "dateRange[end]": "1700086400"},
		"inverted":      {"dateRange[start]": "1700086400", "dateRange[end]": "1700000000"},
		"equal":         {"dateRange[start]": "1700000000", "dateRange[end]": "1700000000"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, ok := customWindow(urlValues(values))
			require.False(t, ok, "an incomplete or inverted range must be refused")
		})
	}
}

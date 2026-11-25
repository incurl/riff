// The statistics periods, and the arithmetic that turns a timed series into
// the two lines the chart draws.
//
// This file is PURE — it imports nothing — so the arithmetic can be tested
// without a browser, and so the widget has no excuse to re-derive it. The
// numbers are upstream's
// (`flarum-statistics/js/src/admin/components/StatisticsWidget.tsx`):
// windows are built in UNIX SECONDS from midnight UTC, and the "previous"
// series is the same window shifted back by exactly its own length.
//
// The wire payload is `{ "<unix seconds>": count }`. Upstream sums it with
//
//	for (const t in timed) { const time = parseInt(t); if (time >= start && time < end) count += timed[time]; }
//
// which is why the SERVER keys by unix seconds: a date string parses to NaN,
// the comparisons are all false, and every chart reads zero.

/**
 * A day, in seconds.
 */
export const DAY = 86400;

/**
 * An hour, in seconds.
 */
export const HOUR = 3600;

/**
 * The entities to report on, in the order the words appear.
 *
 * The forum document carries the list, published to administrators only
 * (upstream's `statisticsEntities`). The fallback is upstream's own
 * (`statisticsEntities.ts`: `?? ['users', 'discussions', 'posts']`), which is
 * what makes the page draw its built-in three even when the attribute is
 * absent.
 *
 * @param {string[] | null | undefined} attribute
 * @returns {string[]}
 */
export function statisticsEntities(attribute) {
  if (Array.isArray(attribute) && attribute.length > 0) return attribute;

  return ['users', 'discussions', 'posts'];
}

/**
 * Midnight UTC today, in seconds.
 *
 * The windows are anchored here rather than at "now" so that a bucket the
 * server put at midnight lines up with the window edge — anchoring at now
 * would clip the first bucket of every day.
 *
 * @param {number} nowSeconds
 * @returns {number}
 */
export function startOfToday(nowSeconds) {
  return Math.floor(nowSeconds / DAY) * DAY;
}

/**
 * The selectable periods.
 *
 * Every window's `start`/`end` is in seconds and `step` is the bucket the
 * chart labels each point with — an hour for "today", a day otherwise.
 *
 * @param {number} nowSeconds
 * @returns {Record<string, {start: number, end: number, step: number}>}
 */
export function periods(nowSeconds) {
  const today = startOfToday(nowSeconds);
  const oneYearAgo = subtractMonths(today, 12);

  return {
    today: { start: today, end: today + DAY, step: HOUR },
    last_7_days: { start: today - DAY * 7, end: today, step: DAY },
    previous_7_days: { start: today - DAY * 14, end: today - DAY * 7, step: DAY },
    last_28_days: { start: today - DAY * 28, end: today, step: DAY },
    previous_28_days: { start: today - DAY * 56, end: today - DAY * 28, step: DAY },
    last_12_months: { start: oneYearAgo, end: today, step: DAY },
    previous_12_months: { start: subtractMonths(oneYearAgo, 12), end: oneYearAgo, step: DAY },
  };
}

/**
 * The period a custom range selects.
 *
 * A custom range is bucketed by the day: the range comes from a date picker,
 * so the reader is asking about days, and the server's hourly tail only
 * covers the last 25 hours anyway.
 *
 * @param {{start: number, end: number}} range
 * @returns {{start: number, end: number, step: number}}
 */
export function customPeriod(range) {
  return { start: range.start, end: range.end, step: DAY };
}

/**
 * The number of records inside a window.
 *
 * A bucket is counted when it falls in `[start, end)`. The server may return
 * buckets outside the window (a custom range is widened backwards so the
 * previous series has points), so this filters rather than trusting the
 * payload's extent.
 *
 * @param {Record<string, number> | null | undefined} timed
 * @param {{start: number, end: number}} period
 * @returns {number}
 */
export function periodCount(timed, period) {
  if (!timed) return 0;

  let count = 0;

  for (const key in timed) {
    const at = parseInt(key, 10);

    if (Number.isNaN(at)) continue;

    if (at >= period.start && at < period.end) count += timed[key];
  }

  return count;
}

/**
 * The two series a period draws, and the labels under them.
 *
 * `thisPeriod` is the window itself and `lastPeriod` is the same window
 * shifted back by exactly its own length — upstream's comparison line, so a
 * rise can be read against the period before it.
 *
 * @param {Record<string, number> | null | undefined} timed
 * @param {{start: number, end: number, step: number}} period
 * @returns {{labels: number[], thisPeriod: number[], lastPeriod: number[]}}
 */
export function buildSeries(timed, period) {
  const length = period.end - period.start;
  const labels = [];
  const thisPeriod = [];
  const lastPeriod = [];

  for (let from = period.start; from < period.end; from += period.step) {
    labels.push(from);
    thisPeriod.push(periodCount(timed, { start: from, end: from + period.step }));
    lastPeriod.push(periodCount(timed, { start: from - length, end: from - length + period.step }));
  }

  return { labels, thisPeriod, lastPeriod };
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/**
 * The label for a bucket edge.
 *
 * Hours inside a day read as the hour; anything coarser reads as a date.
 *
 * @param {number} seconds
 * @param {number} step
 * @returns {string}
 */
export function bucketLabel(seconds, step) {
  const date = new Date(seconds * 1000);

  if (step < DAY) {
    const hour = date.getUTCHours();
    const suffix = hour < 12 ? 'AM' : 'PM';
    const display = hour % 12 === 0 ? 12 : hour % 12;

    return `${display} ${suffix}`;
  }

  return `${date.getUTCDate()} ${MONTHS[date.getUTCMonth()]}`;
}

/**
 * Subtract whole months from a second-based UTC timestamp.
 *
 * Done through `Date` so month lengths are the calendar's problem, and in UTC
 * to match the server's buckets.
 *
 * @param {number} seconds
 * @param {number} months
 * @returns {number}
 */
function subtractMonths(seconds, months) {
  const date = new Date(seconds * 1000);

  date.setUTCMonth(date.getUTCMonth() - months);
  date.setUTCHours(0, 0, 0, 0);

  return Math.floor(date.getTime() / 1000);
}

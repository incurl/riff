// The statistics arithmetic.
//
// This is the half that decides whether the chart is TRUE, so it is tested
// without a browser: `periods.js` is pure and the widget only draws what
// `buildSeries` hands it.

const { bucketLabel, buildSeries, periodCount, periods, startOfToday, statisticsEntities, customPeriod, DAY, HOUR } = require('./periods');

describe('statisticsEntities', () => {
  it('uses the forum document attribute when it carries one', () => {
    expect(statisticsEntities(['likes', 'posts'])).toEqual(['likes', 'posts']);
  });

  it("falls back to upstream's built-in three", () => {
    // upstream: `?? ['users', 'discussions', 'posts']`
    expect(statisticsEntities(null)).toEqual(['users', 'discussions', 'posts']);
    expect(statisticsEntities(undefined)).toEqual(['users', 'discussions', 'posts']);
    expect(statisticsEntities([])).toEqual(['users', 'discussions', 'posts']);
  });
});

describe('startOfToday', () => {
  it('floors to midnight UTC', () => {
    // 2026-11-25T10:42:13Z
    const now = Date.UTC(2026, 10, 25, 10, 42, 13) / 1000;

    expect(startOfToday(now)).toBe(Date.UTC(2026, 10, 25) / 1000);
  });
});

describe('periodCount', () => {
  it('sums only the buckets inside a half-open window', () => {
    const timed = { 100: 1, 200: 2, 300: 4 };

    expect(periodCount(timed, { start: 100, end: 300 })).toBe(3);
  });

  it('treats the window as [start, end)', () => {
    const timed = { 100: 1, 300: 4 };

    // The end bucket belongs to the NEXT window; counting it here would
    // count it twice.
    expect(periodCount(timed, { start: 100, end: 300 })).toBe(1);
  });

  it('ignores keys that are not unix seconds', () => {
    // A date-string key is what the port used to send. `parseInt` of one is
    // NaN, every comparison is false, and the chart reads zero — the failure
    // this contract exists to prevent.
    const timed = { '2026-11-25T00:00:00Z': 5, 200: 2 };

    expect(periodCount(timed, { start: 0, end: 1000 })).toBe(2);
  });

  it('answers 0 for a missing payload rather than NaN', () => {
    expect(periodCount(null, { start: 0, end: 1 })).toBe(0);
    expect(periodCount(undefined, { start: 0, end: 1 })).toBe(0);
  });
});

describe('buildSeries', () => {
  it('buckets a window into one point per step', () => {
    const period = { start: 0, end: 3 * DAY, step: DAY };
    const series = buildSeries({}, period);

    expect(series.labels).toHaveLength(3);
    expect(series.thisPeriod).toHaveLength(3);
    expect(series.lastPeriod).toHaveLength(3);
  });

  it('draws the previous period shifted back by exactly one period length', () => {
    const period = { start: 10 * DAY, end: 17 * DAY, step: DAY };
    const timed = { [9 * DAY]: 7 };
    const series = buildSeries(timed, period);

    // The first point of "this period" is day 10; the first point of the
    // previous period is day 3, so day 9 lands in the LAST point of the
    // previous series.
    expect(series.thisPeriod[0]).toBe(0);
    expect(series.lastPeriod[series.lastPeriod.length - 1]).toBe(7);
  });

  it('counts a bucket once, in the window it belongs to', () => {
    const period = { start: 0, end: 2 * DAY, step: DAY };
    const series = buildSeries({ 0: 1, [DAY]: 2 }, period);

    expect(series.thisPeriod).toEqual([1, 2]);
  });

  it('uses the hour step for today, so the day has 24 points', () => {
    const today = periods(Date.UTC(2026, 10, 25, 10) / 1000).today;

    expect(today.step).toBe(HOUR);
    expect(buildSeries({}, today).labels).toHaveLength(24);
  });
});

describe('periods', () => {
  it('offers the windows upstream offers', () => {
    const keys = Object.keys(periods(Date.UTC(2026, 10, 25) / 1000));

    expect(keys).toEqual([
      'today',
      'last_7_days',
      'previous_7_days',
      'last_28_days',
      'previous_28_days',
      'last_12_months',
      'previous_12_months',
    ]);
  });

  it('anchors every window on midnight UTC', () => {
    const now = Date.UTC(2026, 10, 25, 13, 5, 0) / 1000;

    for (const [name, period] of Object.entries(periods(now))) {
      if (name === 'previous_12_months') continue;

      expect(period.end % DAY).toBe(0);
    }
  });
});

describe('customPeriod', () => {
  it('buckets a custom range by the day', () => {
    expect(customPeriod({ start: 0, end: DAY * 3 })).toEqual({ start: 0, end: DAY * 3, step: DAY });
  });
});

describe('bucketLabel', () => {
  it('names the hour for an hourly step', () => {
    const noon = Date.UTC(2026, 10, 25, 12) / 1000;

    expect(bucketLabel(noon, HOUR)).toBe('12 PM');
    expect(bucketLabel(noon + HOUR, HOUR)).toBe('1 PM');
  });

  it('names the date for a daily step', () => {
    expect(bucketLabel(Date.UTC(2026, 10, 25) / 1000, DAY)).toBe('25 Nov');
  });
});

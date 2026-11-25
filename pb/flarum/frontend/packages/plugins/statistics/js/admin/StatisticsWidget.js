// The statistics widget: the totals, the entity selector, the period
// selector, and the chart.
//
// It mirrors upstream's `StatisticsWidget` in the parts that are behaviour:
//
//   * the lifetime totals come from `?period=lifetime` and are shown per
//     entity, so the reader sees the number before the chart draws;
//   * the chart comes from `?period=timed&model=<entity>`, ONE request per
//     entity, because the entity is part of the window's identity;
//   * the period is selected client-side and the series are summed from the
//     single payload (`buildSeries`), which is why the server returns two
//     years at once.
//
// A request that fails shows the failure rather than an empty chart: an
// empty chart reads as "no activity", which is a different and wrong
// statement.
//
// The selectors are native <select> elements rather than upstream's
// `SelectDropdown`. The two do the same job, and a native control cannot
// drift from the port's component vocabulary — the value it hands back is the
// entity or period id, which is exactly what the widget needs.
import app from 'flarum/admin/app';
import DashboardWidget from 'flarum/admin/components/DashboardWidget';
import LoadingIndicator from 'flarum/common/components/LoadingIndicator';

import StatisticsChart from './StatisticsChart';
import { bucketLabel, buildSeries, periods, statisticsEntities } from './periods';

/**
 * The period labels, in the order the dropdown shows them.
 *
 * Plain strings rather than `app.translator.trans(...)`: this port vendors no
 * upstream extension locale files, so a translation key would render as the
 * key itself.
 */
const PERIOD_LABELS = {
  today: 'Today',
  last_7_days: 'Last 7 days',
  previous_7_days: 'Previous 7 days',
  last_28_days: 'Last 28 days',
  previous_28_days: 'Previous 28 days',
  last_12_months: 'Last 12 months',
  previous_12_months: 'Previous 12 months',
};

/**
 * The human label for an entity.
 *
 * Upstream reads `flarum-statistics.admin.statistics.{name}_heading`; this
 * port has no such catalogue, so the built-in three are named here and an
 * unknown entity falls back to its id — which is what an extension-added
 * entity would show until it ships a label.
 *
 * @param {string} entity
 * @returns {string}
 */
export function entityLabel(entity) {
  switch (entity) {
    case 'users':
      return 'Users';
    case 'discussions':
      return 'Discussions';
    case 'posts':
      return 'Posts';
    default:
      return entity;
  }
}

export default class StatisticsWidget extends DashboardWidget {
  oninit(vnode) {
    super.oninit(vnode);

    this.entities = statisticsEntities(app.forum.attribute('statisticsEntities'));
    this.selectedEntity = this.entities[0];
    this.selectedPeriod = 'last_28_days';

    this.lifetime = null;
    this.lifetimeError = false;

    this.timed = {};
    this.timedLoading = {};
    this.timedError = {};

    this.loadLifetime();
    this.loadTimed(this.selectedEntity);
  }

  className() {
    return 'StatisticsWidget';
  }

  loadLifetime() {
    app
      .request({
        method: 'GET',
        url: app.forum.attribute('apiUrl') + '/statistics',
        params: { period: 'lifetime' },
      })
      .then((totals) => {
        this.lifetime = totals;
        this.lifetimeError = false;
        m.redraw();
      })
      .catch(() => {
        this.lifetimeError = true;
        m.redraw();
      });
  }

  /**
   * Load one entity's timed series.
   *
   * Cached per entity: switching back and forth between two entities must not
   * re-request a payload that does not change.
   */
  loadTimed(entity) {
    if (this.timed[entity] || this.timedLoading[entity]) return;

    this.timedLoading[entity] = true;

    app
      .request({
        method: 'GET',
        url: app.forum.attribute('apiUrl') + '/statistics',
        params: { period: 'timed', model: entity },
      })
      .then((series) => {
        this.timed[entity] = series;
        this.timedLoading[entity] = false;
        this.timedError[entity] = false;
        m.redraw();
      })
      .catch(() => {
        this.timedLoading[entity] = false;
        this.timedError[entity] = true;
        m.redraw();
      });
  }

  content() {
    return (
      <div>
        <div className="StatisticsWidget-header">
          <h3 className="StatisticsWidget-title">Statistics</h3>

          {this.entitySelector()}
          {this.periodSelector()}
        </div>

        {this.totals()}
        {this.chart()}
      </div>
    );
  }

  entitySelector() {
    if (this.entities.length < 2) return null;

    return (
      <select
        className="StatisticsWidget-entity Select"
        onchange={(event) => {
          this.selectedEntity = event.target.value;
          this.loadTimed(this.selectedEntity);
        }}
      >
        {this.entities.map((entity) => (
          <option value={entity} selected={entity === this.selectedEntity}>
            {entityLabel(entity)}
          </option>
        ))}
      </select>
    );
  }

  periodSelector() {
    return (
      <select
        className="StatisticsWidget-period Select"
        onchange={(event) => {
          this.selectedPeriod = event.target.value;
        }}
      >
        {Object.keys(PERIOD_LABELS).map((period) => (
          <option value={period} selected={period === this.selectedPeriod}>
            {PERIOD_LABELS[period]}
          </option>
        ))}
      </select>
    );
  }

  totals() {
    if (this.lifetimeError) {
      return <div className="StatisticsWidget-error">The statistics could not be read.</div>;
    }

    if (!this.lifetime) return <LoadingIndicator />;

    return (
      <div className="StatisticsWidget-totals">
        {this.entities.map((entity) => (
          <div className="StatisticsWidget-total">
            <span className="StatisticsWidget-totalValue">{this.lifetime[entity] ?? 0}</span>
            <span className="StatisticsWidget-totalLabel">{entityLabel(entity)}</span>
          </div>
        ))}
      </div>
    );
  }

  chart() {
    const entity = this.selectedEntity;

    if (this.timedError[entity]) {
      return <div className="StatisticsWidget-error">The statistics could not be read.</div>;
    }

    if (this.timedLoading[entity] || !this.timed[entity]) return <LoadingIndicator />;

    const period = periods(Math.floor(Date.now() / 1000))[this.selectedPeriod];

    if (!period) return null;

    const series = buildSeries(this.timed[entity], period);

    return (
      <StatisticsChart
        labels={series.labels}
        labelFor={(seconds) => bucketLabel(seconds, period.step)}
        datasets={[
          { name: 'This period', values: series.thisPeriod },
          { name: 'Previous period', values: series.lastPeriod },
        ]}
      />
    );
  }
}

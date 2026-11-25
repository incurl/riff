// The dashboard's mini statistics widget.
//
// Upstream adds this to `DashboardPage`'s `availableWidgets` at priority 20,
// so the admin's landing page opens with the instance's totals. It shows the
// three numbers and nothing else: the chart belongs on the page, where there
// is room for it, and upstream's mini widget has no link either.
import app from 'flarum/admin/app';
import DashboardWidget from 'flarum/admin/components/DashboardWidget';
import LoadingIndicator from 'flarum/common/components/LoadingIndicator';

import { statisticsEntities } from './periods';
import { entityLabel } from './StatisticsWidget';

export default class MiniStatisticsWidget extends DashboardWidget {
  oninit(vnode) {
    super.oninit(vnode);

    this.entities = statisticsEntities(app.forum.attribute('statisticsEntities'));
    this.totals = null;
    this.failed = false;

    app
      .request({
        method: 'GET',
        url: app.forum.attribute('apiUrl') + '/statistics',
        params: { period: 'lifetime' },
      })
      .then((totals) => {
        this.totals = totals;
        m.redraw();
      })
      .catch(() => {
        this.failed = true;
        m.redraw();
      });
  }

  className() {
    return 'MiniStatisticsWidget';
  }

  content() {
    if (this.failed) {
      return <div className="StatisticsWidget-error">The statistics could not be read.</div>;
    }

    if (!this.totals) return <LoadingIndicator />;

    return (
      <div>
        <h3 className="StatisticsWidget-title">Statistics</h3>

        <ul className="MiniStatisticsWidget-list">
          {this.entities.map((entity) => (
            <li className="MiniStatisticsWidget-item">
              <span className="MiniStatisticsWidget-value">{this.totals[entity] ?? 0}</span>
              <span className="MiniStatisticsWidget-label">{entityLabel(entity)}</span>
            </li>
          ))}
        </ul>
      </div>
    );
  }
}

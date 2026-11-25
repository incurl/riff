// The Statistics extension's admin page.
//
// Upstream registers `Extend.Admin().page(StatisticsPage)` and the page
// renders its `StatisticsWidget` — the totals, the entity and period
// selectors, and the chart. The custom page REPLACES the plain settings list,
// which is why this port declares no settings for the extension: there are
// none to render, and a page plus a settings list would fight over the same
// screen.
//
// The page body is the widget, so the dashboard and the page draw the same
// thing from the same code path — a divergence between them would be two
// answers to one question.
import ExtensionPage from 'flarum/admin/components/ExtensionPage';

import StatisticsWidget from './StatisticsWidget';

export default class StatisticsPage extends ExtensionPage {
  content() {
    return (
      <div className="StatisticsPage">
        <StatisticsWidget />
      </div>
    );
  }
}

// The Statistics extension's admin initialiser.
//
// It mirrors upstream's `js/src/admin/index.tsx` + `extend.tsx` exactly:
//
//   * `Extend.Admin().page(StatisticsPage)` makes the extension's page the
//     statistics page — the admin application renders a custom page INSTEAD
//     of the plain settings list, and this extension has no settings;
//   * `app.initializers.add(...)` adds the mini widget to the DASHBOARD at
//     priority 20, so the admin's landing page opens with the instance's
//     totals.
//
// The initialiser id is the composer name flattened (`riff/statistics` ->
// `riff-statistics`), which is what upstream uses for the same purpose
// (`app.initializers.add('flarum-statistics', ...)`).
//
// The page registration is the `extend` array; the dashboard registration is
// an initialiser because it needs the application's `DashboardPage`
// prototype to exist — the initialisers run inside `app.boot()`, after the
// app has loaded.
import app from 'flarum/admin/app';
import { extend as extendMethod } from 'flarum/common/extend';
import Extend from 'flarum/common/extenders';
import DashboardPage from 'flarum/admin/components/DashboardPage';

import StatisticsPage from './admin/StatisticsPage';
import MiniStatisticsWidget from './admin/MiniStatisticsWidget';

export const extend = [new Extend.Admin().page(StatisticsPage)];

app.initializers.add('riff-statistics', () => {
  extendMethod(DashboardPage.prototype, 'availableWidgets', function (widgets) {
    widgets.add('statistics', <MiniStatisticsWidget />, 20);
  });
});

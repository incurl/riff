// The `/rankings` page: the leaderboard.
//
// It is upstream's `RankingsPage`, which reads a dedicated COMPUTED
// endpoint rather than a sorted list of users: an entry's score and
// position belong to the metric and period being asked for rather than to
// the person, so there is no stored row to sort.
//
// Everything the page needs to start is on the FORUM document
// (`canViewRankingPage`, `fof-gamification.leaderboardMetrics`,
// `.leaderboardPeriods`, `.defaultLeaderboardMetric`,
// `.defaultLeaderboardPeriod`): the selectors are built from it before a
// single entry is fetched, and the page it opens on is the administrator's
// choice.
//
// NOT ported, deliberately: `meta.standing` ("Find me"), `meta.highlights`
// and the podium images. Each is a second computation over a previous
// window this port does not keep, and a control that could not answer is
// worse than an absent one.
import app from 'flarum/forum/app';
import Page from 'flarum/common/components/Page';
import PageStructure from 'flarum/forum/components/PageStructure';
import LoadingIndicator from 'flarum/common/components/LoadingIndicator';
import Link from 'flarum/common/components/Link';
import Select from 'flarum/common/components/Select';
import Button from 'flarum/common/components/Button';
import Avatar from 'flarum/common/components/Avatar';
import username from 'flarum/common/helpers/username';

import { ranksToShow, rankLabel } from '../models';

const PAGE_LIMIT = 20;

export default class RankingsPage extends Page {
  oninit(vnode) {
    super.oninit(vnode);

    this.bodyClass = 'App--rankings';

    if (!app.forum.attribute('canViewRankingPage')) {
      m.route.set('/');
      return;
    }

    // The metric and the period live in the URL, so a board can be linked
    // to and survives a refresh. The fallbacks are the SAME ones the
    // server publishes, so an unconfigured forum and a configured one open
    // on the same board.
    this.metric = m.route.param('metric') || app.forum.attribute('fof-gamification.defaultLeaderboardMetric') || 'posts';
    this.period = m.route.param('period') || app.forum.attribute('fof-gamification.defaultLeaderboardPeriod') || 'year';

    this.entries = [];
    this.loading = true;
    this.loadingMore = false;
    this.hasMore = false;

    this.refresh();
  }

  metrics() {
    return app.forum.attribute('fof-gamification.leaderboardMetrics') || [];
  }

  periods() {
    return app.forum.attribute('fof-gamification.leaderboardPeriods') || [];
  }

  refresh() {
    this.loading = true;
    this.entries = [];

    return this.load(0).then(() => {
      this.loading = false;
      m.redraw();
    });
  }

  load(offset) {
    return app.store
      .find(
        'leaderboard-entries',
        {
          filter: { metric: this.metric, period: this.period },
          page: { offset, limit: PAGE_LIMIT },
        },
        { payloadType: 'api' }
      )
      .then((results) => {
        this.entries.push(...results);

        // Trust the API's order rather than re-sorting here: sorting each
        // page as it arrives interleaves it with the ones already loaded.
        const payload = results.payload;
        this.hasMore = Boolean(payload?.links?.next);

        return results;
      })
      .catch(() => {
        this.hasMore = false;
      });
  }

  loadMore() {
    this.loadingMore = true;

    this.load(this.entries.length).then(() => {
      this.loadingMore = false;
      m.redraw();
    });
  }

  changeMetric(metric) {
    this.metric = metric;
    m.route.set(app.route('rankings', { metric, period: this.period }), {}, { replace: true });
    this.refresh();
  }

  changePeriod(period) {
    this.period = period;
    m.route.set(app.route('rankings', { metric: this.metric, period }), {}, { replace: true });
    this.refresh();
  }

  view() {
    return (
      <PageStructure className="RankingsPage" hero={() => this.hero()} sidebar={() => this.sidebar()} loading={this.loading}>
        {this.loading ? <LoadingIndicator /> : this.content()}
      </PageStructure>
    );
  }

  hero() {
    return (
      <header className="Hero RankingsPage-hero">
        <div className="container">
          <h1 className="RankingsPage-title">Rankings</h1>
          {this.selectors()}
        </div>
      </header>
    );
  }

  sidebar() {
    return null;
  }

  /**
   * The metric and period selectors.
   *
   * A selector with no options is not drawn at all: an empty dropdown reads
   * as "nothing to choose", and it is the shape a control takes when the
   * forum document does not carry its list.
   */
  selectors() {
    const metrics = this.metrics();
    const periods = this.periods();

    if (metrics.length === 0) return null;

    const metricOptions = {};
    metrics.forEach((metric) => {
      metricOptions[metric.key] = metric.label;
    });

    const periodOptions = {};
    periods.forEach((period) => {
      periodOptions[period] = period.charAt(0).toUpperCase() + period.slice(1);
    });

    return (
      <div className="RankingsPage-selectors">
        <Select options={metricOptions} value={this.metric} onchange={(value) => this.changeMetric(value)} />
        {periods.length > 0 ? <Select options={periodOptions} value={this.period} onchange={(value) => this.changePeriod(value)} /> : null}
      </div>
    );
  }

  content() {
    if (this.entries.length === 0) {
      return <div className="RankingsPage-empty">Nobody has made the leaderboard yet.</div>;
    }

    return (
      <div className="RankingsPage-content">
        <table className="RankingsPage-table">
          <thead>
            <tr>
              <th className="RankingsPage-place">Place</th>
              <th className="RankingsPage-member">Member</th>
              <th className="RankingsPage-score">Points</th>
            </tr>
          </thead>
          <tbody>{this.entries.map((entry) => this.row(entry))}</tbody>
        </table>
        {this.hasMore ? (
          <Button className="Button RankingsPage-more" loading={this.loadingMore} onclick={() => this.loadMore()}>
            Load more
          </Button>
        ) : null}
      </div>
    );
  }

  row(entry) {
    const user = entry.user ? entry.user() : null;
    const ranks = user ? ranksToShow(user, app.forum) : [];

    return (
      <tr className="RankingsPage-row" key={entry.id()}>
        <td className="RankingsPage-place">{entry.position()}</td>
        <td className="RankingsPage-member">
          {user ? (
            <Link href={app.route.user(user)} className="RankingsPage-user">
              <Avatar user={user} /> <span className="RankingsPage-username">{username(user)}</span>
            </Link>
          ) : (
            <span className="RankingsPage-username">A deleted account</span>
          )}
          {ranks.length > 0 ? <span className="RankingsPage-ranks">{ranks.map((rank) => rankLabel(rank))}</span> : null}
        </td>
        <td className="RankingsPage-score">{entry.score()}</td>
      </tr>
    );
  }
}

// The Gamification extension's admin initialiser.
//
// Upstream's `fof/gamification` uses a CUSTOM PAGE with a large
// settings surface (~24 settings, plus rank management, image uploads
// and a group picker). This port registers only the settings the
// SERVER actually READS — a control that changes nothing would be a
// lie about what the forum does.
//
// The three settings are upstream's own:
// `fof-gamification.allowSelfVotes`,
// `fof-gamification.defaultMetric` and
// `fof-gamification.defaultPeriod`.
//
// Upstream's `defaultMetric` and `defaultPeriod` are SELECTs whose
// options come from the FORUM document
// (`fof-gamification.leaderboardMetrics` and
// `fof-gamification.leaderboardPeriods`). This port has no such
// document attributes, so the options are listed here — and they are
// the exact set the server accepts (`knownMetric` / `knownPeriod` in
// `pb/flarum/plugins/gamification/leaderboard.go`).
//
// The label and the help text are plain strings rather than
// `app.translator.trans(...)` calls: this port vendors no upstream
// extension locale files, so a translation key would render as the
// key itself.
import Extend from 'flarum/common/extenders';

export const extend = [
  new Extend.Admin()
    .section(() => ({
      id: 'voting',
      icon: 'fas fa-thumbs-up',
      label: 'Voting',
      description: 'How members vote on posts, and what the leaderboard shows.',
    }))
    .setting(() => ({
      setting: 'fof-gamification.allowSelfVotes',
      section: 'voting',
      type: 'bool',
      label: 'Allow voting for your own posts',
      help: 'When enabled, a member may vote for their own post.',
    }))
    .setting(() => ({
      setting: 'fof-gamification.defaultMetric',
      section: 'voting',
      type: 'select',
      label: 'Default leaderboard metric',
      help: 'The metric the leaderboard shows when no metric is asked for.',
      options: {
        posts: 'Posts',
        discussions: 'Discussions',
        upvotes: 'Upvotes',
        votes: 'Votes',
      },
      default: 'posts',
    }))
    .setting(() => ({
      setting: 'fof-gamification.defaultPeriod',
      section: 'voting',
      type: 'select',
      label: 'Default leaderboard period',
      help: 'The period the leaderboard covers when no period is asked for.',
      options: {
        day: 'Day',
        week: 'Week',
        month: 'Month',
        year: 'Year',
        all: 'All time',
      },
      default: 'year',
    }))
    .setting(() => ({
      setting: 'fof-gamification.rankAmt',
      section: 'voting',
      type: 'number',
      label: 'Rank badges shown next to a name',
      help: 'A member who has passed more ranks wears the highest few, so a name is not buried under a wall of labels. Leave empty to show them all.',
      default: 2,
    })),
];

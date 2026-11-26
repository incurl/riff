// The Gamification extension's JSON:API models.
//
// Both types are registered with `new Extend.Store().add(type, Model)`,
// which is what the store requires before it will hold a resource of that
// type: `pushObject` throws "Pushing object of type `ranks` not allowed"
// for a type nobody registered.
//
// The accessors are the attribute names the server sends, and they are
// MODEL METHODS because that is how the frontend reads them
// (`rank.name()`, `entry.position()`). A plain property would not exist.
import Model from 'flarum/common/Model';

/** A rank: a name, a colour and the points needed to reach it. */
export class Rank extends Model {
  name() {
    return Model.attribute('name').call(this);
  }

  color() {
    return Model.attribute('color').call(this);
  }

  points() {
    return Model.attribute('points').call(this);
  }
}

/**
 * One place on the leaderboard.
 *
 * Entries are computed per request rather than stored, so the position and
 * the score belong to THIS ranking: the same person has a different entry
 * under a different metric or period.
 */
export class LeaderboardEntry extends Model {
  position() {
    return Model.attribute('position').call(this);
  }

  score() {
    return Model.attribute('score').call(this);
  }

  metric() {
    return Model.attribute('metric').call(this);
  }

  period() {
    return Model.attribute('period').call(this);
  }

  /**
   * Places gained since the previous window, or null.
   *
   * This port always sends null: the board is computed per request and no
   * previous window is kept, which is what upstream itself sends for
   * all-time.
   */
  movement() {
    return Model.attribute('movement').call(this);
  }

  user() {
    return Model.hasOne('user').call(this);
  }

  ranks() {
    return Model.hasMany('ranks').call(this);
  }
}

/**
 * A rank's label, coloured the way the rank is.
 *
 * It is upstream's `rankLabel`, and the colour is set inline because it is
 * the OPERATOR's choice per rank — a stylesheet cannot know it.
 */
export function rankLabel(rank, attrs = {}) {
  attrs.className = 'Rank-badge ' + (attrs.className || '');
  attrs.style = { ...(attrs.style || {}), backgroundColor: rank.color() || '#888' };

  return <span {...attrs}>{rank.name()}</span>;
}

/**
 * The ranks a member wears, highest first.
 *
 * Upstream attaches EVERY rank whose threshold the member has passed and
 * renders them as a stack, trimmed to `fof-gamification.rankAmt` (the
 * forum document's `rankAmt`) so a name is not buried under a wall of
 * labels. The order is highest-first because the highest rank is the one
 * that says the most.
 */
export function ranksToShow(user, forum) {
  if (!user || typeof user.ranks !== 'function') return [];

  // `hasMany` keeps an identifier whose resource has not arrived, and
  // calling `position()` on it throws — which takes the whole post down.
  const ranks = (user.ranks() || []).filter(Boolean);

  const sorted = ranks.sort((a, b) => (b.points() || 0) - (a.points() || 0));

  const amount = Number(forum?.attribute('fof-gamification.rankAmt'));
  if (!Number.isFinite(amount) || amount <= 0) return sorted;

  return sorted.slice(0, amount);
}

/**
 * `ranksToShow`, the pure half of the rank stack.
 *
 * Two things it must get right, both of which render as "the badge is
 * missing" rather than as an error:
 *
 *   - a `hasMany` keeps an IDENTIFIER whose resource has not arrived in
 *     `included`, and that entry has no `points()` method — calling it throws
 *     and takes the post down with it;
 *   - the operator's `fof-gamification.rankAmt` trims the stack, and it is a
 *     SETTING, so an unset or empty value must mean "show them all" rather
 *     than "show none".
 */
import { beforeEach, describe, expect, it } from '@jest/globals';

let ranksToShow: any;

beforeEach(async () => {
  ({ ranksToShow } = await import('./models'));
});

/** A rank, as the store holds it. */
function rank(name: string, points: number) {
  return {
    name: () => name,
    color: () => '#123456',
    points: () => points,
  };
}

/** A user whose `ranks()` answers the given entries. */
function userWith(ranks: any[]) {
  return { ranks: () => ranks };
}

/** A forum document that answers one attribute. */
function forumWith(rankAmt: any) {
  return {
    attribute: (key: string) => (key === 'fof-gamification.rankAmt' ? rankAmt : undefined),
  };
}

describe('ranksToShow', () => {
  it('shows the highest ranks first, trimmed to rankAmt', () => {
    const shown = ranksToShow(userWith([rank('Rookie', 1), rank('Veteran', 100), rank('Regular', 10)]), forumWith(2));

    expect(shown.map((entry: any) => entry.name())).toEqual(['Veteran', 'Regular']);
  });

  it('shows them all when the setting is unset', () => {
    const shown = ranksToShow(userWith([rank('Rookie', 1), rank('Veteran', 100)]), forumWith(undefined));

    expect(shown).toHaveLength(2);
  });

  it('drops an identifier whose resource has not arrived', () => {
    const shown = ranksToShow(userWith([undefined, rank('Veteran', 100)]), forumWith(3));

    expect(shown.map((entry: any) => entry.name())).toEqual(['Veteran']);
  });

  it('answers nothing for a guest', () => {
    expect(ranksToShow(null, forumWith(2))).toEqual([]);
  });
});

// The picker's filter.
//
// A picker that offers a tag the caller may not use is a DEAD AFFORDANCE:
// the author selects it, submits, and the server refuses the save. That
// became real when the restricted-tag permission started being enforced
// (`domain.PermissionChecker.CanScoped`), so the two rules the pickers ask
// are pinned here:
//
//   - a NEW discussion offers only the tags the caller may START in;
//   - an EXISTING discussion offers the ones the caller may ADD to, PLUS
//     whatever it already carries — a tag that may no longer be added must
//     still be removable.
//
// The function is pure so it can be tested without Mithril: the same
// arrangement as `js/admin/scope.js`.

const { selectableTags } = require('./selectableTags');

/**
 * A stand-in for a `Tag` model: the picker only reads the id and the two
 * permission attributes.
 */
function tag(id, { canStart, canAdd } = {}) {
  const model = {
    id: () => id,
  };

  if (canStart !== undefined) model.canStartDiscussion = () => canStart;
  if (canAdd !== undefined) model.canAddToDiscussion = () => canAdd;

  return model;
}

/**
 * A stand-in for a `Discussion` model: the picker only reads its tags.
 */
function discussion(tags) {
  return { tags: () => tags };
}

const ids = (tags) => tags.map((entry) => entry.id());

describe('selectableTags', () => {
  it('offers every tag a new discussion may start in', () => {
    const tags = [tag('a', { canStart: true }), tag('b', { canStart: false })];

    expect(ids(selectableTags(tags, null))).toEqual(['a']);
  });

  it('offers a tag that may be added to an existing discussion', () => {
    const tags = [tag('a', { canAdd: true }), tag('b', { canAdd: false })];

    expect(ids(selectableTags(tags, discussion([])))).toEqual(['a']);
  });

  it('keeps a tag the discussion already carries even when it may not be added', () => {
    // The rule that matters: the author must be able to REMOVE a tag they
    // can no longer add, or the discussion is stuck with it.
    const tags = [tag('a', { canAdd: false }), tag('b', { canAdd: false })];

    expect(ids(selectableTags(tags, discussion([tag('a', { canAdd: false })])))).toEqual(['a']);
  });

  it('matches the current tags by id, not by object identity', () => {
    // The discussion's relationship resolves through the store, so its tag
    // objects are fresh instances: an identity check would keep nothing.
    const tags = [tag('a', { canAdd: false })];

    expect(ids(selectableTags(tags, discussion([tag('a', { canAdd: true })])))).toEqual(['a']);
  });

  it('ignores identifiers the store could not resolve', () => {
    // `hasMany` keeps an unresolved identifier as `undefined`; reading
    // `id()` off it would throw and take the whole modal down.
    const tags = [tag('a', { canAdd: true })];

    expect(ids(selectableTags(tags, discussion([undefined, null])))).toEqual(['a']);
  });

  it('does not offer a tag whose permission attribute is missing', () => {
    // A tag pushed into the store by another code path may lack the
    // attributes, and a missing answer is not a "yes".
    expect(selectableTags([tag('a')], null)).toEqual([]);
  });

  it('offers nothing when every tag is forbidden', () => {
    const tags = [tag('a', { canStart: false }), tag('b', { canStart: false })];

    expect(selectableTags(tags, null)).toEqual([]);
  });

  it('tolerates an empty or absent tag list', () => {
    expect(selectableTags([], null)).toEqual([]);
    expect(selectableTags(undefined, null)).toEqual([]);
  });
});

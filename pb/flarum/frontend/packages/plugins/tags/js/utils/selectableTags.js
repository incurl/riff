// The tags a picker may OFFER.
//
// Upstream's `getSelectableTags`: a tag the caller may not use is not
// listed, because offering it produces a selection the server then
// REFUSES. The rule differs between the two pickers:
//
//   - a NEW discussion is filtered on `canStartDiscussion()` — the caller
//     is choosing where the discussion will live, and a restricted tag
//     asks the scoped question;
//   - an EXISTING discussion is filtered on `canAddToDiscussion()`, plus
//     whatever the discussion already carries — a tag that may no longer
//     be ADDED must still be REMOVABLE, or the author is stuck with it.
//
// This is load-bearing rather than cosmetic: a restricted tag is really
// restricted on the SERVER (the scoped permission check), so an
// unfiltered picker is a dead affordance — the author chooses a tag and
// the save comes back refused.
//
// It is a pure function in its own module so it can be unit-tested
// without Mithril, the same arrangement as `js/admin/scope.js`.

/**
 * Whether a tag may be STARTED in.
 *
 * The method is guarded: a tag pushed into the store by another code path
 * may lack the attribute, and a missing attribute is not a "yes".
 *
 * @param {object} tag
 * @returns {boolean}
 */
function canStart(tag) {
  return typeof tag.canStartDiscussion === 'function' && !!tag.canStartDiscussion();
}

/**
 * Whether a tag may be ADDED to an existing discussion.
 *
 * @param {object} tag
 * @returns {boolean}
 */
function canAdd(tag) {
  return typeof tag.canAddToDiscussion === 'function' && !!tag.canAddToDiscussion();
}

/**
 * The ids of the tags a discussion already carries.
 *
 * Comparison is by ID, not by object identity: the discussion's
 * relationship resolves through the store, and a store object is a fresh
 * instance whenever the payload arrives again. `hasMany` also keeps the
 * identifiers it could not resolve, so an `undefined` is dropped rather
 * than read.
 *
 * @param {object} discussion
 * @returns {Set<string>}
 */
function currentTagIDs(discussion) {
  const tags = typeof discussion.tags === 'function' ? discussion.tags() || [] : [];

  return new Set(tags.filter(Boolean).map((tag) => tag.id()));
}

/**
 * The tags a picker may offer.
 *
 * @param {object[]} tags every tag in the store
 * @param {object | null | undefined} discussion the discussion being edited, if any
 * @returns {object[]}
 */
export function selectableTags(tags, discussion) {
  const all = (tags || []).filter(Boolean);

  if (!discussion) {
    return all.filter(canStart);
  }

  const current = currentTagIDs(discussion);

  return all.filter((tag) => canAdd(tag) || current.has(tag.id()));
}

export default selectableTags;

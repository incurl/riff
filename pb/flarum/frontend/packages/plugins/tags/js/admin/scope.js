// The admin permission scope's pure parts.
//
// They are separated from the extender because they are the half worth
// testing without a browser: the extender wires them into the grid,
// and a mistake in either half is the same mistake.
//
// The scope is what makes a RESTRICTED tag restrict. A tag that inherits
// the global grant restricts nobody, and the member group holds
// `startDiscussion` globally — so the scoped key has to be the SCOPED
// one, or the operator closes a tag that stays open.

/**
 * The prefix of a scoped permission key.
 *
 * It is upstream's spelling: `tag{id}.{permission}`.
 *
 * The id is the tag's own id, and THIS PORT'S tag ids are PocketBase
 * ids — 15 characters of digits and letters — not upstream's integers.
 * A pattern that demanded a numeric id would never match the key this
 * forum's own admin grid writes.
 */
const SCOPED_KEY = /^tag[0-9A-Za-z]+\./;

/**
 * The scoped key for a tag and a permission.
 *
 * @param {string} tagID
 * @param {string} permission
 * @returns {string}
 */
export function scopedKey(tagID, permission) {
  return `tag${tagID}.${permission}`;
}

/**
 * The global part of a scoped key, or the key itself when it is not one.
 *
 * It is what the grid asks about for the REQUIRED permissions: a scoped
 * key is not a permission the forum knows, so the lookup has to strip the
 * scope first.
 *
 * @param {string} permission
 * @returns {string}
 */
export function globalPermission(permission) {
  const match = (permission || '').match(SCOPED_KEY);

  return match ? permission.substring(match[0].length) : permission;
}

/**
 * The required permissions of a scoped key, scoped again.
 *
 * The global permission's requirements carry the scope back, because a
 * group that may see the forum must be able to see the tag too.
 *
 * @param {string[]} required
 * @param {string} permission
 * @returns {string[]}
 */
export function scopedRequired(required, permission) {
  const match = (permission || '').match(SCOPED_KEY);

  if (!match) {
    return required;
  }

  return required.map((entry) => match[0] + entry);
}

/**
 * The permissions a restricted tag may carry its own grant for.
 *
 * They are upstream's: the forum-wide ones and the discussion ones. A
 * permission that is not in this list is not scoped — upstream's own
 * rule, and the reason a `tag{id}.{permission}` key is only written for
 * these.
 */
export const SCOPED_PERMISSIONS = ['viewForum', 'startDiscussion'];

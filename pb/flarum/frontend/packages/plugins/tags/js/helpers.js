// The Tags extension's helpers.
//
// They mirror upstream `flarum/tags`'s `js/src/common/helpers/` and
// `js/src/common/utils/sortTags.tsx`, so the admin page sorts and
// renders tags the way upstream's does.

import app from 'flarum/forum/app';
import classList from 'flarum/common/utils/classList';
import Icon from 'flarum/common/components/Icon';
import Link from 'flarum/common/components/Link';

/**
 * A tag's icon.
 *
 * It is upstream's `tagIcon`: the tag's own Font Awesome classes, in
 * the tag's colour when there is one.
 */
export function tagIcon(tag, attrs = {}, settings = {}) {
  const hasIcon = tag && tag.icon();
  const { useColor = true } = settings;

  attrs.className = classList([attrs.className, 'text-colored', !hasIcon && 'icon TagIcon']);

  if (tag && useColor) {
    attrs.style = attrs.style || {};
    attrs.style['--color'] = tag.color();
  } else if (!tag) {
    attrs.className += ' untagged';
  }

  // Rendered through the Icon component so the global icon behaviour
  // (a forced FontAwesome style, say) applies to tag icons too.
  return hasIcon ? <Icon name={tag.icon()} {...attrs} /> : <span {...attrs} />;
}

/**
 * Sort tags the way upstream's `sortTags` does.
 *
 * The order is not a plain position sort: a SECONDARY tag has no
 * position at all, so the rule is by kind first, and then by position
 * within the kind. Two tags of the same kind under the SAME parent
 * compare their own positions, because a child's position is local to
 * its parent; two CHILDREN compare their PARENTS' positions, because
 * that is where the child sits in the overall order.
 *
 * A secondary tag sorts by its discussion count, descending.
 */
export function sortTags(tags) {
  // `hasMany` maps a relationship's identifiers through the store WITHOUT
  // dropping the ones that have not arrived, so `discussion.tags()` can hold
  // `undefined` entries. Sorting one would throw on `position()`, and the
  // whole discussion list would fail to render — a much worse outcome than a
  // missing badge.
  return (tags || []).filter(Boolean).slice(0).sort((a, b) => {
    const aPos = a.position();
    const bPos = b.position();

    // Both secondary: by discussion count, descending.
    if (aPos === null && bPos === null) return b.discussionCount() - a.discussionCount();

    // Just one secondary: the primary tag comes first.
    if (bPos === null) return -1;
    if (aPos === null) return 1;

    // Both primary from here on.
    const aParent = a.parent();
    const bParent = b.parent();

    // The same parent: the positions are local, so compare them
    // directly.
    if (aParent === bParent) return aPos - bPos;

    // Both children: compare the parents' positions.
    if (aParent && bParent) return aParent.position() - bParent.position();

    // A child against its own parent: the parent comes first.
    if (aParent) return aParent === b ? 1 : aParent.position() - bPos;
    if (bParent) return bParent === a ? -1 : aPos - bParent.position();

    return 0;
  });
}

/**
 * A tag's page path.
 *
 * It is the ROUTE PATH, not `app.route('tag', ...)`: the Link component renders
 * through `m.route.Link`, which prepends the router's own prefix. Passing the
 * already-prefixed URL would double the base path on a sub-path deploy.
 *
 * @param {object} tag
 * @returns {string}
 */
export function tagPath(tag) {
  return '/t/' + tag.slug();
}

/**
 * One tag, as a label.
 *
 * Upstream's `common/helpers/tagsLabel`:
 *
 *	tagLabel(tag, { link }) -> <span class="TagsLabel-item">…</span>
 *
 * The colour is a DOT rather than the text colour, so a pale tag colour stays
 * readable; the icon is the tag's own Font Awesome class.
 *
 * @param {object} tag
 * @param {{link?: boolean}} options
 * @returns {object}
 */
export function tagLabel(tag, { link = false } = {}) {
  const children = [
    tag.color() ? <span className="TagsLabel-dot" style={{ background: tag.color() }} /> : null,
    tagIcon(tag, { className: 'TagsLabel-icon' }),
    tag.name(),
  ];

  const attrs = {
    className: 'TagsLabel-item',
    title: tag.description() || null,
    'data-tag-id': tag.id(),
  };

  return link ? (
    <Link {...attrs} href={tagPath(tag)}>
      {children}
    </Link>
  ) : (
    <span {...attrs}>{children}</span>
  );
}

/**
 * A list of tags, as one label group.
 *
 * Upstream renders the tags SORTED, so the primary tag leads — the order is
 * what makes the first tag's colour the hero's colour.
 *
 * @param {object[]} tags
 * @param {{link?: boolean}} options
 * @returns {object}
 */
export function tagsLabel(tags, { link = false, className } = {}) {
  return (
    <span className={classList('TagsLabel', className)}>
      {sortTags(tags).map((tag) => tagLabel(tag, { link }))}
    </span>
  );
}

// The in-flight (or settled) tag-list request.
//
// It is a single shared promise so a list of 20 discussions issues one request,
// not 20 — and so `/tags`, the labels and the sidebar all read the same payload.
let tagsRequest = null;

/**
 * Load the tag list into the store.
 *
 * A failure is not fatal: the discussions still render, just without tags. The
 * promise is cleared on failure so a later redraw retries.
 *
 * It lives here rather than in `forum.js` because the labels, the tag page and
 * the filter all need it, and a second copy would be a second request.
 *
 * @returns {Promise<object>}
 */
export function loadTags() {
  if (!tagsRequest) {
    tagsRequest = app
      .request({ url: app.forum.attribute('apiUrl') + '/tags' })
      .then((payload) => {
        app.store.pushPayload(payload);

        return payload;
      })
      .catch((error) => {
        tagsRequest = null;
        throw error;
      });
  }

  return tagsRequest;
}

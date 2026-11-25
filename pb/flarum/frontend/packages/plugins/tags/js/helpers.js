// The Tags extension's helpers.
//
// They mirror upstream `flarum/tags`'s `js/src/common/helpers/` and
// `js/src/common/utils/sortTags.tsx`, so the admin page sorts and
// renders tags the way upstream's does.

import classList from 'flarum/common/utils/classList';
import Icon from 'flarum/common/components/Icon';

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
  return tags.slice(0).sort((a, b) => {
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

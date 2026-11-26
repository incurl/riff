// The "Parent tag" field's arithmetic: which tags may be a parent, and in
// which order they are offered.
//
// It is a PURE module (no `app`, no mithril) so the rules can be tested without
// booting an admin app. The admin modal is the only caller.
//
// The tree is two levels — a root tag (a "primary" one when it is placed) and
// its children — so the only tags that can be a parent are the root ones with a
// position. A tag that is unplaced (`position() === null`) has no place in the
// tree yet, and a CHILD cannot have children of its own: the page renders one
// level of nesting, and a grandchild would be listed nowhere. The server
// refuses both (see `resolveTagParent`), so offering them here would be a
// control that fails on save.

/**
 * Whether a tag may be the parent of another tag.
 *
 * `tag` is a `Tag` model (or anything with `position()`/`isChild()`).
 */
export function isParentable(tag) {
  if (!tag) return false;

  const position = tag.position();

  return position !== null && position !== undefined && !tag.isChild();
}

/**
 * The options for the parent select.
 *
 * The first entry is the empty one: "No parent" is a real choice — it makes the
 * tag an unplaced secondary tag, which is what the "Secondary tags" list holds
 * until one is dragged under a primary tag.
 *
 * The tag being edited is excluded: a tag cannot be its own parent, and the
 * server answers 422 for it.
 *
 * @param {Array} tags every tag in the store
 * @param {string|null} tagID the tag being edited, null when creating one
 */
export function parentOptions(tags, tagID = null) {
  const options = { '': 'No parent' };

  (tags || [])
    .filter((tag) => tag && tag.id() && tag.id() !== tagID && isParentable(tag))
    .sort((a, b) => String(a.name()).localeCompare(String(b.name())))
    .forEach((tag) => {
      options[tag.id()] = tag.name();
    });

  return options;
}

/**
 * The parent value to send with a tag save.
 *
 * It is an ATTRIBUTE rather than a relationship, because `Model.save` drops a
 * null relationship: a relationship can say "the parent is X" but not "the
 * parent is nobody", and clearing it is a value the page offers. An empty
 * string is what the server reads as "no parent".
 */
export function parentValue(tag) {
  const parent = tag && typeof tag.parent === 'function' ? tag.parent() : null;

  return parent ? parent.id() : '';
}

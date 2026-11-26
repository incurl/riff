// Whether a tag selection satisfies the count rules.
//
// A PURE module, so the rule the confirm button is disabled on can be tested
// without Mithril or an app: the same four numbers are enforced by the server
// (`validateTagCounts`), and a button that offered a save the server refuses
// would be a dead affordance.
//
// A tag is PRIMARY when it has a position and is not a child of another tag —
// upstream's rule, and the server's: a child carries a position of its own, so
// counting by position alone would let a child satisfy the primary minimum.

/**
 * Whether a tag is a primary one.
 *
 * @param {object} tag a `Tag` model
 * @returns {boolean}
 */
export function isPrimaryTag(tag) {
  return tag.position() !== null && !tag.isChild();
}

/**
 * Whether the selection satisfies the limits.
 *
 * @param {object[]} selected the chosen tags
 * @param {{min: {primary: number, secondary: number}, max: {primary: number, secondary: number}}} limits
 * @returns {boolean}
 */
export function selectionMeetsLimits(selected, limits) {
  const tags = selected || [];
  const primary = tags.filter(isPrimaryTag).length;
  const secondary = tags.length - primary;

  const min = limits?.min || { primary: 0, secondary: 0 };
  const max = limits?.max || { primary: Infinity, secondary: Infinity };

  return primary >= min.primary && secondary >= min.secondary && primary <= max.primary && secondary <= max.secondary;
}

/**
 * The tag picker's COMMIT and its count rules.
 *
 * The modal shipped without a confirm button, so the only ways out were the
 * header's close button and the backdrop — and neither calls `onsubmit`. The
 * report was "composer tag selection does not stick": the author picked tags,
 * left the modal, and the selection was silently thrown away.
 *
 * Both halves are asserted here: `selectionMeetsLimits` (the rule the button
 * is disabled on, the same one the server enforces) and `actions()` (that the
 * button IS a submit control, and that it is disabled when the rule fails).
 */
import { describe, expect, it } from '@jest/globals';

import { isPrimaryTag, selectionMeetsLimits } from './selectionLimits';
import TagSelectionModal from '../components/TagSelectionModal';

/** A tag, as the store holds it. */
function tag(id: string, name: string, position: number | null, isChild = false) {
  return {
    id: () => id,
    name: () => name,
    position: () => position,
    isChild: () => isChild,
  };
}

const LIMITS = { min: { primary: 1, secondary: 0 }, max: { primary: 1, secondary: 3 } };

describe('selectionMeetsLimits', () => {
  it('accepts one primary tag, which is the default rule', () => {
    expect(selectionMeetsLimits([tag('a', 'News', 0)], LIMITS)).toBe(true);
  });

  it('refuses an empty selection while a primary tag is required', () => {
    expect(selectionMeetsLimits([], LIMITS)).toBe(false);
  });

  it('refuses more primary tags than the maximum', () => {
    expect(selectionMeetsLimits([tag('a', 'News', 0), tag('b', 'Guides', 1)], LIMITS)).toBe(false);
  });

  it('accepts a child as a secondary tag, not as a primary one', () => {
    const child = tag('c', 'Updates', 0, true);

    expect(isPrimaryTag(child)).toBe(false);
    expect(selectionMeetsLimits([tag('a', 'News', 0), child], LIMITS)).toBe(true);
  });

  it('refuses more secondary tags than the maximum', () => {
    const selected = [tag('a', 'News', 0), tag('c', 'One', 0, true), tag('d', 'Two', 1, true), tag('e', 'Three', 2, true), tag('f', 'Four', 3, true)];

    expect(selectionMeetsLimits(selected, LIMITS)).toBe(false);
  });
});

describe('TagSelectionModal#actions', () => {
  /** A modal-shaped `this`, enough for `actions()`. */
  function modal(selected: any[], limits = LIMITS) {
    return { selected, limits, attrs: {}, meetsRequirements: TagSelectionModal.prototype.meetsRequirements };
  }

  /** The submit control inside the actions row. */
  function submitButton(selected: any[]) {
    const row: any = TagSelectionModal.prototype.actions.call(modal(selected));
    const children = Array.isArray(row.children) ? row.children : [row.children];

    return children.find((child: any) => child && child.attrs && child.attrs.type === 'submit');
  }

  it('offers a submit control, which is what commits the selection', () => {
    const button = submitButton([tag('a', 'News', 0)]);

    expect(button).toBeTruthy();
    expect(button.attrs.disabled).toBe(false);
  });

  it('disables the control while the rules are unmet', () => {
    const button = submitButton([]);

    expect(button).toBeTruthy();
    expect(button.attrs.disabled).toBe(true);
  });
});

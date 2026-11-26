/**
 * The "Parent tag" select's arithmetic.
 *
 * Two things it must get right, and both render as a control that fails on
 * save rather than as an error:
 *
 *   - the options are the ROOT tags only. The tree is two levels, so a child
 *     cannot be a parent (the server answers 422), and an unplaced tag has no
 *     place in the tree yet;
 *   - the tag being edited is never an option. A tag cannot be its own parent.
 */
import { describe, expect, it } from '@jest/globals';

import { isParentable, parentOptions, parentValue } from './tagParent';

/** A tag, as the store holds it. */
function tag(id: string, name: string, position: number | null, isChild = false) {
  return {
    id: () => id,
    name: () => name,
    position: () => position,
    isChild: () => isChild,
  };
}

describe('parentOptions', () => {
  it('offers the placed roots, by name, after "No parent"', () => {
    const options = parentOptions([tag('b', 'Guides', 1), tag('a', 'News', 0)], null);

    expect(Object.keys(options)).toEqual(['', 'b', 'a']);
    expect(options['']).toBe('No parent');
    expect(options.a).toBe('News');
  });

  it('leaves out a child, because a child cannot have children', () => {
    const options = parentOptions([tag('a', 'News', 0), tag('c', 'Updates', 0, true)], null);

    expect(options.c).toBeUndefined();
  });

  it('leaves out an unplaced tag, which has no place in the tree yet', () => {
    const options = parentOptions([tag('a', 'News', 0), tag('u', 'Later', null)], null);

    expect(options.u).toBeUndefined();
  });

  it('leaves out the tag being edited, and a tag that does not exist yet', () => {
    const options = parentOptions([tag('a', 'News', 0), tag('b', 'Guides', 1), tag(undefined as any, 'New', null)], 'a');

    expect(options.a).toBeUndefined();
    expect(options.b).toBe('Guides');
    expect(Object.keys(options)).toEqual(['', 'b']);
  });
});

describe('isParentable', () => {
  it('requires a position and no parent', () => {
    expect(isParentable(tag('a', 'News', 0))).toBe(true);
    expect(isParentable(tag('a', 'News', null))).toBe(false);
    expect(isParentable(tag('a', 'News', 0, true))).toBe(false);
    expect(isParentable(null)).toBe(false);
  });
});

describe('parentValue', () => {
  it('answers the parent id, or an empty string', () => {
    expect(parentValue({ parent: () => ({ id: () => 'a' }) })).toBe('a');
    expect(parentValue({ parent: () => null })).toBe('');
    expect(parentValue(null)).toBe('');
  });
});

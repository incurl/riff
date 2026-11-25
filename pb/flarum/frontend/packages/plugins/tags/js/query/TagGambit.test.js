// The `tag:` gambit's pure parts.
//
// Two of them are load-bearing and neither is visible in a rendered page:
//
//   - `predicates = true`. `GambitManager.apply` CONCATENATES the values of a
//     gambit that declares predicates, and overwrites otherwise — so without
//     the flag `tag:a tag:b` sends only `b`, and the second filter silently
//     replaces the first.
//   - `gambitValueToFilterValue` returns an ARRAY. The server reads
//     `filter[tag]` as a list of OR-groups, so a bare string would be read as
//     one group but could not accumulate a second occurrence.
//
// `fromFilter` turns a filter back into search text, which is what the URL
// round-trip does: several values are several gambits separated by SPACES
// (AND), while a comma inside one value is one OR-group.

const TagGambit = require('./TagGambit').default;

describe('TagGambit', () => {
  it('declares predicates, so repeated gambits accumulate', () => {
    expect(new TagGambit().predicates).toBe(true);
  });

  it('writes the server filter key', () => {
    expect(new TagGambit().filterKey()).toBe('tag');
  });

  it('sends one gambit as an array of one, which is one OR-group', () => {
    expect(new TagGambit().gambitValueToFilterValue('guides')).toEqual(['guides']);
  });

  it('keeps a comma list as ONE filter value', () => {
    // The comma is the server's OR separator, so it survives the conversion
    // untouched: `tag:a,b` is one group of two tags.
    expect(new TagGambit().gambitValueToFilterValue('a,b')).toEqual(['a,b']);
  });

  it('renders a single value as one gambit', () => {
    expect(new TagGambit().fromFilter('guides', false)).toBe('tag:guides');
  });

  it('renders a negation with a leading minus', () => {
    expect(new TagGambit().fromFilter(['guides'], true)).toBe('-tag:guides');
  });

  it('renders several values as several SPACE-separated gambits', () => {
    // Spaces, not commas: the values are AND-ed groups, and joining them with
    // a comma would collapse them into one OR-group.
    expect(new TagGambit().fromFilter(['a', 'b'], false)).toBe('tag:a tag:b');
  });
});

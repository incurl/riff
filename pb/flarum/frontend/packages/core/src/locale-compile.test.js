const { filterForFrontend, flatten, resolveReferences } = require('../locale-compile.cjs');

/**
 * The two rules that are easy to get wrong, and whose failure is silent in
 * the UI:
 *
 *  - a `=> other.key` value is a REFERENCE, not a literal. Leaving it
 *    unresolved renders `=> core.ref.foo` as the label.
 *  - the per-frontend filter keeps `*.forum.*`/`*.lib.*` in the forum
 *    bundle and `*.admin.*`/`*.lib.*` in the admin one. Getting it wrong
 *    loads admin-only strings into the forum (or drops the labels the
 *    forum needs), and nothing errors.
 */
describe('locale-compile', () => {
  it('flattens a nested catalogue into dotted keys', () => {
    const flat = flatten({ core: { forum: { header: { back: 'Back to index' } } }, validation: { required: 'Required' } });

    expect(flat).toEqual({ 'core.forum.header.back': 'Back to index', 'validation.required': 'Required' });
  });

  it('resolves a reference to another key', () => {
    const resolved = resolveReferences({ 'core.ref.back': 'Back to index', 'core.forum.header.back': '=> core.ref.back' });

    expect(resolved['core.forum.header.back']).toBe('Back to index');
  });

  it('resolves a chain of references', () => {
    const resolved = resolveReferences({
      'core.ref.x': 'Read more',
      'core.ref.y': '=> core.ref.x',
      'core.forum.z': '=> core.ref.y',
    });

    expect(resolved['core.forum.z']).toBe('Read more');
  });

  it('leaves a cyclic reference alone rather than looping forever', () => {
    const resolved = resolveReferences({ 'core.ref.a': '=> core.ref.b', 'core.ref.b': '=> core.ref.a' });

    // Still a reference: the compiler throws on it, which is the loud
    // failure the build wants. Hanging or silently emitting `=> …` is not.
    expect(resolved['core.ref.a']).toBe('=> core.ref.b');
  });

  it('keeps forum and lib keys for the forum bundle', () => {
    const filtered = filterForFrontend(
      {
        'core.forum.a': 'A',
        'core.admin.b': 'B',
        'core.lib.c': 'C',
        'core.ref.d': 'D',
      },
      'forum'
    );

    expect(Object.keys(filtered).sort()).toEqual(['core.forum.a', 'core.lib.c']);
  });

  it('keeps admin and lib keys for the admin bundle', () => {
    const filtered = filterForFrontend(
      {
        'core.forum.a': 'A',
        'core.admin.b': 'B',
        'core.lib.c': 'C',
        'validation.required': 'D',
      },
      'admin'
    );

    expect(Object.keys(filtered).sort()).toEqual(['core.admin.b', 'core.lib.c']);
  });

  it('filters an extension namespace by the same rule', () => {
    // An extension's keys carry the frontend segment too, which is what
    // makes a language pack's `flarum-tags.yml` split correctly.
    const filtered = filterForFrontend(
      { 'flarum-tags.forum.all_tags': 'All tags', 'flarum-tags.admin.nav': 'Tags', 'flarum-tags.lib.x': 'X' },
      'forum'
    );

    expect(Object.keys(filtered).sort()).toEqual(['flarum-tags.forum.all_tags', 'flarum-tags.lib.x']);
  });
});

/**
 * An extension's settings page must be REACHABLE.
 *
 * The admin app's extension page lives at `/extension/:id`, and the id
 * of a Flarum extension is `vendor/name` — it CONTAINS A SLASH. The
 * route builder substitutes the id into the path verbatim, so an id
 * with a slash becomes TWO path segments, and a route pattern with one
 * `:id` segment does not match it: the page is unreachable.
 *
 * The symptom is that every extension in the sidebar links to a URL the
 * router cannot resolve, so clicking one lands on the dashboard instead
 * of the extension's settings page.
 *
 * Upstream avoids this by flattening the id at the PAYLOAD boundary:
 * `Extension::nameToId()` turns `flarum/tags` into `flarum-tags`, and
 * that flattened id is both the key of `app.data.extensions` and the
 * route parameter. The Go side does the same, so the two agree.
 */

/**
 * The route builder, in the shape the vendored core implements it.
 *
 * It is inlined rather than imported because the builder reaches for
 * `m.buildQueryString` and `this.forum`, both of which are part of the
 * running application.
 */
function buildRoute(path, params) {
  return path.replace(/:([^/]+)/g, (_m, key) => `${params[key]}`);
}

describe("the admin app's extension route", () => {
  it('cannot build a route for an id that contains a slash', () => {
    // This is the shape the bug took: the raw id is NOT a path
    // segment, so it must never reach the route builder.
    const url = buildRoute('/extension/:id', { id: 'riff/likes' });

    expect(url.split('/').filter(Boolean)).toHaveLength(3);
  });

  it('builds a route for the FLATTENED id', () => {
    const url = buildRoute('/extension/:id', { id: 'riff-likes' });

    // One segment for the id, so the route matches.
    expect(url.split('/').filter(Boolean)).toHaveLength(2);
    expect(url).toBe('/extension/riff-likes');
  });

  it('flattens the id the way upstream does', () => {
    // Upstream's `Extension::nameToId()`:
    //   "flarum/tags"               -> "flarum-tags"
    //   "flarum/flarum-ext-suspend" -> "flarum-suspend"
    //   "some-vendor/foo"           -> "some-vendor-foo"
    const nameToId = (name) =>
      name === 'flarum/core' ? 'core' : name.replace('/flarum-ext-', '-').replace('/flarum-', '-').replace('/', '-');

    expect(nameToId('flarum/tags')).toBe('flarum-tags');
    expect(nameToId('flarum/flarum-ext-suspend')).toBe('flarum-suspend');
    expect(nameToId('some-vendor/foo')).toBe('some-vendor-foo');
    expect(nameToId('riff/likes')).toBe('riff-likes');
  });
});

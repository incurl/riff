/**
 * A plugin's admin bundle must REGISTER the settings it edits.
 *
 * The admin application renders a setting that a bundle registered with
 * `Extend.Admin().setting(...)` and nothing else — the Go side's
 * `plugin.AddSettings` only names the setting and its default; the FORM
 * is the bundle's job. So a declared setting with no bundle is a
 * setting the operator cannot reach.
 *
 * A plugin may also register a CUSTOM PAGE (`Extend.Admin().page(...)`),
 * and then the form lives in that page instead: upstream's `flarum/tags`
 * does exactly that, because its extension page is the tag TREE with
 * the settings on a second tab. The admin application renders a custom
 * page INSTEAD of the plain settings list, so a plugin that registers
 * one must not also register plain settings — the two would fight over
 * the same page.
 *
 * The test reads the bundle SOURCES, because the contract is the key
 * the bundle registers, and the built bundle is not committed.
 *
 * The keys are upstream's own, so an operator's saved setting carries
 * over and means what it means upstream.
 */

const fs = require('fs');
const path = require('path');

/**
 * The plugins that expose settings, and the upstream keys each one edits.
 *
 * `page` names the file the settings live in when the plugin registers
 * a custom page instead of the plain settings list.
 */
const PLUGIN_SETTINGS = {
  likes: { keys: ['flarum-likes.like_own_post'] },
  gamification: { keys: ['fof-gamification.allowSelfVotes', 'fof-gamification.defaultMetric', 'fof-gamification.defaultPeriod'] },
  tags: {
    // `allow_tag_change` is upstream's FLAT key (no `flarum-tags.` prefix):
    // the server reads it by that name (api.AllowTagChangeSetting) and the
    // admin payload publishes it there, so the control binds to the same
    // string the reader uses.
    keys: [
      'flarum-tags.min_primary_tags',
      'flarum-tags.max_primary_tags',
      'flarum-tags.min_secondary_tags',
      'flarum-tags.max_secondary_tags',
      'allow_tag_change',
    ],
    page: 'admin/TagsPage.js',
  },
};

/** The plugins directory, relative to this test. */
const PLUGINS_DIR = path.resolve(__dirname, '..', '..', '..', 'plugins');

/** The setting key as it appears in a bundle: a quoted string. */
/**
 * The setting key as it appears in a bundle.
 *
 * Two shapes: the registry's `setting: 'key'` (the plain settings
 * list) and a custom page's `this.setting('key', ...)` (the page's
 * own settings streams).
 */
const SETTING_PATTERN = /(?:setting:\s*|\.setting\()'([^']+)'/g;

/**
 * The settings a source registers.
 */
function registeredSettings(source) {
  const keys = [];
  let match;

  SETTING_PATTERN.lastIndex = 0;
  while ((match = SETTING_PATTERN.exec(source)) !== null) {
    keys.push(match[1]);
  }

  return keys;
}

/**
 * The source a plugin's settings live in: the admin bundle, or the
 * custom page it registers.
 */
function settingsSource(plugin, config) {
  const base = path.join(PLUGINS_DIR, plugin, 'js');

  return config.page ? fs.readFileSync(path.join(base, config.page), 'utf8') : fs.readFileSync(path.join(base, 'admin.js'), 'utf8');
}

describe("the plugins' admin bundles", () => {
  it.each(Object.entries(PLUGIN_SETTINGS))('%s registers its upstream settings', (plugin, config) => {
    const source = settingsSource(plugin, config);

    for (const key of config.keys) {
      expect(source).toContain(key);
    }
  });

  it.each(Object.entries(PLUGIN_SETTINGS))('%s registers nothing else', (plugin, config) => {
    const source = settingsSource(plugin, config);

    // A setting nobody reads is a control that changes nothing, and
    // the operator turns it and the forum does not move.
    expect(registeredSettings(source).sort()).toEqual([...config.keys].sort());
  });

  it.each(Object.entries(PLUGIN_SETTINGS))('%s registers through the admin vocabulary', (plugin, config) => {
    const bundle = fs.readFileSync(path.join(PLUGINS_DIR, plugin, 'js', 'admin.js'), 'utf8');

    // The bundle must register through the admin application's own
    // vocabulary, or the form renders the wrong control.
    expect(bundle).toContain('Extend.Admin()');
  });

  it('the mentions bundle registers nothing, because the plugin has no settings', () => {
    const bundle = fs.readFileSync(path.join(PLUGINS_DIR, 'mentions', 'js', 'admin.js'), 'utf8');

    // Upstream's only setting turns the bare `@username` form off, and
    // this port has no other form: turning it off would leave no way to
    // mention at all. So the plugin declares no settings, and its admin
    // bundle registers none.
    expect(bundle).not.toContain('setting:');
  });

  it('the tags bundle registers the tag tree page', () => {
    const bundle = fs.readFileSync(path.join(PLUGINS_DIR, 'tags', 'js', 'admin.js'), 'utf8');

    // Upstream's extension page is the tag TREE, so the plugin
    // registers a custom page. The admin application renders it
    // instead of the plain settings list — which is why the settings
    // live in the page and not in the bundle.
    expect(bundle).toContain('.page(');

    // The store needs to know the `tags` type, or the page's tag list
    // is refused: `pushObject` throws for an unregistered type.
    expect(bundle).toContain("add('tags'");
  });
});

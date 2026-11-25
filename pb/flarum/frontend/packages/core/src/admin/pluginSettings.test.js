/**
 * A plugin's admin bundle must REGISTER the settings it edits.
 *
 * The admin application renders a setting that a bundle registered with
 * `Extend.Admin().setting(...)` and nothing else — the Go side's
 * `plugin.AddSettings` only names the setting and its default; the FORM
 * is the bundle's job. So a declared setting with no bundle is a
 * setting the operator cannot reach.
 *
 * The test reads the bundle SOURCES, because the contract is the key
 * the bundle registers, and the built bundle is not committed.
 *
 * The keys are upstream's own, so an operator's saved setting carries
 * over and means what it means upstream.
 */

const fs = require('fs');
const path = require('path');

/** The plugins that expose settings, and the upstream keys each one edits. */
const PLUGIN_SETTINGS = {
  likes: ['flarum-likes.like_own_post'],
  mentions: ['flarum-mentions.allow_username_format'],
  tags: ['flarum-tags.min_primary_tags', 'flarum-tags.max_primary_tags', 'flarum-tags.min_secondary_tags', 'flarum-tags.max_secondary_tags'],
  gamification: ['fof-gamification.allowSelfVotes', 'fof-gamification.defaultMetric', 'fof-gamification.defaultPeriod'],
};

/** The plugins directory, relative to this test. */
const PLUGINS_DIR = path.resolve(__dirname, '..', '..', '..', 'plugins');

/** The setting key as it appears in a bundle: a quoted string. */
const SETTING_PATTERN = /setting:\s*'([^']+)'/g;

/**
 * The settings a bundle registers.
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

describe("the plugins' admin bundles", () => {
  it.each(Object.entries(PLUGIN_SETTINGS))('%s registers its upstream settings', (plugin, keys) => {
    const source = fs.readFileSync(path.join(PLUGINS_DIR, plugin, 'js', 'admin.js'), 'utf8');

    for (const key of keys) {
      expect(source).toContain(key);
    }

    // The bundle must register through the admin application's own
    // vocabulary, or the form renders the wrong control.
    expect(source).toContain('Extend.Admin()');
  });

  it.each(Object.entries(PLUGIN_SETTINGS))('%s registers nothing else', (plugin, keys) => {
    const source = fs.readFileSync(path.join(PLUGINS_DIR, plugin, 'js', 'admin.js'), 'utf8');

    // A setting nobody reads is a control that changes nothing, and
    // the operator turns it and the forum does not move.
    expect(registeredSettings(source).sort()).toEqual([...keys].sort());
  });
});

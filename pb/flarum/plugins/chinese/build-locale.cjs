// Compile the Simplified Chinese catalogue into the bundles the SPA loads.
//
// A language pack is a catalogue, not a webpack extension: there is no
// `js/` bundle to build, only the `locale/*.yml` files. The output is the
// same two files the core's own compiler produces, under this locale's
// name:
//
//   dist/forum-zh-Hans.js   the *.forum.* and *.lib.* keys
//   dist/admin-zh-Hans.js   the *.admin.* and *.lib.* keys
//
// The rules (flattening, `=>` reference resolution, the per-frontend key
// filter) come from the core's `locale-compile.cjs`. They are shared on
// purpose: a second copy would drift, and the reference resolution in
// particular is load-bearing — leaving the `=>` values unresolved renders
// the literal `=> core.ref.foo` in the UI.
//
// `scripts/build_flarum_spa.sh` runs this and stages the output into
// `ui/dist/plugins/chinese/`, which is what the Go binary embeds.
const path = require('path');

const { compileLocale } = require('../../frontend/packages/core/locale-compile.cjs');

const ROOT = __dirname;

// The locale code upstream advertises in its composer.json
// (`extra.flarum-locale.code`). It is what the host passes as
// `RIFF_FORUM_LOCALE`, and what the asset name carries.
const LOCALE = 'zh-Hans';

// The catalogue files, in load order.
//
// Only the extensions this port serves are vendored — see README.md.
const SOURCES = [
  'core.yml',
  'validation.yml',
  'flarum-tags.yml',
  'flarum-likes.yml',
  'flarum-lock.yml',
  'flarum-sticky.yml',
  'flarum-suspend.yml',
  'fof-upload.yml',
  'fof-polls.yml',
];

compileLocale({
  sources: SOURCES,
  localeDir: path.join(ROOT, 'locale'),
  outDir: path.join(ROOT, 'dist'),
  locale: LOCALE,
  log: (message) => console.log(message),
});

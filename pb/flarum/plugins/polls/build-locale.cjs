// Compile the Polls extension's catalogue into the bundle the SPA loads.
//
// The polls extension is NOT a language pack: it has its own code
// bundle, and the catalogue is an ADDITION to whatever the active
// locale's bundle already carries.
//
// The rules (flattening, `=>` reference resolution, the per-frontend key
// filter) come from the core's `locale-compile.cjs`, which is shared on
// purpose: two copies would drift, and the reference resolution in
// particular is load-bearing — an unresolved `=> core.ref.foo` renders
// literally in the UI.
//
// The output is one script:
//
//   dist/forum-en.js   the *.forum.* keys
//
// `scripts/build_flarum_spa.sh` runs this and stages the output into
// `ui/dist/plugins/polls/`, which is what the Go binary embeds.
const path = require('path');

const { compileLocale } = require('../../frontend/packages/core/locale-compile.cjs');

const ROOT = __dirname;

// The locale this catalogue is written in.
//
// The extension's catalogue is English, because the extension ships no
// other language: a translation of the extension's own strings belongs
// in the language pack, which compiles the extension's catalogue INTO
// its own bundle for that language.
const LOCALE = 'en';

// The catalogue files, in load order.
const SOURCES = ['fof-polls.yml'];

compileLocale({
  sources: SOURCES,
  localeDir: path.join(ROOT, 'locale'),
  outDir: path.join(ROOT, 'dist'),
  locale: LOCALE,
  log: (message) => console.log(message),
});

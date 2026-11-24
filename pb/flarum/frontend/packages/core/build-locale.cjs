// Compile the forum's English catalogue into the locale bundles the
// frontend loads.
//
// The translation rules (flattening, `=>` reference resolution, the
// per-frontend key filter) live in `locale-compile.cjs`, which a
// language-pack plugin's build script uses too: the two must agree, and
// two copies would drift.
//
// See that module for the two upstream behaviours reproduced here.
const path = require('path');

const { compileLocale } = require('./locale-compile.cjs');

const ROOT = __dirname;

// The locale upstream compiles. riff ships English as the built-in
// catalogue; the boot payload advertises it unless the host asks for
// another one through `RIFF_FORUM_LOCALE`.
const LOCALE = 'en';

// The files whose keys make up the catalogue. Each is rooted at its own
// namespace (`core:`, `validation:`).
const SOURCES = ['core.yml', 'validation.yml'];

compileLocale({
  sources: SOURCES,
  localeDir: path.join(ROOT, 'locale'),
  outDir: path.join(ROOT, 'dist'),
  locale: LOCALE,
  log: (message) => console.log(message),
});

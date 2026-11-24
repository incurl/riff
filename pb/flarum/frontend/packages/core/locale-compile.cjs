// Compile Flarum locale catalogue files into the bundles the SPA loads.
//
// Upstream compiles `locale/*.yml` into `forum-<locale>.js` and
// `admin-<locale>.js`, each containing a single
// `app.translator.addTranslations(...)` call (see upstream's
// Frontend\AddTranslations), and loads them right after the entry bundle.
// This port has no PHP, so the same bundles are produced here.
//
// It is shared by the core's own compiler and by a language-pack plugin's:
// both must apply the SAME two rules, and two copies of them drift.
//
// Two details of upstream's compiler are reproduced deliberately:
//
//   - the keys are filtered by frontend, so the forum bundle carries
//     `*.forum.*` and `*.lib.*` and the admin bundle carries `*.admin.*`
//     and `*.lib.*`. A key with neither segment (the `core.ref.*`, `views`,
//     `api` and `email` namespaces) is server-side and belongs in neither;
//   - a value written `=> some.key` is a REFERENCE to another key's value,
//     not a literal. `core.yml` uses them 180-odd times for repeated
//     labels, and leaving them unresolved would render `=> core.ref.foo`
//     in the UI.
const fs = require('fs');
const path = require('path');

/**
 * Require the YAML parser from the workspace that owns this file.
 *
 * A caller outside `packages/` (a plugin's build script) resolves
 * `js-yaml` from its own directory, where it is not installed, so the
 * module is resolved relative to this file instead.
 */
function loadYaml() {
  // eslint-disable-next-line global-require, import/no-dynamic-require
  return require(require.resolve('js-yaml', { paths: [__dirname] }));
}

/** Flatten a nested YAML document into dotted keys. */
function flatten(node, prefix = '', out = {}) {
  for (const [key, value] of Object.entries(node)) {
    const id = prefix ? `${prefix}.${key}` : key;

    if (value && typeof value === 'object' && !Array.isArray(value)) {
      flatten(value, id, out);
    } else {
      out[id] = value;
    }
  }

  return out;
}

/**
 * Resolve `=> key` references.
 *
 * The referenced key may itself be a reference, so this walks to a fixed
 * point and gives up rather than looping on a cycle.
 */
function resolveReferences(translations) {
  const resolved = { ...translations };

  for (let pass = 0; pass < 10; pass++) {
    let changed = false;

    for (const [id, value] of Object.entries(resolved)) {
      if (typeof value !== 'string') continue;

      const match = /^=>\s*(.+)$/.exec(value.trim());
      if (!match) continue;

      const target = resolved[match[1].trim()];

      if (typeof target === 'string' && !/^=>\s*/.test(target)) {
        resolved[id] = target;
        changed = true;
      }
    }

    if (!changed) break;
  }

  return resolved;
}

/** The keys upstream's AddTranslations keeps for a given frontend. */
function filterForFrontend(translations, name) {
  // Upstream's filter is /^.+(?:\.|::)(forum|lib)\./. String matching says
  // the same thing without the regex escaping, and the keys here only ever
  // use '.' as the separator.
  const own = `.${name}.`;
  const lib = '.lib.';
  const out = {};

  for (const [id, value] of Object.entries(translations)) {
    if (id.includes(own) || id.includes(lib)) {
      out[id] = value;
    }
  }

  return out;
}

/**
 * Compile a locale catalogue.
 *
 * @param {object} options
 * @param {string[]} options.sources  Catalogue files, in load order. Their
 *   paths are resolved against `options.localeDir`.
 * @param {string} options.localeDir  The directory holding the catalogue.
 * @param {string} options.outDir     Where the bundles are written.
 * @param {string} options.locale     The locale code, e.g. `en`, `zh-Hans`.
 * @param {(message: string) => void} [options.log]  Progress sink.
 * @returns {{forum: number, admin: number, references: number}}
 */
function compileLocale({ sources, localeDir, outDir, locale, log = () => {} }) {
  fs.mkdirSync(outDir, { recursive: true });

  const yaml = loadYaml();

  let translations = {};

  for (const file of sources) {
    const document = yaml.load(fs.readFileSync(path.join(localeDir, file), 'utf8'));

    if (!document || typeof document !== 'object') {
      throw new Error(`${file}: not a locale catalogue (expected a YAML mapping)`);
    }

    translations = { ...translations, ...flatten(document) };
  }

  const references = Object.entries(translations).filter(
    ([, value]) => typeof value === 'string' && /^=>\s*/.test(value.trim())
  ).length;

  translations = resolveReferences(translations);

  const stillUnresolved = Object.entries(translations).filter(
    ([, value]) => typeof value === 'string' && /^=>\s*/.test(value.trim())
  );

  if (stillUnresolved.length > 0) {
    throw new Error(
      `${stillUnresolved.length} translation references could not be resolved, e.g. ${stillUnresolved[0][0]}`
    );
  }

  const counts = {};

  for (const name of ['forum', 'admin']) {
    const bundle = filterForFrontend(translations, name);
    const target = path.join(outDir, `${name}-${locale}.js`);

    fs.writeFileSync(target, `app.translator.addTranslations(${JSON.stringify(bundle)});\n`);

    counts[name] = Object.keys(bundle).length;

    log(`${name}-${locale}.js: ${counts[name]} keys (${references} references resolved)`);
  }

  return { ...counts, references };
}

module.exports = { compileLocale, flatten, resolveReferences, filterForFrontend };

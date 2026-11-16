// Compile the forum's translations into the locale bundles the frontend
// loads.
//
// Upstream compiles `locale/*.yml` into `forum-<locale>.js` and
// `admin-<locale>.js`, each containing a single
// `app.translator.addTranslations(...)` call (see upstream's
// Frontend\\AddTranslations), and loads them right after the entry bundle.
// This port has no PHP, so the same bundles are produced here.
//
// Two details of upstream's compiler are reproduced deliberately:
//
//   - the keys are filtered by frontend, so the forum bundle carries
//     `*.forum.*` and `*.lib.*` and the admin bundle carries `*.admin.*`
//     and `*.lib.*`. A key with neither segment (the `core.ref.*`, `views`,
//     `api` and `email` namespaces) is server-side and belongs in neither;
//   - a value written `=> some.key` is a REFERENCE to another key's value,
//     not a literal. `core.yml` uses them 187 times for repeated labels,
//     and leaving them unresolved would render `=> core.ref.foo` in the UI.
const fs = require('fs');
const path = require('path');
const yaml = require('js-yaml');

const ROOT = __dirname;
const LOCALE_DIR = path.join(ROOT, 'locale');
const OUT_DIR = path.join(ROOT, 'dist');

// The locale upstream compiles. riff ships English only; the boot payload
// advertises `en`, so that is the bundle the shell loads.
const LOCALE = 'en';

// The files whose keys make up the catalogue. Each is rooted at its own
// namespace (`core:`, `validation:`).
const SOURCES = ['core.yml', 'validation.yml'];

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

function compile() {
  fs.mkdirSync(OUT_DIR, { recursive: true });

  let translations = {};

  for (const file of SOURCES) {
    translations = { ...translations, ...flatten(yaml.load(fs.readFileSync(path.join(LOCALE_DIR, file), 'utf8'))) };
  }

  const unresolved = Object.entries(translations).filter(([, v]) => typeof v === 'string' && /^=>\s*/.test(v.trim()));
  translations = resolveReferences(translations);

  const stillUnresolved = Object.entries(translations).filter(([, v]) => typeof v === 'string' && /^=>\s*/.test(v.trim()));
  if (stillUnresolved.length > 0) {
    throw new Error(`${stillUnresolved.length} translation references could not be resolved, e.g. ${stillUnresolved[0][0]}`);
  }

  for (const name of ['forum', 'admin']) {
    const bundle = filterForFrontend(translations, name);
    const target = path.join(OUT_DIR, `${name}-${LOCALE}.js`);

    fs.writeFileSync(target, `app.translator.addTranslations(${JSON.stringify(bundle)});\n`);

    console.log(`${name}-${LOCALE}.js: ${Object.keys(bundle).length} keys (${unresolved.length} references resolved)`);
  }
}

compile();

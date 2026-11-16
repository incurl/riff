// Compile the forum's Less stylesheets.
//
// Upstream Flarum compiles these with less.php (the PHP half of the
// project) and serves `forum.css` and `admin.css` alongside the
// bundles. This port has no PHP, so the stylesheets are compiled here
// with the `less` npm package instead.
//
// Two things upstream's compiler does that this has to reproduce:
//
//   - the Font Awesome stylesheets are imported INLINE, so the glyph
//     definitions end up in the compiled css rather than being a
//     separate request. They come from the `@fortawesome/fontawesome-free`
//     npm package, which is the same Font Awesome 7 the Composer package
//     `flarum/font-awesome` provides upstream;
//   - `@fa-bundled-fonts` decides whether the Free font faces are
//     bundled. Upstream sets it from `FontAwesome::needsBundledFonts()`,
//     which is false when an operator points the forum at a Font Awesome
//     Kit or CDN. This port bundles the Free fonts, so it is true.
const fs = require('fs');
const path = require('path');
const less = require('less');

const ROOT = __dirname;
const LESS_DIR = path.join(ROOT, 'less');
const OUT_DIR = path.join(ROOT, 'dist');

// The Font Awesome stylesheets the Less tree imports by name.
const FONT_AWESOME_DIR = path.dirname(require.resolve('@fortawesome/fontawesome-free/package.json'));
const FONT_AWESOME_CSS = path.join(FONT_AWESOME_DIR, 'css');

// The entries upstream compiles, and the file each becomes.
const ENTRIES = [
  { entry: path.join(LESS_DIR, 'forum.less'), out: 'forum.css' },
  { entry: path.join(LESS_DIR, 'admin.less'), out: 'admin.css' },
];

// The base sources upstream's compiler adds BEFORE the entry, as file
// sources rather than imports (see upstream's
// FrontendServiceProvider::addBaseCss). The variables come first, then
// the mixins; the entry then imports the rest.
const BASE_SOURCES = ['variables.less', 'mixins.less'].map((name) => path.join(LESS_DIR, 'common', name));

async function compile() {
  fs.mkdirSync(OUT_DIR, { recursive: true });

  for (const { entry, out } of ENTRIES) {
    const source = BASE_SOURCES.map((file) => fs.readFileSync(file, 'utf8')).join('\n') + '\n' + fs.readFileSync(entry, 'utf8');

    const output = await less.render(source, {
      filename: entry,
      paths: [LESS_DIR, LESS_DIR + path.sep + 'common', FONT_AWESOME_CSS],
      // The bundled Free fonts, so the icons render without a CDN.
      modifyVars: { 'fa-bundled-fonts': 'true' },
      javascriptEnabled: true,
      compress: true,
    });

    // The Font Awesome stylesheets reference their webfonts as
    // `../webfonts/…`, which is relative to the npm package's `css/`
    // directory — the layout they had before the import was inlined.
    // The compiled stylesheet is served from the assets root instead, so
    // the paths are rewritten to the assets-root-relative `webfonts/…`.
    //
    // The font files themselves are staged next to the stylesheet by
    // scripts/build_flarum_spa.sh.
    const css = output.css.replace(/url\(["']?\.\.\/webfonts\//g, 'url("webfonts/');

    const target = path.join(OUT_DIR, out);
    fs.writeFileSync(target, css);
    console.log(`${out}: ${(css.length / 1024).toFixed(1)} KiB`);
  }
}

compile().catch((error) => {
  console.error(error);
  process.exit(1);
});

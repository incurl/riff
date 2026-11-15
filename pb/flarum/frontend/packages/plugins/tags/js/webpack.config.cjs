// The tags extension's webpack config.
//
// It reuses the vendored Flarum config so the build carries the same
// externals ( `flarum/...` imports resolve through flarum.reg, so the
// bundle never duplicates core), the same babel preset (JSX compiles to
// Mithril's `m`), and the same output directory.
//
// The only override is the output library: the shared config emits
// `module.exports = ...`, which is meaningless in a <script> tag. An
// extension has to assign itself onto `flarum.extensions`, and the key
// is the id the bridge reads back (`riff-tags`, derived from the
// composer name `riff/tags`).
const config = require('flarum-webpack-config');

module.exports = function () {
  const base = config();

  base.output.library = 'flarum.extensions["riff-tags"]';

  return base;
};

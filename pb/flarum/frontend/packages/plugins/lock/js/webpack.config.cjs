// The Lock extension's webpack config.
//
// It reuses the vendored Flarum config for the same reasons the Tags
// extension does: the `flarum/...` imports resolve through `flarum.reg`,
// JSX compiles to Mithril's `m`, and the build carries the shared
// externals. The only override is the output library — the shared config
// emits a CommonJS export, which is meaningless in a <script> tag, and an
// extension has to assign itself onto `flarum.extensions`.
const config = require('flarum-webpack-config');

module.exports = function () {
  const base = config();

  base.output.library = 'flarum.extensions["riff-lock"]';

  return base;
};

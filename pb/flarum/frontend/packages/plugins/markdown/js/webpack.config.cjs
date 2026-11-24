// The Markdown extension's webpack config.
//
// Same shape as the other riff extensions: reuse the vendored Flarum config
// (so `flarum/...` resolves through `flarum.reg` and JSX compiles to
// Mithril's `m`), and override only the output library, because the shared
// config emits a CommonJS export that a <script> tag ignores.
const config = require('flarum-webpack-config');

module.exports = function () {
  const base = config();

  base.output.library = 'flarum.extensions["riff-markdown"]';

  return base;
};

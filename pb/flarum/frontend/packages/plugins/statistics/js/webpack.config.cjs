// The Statistics extension's webpack config.
//
// Same shape as the other riff extensions: reuse the vendored Flarum config
// and override only the output library, because the shared config emits a
// CommonJS export that a <script> tag ignores. The id is the composer name
// flattened (`riff/statistics` -> `riff-statistics`), which is the key
// `app.bootExtensions(flarum.extensions)` reads.
const config = require('flarum-webpack-config');

module.exports = function () {
  const base = config();

  base.output.library = 'flarum.extensions["riff-statistics"]';

  return base;
};

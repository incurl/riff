// The likes extension's webpack config.
//
// Same shape as the Tags extension: reuse the vendored Flarum config for
// its externals, babel preset and output directory, and override only
// the output library so the bundle assigns itself onto
// `flarum.extensions` under the id the bridge reads back (`riff-likes`,
// derived from the composer name `riff/likes`).
const config = require('flarum-webpack-config');

module.exports = function () {
  const base = config();

  base.output.library = 'flarum.extensions["riff-likes"]';

  return base;
};

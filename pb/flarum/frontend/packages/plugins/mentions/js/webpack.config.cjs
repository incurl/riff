// The mentions extension's webpack config.
//
// Same shape as the Likes extension: reuse the vendored Flarum config
// for its externals, babel preset and output directory, and override
// only the output library so the bundle assigns itself onto
// `flarum.extensions` under the id the bridge reads back
// (`flarum-mentions`, derived from the composer name `flarum/mentions`).
const config = require('flarum-webpack-config');

module.exports = function () {
  const base = config();

  base.output.library = 'flarum.extensions["flarum-mentions"]';

  return base;
};

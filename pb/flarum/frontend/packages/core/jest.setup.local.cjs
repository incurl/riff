// The globals Mithril (and the vendored components) expect.
//
// The upstream `@flarum/jest-config` does the same in its setup-env.js;
// this file is the local equivalent, minus the Flarum-specific globals
// these component tests do not need.
const m = require('mithril');

global.window = global.window || {};
global.window.m = m;
global.window.testing = true;
global.window.scrollTo = () => {};
global.window.matchMedia = () => ({
  addListener: () => {},
  removeListener: () => {},
  addEventListener: () => {},
  removeEventListener: () => {},
});
global.m = m;

// The common `app` singleton is a Proxy over the forum application.
// A component test has no application, so a minimal stub stands in: the
// Icon component reads `app.forum.attribute(...)`, and the Alert
// component reads the translator.
global.window.flarum = { extensions: {}, reg: { all: () => [] } };
global.app = {
  forum: { attribute: () => null },
  translator: { trans: (key) => key },
  session: { user: null },
};

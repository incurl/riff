// A minimal, self-contained Jest config for the forum's frontend.
//
// The upstream `@flarum/jest-config` is written for an ESM module graph
// (extensionsToTreatAsEsm plus a `pollyfills.js` that uses `import`),
// which this vendored tree — built with webpack, in CommonJS — does not
// need. This config carries the same Babel transform (the Mithril JSX
// pragma in particular) in CommonJS mode, which is all the component
// tests need.
//
// The Babel presets and plugins are the ones the vendored tree already
// builds with; they resolve from the workspace's node_modules.
const path = require('path');

const root = path.join(__dirname, '..', '..');

const babelConfig = {
  assumptions: {
    constantSuper: true,
    ignoreFunctionLength: true,
    noDocumentAll: true,
    noNewArrows: true,
    privateFieldsAsProperties: true,
  },
  presets: [
    require.resolve('@babel/preset-react'),
    require.resolve('@babel/preset-typescript'),
    [require.resolve('@babel/preset-env'), { modules: 'commonjs' }],
  ],
  plugins: [
    [require.resolve('@babel/plugin-transform-react-jsx'), { pragma: 'm', pragmaFrag: "'['", useBuiltIns: true }],
  ],
};

module.exports = {
  testEnvironment: 'jsdom',
  rootDir: __dirname,
  // A plugin may own a PURE module whose arithmetic is worth a unit test
  // without a browser — the Statistics extension's period bucketing is the
  // first, and the Tags extension's admin permission scope is the second.
  // `<rootDir>` is packages/core, so `../plugins/<name>/js` is the
  // sibling package's source tree.
  roots: ['<rootDir>/src', '<rootDir>/../plugins/statistics/js', '<rootDir>/../plugins/tags/js', '<rootDir>/../plugins/gamification/js'],
  setupFilesAfterEnv: ['<rootDir>/jest.setup.local.cjs'],
  transform: {
    '^.+\\.[tj]sx?$': ['babel-jest', babelConfig],
  },
  moduleDirectories: ['node_modules', 'src'],
  modulePaths: [path.join(root, 'node_modules')],
  moduleNameMapper: {
    '^flarum/(.*)$': path.join(__dirname, 'src', '$1'),
  },
};

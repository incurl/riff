/**
 * The first-party plugin bundles must REGISTER themselves on
 * `flarum.extensions`, and their extenders must actually run.
 *
 * The bundles are the BUILT artefacts (the `forum.js` under each
 * plugin's directory in `ui/dist`), not the sources: the bundle is what
 * the browser loads, and the registration is the one thing the build can
 * lose (a webpack config that emits a CommonJS export instead of
 * assigning onto `flarum.extensions` produces a bundle that loads
 * without error and contributes nothing).
 *
 * The core bundle is loaded FIRST, because the plugin bundles ask the
 * registry for their dependencies at load time:
 *
 *   flarum.reg.get("core", "forum/app")
 *
 * so a bundle loaded before the core one would throw on an undefined
 * registry.
 *
 * The test drives the real boot sequence: the core bundle, then the
 * plugin bundles, then `app.bootExtensions(flarum.extensions)`.
 */
import fs from 'fs';
import path from 'path';

/**
 * The built artefacts.
 *
 * `ui/dist` is build output and is gitignored, so the test skips
 * itself when the bundles have not been built — the same way the Go
 * side treats the embedded frontend.
 */
const distDir = path.join(__dirname, '..', '..', '..', '..', 'ui', 'dist');

const coreBundle = path.join(distDir, 'forum.js');

const pluginBundles = [
  { id: 'riff-tags', file: path.join(distDir, 'plugins', 'tags', 'forum.js') },
  { id: 'riff-likes', file: path.join(distDir, 'plugins', 'likes', 'forum.js') },
  { id: 'riff-uploads', file: path.join(distDir, 'plugins', 'uploads', 'forum.js') },
];

const built = fs.existsSync(coreBundle) && pluginBundles.every((b) => fs.existsSync(b.file));

const describeIfBuilt = built ? describe : describe.skip;

/**
 * Evaluate a built bundle in the current (jsdom) global.
 *
 * The bundles are IIFEs that read the `flarum` global, so the global
 * has to be in place before they run.
 *
 * The core bundle's webpack runtime resolves its public path from the
 * `<script src>` that loaded it, and jsdom has none (the bundle is
 * evaluated, not fetched), so a script tag carrying the real asset URL
 * is planted first. Without it the runtime throws "Automatic
 * publicPath is not supported in this browser".
 */
function evaluateBundle(file: string): void {
  const source = fs.readFileSync(file, 'utf-8');

  const script = document.createElement('script');
  script.src = 'http://localhost/forum/assets/' + path.basename(file);
  document.head.appendChild(script);

  try {
    // eslint-disable-next-line no-new-func
    new Function(source)();
  } finally {
    script.remove();
  }
}

describeIfBuilt('the first-party plugin bundles', () => {
  beforeEach(() => {
    // The core bundle registers its modules on `flarum.reg` and the
    // plugin bundles assign themselves onto `flarum.extensions`.
    (global as any).flarum = { extensions: {}, reg: { all: () => [] } };

    // The extenders are handed `globalThis` as their application, so the
    // harness supplies the surface the first-party bundles actually touch: a
    // model registry (`Extend.Store`), the route table and its helper map
    // (`Extend.Routes`), and the boot hook. Without these the FIRST extender
    // that registers a model throws and the test never reaches the rest —
    // which is how it hid a bundle that registered nothing at all.
    (global as any).store = { models: {} };
    (global as any).routes = {};
    (global as any).route = () => '';
    (global as any).beforeMount = () => {};
    // The component registries: `Extend.PostTypes` adds to
    // `postComponents`, and `Extend.Notification` adds to
    // `notificationComponents`.
    (global as any).postComponents = {};
    (global as any).notificationComponents = {};
    // The search registry: `Extend.Search` pushes a gambit onto
    // `app.search.gambits.gambits[modelType]` (the tags extension registers
    // its `tag:` gambit there).
    (global as any).search = { gambits: { gambits: {} } };
  });

  it('register themselves on flarum.extensions under their own id', () => {
    evaluateBundle(coreBundle);

    for (const bundle of pluginBundles) {
      evaluateBundle(bundle.file);
    }

    for (const bundle of pluginBundles) {
      expect((global as any).flarum.extensions[bundle.id]).toBeDefined();
    }
  });

  it('export an extend array whose entries have an extend() method', () => {
    evaluateBundle(coreBundle);

    for (const bundle of pluginBundles) {
      evaluateBundle(bundle.file);
    }

    for (const bundle of pluginBundles) {
      const extension = (global as any).flarum.extensions[bundle.id];

      expect(Array.isArray(extension.extend)).toBe(true);
      expect(extension.extend.length).toBeGreaterThan(0);

      for (const extender of extension.extend) {
        expect(typeof extender.extend).toBe('function');
      }
    }
  });

  it('run their extenders when the app boots the extensions', () => {
    evaluateBundle(coreBundle);

    for (const bundle of pluginBundles) {
      evaluateBundle(bundle.file);
    }

    const booted: string[] = [];

    (global as any).app.bootExtensions = (extensions: Record<string, any>) => {
      Object.keys(extensions).forEach((name) => {
        const extension = extensions[name];

        if (!extension.extend) return;

        for (const extender of extension.extend.flat(Infinity)) {
          booted.push(name);
          extender.extend(globalThis, { name, exports: extension });
        }
      });
    };

    (global as any).app.bootExtensions((global as any).flarum.extensions);

    // Every plugin contributed its extender.
    for (const bundle of pluginBundles) {
      expect(booted).toContain(bundle.id);
    }
  });
});

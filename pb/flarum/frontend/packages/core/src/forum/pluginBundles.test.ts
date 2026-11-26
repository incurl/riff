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
// Imported rather than taken from the injected globals: this file is
// type-checked (`npm run typecheck`), and the ambient Jest types are not in
// scope for the TS build.
import { beforeEach, describe, expect, it } from '@jest/globals';
import fs from 'fs';
import path from 'path';

/**
 * The built artefacts.
 *
 * `ui/dist` is build output and is gitignored, so the test skips
 * itself when the bundles have not been built — the same way the Go
 * side treats the embedded frontend.
 *
 * BOTH APPLICATIONS are covered, and that is not symmetry for its own sake:
 * the admin app is a DIFFERENT core bundle. `admin.js` carries `common/*` and
 * `admin/*` and no `forum/*` modules at all, so a plugin's admin half that
 * reaches for `flarum/forum/app` resolves it to `undefined` — quietly, with one
 * console line — and a plugin that puts such a value in its extenders array
 * takes the whole admin boot down. The admin bundles were untested, and the
 * tags permission scope shipped exactly that way.
 */
const distDir = path.join(__dirname, '..', '..', '..', '..', 'ui', 'dist');

interface AppBundles {
  /** The app, for the test names. */
  label: string;
  /** The app's core bundle. */
  core: string;
  /** The plugin bundles, in load order. */
  plugins: { id: string; file: string }[];
}

/**
 * Every plugin that ships a front-end bundle, with the id it registers under.
 *
 * The id is the one `app.bootExtensions(flarum.extensions)` reads, and it comes
 * from the plugin's own name — `riff/<name>` for the first-party ones, which is
 * what makes it `riff-<name>`. The mentions plugin keeps UPSTREAM's id
 * (`flarum/mentions`), so its bundles register as `flarum-mentions`; that is
 * not an oversight to "fix" here, it is what the plugin is called.
 */
const pluginBundles: { dir: string; id: string }[] = [
  { dir: 'tags', id: 'riff-tags' },
  { dir: 'likes', id: 'riff-likes' },
  { dir: 'uploads', id: 'riff-uploads' },
  { dir: 'lock', id: 'riff-lock' },
  { dir: 'sticky', id: 'riff-sticky' },
  { dir: 'suspend', id: 'riff-suspend' },
  { dir: 'markdown', id: 'riff-markdown' },
  { dir: 'gamification', id: 'riff-gamification' },
  { dir: 'polls', id: 'riff-polls' },
  { dir: 'statistics', id: 'riff-statistics' },
  { dir: 'mentions', id: 'flarum-mentions' },
];

/** The `<name>/<suffix>` bundle of every plugin, in load order. */
function pluginBundlesFor(suffix: 'forum.js' | 'admin.js'): { id: string; file: string }[] {
  return pluginBundles.map((plugin) => ({
    id: plugin.id,
    file: path.join(distDir, 'plugins', plugin.dir, suffix),
  }));
}

const apps: AppBundles[] = [
  {
    label: 'forum',
    core: path.join(distDir, 'forum.js'),
    plugins: pluginBundlesFor('forum.js'),
  },
  {
    label: 'admin',
    core: path.join(distDir, 'admin.js'),
    plugins: pluginBundlesFor('admin.js'),
  },
];

const built = apps.every((app) => fs.existsSync(app.core) && app.plugins.every((b) => fs.existsSync(b.file)));

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
    resetGlobal();
  });

  for (const app of apps) {
    describe(`the ${app.label} app`, () => {
      beforeEach(() => {
        resetGlobal();
      });

      it('register themselves on flarum.extensions under their own id', () => {
        evaluateApp(app);

        for (const bundle of app.plugins) {
          expect((global as any).flarum.extensions[bundle.id]).toBeDefined();
        }
      });

      /**
       * Every entry is a real extender.
       *
       * `bootExtensions` walks the array and calls `extender.extend(...)`, so
       * ONE `undefined` in it throws out of the boot — and because the boot is
       * a single pass, every extension after it never registers either. The
       * tags admin bundle shipped `addTagsPermissionScope()` in the array, a
       * function that returned nothing: the admin app's loading overlay never
       * cleared and no plugin's admin UI appeared, with one console line to say
       * why.
       */
      it('export an extend array of real extenders', () => {
        evaluateApp(app);

        for (const bundle of app.plugins) {
          const extension = (global as any).flarum.extensions[bundle.id];

          // An EMPTY array is legitimate — the uploads bundle deliberately
          // registers nothing (its admin entry exists so the extension's admin
          // URL is not a 404). What is not legitimate is a HOLE in it.
          expect(Array.isArray(extension.extend)).toBe(true);

          extension.extend.forEach((extender: unknown, index: number) => {
            // Named in the assertion: an undefined entry is the failure this
            // test exists for, and "expected undefined to be defined" alone
            // does not say which bundle at which position.
            expect({ app: app.label, bundle: bundle.id, index, defined: extender !== undefined }).toEqual({
              app: app.label,
              bundle: bundle.id,
              index,
              defined: true,
            });

            expect(typeof (extender as any)?.extend).toBe('function');
          });
        }
      });

      it('run their extenders when the app boots the extensions', () => {
        const booted = bootExtensions(app);

        for (const bundle of app.plugins) {
          const extension = (global as any).flarum.extensions[bundle.id];

          // Nothing to boot is not a failure; a bundle that HAS extenders and
          // still does not boot means the boot aborted before reaching it,
          // which is the symptom the whole admin app showed.
          if (!extension.extend.length) continue;

          expect(booted).toContain(bundle.id);
        }
      });
    });
  }

  /**
   * The model methods the likes UI CALLS.
   *
   * A missing `attribute('name')` registration is invisible in a bundle that
   * loads cleanly: the accessor does not exist on the model, the call throws
   * inside the `extend` callback, and `extend()` swallows it (it reports once
   * through `app.handleErrorOnce`). The symptom is not a wrong count — it is
   * a like button that never renders, which is how `isLiked` shipped missing
   * while the Go side was sending it.
   */
  it('register the model methods the likes UI calls', () => {
    bootExtensions(appById('forum'));

    // The registry is the core bundle's own (`flarum.reg`), which is what the
    // extension bundles resolve their externals through.
    const reg = (global as any).flarum.reg;
    const Post = reg.get('core', 'common/models/Post');

    expect(Post).toBeDefined();

    for (const method of ['isLiked', 'likesCount', 'canLike', 'likes']) {
      expect(typeof Post.prototype[method]).toBe('function');
    }
  });

  it('register the model methods the gamification UI calls', () => {
    bootExtensions(appById('forum'));

    const reg = (global as any).flarum.reg;
    const Post = reg.get('core', 'common/models/Post');
    const Discussion = reg.get('core', 'common/models/Discussion');
    const User = reg.get('core', 'common/models/User');

    // The vote controls read `post.canSeeVotes()` and the list reads
    // `discussion.seeVotes()`. Both are METHOD calls, so an attribute the
    // server sends without a registration here does not exist — the render
    // throws and `extend()` swallows it, and the control never appears.
    for (const method of ['votes', 'canSeeVotes', 'canVote', 'hasUpvoted', 'hasDownvoted', 'seeVoters']) {
      expect(typeof Post.prototype[method]).toBe('function');
    }

    for (const method of ['votes', 'seeVotes', 'canVote', 'hasUpvoted', 'hasDownvoted']) {
      expect(typeof Discussion.prototype[method]).toBe('function');
    }

    // The rank stack reads `user.ranks()`, and the user card reads
    // `user.points()`.
    for (const method of ['points', 'votes', 'rank', 'ranks']) {
      expect(typeof User.prototype[method]).toBe('function');
    }
  });
});

/** One of the applications, by label. */
function appById(label: string): AppBundles {
  const app = apps.find((candidate) => candidate.label === label);

  if (!app) throw new Error(`no bundle set for the ${label} app`);

  return app;
}

/**
 * Install the globals a core bundle expects, and empty the extension registry.
 *
 * The core bundle registers its modules on `flarum.reg` and the plugin bundles
 * assign themselves onto `flarum.extensions`.
 *
 * The registry's two extra methods are what a plugin needs when it extends a
 * LAZY-CHUNK component by path (`extend('flarum/forum/components/NotificationGrid',
 * …)`): `namespaceAndIdFromPath` splits the path and `onLoad` defers the
 * registration until the chunk is fetched. The chunk is never fetched here, so
 * the deferral simply never resolves — which is the truthful shape for a
 * harness with no chunk loader.
 */
function resetGlobal(): void {
  (global as any).flarum = {
    extensions: {},
    reg: {
      all: () => [],
      namespaceAndIdFromPath: (path: string) => {
        const [namespace, ...rest] = path.split('/');

        return [namespace === 'flarum' ? 'core' : namespace, rest.join('/')];
      },
      onLoad: () => {},
    },
  };

  // The extenders are handed `globalThis` as their application, so the harness
  // supplies the surface the first-party bundles actually touch: a model
  // registry (`Extend.Store`), the route table and its helper map
  // (`Extend.Routes`), and the boot hook. Without these the FIRST extender that
  // registers a model throws and the test never reaches the rest — which is how
  // it hid a bundle that registered nothing at all.
  //
  // The admin extenders need nothing beyond `beforeMount`: `Extend.Admin` defers
  // all of its registry work into that hook.
  (global as any).store = { models: {} };
  (global as any).routes = {};
  (global as any).route = () => '';
  (global as any).beforeMount = () => {};
  // The component registries: `Extend.PostTypes` adds to `postComponents`, and
  // `Extend.Notification` adds to `notificationComponents`.
  (global as any).postComponents = {};
  (global as any).notificationComponents = {};
  // The search registry: `Extend.Search` pushes a gambit onto
  // `app.search.gambits.gambits[modelType]` (the tags extension registers its
  // `tag:` gambit there).
  (global as any).search = { gambits: { gambits: {} } };
}

/** Evaluate one application's core bundle and its plugin bundles. */
function evaluateApp(app: AppBundles): void {
  evaluateBundle(app.core);

  for (const bundle of app.plugins) {
    evaluateBundle(bundle.file);
  }
}

/**
 * Boot one application's loaded extensions against the harness app, and return
 * the ids whose extenders ran.
 *
 * It mirrors the real `Application.bootExtensions` — including its shape rather
 * than a defensive version of it: a `undefined` entry throws here, which is
 * exactly what it does in the browser, and is why the extenders are asserted to
 * be objects before this runs.
 */
function bootExtensions(app: AppBundles): string[] {
  evaluateApp(app);

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

  return booted;
}

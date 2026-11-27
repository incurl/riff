#!/bin/sh
# Build the forum's front end and stage it for the Go binary.
#
# The Flarum front end is vendored from upstream (see pb/flarum/README.md).
# It is a Yarn workspace upstream; riff drives npm, which needs the
# legacy peer resolver for the upstream hoisting assumptions.
#
# The build output is copied into pb/flarum/frontend/ui/dist, which is
# what the Go binary embeds. The directory is gitignored (it is build
# output), so the tracked fallback shell in pb/flarum/frontend/ui is
# what a fresh clone compiles against.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FRONTEND="$ROOT/pb/flarum/frontend"
TARGET="$ROOT/pb/flarum/frontend/ui/dist"

# The first-party plugin extensions. Each one is a Flarum extension in
# the upstream layout — <plugin>/composer.json plus <plugin>/js — and
# builds its own forum.js/admin.js bundle. The Go plugin declares those
# paths in its AssetBundle (plugins/<name>/forum.js and friends), so a
# missing build here is a 404 in the forum shell.
PLUGINS="tags likes uploads lock sticky suspend markdown gamification polls mentions statistics"

export PATH="$HOME/.local/bin:$PATH"

cd "$FRONTEND"
npm install --legacy-peer-deps --no-audit --no-fund
npm run build

rm -rf "$TARGET"
mkdir -p "$TARGET"
cp -R "$FRONTEND/packages/core/dist/." "$TARGET/"

# The Font Awesome webfonts.
#
# The compiled stylesheet references them as `webfonts/…`, relative to
# the assets root, so they are staged there. Only the three faces the
# stylesheet declares are copied.
mkdir -p "$TARGET/webfonts"
for font in fa-brands-400.woff2 fa-regular-400.woff2 fa-solid-900.woff2; do
  cp "$FRONTEND/node_modules/@fortawesome/fontawesome-free/webfonts/$font" "$TARGET/webfonts/$font"
done

# Build and stage each plugin bundle.
#
# The plugin packages are NOT npm workspaces: adding them to the
# workspace globs would rewrite package-lock.json and pull the whole
# dependency tree through npm again. NODE_PATH is what lets the plugin's
# webpack.config.cjs resolve `flarum-webpack-config` (and the webpack
# CLI resolve its own plugins) out of the frontend's existing
# node_modules, so the extension builds against the EXACT same toolchain
# as core.
for plugin in $PLUGINS; do
  plugin_dir="$FRONTEND/packages/plugins/$plugin"

  echo "Building the $plugin extension"
  (
    cd "$plugin_dir/js"
    NODE_PATH="$FRONTEND/node_modules" node "$FRONTEND/node_modules/webpack-cli/bin/cli.js" --config webpack.config.cjs --mode production
  )

  # The asset paths the Go plugin registers are relative to the embedded
  # root: plugins/<name>/forum.js, plugins/<name>/admin.js and
  # plugins/<name>/style.css. webpack emits the two bundles; the
  # stylesheet is a plain file (the shared config has no CSS rule), so
  # it is copied rather than bundled.
  mkdir -p "$TARGET/plugins/$plugin"
  cp "$plugin_dir/js/dist/forum.js" "$TARGET/plugins/$plugin/forum.js"
  cp "$plugin_dir/js/dist/admin.js" "$TARGET/plugins/$plugin/admin.js"
  cp "$plugin_dir/style.css" "$TARGET/plugins/$plugin/style.css"

  # An extension's OWN catalogue is compiled alongside its bundles.
  #
  # The polls extension ships one: without it every `trans()` call in the
  # poll renders the raw key, so the UI shows "fof-polls.forum.poll_ended"
  # instead of "This poll has ended."
  #
  # It is a separate script rather than part of the bundle because the
  # translations are added at RUNTIME (`app.translator.addTranslations`),
  # and the shell loads the catalogue before the bundle that renders the
  # poll.
  if [ -f "$plugin_dir/build-locale.cjs" ]; then
    echo "Compiling the $plugin extension's catalogue"
    node "$plugin_dir/build-locale.cjs"
    cp "$plugin_dir/dist/"*.js "$TARGET/plugins/$plugin/"
  fi

  # A plugin whose catalogue lives NEXT TO ITS GO CODE instead of in the
  # frontend package. The polls extension is the case: `pb/flarum/plugins/polls`
  # owns both the `.yml` and the compiler, and its comment has always claimed
  # that "scripts/build_flarum_spa.sh runs this and stages the output into
  # ui/dist/plugins/polls/" — it did not.
  #
  # The cost was not cosmetic. The Go side declares
  # `Locale: "plugins/polls/forum-en.js"` in its AssetBundle, so every boot
  # warned `a plugin's frontend asset is not embedded … plugin=fof/polls
  # asset=plugins/polls/forum-en.js`, and every `trans()` call in a poll
  # rendered its raw key ("fof-polls.forum.poll_ended"). A declared asset that
  # nothing builds is the same class as a declared API path that nothing
  # routes: the claim outlives the code.
  if [ -f "$ROOT/pb/flarum/plugins/$plugin/build-locale.cjs" ]; then
    echo "Compiling the $plugin extension's catalogue (plugin side)"
    ( cd "$ROOT/pb/flarum/plugins/$plugin" && node build-locale.cjs )
    cp "$ROOT/pb/flarum/plugins/$plugin/dist/"*.js "$TARGET/plugins/$plugin/"
  fi

  # The extension's async chunks go under `js/<namespace>/`, next to
  # core's — the runtime builds a chunk's url as
  # `${jsChunksBaseUrl}/${namespace}/${urlPath}.js`, and
  # `jsChunksBaseUrl` is the assets root plus `/js`. The namespace is
  # the composer name flattened the way extensionId.cjs does it
  # (`riff/tags` -> `riff-tags`).
  #
  # These two extensions have no lazy imports yet, so the directory is
  # usually absent; it is copied when it is there so an extension that
  # grows one does not 404.
  if [ -d "$plugin_dir/js/dist/js" ]; then
    find "$plugin_dir/js/dist/js" -mindepth 1 -maxdepth 1 -type d | while read -r chunk_dir; do
      chunk_namespace=$(basename "$chunk_dir")
      mkdir -p "$TARGET/js/$chunk_namespace"
      cp -R "$chunk_dir/." "$TARGET/js/$chunk_namespace/"
    done
  fi
done

# Build and stage the language packs.
#
# A language pack is a catalogue, not a webpack extension: it has no
# js/webpack.config.cjs, so it is not part of the loop above. Its build
# compiles locale/*.yml into the two `app.translator.addTranslations(…)`
# scripts the SPA loads, named after the locale (forum-zh-Hans.js and
# admin-zh-Hans.js). The Go plugin declares those paths, so a missing
# build here is a locale that renders raw translation keys.
#
# EVERY pack in this list must have a plugin under pb/flarum/plugins/ that
# declares the same locale, and vice versa: a pack that is built but not
# declared is dead weight, and a declared pack that is not built is a
# locale whose bundle 404s (the module skips the tag and logs it).
for pack in chinese french; do
  pack_dir="$ROOT/pb/flarum/plugins/$pack"

  echo "Building the $pack language pack"
  node "$pack_dir/build-locale.cjs"

  mkdir -p "$TARGET/plugins/$pack"
  cp "$pack_dir/dist/"*.js "$TARGET/plugins/$pack/"
done

echo "The forum front end is staged in $TARGET"

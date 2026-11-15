# Writing a forum plugin

A plugin is a Go type that implements `plugin.Plugin`. It is compiled
into the host binary — there is no dynamic `.so` loading — and registers
itself with the manager.

## The skeleton

```go
package acme

import (
    "context"

    "github.com/incurl/riff/pb/flarum/plugin"
)

// PluginID is the plugin's id.
const PluginID = "acme/bookmarks"

// Plugin is the Bookmarks plugin.
type Plugin struct {
    manifest plugin.Manifest
}

// New returns the plugin.
func New() *Plugin {
    return &Plugin{
        manifest: plugin.Manifest{
            ID:          PluginID,
            Name:        "Bookmarks",
            Version:     "1.0.0",
            CoreRequire: ">=2.0.0",
            Deps:        []string{"riff/tags"},
            BackendKind: plugin.BackendStaticGo,
            Assets: plugin.Assets{
                Forum: "plugins/bookmarks/forum.js",
                CSS:   "plugins/bookmarks/style.css",
            },
            Icon: plugin.Icon{
                BackgroundColor: "#238c59",
                Glyph:           "fas fa-bookmark",
            },
        },
    }
}

func (p *Plugin) Manifest() plugin.Manifest { return p.manifest }

func (p *Plugin) Register(ctx context.Context, ext *plugin.Extenders) error {
    ext.AddMigration(plugin.Migration{
        Name: "create_bookmarks",
        Up: func(ctx context.Context, app core.App) error { … },
    })
    ext.AddAttribute(plugin.AttributeContributor{ … })
    return nil
}

func (p *Plugin) Boot(ctx context.Context, ext *plugin.Extenders) error { return nil }
func (p *Plugin) Shutdown(ctx context.Context, ext *plugin.Extenders) error { return nil }
```

## The manifest

| Field | Meaning |
| --- | --- |
| `id` | `"vendor/name"`. Unique. |
| `name` | The human-readable name. |
| `version` | The plugin's own version. |
| `coreRequire` | A semver range the running core must satisfy. |
| `deps` | Other plugin ids, each optionally with a version range. |
| `backendKind` | `static-go` (the only kind in v1). |
| `assets` | The frontend bundles, relative to the asset root. |
| `icon` | The icon metadata. |

### The semver ranges

The supported range syntax is deliberately small:

`""` any · `1.2.3` exactly · `>=1.2` at least · `>1.2` newer ·
`<=1.2` at most · `<1.2` older · `^1.2` below 2.0.0 · `~1.2` below 1.3.0 ·
`1.2 - 1.4` a range.

A range may be a conjunction of several comparators. Anything else is an
error, so a typo in a manifest is loud rather than silently permissive.

## The three layers

Every feature is three layers, and the Tags and Likes plugins are the
worked examples:

1. **The migration** — the plugin creates its own collection.
2. **The API** — the plugin contributes attributes and relationships to
   the core's resources, and mounts its own routes.
3. **The UI** — the plugin ships a frontend bundle that the SPA loads.

### The Tags plugin

- Migration: `forum_tags`.
- API: the `tags` relationship on the discussion resource; the
  `/api/tags` endpoints; the tag filter on the discussion listing.
- UI: the tag assignment and filter entries.

### The Likes plugin

- Migration: `forum_likes`, with a unique index on `(post, user)` — that
  index is what prevents a duplicate like.
- API: the `likesCount`, `isLiked` and `canLike` attributes and the
  `likes` relationship on the post resource; the `/api/posts/{id}/like`
  endpoints.
- UI: the like button and the like count.

## The frontend bundle

The bundle is a prebuilt `forum.js` / `admin.js` / CSS file, served
under the module's asset prefix. The manifest endpoint lists the enabled
plugins in dependency order with their bundle URLs; the SPA loads them
in that order and registers each plugin's exports on
`flarum.extensions`.

The version travels in the URL as `?v=<version>` for cache busting.

## Enabling and disabling

The enabled state lives in `forum_plugin_state`. A plugin with no state
row is enabled. A disabled plugin does not register, so it leaves no
listener, route, migration or service override behind.

## Registering a plugin with the host

```go
forum.Register(app, forum.Options{
    Plugins: []plugin.Plugin{
        acme.New(),
    },
})
```

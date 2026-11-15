# Mounting the forum module

The forum is a module inside the riff host application, not an
application of its own. It never binds a listener, never owns the
process, and never registers a route outside its own base path.

## The lifecycle

The host calls three methods:

```go
module, err := forum.New(app, forum.Options{BasePath: "/forum"})
err = module.Register()   // wire the routes and the services
err = module.Boot(ctx)    // load the plugins, run the migrations
err = module.Shutdown(ctx) // release the plugin resources
```

In practice the host uses [forum.Register], which binds the whole
lifecycle onto PocketBase's `OnServe` hook, because the module's
collections do not exist until the app is bootstrapped:

```go
forum.Register(app, forum.Options{
    BasePath:         envOr("RIFF_FORUM_BASE_PATH", "/forum"),
    CollectionPrefix: envOr("RIFF_FORUM_COLLECTION_PREFIX", "forum_"),
})
```

## The base path

`forum.basePath` defaults to `/forum`. Every route, every asset URL and
every link the frontend builds is relative to it:

| Surface | Path |
| --- | --- |
| The SPA | `/forum/`, `/forum/d/{id}`, `/forum/t/{slug}`, `/forum/admin/…` |
| The JSON:API | `/forum/api/…` |
| The assets | `/forum/assets/…` |
| The session | `/forum/login`, `/forum/logout` |

The base path travels to the frontend as the forum document's
`basePath` attribute, which is what upstream Flarum's router, link
router and asset helpers already consume. No frontend change is needed
for a subpath mount.

## Coexistence with the host

- The forum's routes are mounted under the base path only. The host's
  `/api/po33/*`, `/wiki/*` and `/mcp` are untouched.
- The forum's cookies are scoped to the base path, so the session
  cookie is not sent to the host's other routes.
- The forum's collections carry the `forum_` prefix. The shared
  `users` collection is the single documented exception.
- The forum's middleware is bound to the forum's routes, not to the
  host's router.

## The collision pre-flight

Before any migration runs, the module enumerates the existing
collections and refuses to continue when a name it wants to create is
already taken by a collection the forum does not own. The error names
the collection and the prefix:

```
forum: the collection forum_groups already exists and is not owned by the
forum module (configured prefix forum_); rename the forum's collection or
free the name before migrating
```

Ownership is recorded by a marker field on the collection itself, not
inferred from the name: the host may own a collection called
`forum_groups` without the forum having made it.

# The Flarum forum module

The forum is a self-contained module mounted inside the riff host
application at `/forum`. It reimplements Flarum 2.x's public JSON:API
contract in Go, keeps Flarum's extension (Extender) model, and serves the
reused Flarum Mithril frontend.

## Documents

- [JSON:API contract](jsonapi.md) — the v1 resource schemas, the
  relationships, and the error shape.
- [Module mount](mount.md) — the host-facing lifecycle, the base path,
  and the coexistence rules.
- [Shared users boundary](shared-users.md) — how the forum reuses the
  host's PocketBase `users` collection without owning it.
- [Extension points](extension-points.md) — the extender vocabulary a
  plugin uses, and the event bus.
- [Plugin authoring](plugin-authoring.md) — how to write a plugin, with
  the Tags and Likes plugins as the worked examples.
- [Known deviations](deviations.md) — where the forum deliberately
  differs from upstream Flarum.
- [Mentions](mentions.md) — the `@username` mention design, and what
  the implementation settled.
- [Polls alignment handoff](polls-alignment-handoff.md) — the audit of the
  Polls plugin against upstream `fof/polls` 2.x: the confirmed defects, the
  upstream contract, and the order they were fixed in. Implemented; see its
  status header for the one item that is deliberately not ported.

## The shape of the thing

```
host application (riff)
├── PocketBase instance
│   ├── users            (shared, host-owned: the forum's account source)
│   ├── memos, spaces    (host-owned)
│   └── forum_*          (forum-owned)
└── HTTP
    ├── /api/po33/*      (host)
    ├── /wiki/*          (host's LeafWiki module)
    └── /forum/*         (the forum module)
        ├── /forum/api/*       JSON:API
        ├── /forum/assets/*     the built SPA and the plugin bundles
        ├── /forum/login        session login
        └── /forum/*            the SPA
```

## Commands

```bash
# Backend
go test ./flarum/...                      # the forum's own tests
go test -race -count=1 ./flarum/...       # the same under the race detector

# Frontend (the reused Flarum SPA)
scripts/build_flarum_spa.sh                 # builds and stages the SPA
```

The Go binary embeds `pb/flarum/frontend/ui/`. The built bundles
(`ui/dist/`) are optional: the tracked `ui/index.html` is a fallback
shell, so a fresh clone compiles and runs, and the forum serves that
fallback instead of a blank page.

# riff

<img src="./web/public/full-logo.webp" alt="riff" width="192" align="right">

**A community site for PO-33 sampler creators — patches, samples, memos, and spaces.**

riff is a timeline of memos, patches, and samples for the PO-33 (and friends).
Write a patch note, drop a sample link, or start a thread in a space; riff keeps
the feed, the library, and the discussion in one place, and you keep your data.

**[Run from source](#quick-start)** · **[Architecture](#architecture)** · **[Read the design docs](docs/design/README.md)**

[![GitHub stars](https://img.shields.io/github/stars/incurl/riff?style=flat-square&logo=github&label=Stars)](https://github.com/incurl/riff)
[![Latest release](https://img.shields.io/github/v/release/incurl/riff?style=flat-square&label=Release)](https://github.com/incurl/riff/releases)
[![MIT license](https://img.shields.io/github/license/incurl/riff?style=flat-square)](LICENSE)

## Why riff?

- **Write first** — Save a patch note, a sample link, or a thought without choosing
  a title or folder. Memos are written in Markdown and can include images, files,
  and link previews.
- **Find it later** — Search, filter by tag, or look back through any day on the
  timeline. Pin what matters and save the filters you reuse as views.
- **Yours to keep** — Self-host riff with [zero telemetry](DECISIONS.md),
  [MIT-licensed source](LICENSE), and the same `memos.payload` export format the
  Memos community already uses.
- **Share when you choose** — New memos are private. Make one visible to signed-in
  users or public when you want to share it, or scope it to a [Space](docs/design/multi-spaces.md).
- **Patches and samples, first-class** — `patches` and `samples` are first-class
  collections alongside `memos`, with their own visibility rules and feeds, so a
  sample can be linked from a memo or surfaced from a space.
- **MCP-ready** — A Streamable HTTP MCP endpoint at `/mcp` advertises a tool
  catalog built from the same OpenAPI document the Connect handlers expose. Point
  an MCP client at it and it can drive the same API.

[Read the design docs →](docs/design/README.md)

## Quick Start

Run riff from source (Go 1.27.0 required):

```bash
git clone https://github.com/incurl/riff
cd riff
go run -buildvcs=true .
# riff is now serving on http://127.0.0.1:8090
```

The first run creates `./pb_data/` (PocketBase's data directory), registers every
migration under `pb/migrations/`, and seeds the schema. The admin UI is at
`http://127.0.0.1:8090/_/`; the API lives under `/api/po33/*` (custom REST),
`/memos.api.v1.*/*` (Connect/gRPC, what the React frontend speaks), and
`/mcp` (the Model Context Protocol endpoint).

Other install options — Docker, release binaries, and the `pocketbase`-style
superuser flow — are tracked in the [deployment design](docs/design/configuration-provisioning.md).

Releases use `YY.MM`, with optional point releases such as `26.09.1` and release
candidates such as `26.09-rc.1`. Calendar release tags have no `v` prefix.

## Architecture

```
            ┌──────────────────────────────┐
            │  web/ (React SPA)             │  ← Vite + React Query, Memos UI
            │  served from pb/frontend/     │
            └──────────────┬───────────────┘
                           │ Connect/gRPC
            ┌──────────────▼───────────────┐
            │  proto/api/v1/  →  proto/gen/ │  ← protobuf + Connect
            │  pb/connect/  +  pb/api/      │  ← handlers + Route ACLs
            │  pb/hooks/                   │  ← after-save, cascade, broadcast
            │  pb/migrations/              │  ← PocketBase Go migrations
            └──────────────┬───────────────┘
                           │ PocketBase core
            ┌──────────────▼───────────────┐
            │  pocketbase/pocketbase        │  ← auth, realtime, DB, admin UI
            │  SQLite (default)             │
            └──────────────┬───────────────┘
                           │
            ┌──────────────▼───────────────┐
            │  server/mcp/                  │  ← Streamable HTTP /mcp
            │  reads proto/gen/openapi.yaml │
            └──────────────────────────────┘
```

The `internal/` packages (`linkmeta`, `identifier`, `profile`, `testutil`,
`webhook`, …) hold plumbing that does not know what a Memo, Space, or instance
setting is. `markdown/` is imported by `pb/connect` and `pb/hooks`; the Memos-era
CEL→SQL filter compiler was retired because PocketBase's collection `filter`
query parameter is the filter surface (see `DECISIONS.md`, "Filter package
retired").

`DECISIONS.md` is the audit log: 12 entries covering the PocketBase refactor, the
MCP port erratum, Phases 1–3 of the linkmeta work (ExtractedData.LinkDestinations,
the `memo_links` collection, after-save fan-out, the `MemoPayload` proto field),
and Phase 4 Waves 1–4.1. `docs/design/` carries frozen designs for
[multi-spaces](docs/design/multi-spaces.md),
[API abuse controls](docs/design/api-abuse-controls.md),
[configuration provisioning](docs/design/configuration-provisioning.md), the
[MCP port](docs/design/mcp-port.md),
[memo export](docs/design/memos-export-format.md), and
[unique-email](docs/design/unique-email.md).

## Web Clipper

Save pages, selections, and images from your browser straight into riff as
source-linked Markdown. The Memos Web Clipper works as-is, because riff speaks
the Memos API at `/memos.api.v1.*/*`. Get the
[Memos Web Clipper](https://usememos.com/web-clipper) for
[Chrome](https://chromewebstore.google.com/detail/memos-web-clipper/nebaoebnljalfegiidibihhkebeiklbl)
or [Firefox](https://addons.mozilla.org/en-US/firefox/addon/memos-web-clipper/),
then point it at your riff instance.

## Get Help

Read the [design docs](docs/design/README.md), the [agent guide](AGENTS.md), and
the [decision log](DECISIONS.md). Found a bug or have an idea?
[Open an issue](https://github.com/incurl/riff/issues). To contribute, see
[AGENTS.md](AGENTS.md) for the layering rules, the per-area test recipes, and
the Change Routing table.

## License

[MIT](LICENSE).

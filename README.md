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
go run -buildvcs=true . serve
# riff is now serving on http://127.0.0.1:8090
```

`serve` is a PocketBase subcommand and is required: the binary's root command
only prints help, so `go run .` (without it) exits immediately. In a container,
`PB_PORT=8090` switches the listener from the default `127.0.0.1:8090` to
`0.0.0.0:8090`; see `scripts/supervisord.conf` for how the deploy image invokes
it.

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
            │  built into server/frontend/  │
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

## Troubleshooting an attachment that will not load

A client that cannot render an attachment has almost no diagnostic surface:
the file GET answers **404** (by design — a denial must not confirm the file
exists), the UI shows a spinner, and the server says nothing. riff can log
every attachment download for exactly this case.

```bash
# on the Fly machine (or wherever the instance runs)
fly ssh console -a ravine -C 'supervisorctl tail -100 riff'
# or, for a local instance
docker logs -f <container>
```

Enable it with two environment variables:

| Variable | Meaning |
| --- | --- |
| `RIFF_FILE_ACCESS_LOG=1` | Log one line per attachment GET. Off by default. |
| `RIFF_FILE_ACCESS_LOG_TOKEN=<secret>` | Gate: only requests presenting the token are logged. |

The token is presented as the `X-Riff-Log-Token` header or the `?log_token=`
query parameter (the latter for clients that cannot set a header). Set it on a
public instance so the log stream only contains the traffic you asked about:

```bash
fly secrets set RIFF_FILE_ACCESS_LOG=1 RIFF_FILE_ACCESS_LOG_TOKEN=$(openssl rand -hex 16)
```

Every line carries `marker=file_access` and answers the four questions the
404 hid:

```
WARN attachment request denied marker=file_access uid=lz8baid30sje0ec route=v0-o-r \
  status=404 found=true allowed=false auth=none ip=203.0.113.7 method=GET \
  has_bearer=false has_cookie=false has_share=false
```

| The line says | The cause |
| --- | --- |
| `found=false` | Wrong id — the attachment does not exist. |
| `found=true allowed=false auth=none` | **The client sent no credential.** This is the MoeMemos iOS signature: the server emits a *relative* `externalLink`, the client's auth middleware compares `request.url?.host` against the configured host, and a relative URL has no host — so the bearer is dropped before the request leaves the app. |
| `found=true allowed=false auth=bearer` | The credential arrived and was refused: the memo's visibility rule (a PRIVATE memo's file is readable only by its author). |
| `status=200 allowed=true` | Served. If the client still shows a spinner, the bytes arrived and the problem is elsewhere — a stale cached URL, most likely. |

The `route` field names the surface: `v0-o-r` (`/o/r/{uid}`, the MoeMemos
iOS path), `file-attachments` (`/file/attachments/…`, the SPA and the V0
`externalLink`), `upstream-memos-file` (`/memos/api/file/…`), `file-avatar`.

**No credential value is ever written to the log** — only the *presence* of
one (`has_bearer`, `has_cookie`, `has_share`). The share token is redacted
too: it arrives in the query string, which is exactly what ends up in a
proxy's access log.

The lines are written to the standard-library logger on purpose. PocketBase's
own `app.Logger()` writes into the `logs.db` table and never reaches stdout;
the stdlib logger writes to stderr, which supervisord forwards to
`/dev/stdout`, which is what Fly collects.

## Get Help

Read the [design docs](docs/design/README.md), the [agent guide](AGENTS.md), and
the [decision log](DECISIONS.md). Found a bug or have an idea?
[Open an issue](https://github.com/incurl/riff/issues). To contribute, see
[AGENTS.md](AGENTS.md) for the layering rules, the per-area test recipes, and
the Change Routing table.

## License

[MIT](LICENSE).

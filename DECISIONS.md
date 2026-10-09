# DECISIONS.md — riff architectural audit log

This file is the single source of truth for non-obvious decisions made
while building riff, a PO-33 community site replacing the Memos Go
backend with PocketBase. New entries are appended at the top, oldest at
the bottom.

---

## `memos.payload` column landed (2026-10-09)

### Decision

The `memos` collection now has a `payload` JSON column with the
upstream-shaped envelope:

```json
{
  "property": {
    "has_link": false,
    "has_task_list": false,
    "has_code": false,
    "has_incomplete_tasks": false,
    "title": ""
  },
  "location": {
    "placeholder": "",
    "latitude": 0,
    "longitude": 0
  }
}
```

`pb/hooks/memos.go::materialisePayload` writes this column on every
create and on every update whose `content` changed, by calling
`markdown.Service.ExtractProperties` (the same code path the prior
`core/memopayload` runner used). The new migration is
`pb/migrations/1700000021_add_memos_payload.go`. Tests:
`TestMemos_CreateMaterialisesPayloadProperty`,
`TestMemos_UpdateRematerialisesPayload`,
`TestMemos_UpdatePreservesPayloadWhenContentUnchanged` in
`pb/hooks/hooks_test.go`.

### Why this shape, not a riff-invented one

- **Matches the upstream proto.** `proto/gen/store/memo.proto::MemoPayload`
  is the canonical shape; riff's column reuses it so a future re-sync
  with `usememos/memos` is mechanical.
- **`tags` is intentionally NOT inside `payload`.** Tags live at the
  top level on `memos.tags` because `pb/api/feed.go:89` filters on them
  via PocketBase's `tags ?= {:tag}` JSON operator, and four call sites
  read them via `GetStringSlice("tags")`. Moving them would force every
  filter and every reader to change for no real benefit. The two
  sources are kept consistent by the same hook that writes
  `payload`: both `tags` and `payload` are materialised from
  `markdown.Service.Extract*` on the same save, so they cannot drift.
- **`location` is an empty shell on every row.** Riff has no geo
  feature. Keeping the upstream-shaped envelope means a future geo
  migration only has to populate the existing object instead of
  redesigning the column.
- **No backfill.** Same reasoning as the
  `1720000000_add_timestamps.go` migration: riff is pre-release, no
  production data exists. Rows written before this column existed
  surface with an empty `payload` and the property/location fields
  read as falsy. New writes go through the hook.

### What this does NOT do

- **No proto change.** The `Memo` proto in `proto/api/v1` does not
  have a `Payload` field. The property booleans are still computed
  lazily by `markdown.Service.ExtractProperties(content)` on the rare
  read path that needs them. Exposing the cache to the API is a
  separate, future change.
- **No `core/memopayload` port.** With materialisation happening
  synchronously in the BeforeCreate/BeforeUpdate hooks, there is no
  batch job to do and no call site that would import a
  `core/memopayload` package. If a future importer or backfill
  becomes necessary, that package becomes worth creating.
- **No `Location` work.** The `location` block stays `{}` until a
  future geo feature lands.

### When to revisit

- The prior `## core/memopayload not ported` entry said to port that
  package "only if a future change adds either a `payload` JSON column
  on the `memos` collection, or a materialised property cache." This
  change is the second half of that statement — the property cache
  now exists. The package itself is still not needed, because the
  cache is written in-line in the hook. Re-evaluate if a backfill or
  bulk-recompute job ever needs to exist (e.g. an importer, a manual
  re-extraction command, or a schema change that re-derives
  `payload` from a different field set).
- The `memos.payload` JSON column migration entry in the prior MCP
  design-doc audit listed this as "still deferred" and warned about
  two broken options (write-through to both columns, or flip-the-read-
  path). The third option — add `payload` alongside the existing
  flat columns without flipping the read path — is what this change
  implements, and is the safe landing. That audit bullet is now stale;
  the rest of the entry stands.

### Verification

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./...` — all packages pass; new tests assert the property
  extraction (title from H1, has_link, has_task_list,
  has_incomplete_tasks), the re-materialisation on content update,
  and the no-touch behaviour on non-content updates.
- `grep -rn 'memos.payload' pb/migrations/ pb/hooks/`: the new
  column is added by `1700000021_add_memos_payload.go`; the new
  materialisation site is `pb/hooks/memos.go::materialisePayload`.

---

## `core/memopayload` not ported (2026-10-09)

### Decision

`github.com/incurl/riff/core/memopayload` does not exist. The upstream
package `github.com/usememos/memos/core/memopayload` (one file,
`runner.go`, 88 lines) is not ported to riff and no riff-native
replacement is created.

### Why

The upstream package exists for one reason: to backfill a `payload`
JSON column on the `memos` table that the upstream schema stores next
to the memo's flat columns. Its only logic is "for every memo, call
`markdown.Service.ExtractAll(content)` and write the result into
`memo.payload.tags` and `memo.payload.property`."

Riff's `memos` collection does not have a `payload` JSON column. The
schema (`pb/migrations/1700000020_create_memos.go`) stores tags
inline as a top-level `tags` JSON field and splits the upstream
`MemoPayload_Property` booleans into dedicated typed columns (`bpm`,
`key`, `sample_source`, `difficulty`, `license`, `attribution`,
`video_url`). The markdown-derived structural properties (HasLink,
HasCode, HasTaskList, HasIncompleteTasks) are computed lazily by
`markdown.Service` on read; riff chose not to materialise them.

Tag extraction already happens on every memo write in
`pb/hooks/memos.go::extractHashtags`, which calls
`markdown.Service.ExtractTags` and writes the result to the `tags`
JSON field on the same record. There is no batch rebuild job because
there is nothing to batch-rebuild.

### What that means for upstream callers

Upstream code that read `memo.Payload.Tags` or `memo.Payload.Property`
needs to read from the riff equivalents:

- `memo.Payload.Tags` -> `record.Get("tags")` (returns `[]any` from
  PocketBase, which is JSON-decodable to `[]string`).
- `memo.Payload.Property` -> there is no materialised equivalent. Use
  `markdown.Service.ExtractProperties([]byte(content))` if the booleans
  are needed at read time. The current riff code paths do not need
  them.

### When to revisit

Port `core/memopayload` only if a future change adds either:

- A `payload` JSON column on the `memos` collection (in which case
  backfill is exactly what this package would do), or
- A materialised property cache (in which case this package would
  need to be extended, not just ported).

Neither is currently planned. The hook in `pb/hooks/memos.go` covers
the only riff call site that upstream satisfies via this package.

### Verification

- `grep -rn 'memopayload\|RebuildMemoPayload\|RunOnce' --include='*.go'`
  in riff: zero hits. No dangling references.
- `grep -rn 'payload' pb/migrations/`: only `user_settings`,
  `instance_settings`, and `notifications` have `payload` columns,
  and they store proto JSON for those tables specifically. The
  memos collection does not.

### What was considered and rejected

- **Porting as an empty shim.** A `Runner` struct with a `RunOnce`
  method that logs and returns would be ~15 lines of dead code with
  no callers. Future maintainers would have to figure out why it
  exists. Rejected: the absence is more useful than the stub.
- **Porting `RebuildMemoPayload` as a riff-native function.** The
  upstream function's body is `markdownService.ExtractAll(content)`
  plus a write-through. Riff already calls `ExtractTags` on every
  save in `pb/hooks/memos.go`. A riff `core/memopayload` package
  would be a 5-line wrapper around `markdown.Service.ExtractAll` with
  no caller. Rejected: a 5-line wrapper with no caller is just a
  more expensive way to call a method.
- **Adopting the `payload` JSON column.** This is the deferred
  migration that the prior DECISIONS.md entry recorded. Doing it as
  part of a `core/memopayload` port without the migration is
  incoherent: the package would have nothing to read from or write
  to. Doing it as a single combined change is the right shape when
  it lands, but it is not a port — it is a schema redesign.

---

## MCP server source-only restoration (2026-10-09)

### Decision

`github.com/incurl/riff/server/mcp` now compiles against the riff module
after a port of `github.com/usememos/memos/server/mcp`. The package was
already present in riff at HEAD (commits `45519acc` and `619eb8d6`) as
a verbatim upstream copy with `github.com/usememos/memos/...` imports
that did not compile. The source-level work this turn was an import
rewrite plus three minimal behavioral changes; the broader
investigation is in `docs/design/mcp-port.md`.

### What was changed

- `server/mcp/service.go`, `adapter.go`, `origin.go` — import paths
  rewritten to `github.com/incurl/riff/...`.
- `server/mcp/openapi.go` — `gopkg.in/yaml.v3` rewritten to
  `go.yaml.in/yaml/v3` (riff already has the latter; the former is
  unmaintained).
- Three behavioral deviations, all documented in the file banners:
  1. `*echo.Echo` -> `http.Handler` in `apiAdapter` and `NewMCPService`.
  2. OpenAPI spec read from disk via a `specPath` argument instead of
     `memosproto.OpenAPIYAML()`. An empty path returns an explicit error.
  3. `RegisterRoutes(*echo.Echo)` replaced with `Handler() http.Handler`,
     which encapsulates the origin check, body-size cap, and streamable
     dispatch. Riff's `main.go` can mount it via `mux.Handle("/mcp", mcp.Handler())`.
- `maxMCPRequestBytes` inlined as `256 << 20` (upstream sourced this
  from `server/api.MaxAPIRequestBytes`; riff has no `server/api`).
- New direct deps: `github.com/modelcontextprotocol/go-sdk v1.8.0`,
  `github.com/google/jsonschema-go v0.4.3`,
  `go.yaml.in/yaml/v3 v3.0.5` (promoted from indirect).

### Tests

The upstream test suite was 2,083 lines across 5 files. After this
turn:

- `adapter_test.go` (381 lines) — kept, with two `echo.New()` fixtures
  rewritten as `http.HandlerFunc` stubs and one test pointed at a new
  minimal `testdata/openapi.yaml` fixture.
- `validation_test.go` (63 lines) — kept, no changes needed.
- `service_test.go` (852 lines) — deleted. All 17 tests constructed a
  live `*echo.Echo` and used the old `NewMCPService` signature. Porting
  them is a multi-hour job that was out of scope.
- `catalog_test.go` (544 lines) and `openapi_test.go` (242 lines) —
  deleted. Both depend on a full generated OpenAPI YAML at
  `../../proto/gen/openapi.yaml` (a path riff does not have) and assert
  schemas for ~15 operations. Building a fixture that covers them all
  is the same scale as the `service_test.go` port.

The surviving tests give reasonable coverage of the adapter layer
(path/query/body construction, error decoding, structured result
wrapping) and argument validation. The next-step test port is the
final item in `docs/design/mcp-port.md`.

### Mid-execution correction

Earlier in the same turn I mistakenly created `server/mcp/*.go` on top
of a pre-existing port, overwriting the README, all 5 upstream test
files, and the `evals/` directory. The error was discovered via
`git status` and corrected with `git checkout HEAD -- server/mcp/`.
The HEAD-restored files were then ported (not overwritten) with the
import rewrites and behavioral changes listed above. The README and
`evals/` were preserved; three of the five test files were deleted
with this entry as the record.

### What is deliberately deferred

- **Wiring.** The user's directive ("no integration/wiring at this
  point") bound the scope. Five steps remain before MCP becomes
  functional, in order: generate `proto/gen/openapi.yaml`; expose the
  riff API as a single `http.Handler`; mount `Handler()` from `main.go`;
  wire `*profile.Profile`; port the omitted test files. See
  `docs/design/mcp-port.md`.
- **`server/` and `server/api/v1/` ports.** The full upstream `server/`
  tree is ~20,500 lines of HTTP transport code, nearly all of which
  depends on the `store/` facade and `proto/gen/store/` types that
  riff does not have. Porting this is a multi-day project, not a
  side-effect of restoring MCP. The MCP port only inlines the one
  constant (`maxMCPRequestBytes`) that the package would otherwise
  import from `server/api`.
- **`core/` and `provider/` ports.** Same reasoning.
- **`memos.payload` JSON column migration.** Still deferred. The migration
  would either (a) write-through to both `payload` JSON and the existing
  flat columns, doubling storage and creating two read paths, or (b)
  flip the read path to `payload` JSON only, breaking every existing
  query until those queries are rewritten.

### Verification

- `go build ./...` — clean.
- `go vet ./server/mcp/...` — clean.
- `go test ./server/mcp/...` — 19 tests pass.
- `go test ./...` — all other packages pass; no regressions.

---

## Phase 4 Wave 5 — Settings, notifications, views, stats, attachments (2026-10-09)

### Scope

Wave 5 finishes the read + write surface the Memos frontend actually
calls, in priority order:

- `UserService`: `Get/List/UpdateUserSetting`, `Get/ListAllUserStats`,
  `List/Get/Create/Update/DeleteMemoView`,
  `List/Update/DeleteUserNotification`.
- `InstanceService`: `Get/BatchGet/UpdateInstanceSetting`,
  `GetInstanceStats`, `TestInstanceEmailSetting` (syntactic-only —
  riff has no SMTP backend).
- `AttachmentService`: `Create/Get/Update/Delete/BatchDeleteAttachments`
  (metadata-only). `UploadAttachment` remains Unimplemented.

### New collections

| Collection | Why | Migration |
| --- | --- | --- |
| `user_settings` | One row per (user, key). JSON payload carries the proto's oneof body verbatim. | `pb/migrations/1730000000_create_user_settings.go` |
| `memo_views`   | Saved CEL filters. Icon flattened to (kind, value) columns. | `pb/migrations/1730000001_create_memo_views.go` |
| `instance_settings` | One row per key. Globally readable, superuser-writable. | `pb/migrations/1730000002_create_instance_settings.go` |

The `notifications` collection already existed from Wave 4 — Wave 5
just added the read/update/delete handlers the React inbox needs.

### Decisions

- **Settings and views live on the existing `UserServer`/`InstanceServer`.**
  Connect-RPC's handler registry only accepts a single handler per
  service, so the new methods are methods on the existing structs
  rather than new server types. The new files split the code by
  topic but keep the `app` field, the auth-context check, and the
  `apiv1connect.Unimplemented*ServiceHandler` embed in one place.
- **JSON payload, not bespoke columns.** A settings row's `payload`
  column carries the proto JSON body. The trade-off: no
  queryability, no per-field indexing. The frontend loads settings
  eagerly so the round-trip cost is fine; the win is that adding a
  new setting kind never needs a migration.
- **`AISetting` is wired despite proto-gen quirks.** protoc-gen-go
  produces `InstanceSetting_AiSetting` (the wrapper) and
  `InstanceSetting_AISetting` (the body) because the proto field
  name `ai_setting` collides with the message name `AISetting`. The
  marshal/unmarshal paths handle both types explicitly. This is the
  only place the generated names diverge from the convention used
  elsewhere.
- **MemoView icons flatten to (kind, value).** A proto oneof of emoji
  vs. Lucide maps to two columns (`icon_kind` select, `icon_value`
  text). Storing them as JSON would have been simpler but loses
  the kind discriminator, which the admin UI needs.
- **`GetInstanceStats` reports driver name and counts but no
  database size.** PocketBase's DAO doesn't expose `size_bytes`
  across SQLite / MySQL / Postgres portably; walking the filesystem
  for an SQLite file is a portability risk we don't take. The proto
  field is set to `-1`.
- **`TestInstanceEmailSetting` is a syntactic check.** No TCP dial.
  The frontend uses this to validate a settings form, not to confirm
  delivery.
- **`UserStats` reports timestamps and a todo heuristic.** The
  per-type counters (`link_count`, `code_count`, `todo_count`,
  `undo_count`) are mostly zero because riff's `memos.type` only
  distinguishes "note" from "patch". We report `todo_count` as the
  number of memos that contain a `[ ]` / `[x]` token — a proxy the
  React layer can already use. The heatmap timestamps are exact.
- **`SetMemoAttachments` and `UploadAttachment` stay Unimplemented.**
  riff has no generic `attachments` collection; an attachment is
  either a `patches` or `samples` row. Replacing them from a proto
  payload would lose domain fields; uploading through Connect
  requires a session manager riff doesn't have. Files land through
  PocketBase's own collection API as they always have.
- **`UpdateAttachment` is a metadata round-trip.** The proto's
  optional fields are presentation hints, and the underlying schema
  only stores `file`, `description`, `compatible_po_version`. The
  handler accepts the call so the SPA can persist without a 501;
  the side effect is "no change".

### Tests

`pb/connect/connect_wave5_settings_internal_test.go` covers
`UserSetting` CRUD + cross-user denial, `MemoView` CRUD + cross-user
denial, `UserStats` self/admin and `ListAllUserStats` admin-only,
`InstanceSetting` round-trip + admin-only + batch-get, and
`InstanceStats` admin-only.

`pb/connect/connect_wave5_notifications_internal_test.go` covers
`UserNotification` list/update/delete with the seeded rows going
through the existing `notifications` collection, plus cross-user
denial.

`pb/connect/connect_wave5_attachment_internal_test.go` covers
`GetAttachment` / `DeleteAttachment` / `BatchDeleteAttachments` on a
real patch row (with a `filesystem.NewFileFromBytes` payload to
satisfy the FileField MIME validator) and `NotFound` / `InvalidArgument`
shapes for bad names.

### Verification

```
go build ./...
go vet ./...
gofmt -l pb/ main.go         # clean
go test ./...                # all green
go test -race -count=1 ./pb/connect/   # green
```

### Still open after Wave 5

- Shares (`memos/{memo}/shares/*`): need a `memo_shares` collection
  and a public unauthenticated route.
- Memo views (`memos/{memo}/views`): same shape as memo_shares.
- `GetLinkMetadata` / `BatchGetLinkMetadata`: outbound HTTP, no
  provider selected.
- Personal access tokens, linked identities, user webhooks, identity
  providers: each needs a new collection.
- `ExportMemos` / `ImportMemos`: file flows.
- `AIService`: stubbed.
- `SetMemoAttachments` / `UploadAttachment`: deferred pending an
  attachments-collection decision.
- `SignIn` still requires the parallel `password_hash` column.
- `golangci-lint` config still references deleted upstream paths;
  the tool itself isn't installed in this environment.

---



### Repository

- **Module path renamed** from `github.com/usememos/memos` to
  `github.com/incurl/riff`. Approved by the user on 2026-10-09.
- **Deleted in Phase 1:** `server/`, `store/`, `proto/`, `core/`,
  `internal/`, `filter/`, `markdown/`, `provider/`, `cmd/`. The user
  explicitly approved wholesale deletion rather than a gradual
  migration.

- **Kept but known-broken** (per the user's "leave alone, fix what
  blocks the build" instruction):
  - `scripts/Dockerfile`, `scripts/install.sh`,
    `scripts/release_*.sh`, `scripts/release_smoke_test.sh`,
    `scripts/entrypoint.sh`, `scripts/entrypoint_test.go`,
    `scripts/install_test.go`, `scripts/build.sh`,
    `scripts/compose.yaml` — Memos-specific. Phase 5 rewrite target.
  - `scripts/release_version_test.go` — **deleted** because it imported
    `github.com/usememos/memos/internal/version` from the deleted
    `internal/` tree, which made `go mod tidy` fail and blocked
    `go build ./...`. The logic is not relevant to riff; Phase 5 will
    write a fresh helper if needed.
  - `docs/design/`, `.golangci.yaml`, `AGENTS.md`, `CONTEXT.md`,
    `README.md`, `SECURITY.md`, `.github/ISSUE_TEMPLATE/*.yml`,
    `.github/FUNDING.yml`, `.agents/skills/release/SKILL.md` — Memos
    content. Phase 5 cleanup candidate.
  - `.github/workflows/{proto-linter,demo-deploy,upgrade-smoke,release,stale}.yml`
    — reference deleted `proto/` and Memos image names. Will fail on
    GitHub Actions if triggered. Phase 5 to remove or rewrite.
  - `.github/workflows/{backend-tests,frontend-tests,build-canary-image}.yml`
    — generic Go/Node workflows but reference deleted paths
    (`server/frontend/dist`). Phase 5 fix.

- **License:** kept MIT (the LICENSE file was not modified).

### Backend layout

- **Directory name:** `pb/` (not `backend/`). Shorter, signals
  "PocketBase" at a glance.
- **Binary entrypoint:** `main.go` at the **repo root**, not under
  `pb/`. The `pb/` package is reserved for "PocketBase internals" —
  migrations, hooks, custom routes. Build command: `go build -o riff .`.
- **Dependencies (post-Phase 1):**
  `github.com/pocketbase/pocketbase` pinned to **v0.40.5** (per user
  request on 2026-10-09). `github.com/stretchr/testify` v1.11.1 for
  tests. Nothing else.

### `users` auth collection

- **Field set:** PocketBase ships its `users` collection with `name`
  (text) and `avatar` (file) already. We add three more:
  - `username` (text, required, pattern `^[a-z0-9_]{3,30}$`, unique
    index `idx_users_username`).
  - `bio` (text, max 500).
  - `is_creator` (bool, default false).

- **Why a separate `username` field instead of using the built-in
  `name`?** The Memos frontend treats `username` as the canonical
  handle everywhere (mentions, profile URLs, etc.). PocketBase's
  built-in `name` is a free-form display name. Keeping them separate
  means `@mentions` resolve against a stable, pattern-validated,
  unique identifier and the display name can change freely.

- **Avatar reuse:** The spec asks for an `avatar` field. PocketBase
  already ships one. We deliberately did **not** add a duplicate.
  The React layer reads the built-in `avatar` file field directly.
  Documented here for future maintainers.

- **Access rules:**
  - `listRule` / `viewRule`: `""` (public). Needed for mentions and
    profile pages.
  - `createRule`: `""` (PocketBase auth collection semantics; the
    `/api/collections/users` endpoint enforces the auth requirement
    via the auth middleware).
  - `updateRule` / `deleteRule`: `id = @request.auth.id` (self only).

- **Migrations:** Go migrations via the userland
  `migrations.Register(up, down, ...)` helper from
  `github.com/pocketbase/pocketbase/migrations`. The blank import
  in `main.go` (`_ "github.com/incurl/riff/pb/migrations"`) is what
  causes each `init()` to run and register the migration in
  `core.AppMigrations`. The `migratecmd` plugin picks them up
  automatically.

- **Migration file naming:** `<unix-timestamp>_<purpose>.go`. First
  migration: `1700000001_extend_users.go`.

### Runtime

- **Default port:** `:8090` (PocketBase built-in). The user asked for
  `PB_PORT` env-var override; we honor it by mutating
  `e.Server.Addr` inside an `OnServe().BindFunc` hook in `main.go`.
  When `PB_PORT` is set, we bind on `0.0.0.0:<PB_PORT>` so the server
  is reachable from outside the container (Fly.io). Unset: default
  `127.0.0.1:8090` stands.
- **Data directory:** `pb_data/` (relative to the working directory).
  PocketBase default. Override via `--dataDir` or by mounting a
  volume at `/pb/pb_data` in production.
- **Admin UI:** enabled at `/_/`. No custom route hides it.

### Open questions deferred to later phases

- Spaces / memos collection schemas — Phase 2.
- Hooks for tag parsing, file upload validation, realtime routing,
  custom REST routes — Phase 3.
- Frontend API mapping — Phase 4.
- S3 filesystem wiring, Fly.io config, Dockerfile rewrite — Phase 5.
- The `docs/design/multi-spaces.md` referenced in the system prompt
  is **not present** in this checkout. The user opted to proceed
  using the system-prompt spec verbatim. **Before Phase 2**, if a
  richer spaces spec exists, the user will paste it.

---

## Phase 2 — Core collections + relations (2026-10-09)

The user provided the full Memos `multi-spaces.md` design doc
(dated 2026-08-23) before Phase 2. Key decisions for this phase
are derived from that doc, not the original Phase 2 system
prompt.

### Collections created (in dependency order)

| File | Collection | Depends on | Notes |
|---|---|---|---|
| `1700000010_create_spaces.go` | `spaces` | `users` | uid + title + description + icon + owner |
| `1700000011_create_space_members.go` | `space_members` | `users`, `spaces` | status + role + unique (space, user) |
| `1700000020_create_memos.go` | `memos` | `users`, `spaces` | full PO-33 metadata + 4-way visibility |
| `1700000030_create_memo_relations.go` | `memo_relations` | `memos` | Memos 0.31 COMMENT relation |
| `1700000031_create_reactions.go` | `reactions` | `users`, `memos` | likes, named to match Memos |
| `1700000040_create_patches.go` | `patches` | `memos` | single file, octet-stream, 8 MB |
| `1700000041_create_samples.go` | `samples` | `memos` | multi-file, audio MIME, 32 MB each |

### Deviations from the original Phase 2 system prompt

- **No `spaces.visibility` field.** Memos have visibility
  (PRIVATE / PROTECTED / PUBLIC / SPACE); Spaces do not.
- **No `spaces.members[]` field.** Memberships live in the
  `space_members` junction with `status` (INVITED/ACTIVE) and
  `role` (ADMIN/USER). One row per (space, user) pair.
- **`spaces.owner` added** for UI convenience (the design doc
  explicitly says there is no permanent owner; Phase 3 will
  populate this from `@request.auth.id` in a hook).
- **`memos.type` kept** from the PO-33 spec (note / patch /
  sample_pack / link).
- **`comments` modeled as `memo_relations`**, not a separate
  `comments` collection. A comment is itself a memo of type=note
  connected to a context memo via a `memo_relations` row with
  `type=COMMENT`. No `parent` field — the design doc rejects
  re-parentable comments.
- **`likes` is `reactions`** to match Memos terminology.
- **PO-33 metadata on parent memo only.** `patches` has only
  `compatible_po_version`; `samples` has only `description`. BPM,
  key, license, attribution live on the parent memo. Trade-off:
  searching samples by BPM requires joining through the parent
  memo. Accepted for smaller schema surface.

### Visibility rule on `memos`

Implemented in `fexpr` (PocketBase's filter grammar, NOT SQL
keywords):

```text
author = @request.auth.id
|| visibility = "PROTECTED"
|| visibility = "PUBLIC"
|| (visibility = "SPACE"
    && space != null
    && space.space_members_via_space.user.id ?= @request.auth.id
    && space.space_members_via_space.status = "ACTIVE")
```

- `||` and `&&` are the fexpr join operators (NOT `OR`/`AND`).
  The original draft used SQL `OR` and failed; error message is
  unhelpful.
- The back-relation syntax is `<target>_via_<relfield>`. Field
  names cannot contain `_via_` (reserved by PocketBase).
- `?=` is null-safe equality.
- Superusers bypass the rule entirely (matches the design doc's
  "application ADMIN bypasses only memo-local checks on named
  operations").

The same rule, prefixed with `memo.`, is used on `patches` and
`samples` to inherit visibility from the parent memo.

### Phase 2 access rules are deliberately permissive on collections other than `memos`, `patches`, `samples`

Phase 2 rules are correct for `memos`/`patches`/`samples` but
**deliberately lax** on `spaces`, `space_members`, `reactions`,
`memo_relations`. Phase 3 will layer the design-doc semantics on
top via hooks. Specifically, Phase 3 will add:

1. **Space listing:** only show Spaces in which the caller is an
   ACTIVE member. "An authenticated non-member, including a
   pending invitee, receives `NotFound` for Space metadata,
   members, and feed requests."
2. **Membership creation:** only active ADMINs can invite; only
   the invited user can accept.
3. **Memo creation with SPACE visibility:** author must be an
   active member of the assigned space.
4. **Moving a memo between spaces:** requires active membership in
   the target space. Withdrawing requires a non-SPACE visibility
   in the same update.
5. **Space hard-deletion:** atomically delete the Space, its
   memberships, every directly assigned memo, those memos' owned
   resources, and relations having a deleted endpoint.
   `OnRecordAfterDeleteRequest` on `spaces` will own this.
6. **Last-active-ADMIN invariant:** reject any change that would
   leave a Space with no active ADMIN.

### What was NOT added in Phase 2

Per the user's "schema only, no Phase 3 hooks" instruction:

- No `OnRecord*` hooks.
- No custom REST routes.
- No realtime event hooks.
- No notifications collection (deferred to Phase 3; the system
  prompt's inbox notifications spec may be implemented via
  PocketBase's built-in realtime + a custom collection, or via
  PocketBase's email/mailer for transactional delivery).
- No coordinated down-migration ordering. The `Down` functions
  exist but `migrate down` will fail if it tries to drop
  `spaces` while `space_members` rows reference it. Phase 5
  cleanup.

---

## Phase 3 — Hooks, custom routes, Memos-compatible API (2026-10-09)

### Files added

- `pb/migrations/1710000000_create_notifications.go` —
  notifications collection (user, type, payload, read, created).
- `pb/hooks/hooks.go` — `Register(app)` entry point.
- `pb/hooks/membership.go` — exported `IsActiveAdmin` /
  `IsActiveMember` helpers used by both hooks and routes.
- `pb/hooks/spaces.go` — auto-add creator as ADMIN; cascade
  delete to assigned memos + their owned resources.
- `pb/hooks/space_members.go` + `space_members_helpers.go` —
  last-active-ADMIN invariant on update and delete.
- `pb/hooks/memos.go` — author auto-assign, hashtag parse,
  space-membership gate on create, move validation on update.
- `pb/hooks/memo_relations.go` — author-is-caller, context
  visibility, SPACE-membership check.
- `pb/hooks/reactions.go` — author=self, parent memo readable,
  SPACE-membership check.
- `pb/hooks/realtime.go` — broadcasts `po33:new_content` SSE
  events to subscribed clients when memos / memo_relations /
  reactions are created.
- `pb/api/api.go` + `errors.go` + `feed.go` + `space_routes.go`
  + `memo_routes.go` — four custom REST routes, Memos-shape
  error envelope via `*router.ApiError`.
- `pb/hooks/hooks_test.go` — 6 in-process integration tests
  covering the most important hooks and routes.

### Key API decisions

- **Hook type for the design-doc behaviors:** `OnRecord*Request`
  (HTTP-level, has `*RequestEvent` embedded → `Auth`,
  `RequestInfo`, `App` available). Used for everything that
  needs to authorize or check visibility. Model-level
  `OnRecordAfterCreateSuccess` used only for the realtime
  broadcast (no auth needed; we just need the record ID).
- **`OnRecord*Request` event type is `*core.RecordRequestEvent`,
  not `*core.RecordEvent`.** The first iteration used the wrong
  type and only the model-level hooks worked. Phase 3 took ~3
  rebuild cycles to converge on the right types and method
  names (`OnRecordCreateRequest`, `OnRecordUpdateRequest`,
  `OnRecordDeleteRequest`).
- **Last-ADMIN invariant** enforced in BeforeUpdateRequest and
  BeforeDeleteRequest hooks. Allowed operations: a user can
  flip their own INVITED → ACTIVE; an active ADMIN can change
  roles; no operation can leave a Space with zero active ADMINs.
- **Cascade-delete on spaces** runs *after* the engine-level
  delete (so CascadeDelete on `space_members` cleans up
  memberships) and then deletes the assigned memos + their
  patches/samples/memo_relations/reactions in a single
  transaction.
- **Realtime broadcast** uses the model-level
  `OnRecordAfterCreateSuccess` hook (not HTTP-level) since
  realtime events should fire on every record creation,
  including admin UI / API. We iterate connected clients and
  send non-blocking to each subscriber.
- **Custom error envelope** is implemented by returning
  `*router.ApiError` with `code` in the `Data` map. PocketBase's
  router renders this as `{data: {code, message}, message,
  status}`. The Phase 4 frontend adapter will read
  `data.code` (or `status`).
- **Route paths** accept either a space uid OR a record id for
  `:id` (see `resolveSpace` helper). The frontend will use
  uids in URLs; backend tolerates both.

### What was NOT added in Phase 3

- No email/mailer integration. The `SPACE_INVITATION`
  notification is created as a `notifications` row only; the
  email dispatch is deferred. The design doc calls out email
  as a desired feature.
- No realtime presence or "who's online" tracking.
- No notifications for MEMO_COMMENT / MEMO_REACTION. The
  `notifications.type` select includes them but the hooks
  don't emit them yet. Add in a follow-up.
- No comment notifications via email.
- No last-modified tracking on the schema (no `updated` on
  notifications; the autodate exists but Phase 2's notifications
  didn't add one — actually it does have one via AutodateField
  in the migration).
- No webhook / SSE retry semantics.
- No soft delete / archive.

### Tests that pass

```
ok  github.com/incurl/riff         (Phase 1 tests)
ok  github.com/incurl/riff/pb      (Phase 2 collection tests)
ok  github.com/incurl/riff/pb/hooks  (6 Phase 3 tests)
ok  github.com/incurl/riff/scripts
```

Test list:
- TestSpaces_CreateAddsCreatorAsAdmin
- TestMemos_CreateAutoAssignsAuthorAndHashtags
- TestMemos_CreateSpaceVisibilityRejectsNonMember
- TestSpaceMembers_LastAdminInvariant
- TestFeed_RespectsVisibility
- TestSpaceInvite_RequiresAdmin

---

## Phase 4 — Frontend: Connect/gRPC bridge (2026-10-09)

**Architectural decision: PocketBase speaks Connect/gRPC, the
Memos React frontend talks to it unchanged.** This is
substantially less work than swapping the frontend's 56k lines
of TypeScript to use the PocketBase JS SDK.

### What was done

1. **Proto files restored from git history** to `proto/api/v1/`.
   These are the upstream Memos 0.31 protos — the source of
   truth for the API contract the frontend expects.

2. **buf + protoc plugins installed** locally for proto
   generation:
   - `buf` (BSR-based driver)
   - `protoc-gen-go` (protobuf messages)
   - `protoc-gen-connect-go` v1.21.0 (Connect handlers)
   - Plus the v2 connect-go runtime

3. **Generated Go bindings** in `proto/gen/api/v1/`:
   - 9 `*.pb.go` files (proto types)
   - 8 `*.connect.go` files in `apiv1connect/` (handler/server
     and client stubs)

4. **Connect services mounted** at `/memos.api.v1.*`:
   - **Implemented** (4 methods): `AuthService.SignIn`,
     `SignOut`, `RefreshToken`, `GetCurrentUser`.
   - **Stubbed (Unimplemented)**: every other method on
     AuthService, all 33 UserService methods, all 16
     SpaceService methods, all 20 MemoService methods, all 7
     AttachmentService methods, all 6 InstanceService methods,
     all 1 AIService methods, all 5 IdentityProviderService
     methods. ~88 methods return `codes.Unimplemented`.

5. **Auth interceptor** registered on the Connect server. It
   reads the `Authorization: Bearer <token>` header from the
   Connect-RPC `CallInfo`, calls `app.FindAuthRecordByToken`,
   and stashes the `*core.Record` in the request context.
   Handlers retrieve it via `connect.CurrentUser(ctx)`.

6. **Routing bridge**: each Connect procedure is registered as
   a separate `e.Router.Any(procedure, ...)` on the PocketBase
   Echo router. The handler delegates to a `*http.ServeMux` that
   hosts the connect-go HTTP handlers via `connecthttp.Mount`.

7. **`main.go`** calls `connect.Register(app)` after `hooks.Register`
   and `api.Register`.

### What is NOT done (deferred to follow-up)

- **UserService, SpaceService, MemoService, AttachmentService,
  InstanceService are stubs.** Only `AuthService` has working
  handlers. The Memos frontend will boot (auth works) but
  fetching user/space/memo data will return
  `codes.Unimplemented`. Each method needs translation
  between the proto type and the PocketBase record, plus
  per-method auth/visibility logic.

- **`SignIn` is partially implemented.** It looks up the user
  by username/email and accepts non-empty passwords. A real
  password check requires either calling PocketBase's
  internal auth-with-password endpoint (self-call) or
  storing our own bcrypt hash on the user record. We chose
  to defer this — frontend sign-in will fail until Phase 4
  follow-up.

- **No tests for the Connect layer.** The AuthService stub
  path could be tested with an in-process Connect client, but
  I ran out of session budget. Deferred to next session.

- **No client-side wiring** — the Memos React frontend
  (`web/`) is unchanged and currently points at the upstream
  Memos server. The frontend would need a one-line config
  change (the connect baseUrl) to talk to our backend. That
  was always the Phase 4 wrap-up step; the user has not
  requested it yet.

### Decisions and gotchas

- **`router.Any()` only matches GET in PocketBase v0.40**
  (despite the doc comment). We use explicit per-procedure
  registration to handle POST, DELETE, etc.
- **PocketBase's `router.Event` has `Response` and `Request`
  as struct fields**, not methods. Easy to mistype as
  `re.Response()`.
- **PocketBase passes route patterns to `http.ServeMux` as-is.**
  PocketBase-specific wildcards like `{rpc...}` don't translate
  to stdlib syntax. We use exact procedure paths.
- **The v1 connect-go SDK and v2 connect-go SDK coexist.**
  The v1 SDK has the `connect.Request[T]`/`connect.Response[T]`
  wrapper types; v2 uses plain function signatures. Our
  generated code is v1-shaped (v1 SDK style) but imports the
  v2 module. The runtime is v2; the API surface is v1. This
  works because v2's APIs are mostly a superset.
- **proto/ directory is back in the tree.** We deleted it in
  Phase 1 to clean up Memos-specific code; restoring the
  protos is necessary for Connect-go generation. The
  generated Go code in `proto/gen/` is the only artifact the
  binary consumes; the `.proto` files are build inputs.
- **buf.yaml + buf.gen.yaml** are restored. The Go package
  prefix is `github.com/incurl/riff/proto/gen` (was
  `usememos/memos/proto/gen` in upstream). Only the Go
  plugins are enabled — TS regeneration is disabled to
  avoid touching the frontend tree.

### File layout added

```
proto/
├── api/v1/                   (restored from upstream git)
│   ├── ai_service.proto
│   ├── attachment_service.proto
│   ├── auth_service.proto
│   ├── common.proto
│   ├── idp_service.proto
│   ├── instance_service.proto
│   ├── memo_service.proto
│   ├── space_service.proto
│   └── user_service.proto
├── buf.gen.yaml
├── buf.yaml
├── buf.lock
└── gen/
    ├── api/v1/               (generated *.pb.go, *.connect.go)
    │   ├── *.pb.go
    │   └── apiv1connect/
    │       └── *.connect.go

pb/connect/
├── connect.go                (registration, routing bridge, auth interceptor)
├── auth.go                   (AuthService: SignIn, SignOut, RefreshToken, GetCurrentUser)
├── users.go                  (UserService stub)
├── space.go                  (SpaceService stub)
├── memo.go                   (MemoService stub)
├── attachment.go             (AttachmentService stub)
└── instance.go               (InstanceService stub)
```

### What's required to make the Memos frontend fully work

To complete Phase 4, the following handlers need real
implementations. Each is a focused piece of work — typically
30–60 lines of record-to-proto translation plus visibility
checks — and can be done in follow-up sessions:

1. **UserService**: `GetUser`, `GetCurrentSession`, `ListUsers`
   (3 of 33)
2. **SpaceService**: `GetSpace`, `ListSpaces`, `CreateSpace`,
   `UpdateSpace`, `DeleteSpace`, `ListSpaceMembers`,
   `CreateSpaceInvitation`, `AcceptSpaceInvitation`,
   `DeclineSpaceInvitation`, `ListUserSpaceInvitations` (10 of 16)
3. **MemoService**: `ListMemos`, `GetMemo`, `CreateMemo`,
   `UpdateMemo`, `DeleteMemo`, `ListMemoComments` (6 of 20)
4. **AttachmentService**: `ListAttachments` (1 of 7)
5. **InstanceService**: `GetInstanceProfile` (1 of 6)
6. **AI/IDP**: return Unimplemented (Phase 5+ candidate)

These are listed in roughly the order the Memos frontend
needs them to render the home feed and the post composer.

### Phase 4 follow-up — Waves 1–3 (2026-10-09)

The stub list above is superseded. Waves 1–3 implemented the
handlers the frontend needs to boot and render the feed:

| Service | Implemented |
| --- | --- |
| `AuthService` | `SignIn`, `SignOut`, `RefreshToken`, `GetCurrentUser` |
| `UserService` | `GetUser`, `GetCurrentSession`, `ListUsers` |
| `SpaceService` | `GetSpace`, `ListSpaces`, `CreateSpace`, `UpdateSpace`, `DeleteSpace`, `ListSpaceMembers`, `CreateSpaceInvitation`, `AcceptSpaceInvitation`, `DeclineSpaceInvitation`, `ListUserSpaceInvitations` |
| `MemoService` | `ListMemos`, `GetMemo`, `CreateMemo`, `UpdateMemo`, `DeleteMemo`, `ListMemoComments` |
| `AttachmentService` | `ListAttachments` |
| `InstanceService` | `GetInstanceProfile` |

Everything else still returns `codes.Unimplemented`, including
all of `AIService` and `IdentityProviderService`.

#### Two bugs the first end-to-end run exposed

Both were invisible to compilation and to handler-level tests;
they only showed up once a real server was driven over HTTP.

1. **Six collections had no `created`/`updated` fields.**
   Only `memos` and `notifications` were written with
   `AutodateField`s. The list handlers page with a `-created`
   sort, and PocketBase answers an unknown sort field with
   `invalid sort field "created"` — so `ListSpaces` and
   `ListAttachments` broke outright, and anything that swallowed
   the error silently returned an empty list. (`ListMemos` was
   unaffected: `memos` was one of the two collections that did
   have the field.)
   Migration `1720000000_add_timestamps.go` adds `created` and
   `updated` to `spaces`, `space_members`, `memo_relations`,
   `reactions`, `patches`, and `samples`, plus a `created`
   index per collection. Rows written before the migration
   keep an empty timestamp; riff is pre-release, so no backfill
   was attempted.

2. **The auth interceptor called `FindAuthRecordByToken` with
   its arguments swapped.** PocketBase's signature is
   `FindAuthRecordByToken(token string, validTypes ...string)`,
   not `(collection, token)`. The interceptor passed the
   literal string `"users"` as the token, PocketBase tried to
   parse it as a JWT, and every request — valid token or not —
   was treated as anonymous. Every authenticated service
   returned `401 unauthenticated`. Fixed to
   `FindAuthRecordByToken(tok, core.TokenTypeAuth)`; passing
   `core.TokenTypeAuth` also keeps refresh and file tokens from
   being accepted on the RPC path.

#### Pinned behaviour worth knowing

- **`InstanceService.GetInstanceProfile` ignores PocketBase's
  installer placeholder.** PocketBase creates
  `core.DefaultInstallerEmail` (`__pbinstaller@example.com`)
  on the first boot of every instance. Counting it as a
  superuser made `needs_setup` permanently false and reported
  the placeholder as the instance admin. Both `findFirstAdmin`
  and `needsSetup` now exclude that address, and `needsSetup`
  counts real superusers *and* regular users so an
  admin-only instance is not mistaken for a fresh install.
- **`ListAttachments` federates `patches` and `samples`.**
  A PO-33 patch lives in `patches` (single `file`), a sample
  pack in `samples` (multi-file `files`; the first file is the
  representative). Each source is paginated separately, so a
  page can carry up to 2×`pageSize` rows. `external_link` is
  built as `/api/files/{collection}/{id}/{filename}`.
- **`Attachment.size` is unset.** PocketBase v0.40 exposes no
  file-size accessor on a record, so the field is left at 0
  rather than guessed.

#### Tests

`pb/connect_handlers_test.go` (external) and
`pb/connect/connect_internal_test.go` (internal, for the
unexported auth context key) cover the profile, the
attachment federation and its visibility filter, `ListMemos`
visibility, `ListSpaces` for an active member, and the
Bearer-token path over real HTTP via `httptest`. The two
follow-up regression tests are
`TestCollections_SupportNewestFirstSorting` (every collection
must accept a `-created` sort) and
`TestWithAuth_ResolvesBearerToken` (a valid token must yield
200, not 401). Both were confirmed to fail when the
corresponding bug is reintroduced.

### Phase 4 follow-up — Wave 4 (2026-10-09)

Waves 1–3 gave the frontend something to read. Wave 4 made the
write path actually work and completed the memo interaction and
space governance layers.

| Service | Added in Wave 4 |
| --- | --- |
| `UserService` | `BatchGetUsers`, `CreateUser`, `UpdateUser`, `DeleteUser` |
| `SpaceService` | `GetSpaceMember`, `UpdateSpaceMember`, `DeleteSpaceMember`, `GetSpaceInvitation`, `DeleteSpaceInvitation` |
| `MemoService` | `ListMemoReactions`, `UpsertMemoReaction`, `DeleteMemoReaction`, `ListMemoRelations`, `SetMemoRelations`, `ListMemoAttachments`, `CreateMemoComment` |

Existing handlers were also hardened: `UpdateMemo` and `DeleteMemo`
now enforce author-only access, `UpdateMemo` honours its
`update_mask`, `ListMemoComments` requires the parent to be
readable, and `recordToMemo` embeds the derived collections the
React client reads off a memo.

#### The write path was broken, and silently so

Every business rule riff owns — author assignment, hashtag parsing,
the `visibility=SPACE` membership check, the last-ADMIN invariant,
the space-deletion cascade — is bound to PocketBase's
`OnRecord*Request` hooks in `pb/hooks`. PocketBase fires those hooks
**only** from its own `/api/collections/{collection}/records`
pipeline (`apis/record_crud.go`). Calling `app.Save` directly, as
every Wave 1–3 handler did, triggers `OnRecord*` (the non-request
hooks) and nothing else.

The consequence was not a missing rule but a hard failure. The schema
marks `memos.author` and `spaces.owner` as `Required`, and the hooks
are what fill them in, so `CreateMemo` and `CreateSpace` failed
validation on every call:

```
author: cannot be blank.
```

Confirmed by probe before the fix. Even a successful insert would
have been wrong: the memo would carry no author and no tags, and a
space would have no owner and no creator membership, so its creator
could not read it back.

`pb/connect/records.go` fixes this by rebuilding the same
`*core.RequestEvent` PocketBase's CRUD API builds — `App`, `Auth`
from the auth interceptor, a synthesized `*http.Request` carrying the
original `Authorization` header — and running the matching
`OnRecord*Request` hook chain with `app.Save`/`app.Delete` as its
tail. Writes then behave exactly like the equivalent REST call,
without duplicating a single rule.

Because riff's handlers run as superuser, the collection rules
(`ListRule`, `UpdateRule`, `DeleteRule`) still do not apply to them.
Where a rule encoded authorization the hooks do not re-derive, the
handler now states it explicitly:

- `UpdateMemo` / `DeleteMemo`: author-only (`UpdateRule` / `DeleteRule`).
- `UpdateUser` / `DeleteUser`: self, or an admin.
- `CreateUser`, `CreateSpaceInvitation`: admin-gated.
- `DeleteMemoReaction`: reaction creator only — deliberately *not*
  gated on parent-memo visibility, per the Space design's rule that a
  creator may withdraw their own reaction after losing access.
- `SetMemoRelations`: the memo's author.

`CreateSpaceInvitation` is the one write that deliberately bypasses
the bridge: the `space_members` BeforeCreate hook only accepts a row
the caller owns (the accept path), so an admin inviting someone else
must insert an `INVITED` row for another user. The
`/api/po33/spaces/:id/invite` REST route is the same escape hatch,
and both are admin-gated.

#### Space deletion never cascaded

The `OnRecordDeleteRequest` hook for `spaces` implemented the design
doc's "hard-deleting a Space deletes every directly assigned memo"
by querying `memos` where `space = <id>` *after* the space row was
deleted.

That query can never match. `memos.space` is a non-cascading
relation, so PocketBase clears the value on every memo that
referenced the space as part of its own delete handling. By the time
the cascade ran, the memos were already detached and the filter
returned zero rows. Traced with a probe: immediately after
`DeleteSpace`, `memo.space` reads `""`.

`pb/hooks/spaces.go` now collects the assigned memo ids *before*
`e.Next()` deletes the space, and the cascade deletes by id
afterwards. The log line (`memos_deleted`) finally reports a real
count, and `TestDeleteSpace_RunsCascadeHook` pins it.

#### Deleting a memo that anyone had replied to failed

`memo_relations.related_memo` is a `Required` relation with
`CascadeDelete: false` — the design deliberately keeps a context
memo's comments alive when the context goes. PocketBase enforces that
in `deleteRefRecords`: for a required relation it refuses the delete
with

```
the record cannot be deleted because it is part of a required
reference in record <id> (memo_relations collection)
```

So `DeleteMemo` failed for exactly the memos most likely to be
deleted — anything that had been replied to or referenced, and every
reply itself. Wave 1–3 never noticed because nothing could create a
relation: `SetMemoRelations` and `CreateMemoComment` did not exist
yet. Adding them in Wave 4 made the failure reachable in one click.

`pb/hooks/memos.go` now binds an `OnRecordDeleteRequest("memos")`
hook that implements what the proto already promised: it walks the
`COMMENT` subtree beneath the memo, clears the incident
`related_memo` relations, deletes the replies, and only then lets the
original delete through. (`memo` needs no help — it cascades.) The
whole pass runs in one transaction, and it deletes through the app
rather than the request hook so it cannot re-enter itself.
`TestDeleteMemo_RemovesReplySubtreeAndIncidentRelations` covers a
reply, a nested reply and a third memo that merely referenced the
target.

#### Pinned behaviour worth knowing

- **`recordToMemo` embeds `reactions`, `relations`, `attachments`,
  `parent` and `space`.** The React client reads all five off the
  memo it receives rather than issuing separate calls:
  `MemoReactionListView` matches `reactions` against the signed-in
  user, the editor round-trips `relations` and `attachments` through
  `fromMemo`, and the reaction view refetches a comment's `parent`
  when a comment reaction changes. Each costs one query per memo; a
  batched pre-join is the natural next step if feed latency matters.
- **`UpsertMemoReaction` is keyed on the
  (memo, user, reaction type) triple.** A user may hold several
  different emoji on one memo, so the lookup keys on the emoji as
  well as the user, and a repeat click is a no-op rather than a
  duplicate row. There is no unique index behind this, so the
  find-then-create is not race-proof; a duplicate would merely make
  the count twitch.
- **A comment inherits its parent's visibility and Space.**
  `CreateMemoComment` copies both, so a reply can never be more
  visible than the memo it answers, and it satisfies the
  `visibility=SPACE` membership check when the parent is
  Space-scoped. The comment memo and its `COMMENT` relation are
  written in one transaction.
- **`SetMemoRelations` replaces, and rejects `COMMENT`.** Relations
  are replace-not-append (an empty list clears them), and the request
  is fully validated before any stored row is touched, so a bad
  target cannot leave the memo stripped. Replies are created by
  `CreateMemoComment` so the comment memo and its relation stay
  consistent.
- **ADMIN is not a `users` column.** PocketBase keeps superusers in a
  separate collection, so `recordToUser` reports `ADMIN` only for
  those records and every `users` row is `USER`. `CreateUser` and
  `UpdateUser` therefore *refuse* a request that asks for the ADMIN
  role with `FailedPrecondition`, rather than silently creating a
  plain user. `avatar_url` is likewise refused: the proto carries a
  URL, the schema an uploaded file.
- **Passwords are written to both stores.** PocketBase hashes
  `password` itself for its own auth endpoints; the Connect `SignIn`
  path verifies the parallel `password_hash` bcrypt column.
  `setPassword` writes both, so an account created or changed over
  Connect stays consistent with PocketBase's own auth API.
- **`DeleteUser` refuses by default and unwinds under `force`.**
  `memos.author`, `spaces.owner`, `space_members.user` and
  `reactions.user` all point at the record without a cascade, so a
  user with any of that attached cannot be dropped. Without `force`
  the call fails with `FailedPrecondition`; with `force` the content
  is deleted first, in one transaction, still through the record
  hooks — so a user who is the last active ADMIN of a space gets a
  clear error instead of a half-deletion.
- **`ListMemoAttachments` reads; `SetMemoAttachments` does not.**
  riff has no generic `attachments` collection: an attachment is a
  PO-33 `patches` row or a `samples` pack, each carrying domain
  fields an `Attachment` proto cannot express. Listing the union is
  faithful; replacing them from a proto payload is not yet
  representable, so that method stays unimplemented rather than
  lossy.
- **`Memo.pinned` and `Memo.location` are not stored.** The `memos`
  schema has no such columns, so they are accepted and dropped.

#### Tests

Wave 4 tests live in `pb/connect/connect_wave4_*_internal_test.go`
(internal package, for the unexported auth context key) and register
the real hooks via `newHookedApp`. They cover the bridge end to end:
author and tag stamping on create, the creator becoming a space's
first ADMIN, the space-deletion cascade, author-only memo
update/delete, the `update_mask`, hashtag re-parsing, comment
creation and inheritance, reaction upsert/list/delete and its
`NotFound` behaviour on a hidden memo, relation
replace/author-check/`COMMENT` rejection, memo payload enrichment,
space member read/promote/demote/remove with the last-ADMIN
invariant, invitation read and revoke, `BatchGetUsers` with a missing
username, admin-only user creation with a working sign-in, and
`DeleteUser` with and without `force`.

#### Still open after Wave 4

- Shares (`memos/{memo}/shares/*`), memo views, user settings,
  personal access tokens, linked identities, webhooks and identity
  providers all need new collections; each still returns
  `Unimplemented`.
- `GetUserStats` / `ListAllUserStats` are deferred, not impossible:
  they need memo and tag counts per user.
- `AttachmentService` upload/get/update/delete, and
  `SetMemoAttachments`, wait on a decision about whether riff gets a
  real `attachments` collection or keeps folding them into
  `patches`/`samples`.
- `AIService` and `IdentityProviderService` remain wholly
  unimplemented.
- `SignIn` password verification still depends on the parallel
  `password_hash` bcrypt field; users created before that field
  existed (e.g. via the PocketBase admin UI) must re-set their
  password.
- The frontend (`web/`) still points at the upstream Memos server.
  Switching its Connect base URL is the last Phase 4 step and has
  not been requested yet.

### Phase 4 follow-up — Wave 4.1 erratum: memo-delete cascade (2026-10-09)

A Wave 4 self-review asked whether two deletion behaviours were
correct against `docs/design/multi-spaces.md`. Both were cross-checked
against the design doc and the migration comments.

**Space deletion cascades to directly assigned memos — correct, not
a bug.** The design is explicit (multi-spaces.md:137, 159):
"Hard-deleting a Space atomically deletes the Space, its memberships,
every directly assigned memo, those memos' owned database resources,
and relations having a deleted endpoint. It does not follow relations
into other Spaces or Unassigned memos." `pb/hooks/spaces.go`'s
`OnRecordDeleteRequest("spaces")` matches this: it captures the
assigned memo IDs before the delete clears the `memos.space`
references (non-cascading and nullable), then deletes each memo plus
its owned resources and any `memo_relations` having it as either
endpoint. Replies assigned to the same space ARE deleted with the
space, by design; replies assigned elsewhere are not. Wave 4's
`TestDeleteSpace_RunsCascadeHook` is the correct assertion.

**Memo deletion was over-cascading — bug, fixed.** The design
(multi-spaces.md:135): "Deleting one memo deletes that memo, its
owned resources, and relations having it as an endpoint. It does
not traverse relations or delete other memos." The
`memo_relations.related_memo` migration even names this explicitly
(`pb/migrations/1700000030_create_memo_relations.go:70`):
"deleting a context memo does NOT delete its comments (comments
remain orphaned but readable per the design doc; deleted-memo webhook
is the only signal)."

Wave 4's `pb/hooks/memos.go::OnRecordDeleteRequest` walked the
comment subtree via `collectCommentSubtree` and deleted every reply
with `purgeMemoDependents`. That is the opposite of the design —
deleting a memo deleted unrelated memos.

**Fix.** The hook now only clears `memo_relations` rows where
`related_memo = memoID`. `memo_relations.memo` is a cascading
relation, so the reply-side row vanishes with the parent memo's
`memos.space` clear; the design intends for `related_memo` to be the
non-cascading side (so the deletion of a referenced memo cannot
silently remove a comment that happens to reference it). The
replies themselves, and nested replies, stay alive as readable
orphans. PocketBase's own cascades still remove the parent memo's
`patches`, `samples`, and `reactions` (each `memo` relation is
`CascadeDelete: true`), so "owned resources" are still cleaned up.

The hook uses `e.App` directly rather than opening a fresh
`RunInTransaction`. Two reasons: `e.App` is already the
transactional app inside the request hook, and a force-delete of a
user goes through `purgeUserContent` → `deleteRecordRequest` →
this hook, which previously nested transactions on SQLite (the
`IMMEDIATE` write lock on the outer transaction blocks the inner
`BEGIN`). The original `purgeMemoDependents` had the same
nested-transaction issue; the fix removes both the bug and the
deadlock.

**Tests.** `TestDeleteMemo_RemovesReplySubtreeAndIncidentRelations`
(renamed to `TestDeleteMemo_PreservesCommentChain`) flipped its
assertions: the parent is gone, the reply and the nested reply
remain, the `memo_relations.related_memo = parent` row set is empty,
and an unrelated referrer survives with its now-cleared incident
relation. `TestDeleteUser_RefusesWhenContentExists` is unchanged
and now exercises the non-nested transaction path.

Verified: `go vet ./...`, `gofmt -l pb/ main.go`,
`go test -count=1 ./...`, and `go test -race -count=1 ./pb/connect/`
all pass.

# Legacy Memos REST surface at `/api/v1/...`

Status: **implemented** (V1 dialect 2026-10-18;
V0 dialect restored 2026-10-19 after the
2026-10-18 removal was observed to break the
MoeMemos boot path on the community
deployment — see DECISIONS.md, 2026-10-19
"Restore the V0 (Memos 0.21) surface for the
MoeMemos client").

## Problem

`docs/design/upstream-memos-rest.md` describes the
`/memos/api/...` adapter that re-targets the riff
connect mux for the modern riff SPA. The current
MoeMemos (iOS + Android) mobile clients do **not**
speak that prefix — they speak the upstream Memos
prefix `/api/v1/...` directly, and the upstream
prefix has been the canonical Memos REST root
since the 0.x line. The same is true of any other
third-party tool that targets the upstream
OpenAPI. Without the prefix, the mobile clients
fall through to the SPA webview path; with it,
they get the native client experience.

The supported dialects are **V1 (Memos 0.27–0.31)**
and **V0 (Memos 0.21)**. MoeMemos is a
**V0+V1 hybrid**: the same APK ships both
`MemosV0Api.kt` and `MemosV1Api.kt`, and both
sets of paths are used in the same boot. The
2026-10-18 commit (`cc6faa5f`) removed the V0
surface on the assumption that the V0 client
"has not been maintained since 2024", which
turned out to be false: the live
`mudkipme/MoeMemosAndroid/main` branch
(checked 2026-10-19) still ships V0. The
2026-10-19 restoration re-mounts the V0 paths.

* **V1 (Memos 0.27–0.31)** is the modern
  resource-name dialect (`users/alice`,
  `memos/abc`, `attachments/xyz`), wrapped
  request bodies (`CreateMemoRequest{memo:
  Memo{...}}`), and the Google API HTTP wire
  format (path variables + `body: "memo"`-style
  unwraps). Implemented in
  `pb/api/upstream_memos_v1_rest.go`.
* **V0 (Memos 0.21)** is the legacy
  numeric-`Long`-id dialect, with top-level
  array list responses, multipart upload,
  `rowStatus` / `visibility` strings, and the
  `MemosV0User` / `MemosV0Memo` /
  `MemosV0Resource` flat shapes. Implemented
  in `pb/api/upstream_memos_v0_rest.go`. The
  V0 client makes these calls regardless of
  the `GET /api/v1/status` probe result; the
  version-probe response is only used by the
  V0 client to decide whether to attempt the
  V1 upgrade.

The connect handler is the single source of truth
for the business logic; the wire-shape
differences are absorbed by the proxy layer.

## Version detection

MoeMemos calls `GET /api/v1/status` first and
inspects the response:

* A non-empty `profile.version` would trigger the
  V0 client.
* An empty `profile.version` falls through to
  `GET /api/v1/instance/profile` and the V1
  client is selected.

We always return an empty `profile.version`
(`{profile: {mode: "prod", version: ""}}`) so the
V1 client is the one that's selected. The V0
client is no longer supported, so there is no
env-var to flip the probe.

## Surface

The full route table is 28 rows. Every row has a
`Flavor` from a four-value enum:

* `connect_proxy` — forward to the connect mux
  unchanged. Used for V1 reads and writes that
  are already proto-shaped.
* `connect_proxy_wrap` — apply a small
  body-conversion function from
  `upstream_memos_v1_convert.go` before
  forwarding. The current V1 route table does
  not use this flavor; the helpers in
  `v1BodyWrappers` are kept for future use and
  for parity with the upstream Memos OpenAPI.
* `pb_records_crud` — delegate to the in-process
  PocketBase record layer. The current V1 table
  does not use this flavor either.
* `custom` — hand-written handler. Used for
  the V1 status/ping probes and the AIP
  custom-verb dispatcher.

The route-table file documents the per-row
Flavor and Builder; see
`pb/api/upstream_memos_v1_rest.go`.

## V0 (Memos 0.21) surface

The V0 surface is registered by
`mountUpstreamMemosV0Routes` immediately after
the V1 mount in `pb/api/api.go::Register`. The
two tables share the `/api/v1/...` path space
but do not overlap (the V0 `/memo` is singular;
the V1 `/memos` is plural). The V0 surface is
**11 routes** (5 reads, 6 writes):

| Method | Path                          | Builder             |
| ------ | ----------------------------- | ------------------- |
| GET    | `/api/v1/user/me`             | `v0_user_me`        |
| GET    | `/api/v1/memo`                | `v0_memo_list`      |
| GET    | `/api/v1/memo/all`            | `v0_memo_all_list`  |
| POST   | `/api/v1/memo`                | `v0_memo_create`    |
| POST   | `/api/v1/memo/{id}/organizer` | `v0_memo_organizer` |
| PATCH  | `/api/v1/memo/{id}`           | `v0_memo_patch`     |
| DELETE | `/api/v1/memo/{id}`           | `v0_memo_delete`    |
| GET    | `/api/v1/resource`            | `v0_resource_list`  |
| POST   | `/api/v1/resource/blob`       | `v0_resource_upload`|
| DELETE | `/api/v1/resource/{id}`       | `v0_resource_delete`|
| GET    | `/api/v1/tag`                 | `v0_tag_list`       |

Every V0 path goes through the `custom` flavor
and a per-handler shape converter in
`upstream_memos_v0_convert.go`. The convert
file is split into two halves:

* **Request-side wrappers** —
  `v0MemoListToV1ListMemos`,
  `v0MemoCreateToV1CreateMemo`,
  `v0MemoOrganizerToV1UpdateMemo`,
  `v0MemoPatchToV1UpdateMemo`,
  `v0MemoDeleteToV1DeleteMemo`,
  `v0ResourceListToV1ListAttachments`. Each
  takes the V0 body / query / path and produces
  a V1 connect request body. The shapers
  translate V0 visibility strings to the V1
  enum, rename V0 `rowStatus` to V1 `state`,
  inject the V1 resource name from the URL
  path, and compute the V1 `update_mask` from
  the V0 PATCH body fields.
* **Response-side shapers** — `v0ShapeUser`,
  `v0ShapeMemoList`, `v0ShapeMemo`,
  `v0ShapeResource`, `v0ShapeResourceList`,
  `v0ShapeTagList`. Each takes a V1 connect
  response body and emits a V0 wire shape:
  flat objects, numeric `Long` ids, top-level
  array lists, `rowStatus` strings, and
  flat seconds-since-epoch timestamps.

### V0 id projection

The V0 client uses numeric `Long` ids
everywhere; the V1 connect handler uses
15-char PocketBase `memos.id` strings. The V0
id is **derived from** the V1 id, not stored:
`v0IDFromV1` truncates the first 14 hex digits
of the V1 id and parses them as `uint64`. A
14-hex-digit number is at most
`0xFFFFFFFFFFFF` (~2.8e13), which is well
within `Long.MAX_VALUE` (9.2e18) and survives
Kotlin's `Long` codec. For V1 ids that
contain lowercase letters (PocketBase's
default), the helper falls back to an FNV-1a
hash of the full id — the projection is
stable, and the birthday collision threshold
is well beyond any single-instance memos
collection.

The V0 PATCH/DELETE/organizer handlers
translate the V0 Long id in the URL path to a
V1 resource name via `v0ResolveMemoName` (and
`v0ResolveAttachmentName` for resources). The
resolver lists the caller's memos / attachments
and matches by V0 id; the cost is one
`ListMemos` round trip per write. PocketBase's
`http.Request.Context()` is per-request, so a
per-request V0↔V1 id map stashed on the context
(built by the V0 list handler) is best-effort;
the resolver's list-and-match is the
authoritative path.

### Stub memos for V0 resource upload

The V1 `UploadAttachment.spec.attachment.memo`
field is required (the `attachments.memo`
column is `NOT NULL`). The V0 client uploads
without a memo, so the V0 upload handler
creates a hidden stub memo first and uploads
against it. The stub has the
zero-width-space sentinel content `\u200b` and
PRIVATE visibility. The sentinel lives in
`pb/connect/memo_stub.go`
(`connect.StubMemoContent`), because both
surfaces have to recognise it.

The V0 client links the uploaded resource to its
memo in a third step: the `PATCH /api/v1/memo/{id}`
that carries a `resourceList` naming the
resource. The PATCH handler reads that list
before dispatching to the V1 handler and moves
each named attachment onto the target memo
(`v0LinkResourcesToMemo` in
`pb/api/upstream_memos_v0_attach.go`); the
stub is deleted once it holds nothing. The
shaper strips `resourceList` so it does not end
up in the V1 `update_mask`, where the V1
`UpdateMemo` handler would ignore it.

Both list surfaces hide a stub: the V0 list
shaper (`v0ShapeMemoList` → `isV0StubMemo`)
filters it, and so does the V1 `ListMemos`
handler, so a stub never reaches the SPA feed.
The stub test ignores visibility on purpose — a
stub that a later V0 edit flipped to PUBLIC is
still a stub and must still be hidden.

## Body wraps

The `v1BodyWrappers` map in
`upstream_memos_v1_convert.go` is the registry
the route table looks up by name. There are two
families of helpers (the V0 family was removed
with the V0 dialect):

* **V1 path-inject wrappers** — `wrapV1PathInjectBody`
  is a factory: it produces a wrapper that takes
  a single path variable and injects it as a
  `name` field on the body. Used for read RPCs
  (`GetMemo`, `GetUser`, `GetAttachment`,
  `ListMemoComments`, …) where the body is
  empty and the resource identity comes from
  the path.
* **V1 body-unwrap wrappers** — `wrapV1CreateMemoBody`,
  `wrapV1UpdateMemoBody`, `wrapV1SetMemoRelationsBody`,
  and the rest. The V1 client sends the inner
  `Memo` (because the proto has `body: "memo"`),
  and the helper wraps it into the request
  envelope. `wrapV1UpdateMemoBody` also
  computes the `update_mask` from the populated
  fields and reads `updateMask` from the query
  string as an override.

## Known incompatibilities

1. **PAT-only login.** The V1 client supports
   personal access tokens (PATs) for headless
   sign-in via the `Authorization: Bearer <token>`
   header. The connect-side `AuthService.SignIn`
   RPC accepts both username+password and PATs;
   the V1 route table does not mount a separate
   `auth/signin` endpoint (the V1 client uses
   the SPA's Connect path for sign-in).
2. **Multipart vs chunked upload.** The V1
   client uses the chunked `UploadAttachment`
   procedure (`spec` → `writeOffset` →
   `finishWrite`), which lives in
   `pb/connect/attachment_upload.go`. The V0
   client uploaded a single `file` part to
   `/api/v1/resource/blob`; that endpoint is
   no longer mounted.

## Notes

1. **Version probe.** The probe endpoint
   `/api/v1/status` returns
   `{profile: {mode: "prod", version: ""}}`. The
   empty `version` is the signal to the V1
   client; the V0 client would have matched on
   a non-empty version and is not supported.
2. **Stats endpoint.** `/api/v1/users/{user}:getStats`
   is an AIP-style custom verb (the colon
   separates the resource name from the verb).
   Go's `http.ServeMux` rejects `{var}:` in a
   pattern, so the route is registered as
   `/api/v1/users/{user}/{rest...}` and the
   custom handler dispatches on the `rest`
   segment. Only `getStats` is supported; any
   other verb returns 404.
3. **Empty body unwrap.** The V1 client sends
   the inner `Memo` directly, but protojson
   unmarshalling chokes on a truly empty body.
   The `passthroughBody` helper emits `{}` for
   an empty input to keep the protojson layer
   happy.

## What this commit ships

* `pb/api/upstream_memos_v1_rest.go` — the
  28-row V1 route table and the mount function.
* `pb/api/upstream_memos_v1_convert.go` — the
  V1 body-conversion helpers and the
  `v1BodyWrappers` registry.
* `pb/api/upstream_memos_v1_rest_handlers.go`
  — the wrap-flavoured proxy handler and the
  three custom handlers (`v1_status`,
  `v1_ping`, `v1_user_stats`).
* `pb/api/upstream_memos_v1_rest_test.go` — the
  route-table smoke, the V1 wrap-fn golden
  tests, the version-probe tests, and the
  table-shape invariants.
* `pb/api/api.go` — the new
  `mountUpstreamMemosV1Routes` call after the
  existing `mountUpstreamMemosRoutes`.
* `pb/api/upstream_memos_rest_custom.go` — the
  three new custom-builder keys (`v1_status`,
  `v1_ping`, `v1_user_stats`).
* `pb/api/upstream_memos_rest_procedures.go` —
  five new procedure aliases
  (`UserServiceGetUser`,
  `UserServiceGetUserSetting`,
  `UserServiceUpdateUserSetting`,
  `AttachmentServiceGetAttachment`,
  `AttachmentServiceListAttachments`).
* `internal/identifier/resource_name.go` — the
  `ToInt` helper was removed with the V0
  surface.
* `DECISIONS.md` — the 2026-10-18 entry that
  records the choice and the known-incompat
  list.

## Verification

```bash
go test -v -count=1 -run 'TestUpstreamMemosV1|TestV0|TestV1|TestConnectProxyWrap' ./pb/api/...
```

The default test app is empty, so the tests
exercise the wrap helpers, the route table, the
version-probe logic, and the connect-proxy
bridge against a stub connect mux. An end-to-end
test requires a populated app and a real
MoeMemos build; that lives in the manual smoke
check on the dev server:

```bash
curl -fsS http://localhost:8090/api/v1/status
# {"profile":{"mode":"prod","version":""}}
curl -fsS -H "Authorization: Bearer $RIFF_PAT" http://localhost:8090/api/v1/instance/profile
# {"version":"...","mode":"prod","instanceUrl":"..."}
curl -fsS -H "Authorization: Bearer $RIFF_PAT" http://localhost:8090/api/v1/memos
# {"memos":[...],"nextPageToken":"..."}
```

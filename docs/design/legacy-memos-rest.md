# Legacy Memos REST surface at `/api/v1/...`

Status: **implemented** (2026-10-18; V0 client
support removed 2026-10-18 in the same slice —
see DECISIONS.md).

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

The supported dialect is **V1 (Memos 0.27–0.31)**,
which is the current MoeMemos build. Resource-name
identity (e.g. `users/alice`, `memos/abc`,
`attachments/xyz`), wrapped request bodies
(`CreateMemoRequest{memo: Memo{...}}`), and the
Google API HTTP wire format (path variables +
`body: "memo"`-style unwraps). The V0 (Memos
0.21) dialect is no longer supported; the singular
`/memo`, `/resource`, `/user/me`, and `/tag` paths
return 404. Legacy MoeMemos 0.21 builds (the
"moememos" repo's `MemosV0Service`) are out of
scope; the V0 MoeMemos builds have not been
maintained since 2024 and the V1 client is the
only one anyone is shipping.

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

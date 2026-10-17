# Upstream-Memos REST surface at `/memos/api/...`

Status: **implemented** (slice 5, commit f90bb74 series on top of
the sketch-resource slice).

## Problem

The riff community needs a Memos-shaped REST compatibility surface
for:

* The riff SPA, which the upstream Memos frontend is wired to
  call. The frontend is hard-coded to paths like
  `/api/v1/memos`, `/api/v1/auth/signin`, `/api/v1/sse`.
* The webclipper extension (a third-party Memos browser
  extension), which is also wired to the upstream REST surface.
* Mobile apps and scripts that have not been (and likely will not
  be) rewritten to speak Connect.

The 2026-10-16 decision landed "Connect-only" on the basis that
grpc-gateway was not in the build. That decision was correct for
the Memos Go-backend replacement path, but it left the riff
community with no path to a Memos-compatible REST surface without
pulling grpc-gateway into the binary, which the layering rules
forbid.

## Approach

A thin transport adapter at `/memos/api/...` that re-targets
requests at the in-process `*http.ServeMux` that the connect
package already mounted for the public `/memos.api.v1.*`
surface. The adapter is **not** a new business-logic layer; the
connect handler is the only authority for the rule engine, the
rate-limit policy, and the validation chain. The adapter's
whole job is to translate an HTTP shape (verb, path, query,
body, headers) into a connect-procedure dispatch.

### Three handler flavors

1. **`connect_proxy` (84 routes)** — the request is re-targeted
   at the connect mux. The body is forwarded verbatim, the
   `Authorization` header is forwarded verbatim, the query
   string is forwarded verbatim. Two transformations are
   applied: the verb is re-encoded to `POST` (because
   connect-go v2 only allows `GET` for
   `IdempotencyNoSideEffects` handlers, and the Memos
   handlers are not), and an empty body is replaced with `{}`
   (because `protojson.Unmarshal` rejects an empty payload).

2. **`pb_records_crud` (4 routes)** — direct calls into the
   PocketBase record-fetch / save / delete APIs. The wire
   shape is the Memos envelope
   (`{items, page, per_page, total_items}` for list,
   `{...record}` for get). The path-to-collection mapping is
   the same one upstream Memos used. Auth, rule, and rate-limit
   policy are unchanged because every write path goes through
   `e.App.Save(rec)`, which triggers the same `OnRecord*`
   hook chain a normal collection REST call would.

3. **`file_server` (3 routes) and `sse` (1 route)** — the
   file-download and realtime-streaming surface that
   grpc-gateway does not provide. The file routes resolve the
   path parameter to a record, then read the file blob from
   the PocketBase filesystem at
   `<collectionId>/<recordId>/<filename>`. The SSE route
   subscribes to PocketBase's `SubscriptionsBroker()` and
   forwards the realtime messages as Server-Sent Events.

### The path-table

The table in `pb/api/upstream_memos_rest.go::upstreamMemosRoutes`
is the single source of truth for all 97 routes. Adding a new
route is one line in the table. The `mountUpstreamMemosRoutes`
loop walks the table at OnServe time and registers each row on
the PocketBase router with the right verb method, scope, and
handler factory. The `Flavor` column picks the handler
implementation; the `Target` column is the Connect procedure
path (for `connect_proxy`) or the PocketBase collection name
(for `pb_records_crud`).

### The custom-verb remapping

Google AIP-136 custom verbs in the upstream Memos surface
(`/api/v1/{name=users/*}:getStats`) become
`/memos/api/users/{name}/getStats` in the riff adapter because
Go's `http.ServeMux` pattern grammar (which PocketBase uses) does
not accept `}` followed by `:`. The 13 remapped routes:

| upstream path | riff path |
| --- | --- |
| `/api/v1/users:batchGet` | `/memos/api/users/batchGet` |
| `/api/v1/{name=users/*}:getStats` | `/memos/api/users/{name}/getStats` |
| `/api/v1/{name=users/*}:exportMemos` | `/memos/api/users/{name}/exportMemos` |
| `/api/v1/{name=users/*}:importMemos` | `/memos/api/users/{name}/importMemos` |
| `/api/v1/{name=users/*/webhooks/*}:getSigningSecret` | `/memos/api/users/{parent}/webhooks/{name}/getSigningSecret` |
| `/api/v1/memos/-/linkMetadata:batchGet` | `/memos/api/memos/-/linkMetadata/batchGet` |
| `/api/v1/attachments:upload` | `/memos/api/attachments/upload` |
| `/api/v1/attachments:batchDelete` | `/memos/api/attachments/batchDelete` |
| `/api/v1/{name=spaces/*/invitations/*}:accept` | `/memos/api/spaces/{name}/invitations/{invitation_name}/accept` |
| `/api/v1/{name=spaces/*/invitations/*}:decline` | `/memos/api/spaces/{name}/invitations/{invitation_name}/decline` |
| `/api/v1/instance/settings:batchGet` | `/memos/api/instance/settings/batchGet` |
| `/api/v1/instance/settings/notification:testEmail` | `/memos/api/instance/settings/notification/testEmail` |
| `/api/v1/ai:transcribe` | `/memos/api/ai/transcribe` |

The riff webclipper client and any other Memos-shaped client
needs a one-line path rewrite on each request. A future slice
adds a request-time regex rewrite at the Caddy layer (under the
`@community` matcher) so the client can use the original
upstream paths unchanged.

### The rate-limit table

Each row in the route table has an optional `Scope` column.
When the scope is non-empty, the registration loop attaches a
`restRateLimit(limiter, scope)` middleware as a route-level
`BindFunc`. The scopes are the same names the connect handler
uses (`ScopeSignInIP`, `ScopeSignupIP`, `ScopeWriteUser`, ...),
so the rate-limit policy is uniform across the connect and REST
surfaces. The bucket key is the authenticated user id
(`e.Auth.Id`) when an auth record is present, and
`e.RealIP()` otherwise.

### Two non-implemented routes

* `GET /memos/api/users/{name}/usage` — upstream Memos exposes
  a per-user storage-usage report; riff has not modelled the
  data. The route is registered, the handler returns 501.
* `GET /memos/api/file/users/{identifier}/avatar` — upstream
  Memos added the avatar field in 0.31. The riff `users`
  collection has no avatar field. The route is registered, the
  handler returns 404 with a clear "avatar not implemented"
  body.

A future slice can wire both: a `usage` RPC on the user service
and a `FileField("avatar")` on the `users` collection.

## Why not grpc-gateway?

`runtime.NewServeMux` would have added ~2,400 LoC of generated
code (`proto/gen/api/v1/*.gw.go`) plus a permanent
`google.golang.org/grpc` runtime dependency in `pb/`. The `pb/`
package imports `proto/gen/`, but never
`google.golang.org/grpc` directly; the latter is held in
`connect-go`'s transitive closure. A hand-rolled gateway would
have changed the layering rules in `AGENTS.md` and made every
Connect-handler change (a routine operation) trigger a
`buf generate` invocation that pulled in gRPC-Gateway's
generator.

The transport adapter the design lands is ~1,500 LoC of Go
across the route table, the handler factory, the
`pb_records_crud` flavor, the file-server flavor, and the SSE
flavor, with no new third-party dependencies. It reuses the
`connect-go` mux that already ships in the binary.

The trade is explicit: the per-route Go shape of the table
replaces the generated gateway code. A new Memos RPC is one
row in the table; a new field on a Memos RPC is a one-line
change to the connect handler. The cost is ~200 LoC of
boilerplate per "the proto has a new RPC" (one row in the
table, one rate-limit scope decision); the benefit is no
`google.golang.org/grpc` dependency in the binary.

## Files

* `pb/api/upstream_memos_rest.go` — the route table
  (97 rows) and the registration loop.
* `pb/api/upstream_memos_rest_procedures.go` — the
  re-exports of the generated Connect procedure names
  (so the route table reads as `memosConnectMemoCreateMemoProcedure`
  rather than the long form
  `apiv1connect.MemoServiceCreateMemoProcedure`).
* `pb/api/upstream_memos_rest_handlers.go` — the
  `connectProxyHandler` and the rate-limit middleware.
* `pb/api/upstream_memos_rest_records_crud.go` — the
  `pb_records_crud` flavor.
* `pb/api/upstream_memos_rest_file.go` — the file-server
  flavor.
* `pb/api/upstream_memos_rest_sse.go` — the SSE flavor.
* `pb/api/upstream_memos_rest_custom.go` — the not-implemented
  custom flavor (the `users/{name}/usage` route).
* `pb/api/upstream_memos_rest_json.go` — the JSON helper
  used by the `pb_records_crud` flavor.
* `pb/api/upstream_memos_rest_test.go` — the table-driven
  smoke test (every row must produce a Memos-shape JSON
  response when hit).
* `pb/api/upstream_memos_rest_crud_test.go` — the focused
  CRUD tests (list, get, delete, create, pagination, unknown
  fields).
* `pb/api/upstream_memos_rest_proxy_test.go` — the focused
  connect-proxy tests (body, headers, query string, no-mux
  fail-soft, unknown-flavor fail-loud).
* `pb/connect/connect.go` — the `ConnectMux()` and
  `SetConnectMux()` accessors (the adapter reads the mux
  the connect package installed at Register time).

## Migration impact

The adapter is purely additive: no existing connect handler is
modified, no existing API route is changed, and the existing
`/memos.api.v1.*` surface continues to work unchanged. The
webclipper client and the Memos-frontend SPA can be retargeted
to the new paths in a follow-up slice; until then, the
adapter simply serves traffic for the new prefix and the
existing connect-prefix handlers continue to answer the
upstream-prefix traffic.

The only non-additive change is the `connectProxyHandler`'s
verb re-encoding (always POST) and body defaulting (always
`{}`); these are invisible to the upstream client because the
client never spoke Connect to begin with.

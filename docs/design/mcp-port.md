# MCP Server Port Plan

Status: Source-only restoration. Package compiles. Not wired into riff.
Date: 2026-10-09

## Scope

This document records the work done to migrate `github.com/usememos/memos/server/mcp`
into `github.com/incurl/riff/server/mcp` as a source-only restoration. The
package is intended to compile against the riff module path with the minimum
necessary deviations from upstream, but it is not mounted on any riff route
and is not invoked from `main.go`. Integration is a separate concern; see
"Remaining work" below.

## What was restored

This section reflects the actual state after the 2026-10-09 port. A
prior port attempt (the work this document records) made a destructive
mistake mid-execution that was corrected; see the DECISIONS.md entry
"MCP server source-only restoration" for the full timeline.

Seven source files from upstream, totaling 1,457 lines of non-test code,
plus banner comments and one extra exported method.

| File | Upstream lines | Riff lines | Notes |
| --- | --- | --- | --- |
| `service.go` | 162 | 195 | Echo swap, OpenAPI path arg, `Handler()` instead of `RegisterRoutes`. |
| `adapter.go` | 264 | 274 | `*echo.Echo` -> `http.Handler` in one struct field and constructor param. |
| `catalog.go` | 413 | 421 | Verbatim, banner added. |
| `openapi.go` | 300 | 306 | Verbatim except yaml import rewritten to `go.yaml.in/yaml/v3`. |
| `validation.go` | 226 | 230 | Verbatim, banner added. |
| `result.go` | 61 | 65 | Verbatim, banner added. |
| `origin.go` | 31 | 36 | Verbatim, package doc added. |
| **Total** | **1,457** | **1,527** | |

## Test status

The upstream test suite is 2,083 lines across 5 files. Of those:

- `adapter_test.go` (381 lines) — ported. Two `echo.New()` test fixtures
  rewritten as `http.HandlerFunc` stubs; one test that read the embedded
  OpenAPI YAML now points at `testdata/openapi.yaml`, a minimal fixture
  containing a single `MemoService_DeleteMemoReaction` operation.
- `validation_test.go` (63 lines) — kept verbatim, no changes needed.
- `service_test.go` (852 lines) — **deleted**. All 17 tests construct a
  live `*echo.Echo` and use the old `NewMCPService` signature.
- `catalog_test.go` (544 lines) and `openapi_test.go` (242 lines) —
  **deleted**. Both depend on a full generated OpenAPI YAML at
  `../../proto/gen/openapi.yaml` (a path riff does not have) and assert
  schemas for ~15 operations. Building a fixture that covers them all
  is the same scale as the `service_test.go` port.

The surviving 19 tests cover the adapter layer (path/query/body
construction, error decoding, structured result wrapping) and argument
validation. Restoring the deleted tests is the final item in the
remaining-work list below.

## Deviation inventory

Three deviations from upstream; each is documented in the file's banner
comment, and the rationale is repeated here.

### 1. `http.Handler` instead of `*echo.Echo`

`adapter.go` and `service.go` hold an `http.Handler` instead of `*echo.Echo`.
The change is local: the call site uses `handler.ServeHTTP(recorder, req)`,
which exists on both. Riff does not run an Echo server (PocketBase + Connect
handlers), so requiring `*echo.Echo` would force the calling code to either
(a) instantiate an Echo server just to host a single route, or (b) write an
adapter struct that wraps riff's transport. `http.Handler` makes the choice
the caller's, not the package's.

### 2. OpenAPI spec loaded from disk

Upstream `service.go` reads the OpenAPI YAML from an embedded byte slice
(`memosproto.OpenAPIYAML()`). Riff does not currently produce an openapi.yaml
artifact — `proto/gen/api/v1/*.pb.go` is generated, but the OpenAPI generation
target in `proto/buf.gen.yaml` is either absent or not run. Until
`buf generate` produces `proto/gen/openapi.yaml`, `NewMCPService` takes a
caller-supplied `specPath string` and reads the file at startup. An empty
`specPath` returns an explicit error so the failure mode is obvious.

### 3. `Handler()` instead of `RegisterRoutes`

`service.go.RegisterRoutes(*echo.Echo)` mounted `/mcp` on an Echo router.
Riff's router is not Echo, so the function cannot be called as-is. Replaced
with `Handler() http.Handler` that returns a self-contained handler applying
the origin check, the body size cap (via `http.MaxBytesReader`), and the
streamable HTTP dispatch. Riff's main.go can mount the returned handler via
the standard library mux or any router riff uses.

## New direct dependencies

Three direct dependencies were added to `go.mod` by this restoration:

| Module | Version | Purpose |
| --- | --- | --- |
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 | The MCP protocol SDK. |
| `github.com/google/jsonschema-go` | v0.4.3 | Argument validation against tool input schemas. |
| `go.yaml.in/yaml/v3` | v3.0.5 | Was an indirect dep; promoted to direct because `openapi.go` uses it. The package is API-compatible with `gopkg.in/yaml.v3` (the upstream import path), which is no longer maintained. |

The total dep delta is small. `github.com/labstack/echo/v5` was already a
direct dep in riff (via `internal/clientip.Middleware`) and is not used by
this package after the swap.

## What was not done

The following are deliberately out of scope for this turn and remain work
for future turns:

1. **Wiring into `main.go`.** No riff code calls `NewMCPService` or
   references `server/mcp` outside its own files. The user's directive
   ("no integration/wiring") bound this.
2. **Porting the upstream test suite.** Upstream `*_test.go` files total
   1,838 lines and depend on (a) a live `*echo.Echo` server, (b) a
   test-only `server/api` fixture package, and (c) the upstream `store`
   facade. None of these exist in riff. The tests can land as a follow-up
   once either the `server/api` port begins or the test harness is rewritten
   against riff's PocketBase collection fixtures.
3. **Porting the larger `server/` and `server/api/v1/` packages.** This
   restoration does not attempt to port the rest of upstream's `server/`
   tree. The only file that the MCP package transitively depends on in
   upstream (`server/api/api.go`, for the `MaxAPIRequestBytes` constant)
   is not present in riff, so the value is inlined with a banner comment.
4. **Migrating `memos.payload` to a JSON column.** Deferred to a separate
   port plan; see `DECISIONS.md` for the rationale.

## Remaining work to make this functional

In dependency order, where each step unblocks the next:

1. **Generate `proto/gen/openapi.yaml`.** Add or verify the
   `openapi` plugin in `proto/buf.gen.yaml`, run `buf generate`, commit
   the resulting YAML. Without this artifact, `NewMCPService` cannot read
   a spec and the catalog builder has nothing to enumerate.
2. **Expose the riff API as a single `http.Handler`.** Riff's transport
   today is a collection of Connect handlers registered on a
   `connect.Handler`. The MCP adapter needs a way to dispatch a synthetic
   HTTP request into the same routes. Two viable shapes:
   - Build a thin `http.Handler` that multiplexes the registered Connect
     routes by URL path. The MCP adapter would then call this handler's
     `ServeHTTP` directly.
   - Or, expose the existing handler chain through a public method
     (`ConnectRouter.HTTPHandler() http.Handler`).
   Either way, the in-process dispatch in `adapter.go` will start
   returning real responses.
3. **Mount `Handler()` from `main.go`.** Add a `mux.Handle("/mcp", ...)`
   registration alongside the existing Connect routes. Apply
   `clientip.Middleware` upstream of the MCP handler so the
   `clientip.FromContext` call inside `adapter.go` returns the real
   client address, not the loopback address from `httptest.NewRequest`.
4. **Update the `Profile` injection site.** `NewMCPService` takes a
   `*profile.Profile` for the `InstanceURL` field used by the origin
   allowlist. Wire it to riff's existing profile shape (already
   `internal/profile.Profile`).
5. **Port the deleted tests.** The three test files removed in the
   2026-10-09 turn are:
   - `service_test.go` (852 lines, 17 tests, all `*echo.Echo` based).
   - `catalog_test.go` (544 lines, ~15 operations asserted).
   - `openapi_test.go` (242 lines, schema-resolution focused).
   Porting them requires (a) replacing every `*echo.Echo` fixture with
   an `http.Handler` stub, and (b) building a test OpenAPI YAML that
   covers the operation surface they exercise. Until that fixture
   exists, the catalog and openapi tests cannot run. The `service_test.go`
   port is the more substantive of the two jobs; once that lands, the
   fixture falls out naturally because the tests will define the shape
   they need.

## Open questions

- **Tool catalog stability.** The upstream catalog lists 36 operation IDs.
  Riff's Connect handlers map 1:1 to the same operation IDs (because the
  proto files are shared), so the list should work as-is. If any tool
  call returns "operation not found" at runtime, the mismatch is in
  `curatedOperationIDs` and the fix is to remove or rename that entry.
- **Body size cap.** Inlined as 256 MiB, matching `server/api.MaxAPIRequestBytes`
  in upstream. If riff's own API grows a different cap, this constant
  should be sourced from there rather than duplicated.
- **Stateless mode.** Upstream comment notes that stateless mode also
  governs protocol version negotiation (2026-07-28 vs 2025-11-25).
  Riff inherits this choice verbatim; if a protocol version mismatch
  surfaces with a real MCP client, this is the lever.

## What to verify after wiring

These are the manual integration checks that become meaningful once
`main.go` mounts the handler. Each maps to a test that should exist by
the end of step 5 above.

- `GET /mcp` from a browser with no `Origin` header returns 405 (the SDK
  rejects non-POST).
- `POST /mcp` with an `Origin` that does not match the host returns 403
  via `isAllowedMCPOrigin`.
- `POST /mcp` with a body larger than 256 MiB returns 413 from
  `http.MaxBytesReader`.
- `POST /mcp` with `tools/list` returns the 36 curated tools.
- `POST /mcp` with `tools/call MemoService_ListMemos` returns a real
  `Memos` list, not a stub error.

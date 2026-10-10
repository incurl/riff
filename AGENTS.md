#| Memo export/import round-trip | `pb/connect/memo_export.go` (the `ExportMemos` RPC that returns a `google.api.HttpBody` ZIP archive), `pb/connect/memo_export_format.go` (manifest, record, attachment wire types, safe-filename mapping, JSON encoders), `pb/connect/memo_import.go` (the `ImportMemos` RPC lifecycle: spec / writeOffset / finishWrite / progress pings / cached report), `pb/connect/memo_import_session.go` (in-memory session manager with 30-minute TTL sweeper started by `installImportManager` in `pb/connect/connect.go::OnServe`), `pb/connect/memo_import_apply.go` (manifest validation, plan computation, per-memo apply with conflict policies SKIP/REPLACE/DUPLICATE, parent/relations pass, attachment round-trip via the same `uploadFileToPocketBase` pipeline as a normal upload), and `pb/connect/memo_import_helpers.go` (`typesDateTime`, `uploadAttachmentBytes`, `timestamppbFromTime`). The format follows `docs/design/memos-export-format.md` verbatim. The cross-user `NotFound` rule applies to the `upload_id` lookup. The `patches` and `samples` `CreateRule = "memo.author = @request.auth.id"` is the only authority for who can upload attachments on import. The export handler builds the archive in memory and returns it as `HttpBody`; for installs above 256 MiB the design calls for streaming. Reactions, webhooks, and mention notifications are deferred per the design's "Deferred design" list. | `go test -v -count=1 -run 'TestExportMemos\|TestImportMemos\|TestSafeFilename\|TestGuessMime\|TestImportSessionManager' ./pb/connect/...`. |
 AGENTS.md

Repository instructions for AI coding agents. Keep this file short, concrete, and tied to commands that actually work in this
repo. If a fact here conflicts with source files or CI config, trust the source file and update this guide.

## Project Snapshot

riff is a self-hosted PocketBase backend serving memos, patches, samples, and spaces for PO-33 sampler creators.

- Backend: Go 1.27.0, PocketBase v0.40.5, Connect RPC, gRPC-Gateway, Protocol Buffers. The Go binary is `main.go` at the repo root (no `cmd/`).
- Frontend: React 19, TypeScript 7, Vite 8, Tailwind CSS v4, React Query v5. The Memos SPA is served unchanged from `pb/frontend/`.
- Storage: PocketBase owns the DB layer (SQLite by default). There is no per-driver SQL; collections are defined in `pb/migrations/*.go` and registered via a blank import from `main.go`.
- Generated API outputs: `proto/gen/` for Go/OpenAPI, `web/src/types/proto/` for TypeScript.
- Module path: `github.com/incurl/riff`.

## Working Rules

- Read relevant code before editing; prefer local patterns over new abstractions.
- Keep diffs scoped. Do not do repo-wide cleanup, dependency churn, or generated-file rewrites unless the task requires it.
- Do not hand-edit generated proto outputs. Change `.proto` files, then run `buf generate` from `proto/`.
- New collections go in `pb/migrations/` as a Go migration that calls `migrations.Register(...)` in an `init()`. The blank import in `main.go` makes the collection discoverable to PocketBase's migrate plugin.
- Public Connect/gRPC endpoints live in `pb/connect/`; custom REST endpoints live in `pb/api/`. PocketBase collection access rules (in the migration) and the `Route` ACLs in those packages together govern who can call what — do not add an endpoint without checking both.
- MCP tool visibility comes from `proto/gen/openapi.yaml`. Add a new endpoint by registering the Connect handler and regenerating OpenAPI; the MCP service picks it up on next start.
- Ask before adding heavy dependencies, changing auth/token behavior, or altering Docker/release workflows.

## Documentation

- ADRs and their companion glossary have been retired. Do not recreate them unless explicitly requested.
- Design documents in `docs/design/` are frozen after implementation. Do not update implemented designs to track later code changes.
- New and unimplemented designs may still evolve. Frozen documents record the original design; verify current behavior in source code and tests.
- Non-obvious design decisions belong in `DECISIONS.md` (the audit log). The current top of the file lists every entry by date and section heading — append, do not rewrite.
- Keep contributor guidance in this file and package-local `README.md` or `doc.go` files current when relevant code changes.

## Commands

Run from the repository root unless a command starts with `cd`.

```bash
# Backend
go run -buildvcs=true .                          # Serve (CalVer derived from HEAD commit date)
go test ./...                                    # All Go tests
go test -v -race ./pb/connect/ ./pb/hooks/       # Race detector on the hot path
go test -v -race ./internal/...                  # Internal package tests with race detector
go test -v -race ./server/mcp/                   # MCP service tests with race detector
go test -v -run TestFoo ./pb/...                 # Run matching Go tests in one tree
go mod tidy -go=1.27.0                           # Match CI tidy check
golangci-lint run                                # Lint (config: .golangci.yaml, includes depguard layering)
golangci-lint run --fix                          # Auto-fix lint, including goimports

# Frontend
cd web && pnpm install             # Install dependencies
cd web && pnpm dev                 # Dev server on :3001
cd web && pnpm lint                # Type check + Biome lint
cd web && pnpm test                # Vitest unit tests
cd web && pnpm build               # Production build
cd web && pnpm release             # Build SPA into pb/frontend/dist

# Protocol Buffers
cd proto && buf generate           # Regenerate Go + TypeScript + OpenAPI
                                   # (needs `protoc-gen-es` on PATH: run
                                   #  `cd web && pnpm install` first, or
                                   #  export PATH="$PWD/web/node_modules/.bin:$PATH")
cd proto && buf lint               # Lint proto files
cd proto && buf format -w          # Format proto files
```

## Repository Layout

Every root folder is named for what it holds. Read the folder's `doc.go` or `README.md` before adding to it.

| Path | Holds | May import |
| --- | --- | --- |
| `main.go`, `main_test.go` | The riff binary entrypoint. Wires the PocketBase app, hooks, custom REST, Connect/gRPC, MCP, and the migratecmd plugin. | anything |
| `pb/` | The riff-on-PocketBase layer (a single Go module under `github.com/incurl/riff/pb`): | |
| `pb/api/` | Custom `/api/po33/*` REST handlers. | `pb/`, `internal/`, `proto/gen/` |
| `pb/connect/` | Connect/gRPC handlers and store↔proto translation. Also holds the `Route` ACLs and `RateLimit` middleware. | `pb/`, `internal/`, `proto/gen/` |
| `pb/hooks/` | PocketBase record hooks (after-save memo-link fan-out, spaces cascade, memos author auto-assign, hashtag parse, space-membership gates, realtime broadcasts). | `pb/`, `internal/`, `proto/gen/` |
| `pb/migrations/` | PocketBase Go migrations. Each file calls `migrations.Register(...)` in an `init()`; the blank import in `main.go` makes the collection discoverable. | `internal/` |
| `pb/frontend/` | Built SPA assets, embedded by the `pb` package or served by PocketBase's static handler. | _(no Go imports)_ |
| `server/mcp/` | Model Context Protocol server (Streamable HTTP at `/mcp`); reads `proto/gen/openapi.yaml` for its tool catalog. | `proto/gen/`, `internal/` |
| `markdown/` | Markdown engine: parser, AST, memos syntax extensions, renderer, memo payload. Exposes `ExtractedData.LinkDestinations` populated by `Service.ExtractAll` from `gast.Link` nodes. | `proto/gen/`, `internal/` |
| `internal/` | Private plumbing with no memos vocabulary (`linkmeta`, `identifier`, `profile`, `webhook`, `ratelimit`, `clientip`, `imagelimit`, `motionphoto`, `storage`, `random`, `testutil`, `version`, …). | third party only |
| `proto/api/v1/` | Public API proto sources; `proto/gen/` is generated. | — |
| `proto/gen/` | Generated Go, TypeScript, and OpenAPI. The OpenAPI YAML feeds the MCP server. Do not hand-edit. | — |
| `web/` | React SPA, reused unchanged from the Memos frontend. `web/src/types/proto/` is the generated TypeScript surface; everything under it must not be hand-edited or Biome-rewritten. | `web/src/types/proto/` |
| `scripts/` | Build, install, Docker, entrypoint, compose, and the CalVer helper `scripts/release_version.sh`. | — |
| `DECISIONS.md` | Audit log of non-obvious design decisions, appended chronologically. | — |
| `docs/design/` | Frozen design documents (multi-spaces, MCP port, configuration provisioning, API abuse controls, memo export, unique-email). | — |

Layering rules, enforced by `depguard` in `.golangci.yaml`:

- Imports point downward only: `main.go` → `pb/` → `server/` → `markdown/`, `internal/`. The `server/mcp/` and `pb/*` packages live at the same level; treat them as siblings, not as a chain.
- `internal/` packages import nothing from this module (except `internal/testutil`, which may import `proto/gen`).
- A package goes in `internal/` only if it could be published as a standalone module unchanged. If it knows what a Memo, Space, or instance setting is, it is a root package.
- Never create `util`, `common`, `base`, `helpers`, `misc`, or `pkg` packages. Name a package for the one thing it does, or put the code next to its only caller.
- New business rules extracted from a handler go to a sibling subpackage, e.g. `pb/connect/feed/` for feed-specific logic. Keep `pb/connect/<resource>_service.go` thin.

File convention inside `pb/connect/`: `<resource>_service.go` holds the RPC handlers, `<resource>_service_converters.go` the
store↔proto conversion, and `<resource>_service_<topic>.go` further splits. Extend the matching file; do not add `*_helpers2.go`.
Tests live next to the code (`*_test.go`) or in the same package's `*_test.go` black-box style.

## Change Routing

| Change | Update | Verify |
| --- | --- | --- |
| Go service or router behavior | Handlers under `pb/api/`, `pb/connect/`, `pb/hooks/`, `server/mcp/`, plus `main.go` if the entrypoint wiring changes; tests near package. | `go test -v -race ./pb/... ./server/mcp/...` |
| Collection or migration behavior | `pb/migrations/<name>.go` (and any new collection rule edits). The blank import in `main.go` keeps the registry in sync. | `go test -v -run TestNewHookedApp ./pb/hooks/...` |
| Markdown or MCP logic | `markdown/`, `server/mcp/` | `go test -v -race ./markdown/... ./server/mcp/...` |
| Internal package logic | Relevant `internal/` package tests | `go test -v -race ./internal/...` |
| Frontend behavior | Components/hooks/contexts under `web/src/` | `cd web && pnpm lint && pnpm test` |
| Frontend production output | Vite config or release-sensitive UI | `cd web && pnpm build` or `pnpm release` |
| Proto API | `.proto` source plus generated outputs | `cd proto && buf generate && buf lint` |
| Public unauthenticated route | Add the route in `pb/api/` or `pb/connect/`; declare the access rule in the matching `pb/migrations/` collection (use the `noAuth` route option in `pb/connect/` if no PocketBase record is loaded). | Targeted test in the package that owns the route, or manual route check. |
| Rate limit middleware on a route | `pb/connect/ratelimit.go` (`RateLimit`) plus `Route.BindFunc` at the call site | `go test -v -run TestRateLimit ./pb/connect/...` |
| Wire `RateLimit` onto a new Connect procedure | Add a row to the `procedureScope` table in `pb/connect/ratelimit.go`; the registration loop in `pb/connect/connect.go` (`registerConnectRoutes`) attaches the middleware automatically. Per-flow counters whose key is in the request body (e.g. `signin_account` keyed on the submitted username) call `Limiter.Consume` / `Refund` from the handler, not from the route-level table. Enable the limiter for an instance with `RIFF_RATE_LIMIT=1` (reads `Profile.RateLimit` in `main.go`). | `go test -v -run 'TestConnectRegister\|TestSignIn' ./pb/connect/...` |
| Link preview metadata RPC | `pb/connect/link_meta.go` (`GetLinkMetadata` / `BatchGetLinkMetadata`); the library lives in `internal/linkmeta`. New sentinels (`ErrInvalidURL`) belong in the library, not the handler. | `go test -v -run 'TestGetLinkMetadata\|TestBatchGetLinkMetadata' ./pb/connect/...` and `go test -race ./internal/linkmeta/...` |
| `Memo.Payload` proto field | `proto/api/v1/memo_service.proto` (`MemoPayload` + `payload` on `Memo`); the API translator that fills it from the `memos.payload` JSON column lives in `pb/connect/memos_translate.go`. | `go test -v -run 'TestMemoPayloadFromRecord\|TestRecordToMemo_PopulatesPayload' ./pb/connect/...`; regen with `cd proto && buf generate`. |
| Link preview auto-fetch on memo save | `pb/hooks/memo_links.go` (the after-save hook) plus `pb/migrations/1740000000_create_memo_links.go` (the `memo_links` collection). URL extraction is `markdown.ExtractedData.LinkDestinations` populated by `markdown.Service.ExtractAll` from `gast.Link` nodes. | `go test -v -run 'TestNormaliseLinkURL\|TestTruncate\|TestTruncateErr' ./pb/hooks/...`; `go test -v -run TestExtractAllCapturesLinkDestinations ./markdown/...`. |
| Memo share RPCs | `pb/connect/memo_shares.go` (the four `MemoServer` methods) plus `pb/migrations/1750000000_create_memo_shares.go` (the `memo_shares` collection). The collection's `*Rule` fields are all `nil` so the Connect handler is the only authority; the public `GetSharedMemo` handler reads the row directly without consulting the rule engine (the `code` is the bearer credential). | `go test -v -run 'TestCreateMemoShare\|TestListMemoShares\|TestDeleteMemoShare\|TestGetSharedMemo\|TestMemoShare_CascadeDeleteOnMemoDelete' ./pb/connect/...`. |
| Attachment motion media | `pb/hooks/motion_media.go` (after-create hook on `patches`/`samples` that calls `internal/motionphoto.DetectJPEG` and writes the `motion_media` JSON column) plus `pb/migrations/1760000000_add_motion_media.go` (idempotent column add). The converter `pb/connect/attachment_motion_media.go` reads the column and populates `Attachment.motion_media`. The hook is detached (goroutine) so a slow disk read doesn't stall the API response. | `go test -v -count=1 -run 'TestShouldInspect\|TestFileField' ./pb/hooks/...` and `go test -v -count=1 -run 'TestMotionMedia\|TestReadMotionMedia' ./pb/connect/...`. |
| Attachment chunked upload | `pb/connect/attachment_upload.go` (the `UploadAttachment` RPC: spec → writeOffset → finishWrite), `pb/connect/upload_session.go` (in-memory session manager with 30-minute TTL sweeper), `pb/connect/attachment_upload_io.go` (idempotent default-id generator), and the new `pb/migrations/1800000000_add_media_metadata.go` (idempotent `media_metadata` column on `patches`/`samples`). The session manager is a package-level singleton started by `installUploadManager` in `pb/connect/connect.go` (the `OnServe` hook) and stopped on `OnTerminate`. The collection (`patches` vs `samples`) is chosen by MIME type; the `patches` and `samples` `CreateRule = "memo.author = @request.auth.id"` are the only authority for who can upload. | `go test -v -count=1 -run 'TestUploadAttachment' ./pb/connect/...`. |
| Attachment storage backend (LOCAL / S3) | `internal/storage` (`Storage` interface, `Local` and `S3` backends). The `S3` backend delegates to PocketBase's `pocketbase/tools/filesystem` rather than carrying the official `aws-sdk-go-v2` (see PB discussion #6562 for the rationale). The `pb/hooks/storage_bridge.go` hook mirrors `patches`/`samples` uploads to the configured remote backend on after-create/after-update and deletes them on after-delete, writing the public URL into the `remote_url` column added by `pb/migrations/1780000000_add_remote_url.go`. The hook is best-effort (a remote failure logs but does not roll back the local save). The legacy `InstanceSetting.StorageSetting.S3Config` proto fields are read; the newer `storages[]/default_storage_id` shape is not yet supported (Phase 6). | `go test -v -count=1 ./internal/storage/...` and `go test -v -count=1 -run 'TestConfigFromProto\|TestJoinSplitURLs\|TestKeyFromURL' ./pb/hooks/...`. |
| Fly.io deployment | `scripts/Dockerfile` (multi-arch `riff` binary on Alpine 3.21, port 8090, non-root user, persistent `/var/opt/riff` volume) plus `fly.toml` (single-service manifest, persistent volume `riff_data`, `/api/health` http check, `release_command` running `scripts/release_version.sh`). Compose-equivalent for local Docker is `scripts/compose.yaml`. | `docker build -f scripts/Dockerfile .` smoke; the `scripts/release_version.sh development-version` CalVer is the value of `--build-arg VERSION` in CI. |
| User webhook lifecycle | `pb/connect/user_webhooks.go` (the five `UserServer` methods: `ListUserWebhooks`, `CreateUserWebhook`, `UpdateUserWebhook`, `DeleteUserWebhook`, `GetUserWebhookSigningSecret`) plus `pb/migrations/1770000000_create_user_webhooks_main.go` and `1770000001_create_webhook_deliveries.go` (the `user_webhooks` and `webhook_deliveries` collections; the two-file split is required because PocketBase's `migrations.Register` uses `runtime.Caller(1)` for filename-based dedup). The signing secret is stored in a plain `TextField` (not `PasswordField` — its in-memory `Plain` is wiped by `PasswordField.Intercept` after save, which would break the drain). The outbox hook is `pb/hooks/webhook_outbox.go` (enqueues one `webhook_deliveries` row per user webhook on memo create/update/delete) and the drain goroutine is `pb/hooks/webhook_drain.go` (30s tick, exponential backoff capped at 30m, 8 attempts, parallel fan-out via `webhookDrainParallelism`). The drain's HTTP transport is `internal/webhook.Post` which already enforces SSRF/private-IP blocking; the test suite toggles `webhook.AllowPrivateIPs` so the local httptest receiver is reachable. | `go test -v -count=1 -run 'TestListUserWebhooks\|TestCreateUserWebhook\|TestUpdateUserWebhook\|TestDeleteUserWebhook\|TestUserWebhooks' ./pb/connect/...` and `go test -v -count=1 -run 'TestWebhook' ./pb/hooks/...`. |
| `buf generate` OpenAPI output | `proto/buf.gen.yaml` includes `buf.build/community/google-gnostic-openapi`. The MCP service reads `proto/gen/openapi.yaml` at startup. | `cd proto && buf generate && buf lint` — confirm `proto/gen/openapi.yaml` is regenerated. |
| `buf generate` TypeScript output | `proto/buf.gen.yaml` runs `local: protoc-gen-es` (`@bufbuild/protoc-gen-es ^2.12.0`, declared as a devDep in `web/package.json`) with `opt: target=ts` to emit `web/src/types/proto/`. The local plugin (rather than the BSR `buf.build/bufbuild/es` remote) matches the existing v2.12.1 file format under `web/src/types/proto/` and lets us pin the major to the runtime `@bufbuild/protobuf` dep. | `cd web && pnpm install && cd ../proto && buf generate` — confirm the 15 `*_pb.ts` files under `web/src/types/proto/` are regenerated with the `// @generated by protoc-gen-es v2.x.x` header. The lockfile for the new devDep is a separate `pnpm install` commit, not this one. |
| Inbox write hooks (MEMO_COMMENT, MEMO_MENTION, space-invite archive/decline) | `pb/hooks/notifications.go` (the four `OnRecordAfter*Success` hooks) plus `pb/migrations/1710000000_create_notifications.go` (the `notifications` collection with select field values `SPACE_INVITATION, MEMO_COMMENT, MEMO_MENTION`). `CreateNotificationBestEffort` is the single authorized insert path (the collection's `createRule` is `nil`). The mention dedupe on memo update uses an `OnRecordUpdate` stashed-content map (`prevMemoContent`) so the after-success hook can compare against the prior mentions. | `go test -v -count=1 -run 'TestNotification_' ./pb/hooks/...`. |
| Notification email delivery | `pb/hooks/notifications_email.go` (`EmailDispatcher`, per-kind message builders, settings reader) plus `internal/email` (the revived SMTP transport: `Config`, `Message`, `Client`, `Send`/`SendAsync`). Wiring is `pb/hooks/notifications_email_wiring.go` (`InstallEmailDispatcher` + the after-success hook on `notifications`). `TestInstanceEmailSetting` in `pb/connect/instance_settings.go` is now a real TCP-dial reachability check with a 5 s timeout, replacing the prior syntactic stub. The feature is gated on `RIFF_INSTANCE_URL` (an empty value disables both the email side and the MCP origin allow-list). | `go test -v -count=1 -run 'TestEmailDispatcher' ./pb/hooks/...` and `go test -v -count=1 ./internal/email/`. |
| IdentityProviderService + SSO sign-in | `pb/connect/identity_providers.go` (the six `IdentityProviderServer` methods: `ListIdentityProviders`, `GetIdentityProvider`, `CreateIdentityProvider`, `UpdateIdentityProvider`, `DeleteIdentityProvider`, `GetIdentityProviderClientSecret`) plus `pb/migrations/1810000000_create_identity_providers.go` (the `identity_providers` collection whose `config` JSON column round-trips the proto `IdentityProviderConfig` oneof) and `pb/connect/identity_providers_converters.go` (the `recordToIdentityProvider` converter masks `OAuth2Config.client_secret` on every read; `GetIdentityProviderClientSecret` bypasses the mask and is the only path that returns the secret). The proto field carries `(google.api.field_behavior) = INPUT_ONLY`, which gnostic renders as `writeOnly: true` in `proto/gen/openapi.yaml`. The SSO branch lives in `pb/connect/auth_sso.go::AuthServer.signInWithSSO` and reuses PocketBase's `core.OAuth2ProviderConfig.InitProvider` + `auth.Providers` registry + `_externalAuths` link table — no `golang.org/x/oauth2/google` direct dependency. The collection's `*Rule` fields are all `nil`; admin-only verbs are guarded by `viewer.IsSuperuser()` in the handler. First-time OAuth sign-ups honour `instance_settings.GENERAL.disallowUserRegistration`; a new user is auto-provisioned only when that flag is false. The SPA builds the authorize URL itself from the `GetIdentityProvider` response (the `client_id` and `auth_url` are public-shaped); the `code_verifier` round-trips through `sso_credentials.code_verifier`. | `go test -v -count=1 -run 'TestListIdentityProviders\|TestCreateIdentityProvider\|TestGetIdentityProvider\|TestDeleteIdentityProvider\|TestListAndGet_MaskClientSecret\|TestGetIdentityProviderClientSecret\|TestUpdateIdentityProvider_PreservesSecretWhenOmitted\|TestGlobMatch\|TestSignInWithSSO\|TestSsoProviderNameFromSlug\|TestDeriveUsername' ./pb/connect/...`. |
| Self-service / bootstrap sign-up | `pb/connect/user.go::UserServer.CreateUser` (unauthenticated bootstrap when `needsSetup=true`, admin honour, self-service gated by `instance_settings.GENERAL.disallowUserRegistration` / `disallow_password_auth`) plus `pb/connect/auth.go::AuthServer.findByIdentity` (fall-through to `_superusers` so the bootstrap admin can sign in) and `pb/migrations/1790000000_add_superuser_password_hash.go` (parallel `password_hash` column on `_superusers` for the bcrypt verify path). The SPA always sends `User_Role.USER`; the server forces `role=ADMIN` on bootstrap. | `go test -v -count=1 -run 'TestCreateUser\|TestSignIn' ./pb/connect/...`. |

## Go Conventions

- Wrap errors with `errors.Wrap(err, "context")` from `github.com/pkg/errors`; do not use `fmt.Errorf`.
- Return Connect/gRPC errors with `connect.NewError(connect.CodeX, ...)`. Reserve `status.Errorf` for the rare case where a handler returns a code from `google.golang.org/genproto/googleapis/rpc/errdetails` directly.
- Keep imports grouped as stdlib, third-party, then `github.com/incurl/riff/...`; goimports is run by golangci-lint.
- Add doc comments for exported identifiers; godot enforces exported comment punctuation.
- Avoid package-level mutable state unless the surrounding package already uses that pattern. PocketBase record hooks are a deliberate exception: `pb/hooks/` registers once on `app.OnServe().BindFunc`.

## Frontend Conventions

- Use `@/` for absolute imports.
- Follow Biome formatting: 2-space indent, double quotes, semicolons, 140-character line width.
- Put server data in React Query hooks under `web/src/hooks/`; keep UI-only state in contexts or component state.
- Use Tailwind CSS v4 utilities, `cn()` for class merging, and CVA for variants.
- Reuse Base UI primitives and existing components before adding new UI primitives.
- Keep generated proto TypeScript under `web/src/types/proto/` out of manual edits and Biome rewrites.

## Database And Proto Rules

- Schema changes are a new Go migration in `pb/migrations/`. PocketBase applies them on the next `app.Bootstrap()` run; the migratecmd plugin handles this automatically.
- Migrations must be **idempotent across fresh installs and existing instances**: do not assume an empty schema, and do not destroy user data when adding optional columns.
- Proto field changes must preserve compatibility unless the task explicitly allows a breaking API change. New RPCs and fields go through `proto/api/v1/`; the generated OpenAPI is the MCP surface.
- Regenerate after proto edits with `cd proto && buf generate` and include both Go/OpenAPI and TypeScript generated outputs in the same commit.

## Verification Policy

- Run the narrowest relevant checks while iterating.
- Before finishing, run the checks that match the changed surface from "Change Routing".
- For docs-only changes, `git diff --check` is sufficient unless the docs include runnable examples that should be tested.
- If a required check cannot run locally, report the reason and the exact command that remains.

## Known Issues (post-refactor)

These are tracked in the commit message of `3bd5c81d` and are not blocking the
rest of the test suite, but are worth flagging before any release:

1. **`main.go` previously passed `&profile.Profile{}` (zero value) to `mcp.Register`**, so
   the origin allow-list matched nothing. Fixed in Phase 5 Slice D:
   `buildProfileFromEnv` reads `RIFF_INSTANCE_URL` and threads the real
   value into both `mcp.Register` and `hooks.InstallEmailDispatcher`.

The `pb/hooks/memo_links.go::memoLinksFanOut` worker-pool race (item 1 in the
prior list) is fixed in the same commit as the user-webhook lifecycle: the
goroutine honours a per-app `memoLinksShutdown` channel that the OnTerminate
hook closes, and `pruneStaleMemoLinks` recovers any in-flight panic from a
closed DB so a test app cleanup does not crash the runner.

## CI Reference

- Releases are cut by pushing a `YY.MM[.N][-rc.N]` tag. To cut, promote, or check a release, follow
  `.agents/skills/release/SKILL.md` (currently still Memos-shaped; needs a riff-specific rewrite before the first riff release).
- Development builds use the HEAD committer date in UTC as `YY.MM`; release builds inject the CalVer tag.
- `go build` embeds Git metadata automatically; `go run` needs `-buildvcs=true`. Builds without Git metadata must inject
  `github.com/incurl/riff/internal/version.Version` through `-ldflags`.
- Docker excludes `.git`; pass `--build-arg VERSION="$(bash scripts/release_version.sh development-version)"` and
  `--build-arg COMMIT="$(git rev-parse HEAD)"` when building locally (build frontend assets first). The
  `scripts/Dockerfile` builds `./` (the repo root, where `main.go` lives) and produces a binary named `riff` on
  Alpine 3.21 with port 8090 exposed. Companion `fly.toml` is the Fly.io deployment manifest.

- Backend CI: Go 1.27.0, `go mod tidy -go=1.27.0`, golangci-lint v2.13.1, test groups `pb`, `server`, `internal`, `other`
  (`markdown`, `proto`).
- Frontend CI: Node 24, pnpm 11.0.1, `pnpm lint`, `pnpm test`, `pnpm build`.
- Proto CI: `buf lint` and `buf format` check.
- Docker: `scripts/Dockerfile`, Alpine 3.21 runtime, non-root user, **port 8090** (PocketBase default; the Memos-era 5230 binding is no longer in use), multi-arch amd64/arm64/arm/v7.

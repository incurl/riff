# PO-33 Device Binding & Sketch Upload

Status: Draft (not yet implemented)

Date: 2026-10-16

Related: [Multi-Spaces](multi-spaces.md), [API abuse controls](api-abuse-controls.md),
[Configuration provisioning](configuration-provisioning.md)

## Context

The PO-33 community site (`riff`) currently serves text memos plus two file
kinds (`patches`, `samples`) uploaded through the web SPA. The new product
line is an **ESP32-S3 remake of the Teenage Engineering PO-33** with WiFi
that uploads sketches (samples + patterns + chains) directly to a user's
account. The web SPA remains the curation surface; the device is an
input mechanism. Device binding is **optional** — users with the original
PO-33 or no device at all can use the site for everything except
device-side uploads.

This document reconciles an external spec with the realities of the
current `riff` codebase. The spec is normative on the **wire format**
and the **security model**; this document is normative on the **storage
shape** and the **routing surface**, which the spec did not cover.

## Hardware contract (pinned)

- The device is an ESP32-S3 with WiFi, a small display (for QR codes),
  and a secure store (NVS encrypted partition or eFuse).
- On first boot the device:
  1. Reads its MAC address.
  2. Derives a stable fingerprint: `sha256(MAC || DEVICE_SALT)` →
     64-char lowercase hex.
  3. Generates an Ed25519 keypair via the device's secure RNG.
  4. Stores the private key in secure storage. **The private key
     never leaves the device.**
  5. Exposes the public key to the backend during binding as
     standard 32-byte Ed25519 public key, **base64 (std encoding, no
     padding)**.
- The backend MUST NOT trust the MAC address. The fingerprint is a
  human-readable identifier; the public key is the trust root.

## Wire format (pinned)

These are the contracts the device and the SPA will implement against.
They are stable across both sides.

- **Canonical JSON for signing.** Metadata is signed over a canonical
  JSON form with sorted keys, no insignificant whitespace, UTF-8 NFC
  text, and numbers in their shortest round-trip form. We define this
  in `internal/ed25519verify/canonical.go` and reuse it everywhere a
  signature is computed or verified.
- **Signature.** Ed25519 over the canonical JSON bytes. The
  signature is base64 (std encoding, no padding).
- **Bearer token.** A 32-byte random value, base64url (no padding).
  Shown **once** to the device on `approved`. The server stores
  `sha256(token)` in `po33_devices.access_token_hash`. (sha256, not
  bcrypt: bcrypt is for low-entropy human passwords; a 32-byte
  device token is already at 256 bits of entropy.)
- **User code.** 8 characters, Crockford base32
  (`0123456789ABCDEFGHJKMNPQRSTVWXYZ` — drops `I`, `L`, `O`, `U`).
- **Device code.** 48 random bytes, hex-encoded (96 chars). Shown
  **once** to the device. The server stores `sha256(device_code)`
  only. Plaintext is never written to disk or logs.

## Mapping to the current `riff` codebase

The external spec uses `/api/po33/device/*` REST for most endpoints.
That shape is fine, but the spec doesn't acknowledge several things
already in the repo. This section is the binding translation.

| Spec line | Where it lands | Why |
|---|---|---|
| `POST /api/po33/device/authorize` | `pb/api/po33_device_routes.go` (REST) | Public, anonymous, rate-limited per IP. |
| `GET /api/po33/device/qrcode/{user_code}` | `pb/api/po33_device_routes.go` (REST) | Anonymous, returns PNG. 5-min in-memory cache keyed by `user_code`. |
| `POST /api/po33/device/token` (poll) | `pb/api/po33_device_routes.go` (REST) | Anonymous, rate-limited per device_code. |
| `POST /api/po33/device/bind` (web) | New `DeviceServer` in `pb/connect/device_service.go` (Connect) | Authenticated; the existing `Route` ACLs in `pb/connect/connect.go` apply. |
| `POST /api/po33/device/{id}/revoke` | `DeviceServer.RevokeDevice` (Connect) | Authenticated; owner-only check is a single SQL lookup. |
| `POST /api/po33/sketch/upload` | New Connect RPC `SketchService.UploadSketch` (multipart) | Reuses `pb/connect/upload_session.go`'s session manager and `pb/connect/attachment_upload_io.go` for default-id generation. **Not** a REST endpoint: the multipart lifecycle and the Ed25519 signature require the same chunked-upload pipeline the existing `attachment_upload.go` already implements. The result is a new `memos` row with `source = "sketch"` plus new `patches` / `samples` rows linked via `memo_relations`. |
| `GET /api/po33/sketch/list` (device) | `SketchService.ListDeviceSketches` (Connect) | Device-authenticated. Filters `memos` by `source = "sketch" AND author = me`. |
| `/my/sketches` (web) | `SketchService.ListMySketches` (Connect) | User-authenticated. Same filter, no device lookup. |
| Download | `GET /api/po33/sketch/{id}/download` (REST) | **See "Open question 1" below** for the presigned-URL decision. |
| Ed25519 verification | `internal/ed25519verify/` | `crypto/ed25519` is in the stdlib. No new dep. |
| QR encoder | `internal/qr/` (new) | Wraps a single small dep — see "Open question 2". |
| Rate limiting | Extend `pb/connect/ratelimit.go` for Connect procedures; add a thin REST middleware in `pb/api/` that delegates to `internal/ratelimit`. | The existing `RateLimit` middleware is Connect-only. |
| Migrations | `pb/migrations/1840000000_create_po33_devices.go`, `1840000001_create_device_authorizations.go`, `1840000002_add_memo_source.go` (adds `memos.source` select), `1840000003_add_memo_relations_backup_kind.go` (extends the `kind` select), `1840000004_add_attachment_device_fingerprint.go` (adds `device_fingerprint_snapshot` to `patches` + `samples`), `1840000005_create_po33_sketch_downloads.go`. | Per the Q3 decision the only NEW collection files are the two device tables plus the device-side event log. The remaining three migrations are idempotent column/select extensions on existing collections. |

## Data model

Per the Q3 decision, **no new sketch-bearing collection**. The
existing `memos` (plus a new typed `MemoPayload.sketch` oneof) and
`memo_relations` collections carry sketches, and PB's existing
`patches` / `samples` collections carry the audio files. Only two
new collections ship in this slice: `po33_devices` and
`device_authorizations`. A third, `po33_sketch_downloads`, is a
device-side event log (per the Q5 decision).

### `po33_devices` (new)

| Field | Type | Notes |
|---|---|---|
| `user` | relation → `users` | required, cascade delete |
| `device_fingerprint` | text | unique, indexed, 64-char hex |
| `device_pubkey` | text | base64 Ed25519 public key (32 bytes) |
| `device_name` | text | user-editable |
| `firmware_version` | text | last reported |
| `access_token_hash` | text | sha256 of bearer token |
| `status` | select | `active` / `revoked` |
| `bound_at` | date | |
| `last_seen_at` | date | updated on any authenticated request |

### `device_authorizations` (new)

| Field | Type | Notes |
|---|---|---|
| `device_code_hash` | text | unique, indexed |
| `user_code` | text | unique, indexed, 8 chars |
| `device_fingerprint` | text | |
| `device_pubkey` | text | |
| `device_name` | text | |
| `firmware_version` | text | |
| `status` | select | `pending` / `approved` / `expired` |
| `bound_user` | relation → `users` | optional, set on approve |
| `expires_at` | date | created + 5 min |

### `po33_sketch_downloads` (new — device-side event log only)

| Field | Type | Notes |
|---|---|---|
| `user` | relation → `users` | the device owner; required |
| `device` | relation → `po33_devices` | the requesting device |
| `memo` | relation → `memos` | the downloaded sketch (filtered `source = "sketch"`) |
| `created` | date | |
| `client_ip` | text | for stats; not used for auth |

Web playback and web download-to-collection do **not** insert into
this table.

### `memos` (existing — extended)

The existing `memos` collection gets one new field:

| Field | Type | Notes |
|---|---|---|
| `source` | select | `text` (default, for backward compat) / `sketch` |

The PO-33-specific data lives in the existing `memos.payload` JSON
column. The proto change is to add a `sketch` oneof case to
`MemoPayload`:

```proto
message MemoPayload {
  oneof content {
    string text = 1;     // existing: the markdown body
    SketchPayload sketch = 2;  // new
  }
}

message SketchPayload {
  uint32 bpm = 1;
  string key = 2;             // e.g. "C minor"
  bytes signed_metadata = 3;  // the canonical-JSON blob the device signed
  string signature = 4;       // base64 std Ed25519 signature
  string device_fingerprint = 5;  // redundant with po33_devices row, but
                                  // kept on the memo for offline provenance
  string device_fingerprint_snapshot = 6; // copied on device revoke
  string chain = 7;           // JSON array of pattern slots
  string license = 8;
  repeated string tags = 9;
}
```

`memos.payload` is already a JSON column; the `sketch` case is
parsed by the existing translator in `pb/connect/memos_translate.go`.
No SQL change to the column.

### `memo_relations` (existing — extended)

`memo_relations.kind` is an existing select field. The PO-33
upload uses two existing kinds plus one new one:

| Kind | Notes |
|---|---|
| `PATCH_FILE` (existing) | The sketch's main patch (the `.p33` blob). |
| `SAMPLE_FILE` (existing) | One row per audio sample attached to the sketch. |
| `BACKUP_FILE` (new) | The optional backup zip the device emits at the end of a session. |

The two existing relation kinds already cover the device's audio
attachments; the new `BACKUP_FILE` kind is a one-row-per-memo
binary blob for restore. All three are subject to the existing
`patches` / `samples` `CreateRule = "memo.author = @request.auth.id"`
for upload authorization, and to the existing `memos.viewRule` for
download.

### `patches` and `samples` (existing — unchanged)

`patches` and `samples` get a new `device_fingerprint_snapshot`
text column so that, after a device is revoked, the upload's
provenance survives. (See Q4.) The `mime_type` discriminator that
the existing `attachment_upload.go` uses picks the right collection
based on the request's `Content-Type` header. The decision in Q3
means we do not add a `sketches` collection's `kind` field; the
existing `patches` / `samples` pair already discriminates.

## Access rules (PocketBase collection rules)

The external spec's rules are generic; we have to pin them against
the `riff` patterns. Per the Q3 decision, the only new rules are on
`po33_devices`, `device_authorizations`, and `po33_sketch_downloads`.
The existing `memos` / `memo_relations` / `patches` / `samples`
rules are unchanged; a sketch is a memo under the existing rules.

- `po33_devices`: `listRule = user = @request.auth.id`;
  `viewRule = user = @request.auth.id`; `createRule = nil` (only the
  custom route); `updateRule = user = @request.auth.id`;
  `deleteRule = nil` (revoke via custom route).
- `device_authorizations`: every `*Rule = nil`. The custom routes are
  the only authority.
- `po33_sketch_downloads`:
  - `listRule = user = @request.auth.id OR @request.auth.is_superuser`.
  - `viewRule = user = @request.auth.id OR @request.auth.is_superuser`.
  - `createRule = nil`. The custom REST route is the only insert
    path (it sets `user`, `device`, `memo` from the validated
    bearer token, never from the request body).
  - `updateRule = nil`; `deleteRule = nil`.
- `memos` (existing — unchanged): the existing rules already gate
  `source = "sketch"` rows correctly. **No new rules needed.**
- `memo_relations` (existing — unchanged): the existing rules
  already gate `kind IN (PATCH_FILE, SAMPLE_FILE, BACKUP_FILE)`
  correctly.
- `patches` / `samples` (existing — unchanged): the existing
  `CreateRule = "memo.author = @request.auth.id"` is the only
  authority for who can upload a device-side attachment.

The SPA's web playback (anonymous) hits the existing
`/api/collections/{collection}/records` (memos) and the
existing PB file-serve routes (patches, samples) under the
existing `viewRule`. No new auth surface for read.

## Security model (unchanged from spec)

1. `device_code` and `access_token` are stored hashed only.
2. All device uploads must carry a valid Ed25519 signature over the
   canonical metadata JSON.
3. Rate limit `POST /api/po33/device/authorize` to 10 req/min/IP.
4. Rate limit `POST /api/po33/sketch/upload` to 10 req/hour/device.
5. PB scheduler purges `device_authorizations` older than 24h and
   marks pending ones past `expires_at` as `expired`.
6. MIME sniffing on the server (already provided by PB's FileField
   interceptor — `mimetype.DetectReader`).
7. Presigned download URLs expire in 15 minutes.
8. Never log plaintext device codes or access tokens.

## Testing plan

For every route and RPC, tests use an isolated `pocketbase.New()` app
(the existing `internal/testutil` helpers), with the same
newHookedApp fixture the other Connect tests use. Coverage:

- Authorize → QR code (200, PNG body) → poll pending → web bind →
  poll approved → device-side `UploadSketch` with a valid
  Ed25519 signature → `memos` row exists with
  `source = "sketch"` and a populated `payload.sketch` →
  `patches` / `samples` rows are linked via `memo_relations`.
- Expired `device_code` rejected on poll.
- Rebinding a device to a second user blocked (1:1 invariant).
- One user binding two devices succeeds (1:N invariant).
- Sketch upload with tampered metadata rejected (signature
  verification fails).
- Sketch upload with wrong-key signature rejected.
- Device-side `GET /api/po33/sketch/{id}/download` requires
  a valid `Authorization: Bearer …` matching
  `po33_devices.access_token_hash`; success inserts one
  `po33_sketch_downloads` row.
- Anonymous `GET` of a public sketch's PB file-serve route for
  `patches` / `samples` succeeds (no auth header). The SPA player
  embed relies on this.
- Rate limit: 11th `authorize` in a minute → 429.
- Access rules: `memos.viewRule` keeps `PRIVATE` sketch memos
  invisible to other users; `PROTECTED` / `PUBLIC` sketch memos
  visible to anonymous per the existing rule; `patches` /
  `samples` `CreateRule = "memo.author = @request.auth.id"`
  blocks a device whose user does not own the memo.
- Device revoke: the `pb/hooks/device_revoked.go` cascade clears
  `memo_relations.device` to null and writes
  `patches.device_fingerprint_snapshot` /
  `samples.device_fingerprint_snapshot`.

## Open questions (need user answers before code lands)

1. **Presigned download URLs vs served downloads.** The spec says
   downloads return a "short-lived S3 presigned URL". The current
   `internal/storage/s3.go` does not generate presigned URLs — it
   delegates to PB's `pocketbase/tools/filesystem`, which has no
   built-in presigner. The existing `patches`/`samples` downloads are
   served through PB's file-serve routes (no presign).
   Options: (a) add `Presign(ctx, key, ttl)` to `internal/storage` —
     touches the S3 backend, possibly pulls in a presigning lib;
   (b) keep the existing pattern: a `pb/api/` route validates the
     bearer token (or session), then proxies the file from local/S3
     to the client (15-min signed query string locally if needed);
   (c) skip the presigned URL entirely and stream through riff.
   **Recommendation: (b).** No new dep, matches the existing pattern,
   the "15-minute expiry" is enforced by the server-side session, not
   the URL.

2. **QR encoder dependency.** The spec suggests
   `github.com/skip2/go-qrcode`. It is a pure-Go, MIT-licensed
   library with no transitive deps. Adding it requires an explicit
   `go get` and a `go.mod` change. **Recommendation: add it under
   `internal/qr/`.** Alternative: `boombuler/barcode` (also pure Go).
   The choice is cosmetic; the user-code-only render path is small
   enough that I can swap libraries later without a wire change.

3. **Sketch storage: dedicated `sketches` collection or reuse
   `memos`?** The `memos` collection already has `payload`,
   `visibility`, `space`, `author`, and a multi-attachment relation
   via `memo_relations`. Reusing it means:
     - one feed pipeline, one share pipeline, one access-rule shape;
     - sketches are first-class in the SPA;
     - the `patches`/`samples` distinction collapses to a
       `patches_and_samples` relation kind or two relation kinds.
   Carving out a separate `sketches` collection means:
     - clean separation between human text memos and PO-33 beats;
     - device-specific columns (bpm, key, signed metadata) live on
       the row instead of inside a JSON blob;
     - one more feed pipeline + SPA page.
   **Decision (this session): reuse `memos`.** Sketches are an
   extension of the existing memo concept: same author model, same
   visibility/space plumbing, same share/reaction pipeline, same
   markdown surface. The PO-33-specific data (bpm, key, signed
   metadata, sample tracks) lives in the existing
   `memos.payload` JSON column via a new typed shape
   (`MemoPayload.sketch` oneof, with a `bpm`, `key`, `signed_metadata`,
   `samples` array, `chain` table) and a new `memo_relations` kind
   `SAMPLE_FILE` for the audio attachments. A new `source` select
   field on `memos` (`text` | `sketch`) discriminates the two.

   Consequences of this choice:

   - **No new `sketches`, `collections`, or `download_events`
     collections.** Instead, the existing `memos`,
     `memo_relations`, and the upcoming `sketch_downloads` (a new
     `po33_sketch_downloads` collection, see Q5) carry the
     functionality.
   - **One new collection is still needed**: `po33_devices` and
     `device_authorizations` are unrelated to memos and live on
     their own. Migrations simplify to two new collection files
     (down from five).
   - **Wire change**: the `MemoPayload` proto gets a new oneof case
     `sketch` alongside the existing `text` case. The translator in
     `pb/connect/memos_translate.go` learns the new shape; the
     markdown renderer does not (a sketch memo is rendered as a
     player embed, not as text).
   - **SPA change**: a new "Sketches" tab on the existing feed page,
     filterable by `memos.source = sketch`. The memo detail view
     learns the player embed. (Deferred to a separate task per
     AGENTS.md's "do not modify `web/` until the backend routes and
     tests are green" rule.)
   - **Access rules inherit from `memos`** — no new `listRule`
     changes; the existing `status = "NORMAL" AND (visibility IN
     ("PUBLIC", "PROTECTED")) OR author = @request.auth.id`
     expression is reused.

4. **Revoke behaviour.** The spec says "keep historical sketch
   records intact but sever the `device` relation (set to null or
   keep as `revoked_device` snapshot)". **Recommendation: set
   `device` to null, copy the fingerprint into
   `device_fingerprint_snapshot`.** Authors and admins can still see
   the upload was device-sourced; the device row stays for audit
   but is no longer queried.

5. **Anonymous download.** The spec leaves this open.
   **Decision (this session): split along the device/web axis.**

   - **Anonymous web playback**: allowed. The existing
     `memos.viewRule` already lets anonymous users list and view
     public memos; the sketch player embed is rendered for
     `visibility = PUBLIC` sketches without authentication. PB's
     file-serve routes for `patches` and `samples` are also
     public-shaped (they sit behind the same `viewRule`), so the
     player can stream the audio without sign-in. No new rule, no
     new auth surface.
   - **Signed-in web download** (the "save to my collection" /
     "download to my device" UI button on the SPA): requires
     sign-in. The existing `user.avatar`, `memos.Create`, and
     `memos.Update` rules all gate on `@request.auth.id != ""`,
     and so does this.
   - **Device-side download** (the ESP32-S3 pulling a sketch over
     `GET /api/po33/sketch/{id}/download` to back up or to chain
     into another beat): requires the device's bearer token. The
     device is already bound at this point, so a sign-in redirect
     would be infeasible anyway; the bearer token is the
     credential.

   The dedicated `download_events` collection from the previous
   sketch is renamed `po33_sketch_downloads` and becomes a
   **device-side** event log only: a row per `GET
   /api/po33/sketch/{id}/download` from a bound device, with
   `user` (the owner of the device) and `client_ip`. Web playback
   does not insert into this table. Web download-to-collection
   updates `memo_relations` (`kind = COLLECTED`) instead — the
   "favorites"-style pattern the existing Memos code already
   implements.

6. **Ed25519 verification on day one or after.** **Recommendation:
   on day one.** The whole point of device tokens (per the spec's
   design notes) is that a stolen token is useless without the
   private key. Shipping without signature verification is shipping
   the threat model without the mitigation. The verification
   contract is small (`internal/ed25519verify.Verify` — ~40 LOC
   including tests) and the device-side cost is a single Ed25519
   sign, which the ESP32-S3's hardware acceleration handles in
   microseconds.

## Out of scope (deferred)

- Real-time upload progress to the SPA. The current `attachment_upload`
  RPCs return progress as a side channel; the sketch upload will
  follow the same pattern.
- Sketch versioning / fork graph. Memos are a flat table; the new
  `memos.source = "sketch"` rows do not get a "based on" relation
  in v1.
- Sketch comments / reactions. The existing `reactions` and
  `memo_relations` (`kind=COMMENT`) collections work for sketch
  memos unchanged — they target `memos`, not a separate `sketches`
  table. The SPA filter `memos.source = "sketch"` lets the UI
  limit to sketches.
- Web-side `EditSketch` flow. v1 has `UploadSketch` (device) and
  `ListMySketches` (web). Editing happens on the device until a
  future web editor lands.
- The `patches` / `samples` collections are unchanged except for
  the new `device_fingerprint_snapshot` text column (Q4).

## File plan (when implementation starts)

### Migrations

- `pb/migrations/1840000000_create_po33_devices.go`
- `pb/migrations/1840000001_create_device_authorizations.go`
- `pb/migrations/1840000002_add_memo_source.go`
- `pb/migrations/1840000003_add_memo_relations_backup_kind.go`
- `pb/migrations/1840000004_add_attachment_device_fingerprint.go`
- `pb/migrations/1840000005_create_po33_sketch_downloads.go`

### REST routes (`pb/api/`)

- `pb/api/po33_device_routes.go` (REST: authorize, qrcode, token)
- `pb/api/po33_sketch_routes.go` (REST: device-side download, plus
  the per-device `sketch_downloads` insert)
- `pb/api/po33_device_routes_internal_test.go`
- `pb/api/po33_sketch_routes_internal_test.go`
- `pb/api/api.go` (register the new REST routes; add a thin REST
  rate-limit middleware that delegates to the existing
  `internal/ratelimit` limiter so the per-IP budgets apply
  uniformly to REST + Connect)

### Connect services (`pb/connect/`)

- `pb/connect/device_service.go` (Connect: bind, revoke,
  listMyDevices)
- `pb/connect/sketch_service.go` (Connect: upload — wraps the same
  `UploadAttachment` chunked-upload pipeline the existing
  `attachment_upload.go` implements, then writes a `memos` row
  with `source = "sketch"` and a `MemoPayload.sketch` oneof)
- `pb/connect/device_service_converters.go`
- `pb/connect/sketch_service_converters.go`
- `pb/connect/device_service_internal_test.go`
- `pb/connect/sketch_service_internal_test.go`
- `pb/connect/connect.go` (register the new services, add
  rate-limit entries to `procedureScope`)
- `pb/connect/ratelimit.go` (extend `procedureScope` table)
- `pb/connect/memos_translate.go` (extend translator to round-trip
  the new `MemoPayload.sketch` oneof)

### Hooks (`pb/hooks/`)

- `pb/hooks/scheduler.go` (or extend the existing one) — purge
  expired `device_authorizations` every 5 min
- `pb/hooks/device_last_seen.go` — update
  `po33_devices.last_seen_at` on every authenticated device request
- `pb/hooks/device_revoked.go` — on `po33_devices` `status=revoked`,
  fan out: set `memo_relations.device` to null on related memos
  (sketch uploads), copy `device_fingerprint` into
  `device_fingerprint_snapshot` on the related `patches` / `samples`
  rows (Q4). The existing memo-link fan-out is the template.

### Proto

- `proto/api/v1/memo_service.proto` — add `MemoPayload.sketch`
  oneof case and the `SketchPayload` message.
- `proto/api/v1/device_service.proto` (new) — the `DeviceServer`
  RPCs (`ListMyDevices`, `RevokeDevice`, etc.)
- `proto/api/v1/sketch_service.proto` (new) — `SketchServer`
  (list, upload).
- Regenerate Go + TypeScript + OpenAPI:
  `cd proto && buf generate`.

### Internal packages

- `internal/ed25519verify/verify.go` (new)
- `internal/ed25519verify/canonical.go` (new)
- `internal/ed25519verify/verify_test.go` (new)
- `internal/qr/qr.go` (new; wraps `github.com/skip2/go-qrcode`,
  pending Q2)
- `internal/random/crockford.go` (new; user-code generator)

## Appendix: Push direction (2026-10-19)

The original design (above) covers pairing, signing, and upload. A
later slice (2026-10-19) added the **inverse** direction: the SPA
asks the server to push a sketch to one of the user's bound
devices. The two transports considered were:

- **device-pushes** (server-initiated): the SPA opens a long-poll
  or WebSocket to the PO-33 and forwards the bytes. Requires
  the firmware to ship a state machine the current revision
  does not have.
- **device-pulls** (device-initiated): the SPA writes a small
  envelope to a per-device row, the device polls the server
  every 2s and fetches the bytes via a short-lived signed URL.
  Reuses the signed-URL pattern the upload route already
  verifies; the device-side code is the same `GetFirstRecordByFilter`
  + signed-URL verify it already has.

The chosen transport is **device-pulls**. The full lifecycle:

```
[SPA]      SketchService.PushSketch        (or POST /api/po33/devices/{id}/push)
            |
            v
[Server]   writes `po33_devices.pending_pull` envelope:
            { push_id, sketch_memo_id, signed_url, signed_expires_at,
              version_no, queued_at, state = "QUEUED" }
            |
            v
[Device]   GET /api/po33/sketches/pending
            | X-Device-Fingerprint: <public_key_fpr>
            v
[Server]   advances state to "SENDING" and returns the envelope
            |
            v
[Device]   GET /api/po33/sketches/{memo}/download?<exp=...&mac=...>
            | X-Device-Fingerprint: <public_key_fpr> (preferred)
            v
[Server]   verifies signed URL, streams .p33 bytes, writes audit row
            |
            v
[Device]   POST /api/po33/sketches/pending/ack { push_id }
            | X-Device-Fingerprint: <public_key_fpr>
            v
[Server]   advances state to "DELIVERED" or "FAILED"
```

### State machine

| State      | Set by                                  | Cleared by                |
|------------|-----------------------------------------|---------------------------|
| (empty)    | —                                       | SPA PushSketch            |
| QUEUED     | SPA PushSketch                          | device poll (→ SENDING)   |
| SENDING    | device `/pending` poll                  | device `/ack` (→ DELIVERED) |
|            |                                         | 15-min timer (→ FAILED)    |
| DELIVERED  | device `/ack` (no `reason` field)       | 15-min keep-alive expires  |
| FAILED     | device `/ack` (with `reason`) or timer  | 15-min keep-alive expires  |

The keep-alive is `15 * time.Minute` — the same as the signed-URL
TTL — so the SPA's `GetPushStatus` poll and the device's
`/pending` poll expire on the same timer. A re-opened dialog
after the keep-alive sees `UNSPECIFIED` / `EXPIRED`; the SPA
clears its local state.

### Files

- `pb/migrations/1870000000_add_device_pending_pull.go` — the
  `pending_pull` JSON column on `po33_devices`.
- `pb/connect/sketch_service_push.go` — `PushSketch` +
  `GetPushStatus` + envelope types + per-`(device, memo)`
  rate-limit + idempotency window.
- `pb/api/po33_device_pull.go` — owner-facing
  `POST /api/po33/devices/{id}/push` and
  `GET /api/po33/devices/{id}/pushes/{pushId}`, plus the
  device-facing `/api/po33/sketches/pending` poll,
  `/api/po33/sketches/pending/ack`, and
  `/api/po33/sketches/{memo}/download` routes.
- `internal/signedurl/` — the 15-min HMAC-SHA256 signer
  persisted to `instance_settings.download_signing_key`.
- `proto/api/v1/po33_sketch_service.proto` — `GetSketch`,
  `PushSketch`, `GetPushStatus` RPCs and their messages.
- `pb/connect/sketch_signer_bootstrap.go` — the per-install
  signer bootstrap (load or mint the HMAC key, install the
  connect-go interceptor that threads the signer into
  every request).
- `pb/connect/sketch_service.go` — `GetSketch` handler.

### Tests

- `pb/connect/sketch_service_push_internal_test.go` — 15 tests
  covering the Connect path: envelope round-trip, double-click
  idempotency, cross-tenant guard, revoked device, text-memo
  guard, unknown push state, push status poll, unauthenticated
  paths.
- `pb/api/po33_device_pull_internal_test.go` — 13 tests
  covering the REST path: auth, ownership, sketch-only rule,
  state machine, audit row, signature checks, idempotency.
- `internal/signedurl/signedurl_test.go` — 8 tests covering
  the signer: round-trip, expired, bad MAC, wrong key, wrong
  path, malformed, TTL override, secret wipe.

### Security posture (recap)

- 404 not 403 on owner / bundle mismatches.
- Per-device 1-push-per-30s rate limit.
- Never log device tokens or signing keys.
- Audit to existing `po33_sketch_downloads` (PATCH kind).
- Signed URL is 15 min, HMAC-SHA256, per-install key.

### Deferred (out of scope)

- WebSocket / SSE "true push" transport.
- Push history table (out-of-band audit of which sketch went
  to which device at which time).
- Batched pushes (a user with N devices pushes the same
  sketch to all in one click).
- Server-rendered email notification deep-linking to the
  signed URL.

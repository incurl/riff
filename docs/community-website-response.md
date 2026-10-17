# Website response to the firmware sketch-resource brief (v0.1, 2026-10-17)

**Status:** Draft 0.1, awaiting firmware-team review.
**Scope:** the website (riff) side of the PO-33 sketch upload lifecycle.
**Counter-position:** the brief's "fork sketch into its own table"
recommendation (§4) is rejected. The website already has a `memos`-shaped
schema with a `source='sketch'` discriminator, a `MemoPayload.sketch` oneof
case on the proto, and a per-attachment `device_fingerprint_snapshot`
column. Adding a parallel `sketch` table would duplicate the index
strategy, break every deployed device, and force a lossy backfill from
`patches`+`samples` that nobody is asking for. The rest of the brief is
accepted in principle; the implementation below layers the lifecycle
(DRAFT/PUBLISHED/HIDDEN/ARCHIVED + immutable `sketch_versions`) on top
of the existing surface.

This document is the single source of truth. Every code change listed
in §6 maps to a concrete file in the repo, and the file paths are
stable references for the PR review.

---

## 1. What already ships in riff (the brief ignores this)

| Concern | Where it lives today | Brief's § |
| --- | --- | --- |
| PO-33 device registry | `po33_devices` collection (`pb/migrations/1840000000_create_po33_devices.go`) | §7 |
| Pairing handshake (Ed25519, user code, QR) | `pb/api/po33_device_routes.go` (POST/GET/DELETE on `/api/po33/pair/*`) | §7 |
| Device authorization rows | `device_authorizations` collection (`pb/migrations/1840000001_create_device_authorizations.go`) | §7 |
| "This memo is a sketch" discriminator | `memos.source='sketch'` select (`pb/migrations/1840000003_add_memo_source.go`) | §4 |
| Per-sketch device metadata | `MemoPayload.sketch` oneof case in `proto/api/v1/memo_service.proto` (`SketchPayload` message) | §5 |
| Per-attachment fingerprint snapshot | `patches.device_fingerprint_snapshot` and `samples.device_fingerprint_snapshot` (migration `1840000005_add_attachment_device_fingerprint.go`) | §7 |
| `BACKUP_FILE` relation kind | `memo_relations.type` extended to `BACKUP_FILE` (migration `1840000004_add_memo_relations_backup_kind.go`) | §3 (tarball) |
| SPA's "my sketches" read path | `SketchService.ListMySketches` (Connect) in `pb/connect/sketch_service.go` | §5 |
| Anonymous download path | `GET /api/po33/sketches/download` (REST, in `pb/api/po33_device_routes.go`, planned — see DECISIONS.md) | §6 |

What the brief gets right: **the lifecycle is genuinely missing**. Today
every memo lands in `NORMAL` state (`recordToMemo` hard-codes it; the
`memos` table has no `state` column), there is no draft / publish
split, and the device upload path is not implemented. Sections 3, 5,
and 6 of the brief are net-new work that the implementation below
adds.

What the brief gets wrong: the "sketch" table recommendation
*duplicates* the index strategy. Tags, comments, reactions, hashtags,
mentions, search, and feed pagination all live on `memos` today. A
parallel `sketch` table would either (a) carry no comments / reactions
at all (bad: the SPA renders a single feed), or (b) fork the
`sketch_comment` / `sketch_reaction` / `sketch_tag` tables too
(costly: every existing query path needs a `WHERE source='sketch'`
predicate to keep rendering the unified feed).

---

## 2. Two load-bearing divergences from the brief

These two items are non-negotiable. The firmware PR will not merge
until both are accepted or the website team reopens the design.

### 2.1. No parallel `sketch` table — `memos.source='sketch'` is the source of truth

The `memos` table already carries `source`, `state` (proto, not yet a
column — see §2.2), `visibility`, `content`, `payload.sketch`, `tags`,
`created`, `updated`, `author`, plus the standard `memo_relations`,
`memo_reactions`, `memo_comments` join tables. The site renders one
feed; the SPA, the device, and the user paths all hit the same
collection. Forking creates two parallel index strategies for the
same data.

**What "rejection" means in code:** zero new sketch-bearing
collections. Every new field goes onto an existing table; the only
*new* table in this slice is the immutable `sketch_versions`
audit log (§6).

### 2.2. The proto `State` enum is the lifecycle enum

`proto/api/v1/common.proto` already declares:

```proto
enum State {
  STATE_UNSPECIFIED = 0;
  NORMAL = 1;
  ARCHIVED = 2;
}
```

and `proto/api/v1/memo_service.proto:230` already says
`State state = 2` on `Memo`. The brief's `DRAFT | PUBLISHED | HIDDEN
| ARCHIVED` is a *replacement* of the existing wire-level enum, not
an extension. We do not replace it; we extend it by adding
`DRAFT = 3` and `HIDDEN = 4` (proto enums are open to extension by
appending new values; renaming or reordering existing values is a
breaking change). The migration 1840000006 aligns the PocketBase
`state` column with the proto.

`PUBLISHED` is the existing `NORMAL` — they mean the same thing in
the Memos upstream vocabulary. We keep `NORMAL` as the wire name for
backward compatibility with the SPA, the export pipeline, and the
Connect clients in the wild, and document the alias in
`docs/design/po33-device-binding.md`.

### 2.3. The "wire into the existing REST gateway" deliverable is impossible in riff

The brief's §12 deliverable 3 reads "wire into the existing REST
gateway". There is no existing REST gateway in riff. The Memos API
is **Connect-only** per the top entry of `DECISIONS.md` dated
2026-10-16: the `/api/...` URL group is owned by PocketBase's own
REST surface, and a grpc-gateway mount under that prefix would
collide with every PocketBase collection route. The only externally
callable transport for the Memos API is the Connect mux at
`/memos.api.v1.<Service>/<Method>`.

The PO-33 device side lives in `pb/api/po33_device_routes.go`
because the PO-33 firmware runs an HTTP/1.1 client with a one-shot
Ed25519 signature header; it does not speak Connect. The user side
lives in `pb/connect/sketch_service.go` (Connect). The two surfaces
do not collapse onto one transport without breaking the device
fleet.

---

## 3. The multipart upload shape (the firm pivot)

The brief's §3 specifies `multipart/form-data` with three parts:
`metadata` (JSON, signed), `signature` (base64 Ed25519), and
`backup_file` (an opaque tarball), plus optional `sample_wav`s.

**Pivots the website makes to that shape:**

1. **The `metadata` is a JSON object whose canonicalised form is
   what the device signs.** The signed payload is a `SketchPayload`
   proto serialised to canonical JSON (sorted keys, no whitespace,
   fixed integer rendering) plus the device's nonce. The same
   `internal/ed25519verify.CanonicalizeAuthorizationRequest`
   deterministic serialiser the pairing handshake uses is the
   template; a new `CanonicalizeUploadMetadata` helper lives next
   to it.
2. **The `backup_file` tarball is a third attachment, not a
   peer-of-memo blob.** The device emits a `.p33` patch (the
   sketch), zero or more `.wav` samples, and an optional `.bkp`
   tarball. The website already has the column-and-relation shape
   for this: a `patches` row for the `.p33`, a `samples` row per
   `.wav`, and a third `patches` row with a `memo_relations` of
   `type='BACKUP_FILE'` for the `.bkp` (the `BACKUP_FILE` kind was
   added in migration 1840000004). The upload handler does **not**
   re-parse the tarball on the server. The §6 "never silently
   modify the original blob" rule means the raw tarball rides as
   an attachment so a future restore-from-backup tool can read it
   byte-for-byte.
3. **The optional `sample_wav`s are zero or more `samples`
   attachments**, not a JSON array inside the metadata. The
   multipart parts that the brief calls "sample_wav" become one
   `samples` row each, linked by `memo_relations` of
   `type='SAMPLE_FILE'` (the existing kind, unchanged).
4. **The handler enforces a hard size cap per part.** The
   `internal/imagelimit` package already enforces
   per-attachment byte caps in the existing chunked-upload
   pipeline; the same pattern applies. Caps from the brief: 16 KB
   for `metadata`, 16 KB for `signature`, 50 MB for `backup_file`,
   10 MB per `sample_wav`. A 413 surfaces with a JSON error
   message that names the offending part.

---

## 4. Counter-design (one slice, ordered)

The slice is 600–800 lines Go + 150 lines proto + 2 migrations +
tests + 1 DECISIONS entry. It is awkward to split; the upload
handler assumes `state='DRAFT'` and the publish RPC assumes
`sketch_versions` exists, so any sub-slice would land half-built.
Recommend one PR.

| # | File | Purpose |
| --- | --- | --- |
| 1 | `proto/api/v1/po33_sketch_service.proto` | Add `UploadSketch` (server-streaming), `PublishSketch`, `ArchiveSketch`, `ListSketchVersions`, `GetSketchVersion`, the `SketchState` re-export, and the `SketchVersion` message. |
| 2 | `pb/migrations/1840000006_add_memo_state.go` | `state` select on `memos` with `DRAFT | NORMAL | HIDDEN | ARCHIVED`, default `NORMAL` for back-compat. `hidden_reason` text + `hidden_by` relation. |
| 3 | `pb/migrations/1840000007_create_sketch_versions.go` | New `sketch_versions` collection, append-only, keyed on `memo_id`, with `device_fingerprint`, `uploaded_at_unix`, `version_no`, `backup_file_key`, `patch_key`, `sample_keys` JSON. |
| 4 | `pb/api/po33_device_routes.go` | `UploadSketch` REST handler. Multipart parse, Ed25519 verify, enforce size caps, write the `patches`+`samples`+`BACKUP_FILE` relations, write a `sketch_versions` row, default `state='DRAFT'`. Return 201 on success, 401 / 413 / 409 on the three failure modes the brief lists. |
| 5 | `pb/connect/sketch_service.go` | Add `PublishSketch` (owner-only, DRAFT→NORMAL), `ArchiveSketch` (owner-only, NORMAL→ARCHIVED), `ListSketchVersions` (owner-only, append-only), `GetSketchVersion` (owner-only, append-only). |
| 6 | `pb/hooks/memos.go` | BeforeUpdate gate: a sketch in `DRAFT` cannot be made `PUBLIC` (the lifecycle must be: DRAFT→NORMAL→ARCHIVED). The gate is a single `if e.Record.GetString("state") == "DRAFT" && e.Record.GetString("visibility") == "PUBLIC" { return errors.New(...) }` line. |
| 7 | `pb/api/po33_device_routes_internal_test.go` | Tests: happy path (signed multipart → 201), tampered signature → 401, oversized `backup_file` → 413, replayed signature (timestamp older than the device's last upload) → 409. |
| 8 | `pb/connect/sketch_service_internal_test.go` | Tests: PublishSketch (DRAFT→NORMAL, non-owner denied, already-PUBLISHED denied), ArchiveSketch, ListSketchVersions (immutability: even after Archive, versions list stays complete), GetSketchVersion. |
| 9 | `DECISIONS.md` | New entry: "Sketch resource: `memos` is the source of truth; `sketch_versions` is the immutable audit log." |

The `proto/api/v1/po33_sketch_service.proto` file already exists
with `SketchService.ListMySketches`. The new RPCs are added in the
same file. The `state` field already exists on the `Memo` proto,
so the field-shape change is *adding* `DRAFT` and `HIDDEN` to the
enum, not creating a new field.

---

## 5. State machine (the bit the brief asks for)

```
            ┌────── DRAFT  (device upload lands here)
            │       │
            │       │  PublishSketch (owner)
            │       ▼
            │     NORMAL  (the existing default; the SPA's feed
            │       │     list; the search index)
            │       │
            │       │  ArchiveSketch (owner) ──► ARCHIVED
            │       │
            │       │  HideSketch (admin) ──► HIDDEN  (kept on
            │       │                       disk, removed from
            │       │                       feed + search)
            │       │  UnhideSketch (admin) ─► NORMAL
            │       │
            │       └─ DeleteMemo (owner) ──► deleted
            │
            └─ DeleteMemo (owner) ──► deleted
```

The website's existing `memos.viewRule` already gates `HIDDEN` rows
out of the SPA feed: the filter is `state != 'HIDDEN'`. Adding the
column does not require a new rule — only the `ListMemos` SQL
predicate is updated.

The `ARCHIVED` state is the existing one (`proto/api/v1/common.proto`
already has it). The brief's "publishing" workflow is the existing
`NORMAL` state, with the new `DRAFT` value sitting underneath it.
`PUBLISHED` and `NORMAL` are the same wire value (`STATE_NORMAL`),
documented as an alias in the proto comments.

---

## 6. The §6 versioning rule (append-only)

The brief's §6 says "never silently modify the original blob". The
implementation makes this a schema-level invariant: every upload
inserts a row into `sketch_versions`, and the upload handler never
updates an existing row. A re-upload of the same sketch is a new
`sketch_versions` row with `version_no = previous + 1`. The `memos`
row's `state` does not change on re-upload; the user invokes
`PublishSketch` explicitly to make the latest version visible.

The `sketch_versions` table holds:

| Field | Type | Notes |
| --- | --- | --- |
| `memo_id` | relation → `memos` | required, indexed |
| `version_no` | int | monotonic per `memo_id`, unique with `memo_id` |
| `device_fingerprint` | text | redundant with `MemoPayload.sketch.device_fingerprint`; kept for fast `WHERE` filters |
| `uploaded_at_unix` | int | the device's clock |
| `received_at` | date | the server's clock |
| `patch_key` | text | the `patches` row id that holds the `.p33` |
| `sample_keys` | json | array of `samples` row ids |
| `backup_key` | text | the `patches` row id that holds the `.bkp` (empty when no tarball) |
| `metadata` | json | the raw signed metadata envelope (for forensic) |
| `signature` | text | the base64 Ed25519 signature |
| `signature_algorithm` | text | pinned to `"ed25519"` for forward-compatibility |

The collection's `*Rule` fields are all `nil` — the Connect handler
is the only authority for read/write. The collection has no
`OnRecordUpdate` or `OnRecordDelete` bindings, which is the
mechanical way the append-only invariant is enforced at the data
layer.

---

## 7. §7 binding, ownership, fingerprint snapshot — already shipped

`po33_devices` carries `user` (relation), `public_key`, `public_key_fpr`
(fingerprint), `status` (ACTIVE | REVOKED), `last_seen_at`. The
`RevokeDevice` RPC flips the status and copies the fingerprint into
`patches.device_fingerprint_snapshot` and
`samples.device_fingerprint_snapshot` so the upload's provenance
survives the device being revoked. No new work for §7.

The §7 "the upload must be signed by the device's private key, not
the user's session token" rule is already in the design: the
`/api/po33/sketches/upload` route is unauthenticated in the OAuth
sense, and the Ed25519 signature is the bearer credential. The
server resolves the `po33_devices` row by `device_fingerprint`
(extracted from the signed metadata) and the public key in the
signature envelope is checked against the row's stored key.

---

## 8. §8 tags, comments, reactions, search — already shipped

The brief's §8 lists the social primitives the firmware team
expects. All of them ride on the existing `memos` table:

| Primitive | Where it lives | Sketch subset |
| --- | --- | --- |
| Tags | `memos.tags` (json) | `WHERE source='sketch' AND ...` filter |
| Comments | `memo_relations` where `type='COMMENT'` | same |
| Reactions | `memo_reactions` table | same |
| Hashtag extraction | `pb/hooks/memos.go::extractHashtags` | same |
| Mentions | `memos.payload.mentions` (extracted by the markdown service) | same |
| Search | `memos.content` LIKE / FTS (the `pb/api/feed.go` SQL filter) | same |

The `source='sketch'` predicate is a single WHERE-clause addition
to the feed query. No new index strategy; the existing
`idx_memos_source` (the migration 1840000003 added it) covers
the subset scan.

---

## 9. The wire shapes (proto delta)

The §4 above describes the new RPCs. The wire shapes:

```proto
// proto/api/v1/po33_sketch_service.proto (delta)

service SketchService {
  // ListMySketches (existing)
  rpc ListMySketches(ListMySketchesRequest) returns (ListMySketchesResponse) {...}

  // New: PublishSketch. Owner-only. DRAFT -> NORMAL.
  rpc PublishSketch(PublishSketchRequest) returns (Memo) {
    option (google.api.http) = {post: "/api/v1/{name=memos/*}:publish"};
  }

  // New: ArchiveSketch. Owner-only. NORMAL -> ARCHIVED.
  rpc ArchiveSketch(ArchiveSketchRequest) returns (Memo) {
    option (google.api.http) = {post: "/api/v1/{name=memos/*}:archive"};
  }

  // New: ListSketchVersions. Owner-only. Append-only.
  rpc ListSketchVersions(ListSketchVersionsRequest) returns (ListSketchVersionsResponse) {
    option (google.api.http) = {get: "/api/v1/{parent=memos/*}/versions"};
  }

  // New: GetSketchVersion. Owner-only. Append-only.
  rpc GetSketchVersion(GetSketchVersionRequest) returns (SketchVersion) {
    option (google.api.http) = {get: "/api/v1/{name=memos/*/versions/*}"};
  }
}

message SketchVersion {
  option (google.api.resource) = {
    type: "memos.api.v1/SketchVersion",
    pattern: "memos/{memo}/versions/{version}"
  };
  string name = 1;                        // memos/{memo}/versions/{version_no}
  string memo = 2;                        // memos/{memo}
  int32 version_no = 3;
  string device_fingerprint = 4;
  google.protobuf.Timestamp received_at = 5;
  int64 uploaded_at_unix = 6;
  string patch_key = 7;
  repeated string sample_keys = 8;
  string backup_key = 9;
}

// common.proto (delta) — extend the existing State enum:
enum State {
  STATE_UNSPECIFIED = 0;
  NORMAL = 1;       // == PUBLISHED in the brief's vocabulary
  ARCHIVED = 2;
  DRAFT = 3;        // device upload, owner has not published
  HIDDEN = 4;       // admin removed from feed; preserved on disk
}
```

The device-side `UploadSketch` is **not** a Connect RPC. The
multipart lifecycle and the Ed25519 signature are not
gRPC-Streaming-shaped (the device is HTTP/1.1). The handler lives
at `POST /api/po33/sketches/upload` in `pb/api/po33_device_routes.go`,
alongside the pairing handshake. The proto file is not the right
place for it.

---

## 10. Error mapping (the §10 table from the brief)

| HTTP | Code | When |
| --- | --- | --- |
| 201 | Created | Successful upload. Body: `{"memo": "memos/abc", "version": 3}` |
| 400 | Bad Request | Missing multipart part, malformed JSON metadata, malformed signature encoding |
| 401 | Unauthorized | Signature does not validate against the device's stored public key |
| 409 | Conflict | Replayed signature (timestamp older than the device's last successful upload), or the device is REVOKED |
| 413 | Payload Too Large | Any multipart part over the size cap (16 KB / 16 KB / 50 MB / 10 MB) |
| 422 | Unprocessable Entity | The signed metadata is well-formed but the slot count / model / BPM does not match the device's reported values |
| 429 | Too Many Requests | Per-device rate limit (10 / hour, per the brief) |

The rate-limit scope is `ScopeUploadSketch`, a new entry in
`pb/connect/ratelimit.go::procedureScope` and a new
`pb/api/ratelimit.go` REST middleware that delegates to
`internal/ratelimit`. The 10/hour budget is per-device (keyed on
the `po33_devices.id`, not the IP).

---

## 11. What is NOT in this slice

The following brief items are deferred and need their own RFCs:

- **§4 sketch_comment / sketch_reaction / sketch_tag forks** —
  the table-fork recommendation rejected above, so the
  comment/reaction/tag forks are also implicitly rejected. Tags,
  comments, reactions, and search all ride on the existing `memos`
  schema with `WHERE source='sketch'` predicates.
- **§9 "edit in place, leave a comment"** — deferred. The lifecycle
  here is upload → publish → archive. Edits are re-uploads that
  create a new `sketch_versions` row.
- **§11 collaborative editing (real-time, OT/CRDT)** — deferred
  entirely. The slice is single-author.
- **§13 "tarball round-trip via the export pipeline"** — the export
  pipeline already serialises `patches`+`samples` attachments.
  The `sketch_versions` rows add three more keys (`patch_key`,
  `sample_keys`, `backup_key`) that the export manifest picks up
  on the next slice. No new export format needed.

---

## 12. Open questions for the firmware team

1. **The brief's `PublishSketch` and `ArchiveSketch` are user-only
   RPCs.** Is there a device-side equivalent (e.g. "device A
   published this sketch but device B should not see it yet")? If
   so, that needs a `device_publish` flag on `sketch_versions`
   rather than on the `memos` row.
2. **The brief's §3 sample_wav array is "zero or more".** The
   device has 16 sample slots; a 17th sample would be a firmware
   bug. Do we cap at 16 server-side, or trust the device to never
   send more? The current design caps at 16 with a 422.
3. **The §6 "immutable blob" rule implies the device can read its
   own uploads back.** Is the device going to call
   `GET /api/po33/sketches/{id}/backup` (a new REST route) for
   restore-from-backup, or is the restore flow entirely
   web-side? If device-side, the route needs the same Ed25519
   bearer-credential shape the upload route uses.

---

## 13. The single line that has to change in the firmware PR

In the brief's `README.md`, replace:

> 4. The website stores sketches in a new `sketch` table…

with:

> 4. The website stores sketches in the existing `memos` table
>    with `source='sketch'` and a new `state` enum value `DRAFT`.
>    The lifecycle is DRAFT → NORMAL → ARCHIVED, with HIDDEN as
>    an admin-only side-state. The append-only `sketch_versions`
>    collection holds the audit log. See
>    `docs/community-website-response.md` in
>    `incurl/riff` for the wire shape and the migrations.

If that paragraph lands, the rest of the implementation follows
mechanically.

---

## 14. Review checklist for the firmware team

- [ ] §2.1: confirm the "no parallel `sketch` table" position.
- [ ] §2.2: confirm the proto `State` enum is the lifecycle enum
  (`PUBLISHED` is an alias for `NORMAL`).
- [ ] §2.3: confirm the device-side upload is REST (HTTP/1.1 +
  Ed25519) and not a Connect streaming RPC.
- [ ] §3: confirm the tarball rides as a `BACKUP_FILE` attachment,
  not as a peer-of-memo blob.
- [ ] §5: confirm the `DRAFT → NORMAL → ARCHIVED` lifecycle and
  the `HIDDEN` side-state.
- [ ] §6: confirm the `sketch_versions` table is append-only and
  the user invokes `PublishSketch` to make a new version visible.
- [ ] §10: confirm the 401 / 413 / 409 / 422 / 429 / 201 status
  codes and the per-device 10/hour rate limit.
- [ ] §12: answer the three open questions above before the
  implementation PR lands.

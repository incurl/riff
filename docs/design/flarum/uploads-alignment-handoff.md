# Flarum uploads alignment with upstream `fof/upload` 2.x — handoff

**Status: IMPLEMENTED (2026-11-26).** The audit is kept as written, and §3's
defects were fixed in the order listed. Where the implementation differs from
this document, `DECISIONS.md` (the "UPLOADS, ALIGNED WITH UPSTREAM" entry)
records the final choice — the two that matter being that `bbcode` carries
MARKDOWN rather than upstream's rendered HTML template, and that the default
sort is `-created_at` rather than upstream's `-id`, because PocketBase ids are
random and `-id` would be an arbitrary order.

**Goal:** align `pb/flarum/plugins/uploads/` with upstream `fof/upload` 2.x —
its resources, its routes, its permissions and its settings — so a client
written against the extension works against this port, and so the composer can
actually attach a file.

---

## 1. Upstream contract (the target)

Fetched from `https://raw.githubusercontent.com/FriendsOfFlarum/upload/2.x/...`.
The repository's default branch is `2.x`.

### 1.1 The route table (`extend.php`)

```php
->get('/fof/uploads',                        ListFilesHandler)
->get('/fof/upload/shared-files',            ListFilesHandler)
->post('/fof/upload',                        UploadHandler)
->post('/fof/watermark',                     WatermarkUploadHandler)
->delete('/fof/watermark',                   WatermarkDeleteController)
->get('/fof/download/{uuid}/{post}/{csrf}',  DownloadController)
->get('/fof/download/{uuid}',                DownloadController)
->post('/fof/upload/inspect-mime',           InspectMimeController)
->patch('/fof/upload/hide',                  HideUploadFromMediaManagerHandler)
->delete('/fof/upload/delete/{uuid}',        DeleteFileController)
```

They are `Extend\Routes('api')` entries, so the public paths are `/api/fof/...`.
The JSON:API resource adds `/api/files` (an `Index` endpoint, `->authenticated()`,
`->defaultInclude(['actor'])`, `->defaultSort('-id')`, `->paginate(20, 50)`) and
`DELETE /api/files/{id}` (a `Delete` endpoint with `->can('delete')`).

### 1.2 `FileResource::fields()` — the full upstream list

```
baseName          (Str)      -> base_name
path              (Str)
url               (Str)      the storage URL, or the private download route
type              (Str)      the MIME type
size              (Int)
humanSize         (Str)      -> humanSize
createdAt         (DateTime) -> created_at
uuid              (Str)      the id the download route takes
tag               (Str)      the render template's tag
hidden            (Bool)
bbcode            (Str)      -> getBbcodeForFile: the template's render
shared            (Bool)
imageWidth        (Int, nullable)
imageHeight       (Int, nullable)
thumbnailUrl      (Str, nullable) -> getThumbnailUrlForFile
thumbnailWidth    (Int, nullable)
thumbnailHeight   (Int, nullable)
isPrivateShared   (Bool)     -> isPrivateShared
canViewInfo       (Bool)     -> $actor->can('viewInfo', $file)
canHide           (Bool)     -> $actor->can('hide', $file)
canDelete         (Bool)     -> $actor->can('delete', $file)
actor             (ToOne users, includable)
```

`FilePolicy::viewInfo()` is a bare `deny()` ("for now.."), so `canViewInfo` is
false for everybody — administrators included.

### 1.3 The upload endpoint

`POST /api/fof/upload`, multipart. The files are a REPEATED field named `files`
(`Arr::get($request->getUploadedFiles(), 'files', [])`), so ONE request carries
several files. The options are read from the body as `options.hidden` and
`options.shared` (`'true'` or boolean `true`). The response is a JSON:API
**collection** — `{data: [file, …], included: [actor, …]}` — because the handler
serialises every uploaded file with the `actor` relationship included. A request
whose files all fail validation throws `InvalidUploadException` (400), not an
empty 201.

### 1.4 The index and its scope (`FileResource::scope()`)

- `filter[shared]` — the shared library. Requires `fof-upload.access-shared-files`
  OR `fof-upload.upload-shared-files`, and a caller who holds only the read
  permission does not see hidden entries.
- otherwise `filter[user]` (default: the actor) — `actor_id = user AND hidden =
  false AND shared = false`. Listing ANOTHER account requires
  `fof-upload.viewUserUploads`.
- `sort` — `-id` by default; `createdAt` is the other declared column.
- `page[offset]` / `page[limit]` — `->paginate(20, 50)`.

The two `/api/fof/uploads…` routes are aliases: `ListFilesHandler` sets
`filter[shared]` for the `/shared-files` spelling and forwards to the `files`
index endpoint.

### 1.5 The permissions (`src/Access/FilePolicy.php`)

| key | meaning |
| --- | --- |
| `fof-upload.upload` | the base upload gate (its `Upload` command asserts it) |
| `fof-upload.viewUserUploads` | list another account's uploads |
| `fof-upload.access-shared-files` | read the shared library |
| `fof-upload.upload-shared-files` | upload, hide and delete shared files |
| `fof-upload.deleteOwnUploads` | delete a file one uploaded |
| `fof-upload.deleteUserUploads` | hide and delete another account's files |

`hide()`: shared → `upload-shared-files`; else owner OR `deleteUserUploads`.
`delete()`: shared → `upload-shared-files`; else `deleteUserUploads`; else
owner AND `deleteOwnUploads`. **Ownership alone is not enough to delete** — an
operator can let a member upload without letting them delete, and the media
manager then offers no delete button.

Administrators may hide and delete anything: the policy returns `null` rather
than `deny()` for those two, so the Gate's admin fallback decides.

### 1.6 The settings (`Extend\Settings()->default(...)`)

`fof-upload.maxFileSize` (default `2048`, KIBIBYTES — `Util::DEFAULT_MAX_FILE_SIZE`),
`fof-upload.s3CustomUrl`, `fof-upload.svgAnimateAllowed`,
`fof-upload.generateThumbnails`, `fof-upload.thumbnailWebp`,
`fof-upload.thumbnailMaxWidth`, `fof-upload.thumbnailQuality`.

### 1.7 The frontend

`js/src/forum/addUploadButton.ts` extends `TextEditor.prototype`'s
`controlItems` and adds its button only when
`app.forum.attribute('fof-upload.canUpload')` is true — the flag the extension
publishes, computed as `$actor->can('fof-upload.upload')`. On success the
uploaded file's `bbcode` is inserted at the cursor
(`composer.editor.insertAtCursor(bbcode + '\n', false)`).

---

## 2. What the port had

A working but NARROW implementation: `POST /api/uploads` (one `file` part, one
resource in the response), `POST /api/posts/{id}/files`, `DELETE /api/files/{id}`
(+ the POST twin), `GET /api/files` (the caller's files only, no filters, no
pagination), `GET /api/files/{id}`, and `GET {basePath}/files/{id}` for the
bytes. The resource carried a SUBSET of upstream's fields, and the frontend
could only RENDER attachments — nothing in the composer ever uploaded one, so
`POST /api/uploads` had no caller outside the tests.

## 3. Confirmed defects

| # | defect | why it matters |
| --- | --- | --- |
| D1 | `POST /api/fof/upload` did not exist; the port's route took one file | a client written against the extension 404s; the port's own response was a single object where upstream's is a collection |
| D2 | `/api/fof/uploads`, `/api/fof/upload/shared-files`, `/api/fof/download/{uuid}`, `/api/fof/upload/delete/{uuid}` and `/api/fof/upload/hide` did not exist | every one is a route the extension's own frontend calls |
| D3 | the resource lacked `bbcode`, `thumbnailUrl`, `thumbnailWidth`, `thumbnailHeight`, `isPrivateShared` and `canViewInfo` | a missing attribute is SILENT: the frontend reads `undefined` and renders nothing |
| D4 | `canHide` was hardcoded `false`, and `canDelete` was "uploader or admin" | the flag disagreed with the policy the endpoint should enforce, and `deleteOwnUploads` / `deleteUserUploads` were not expressible |
| D5 | the listing had no `filter[user]`, `filter[shared]`, no sort and no pagination | the media manager could not page through anything, and the shared library was unreachable |
| D6 | the upload took any signed-in caller | upstream gates it on `fof-upload.upload`, so an operator could not turn uploads off |
| D7 | `fof-upload.maxFileSize` was the constant `MaxFileSize` (32 MiB) only | a setting upstream offers had no equivalent, and the port's limit was not configurable |
| D8 | `createdAt` carried PocketBase's internal date layout (`2006-01-02 15:04:05.000Z`) | the frontend parses it with `new Date(...)`; the layout is not RFC 3339, so the value is engine-dependent |
| D9 | `imageWidth`/`imageHeight` were `0` for a non-image | upstream's are `null`; `0` reads as a zero-pixel image |
| D10 | the DELETE answered `204` with `{"data":null}` | upstream answers a BODYLES 204 |
| D11 | the deletion policy ignored `/`-permission separation | ownership alone let a member delete, whatever the operator granted |
| D12 | the frontend could not upload at all | the plugin's whole purpose is attaching a file to a post |
| D13 | `fof-upload.canUpload` was not published | the button's flag did not exist, so a button would have had to be hardcoded — the "control that changes nothing" failure in both directions |

---

## 4. What was implemented

1. **The resource** (`plugins/uploads/resource.go`) — every upstream attribute,
   with `bbcode` (Markdown), `thumbnailUrl`/`thumbnailWidth`/`thumbnailHeight`
   null, `isPrivateShared` false, `canViewInfo` false, nullable dimensions, and
   `createdAt` in RFC 3339.
2. **The policy** (`resource.go::mayHideFile` / `mayDeleteFile`) — upstream's
   `FilePolicy`, with the six permissions, ownership layered on top, and the
   admin fallback. The attributes and the endpoints ask the same functions.
3. **The routes** (`plugins/uploads/uploads.go`) — upstream's spellings beside
   the port's own: `/api/fof/upload`, `/api/fof/uploads`,
   `/api/fof/upload/shared-files`, `/api/fof/download/{uuid}` (+ the
   three-segment form), `/api/fof/upload/delete/{uuid}` (+ POST twin) and
   `/api/fof/upload/hide` (+ POST twin).
4. **The upload** — `files` as a repeated part (and the port's `file`), the
   `options[hidden]`/`options[shared]` options, the `fof-upload.upload` gate, and
   a COLLECTION response.
5. **The listing** — `filter[user]`, `filter[shared]`, `sort`, `page[offset]`,
   `page[limit]` (20/50), upstream's scope rules and their permission gates.
6. **The permissions** — the six keys in `domain/models.go`, their upstream
   names in `permission_keys.go`, and the seed rows (member: `upload`,
   `deleteOwnUploads`; moderator: those plus `deleteUserUploads`,
   `accessSharedFiles`, `uploadSharedFiles`).
7. **The setting** — `fof-upload.maxFileSize` declared and read, in KiB,
   defaulting to 32 MiB and capped by the collection's field maximum.
8. **The column** — `shared`, added by its own idempotent migration step
   (`add_shared_column`), because a plugin migration that has already been
   recorded never runs again.
9. **The frontend** — the composer's attach button in `TextEditor.controlItems`,
   gated on `fof-upload.canUpload` (which the backend now publishes on the forum
   document through an attribute contributor), posting to `/fof/upload` and
   inserting the returned `bbcode` at the cursor.

## 5. Deliberate deviations (not ported)

- **No thumbnail generation** (`generateThumbnails`, `thumbnailWebp`,
  `thumbnailMaxWidth`, `thumbnailQuality`). The port stores the image's
  dimensions, which is what prevents the layout shift; the three thumbnail
  attributes are null, and the four settings are NOT declared, because a setting
  nobody reads is a control that changes nothing.
- **No watermarking, resizing or storage adapters** (`/fof/watermark`,
  `s3CustomUrl`). The bytes live in PocketBase's filesystem, so the existing
  storage bridge and its S3 support apply, and the file is stored as uploaded.
- **No MIME allow-list** (`fof-upload.mimeTypes`, `/fof/upload/inspect-mime`).
  Upstream rejects a file whose declared and detected types disagree; this port
  derives the type from the file NAME and serves anything non-inline as a
  download, which removes the same class of risk from the serving side. A MIME
  allow-list is a candidate follow-up.
- **No media-manager page, no user-uploads page, no drag-and-drop or paste.**
  The routes they call exist (§4.3), so a frontend that grows them has a server;
  the port's own composer offers the file picker.
- **`include=actor` is not served.** The `actor` relationship carries its
  identifier, but the index does not emit the included user resources. The
  port's own frontend fetches `/api/files/{id}` instead, and nothing else asks.
- **The shared library has no separate storage.** Upstream moves a shared file
  between a public and a private disk; here `shared` is one flag on one row, in
  the one storage PocketBase owns.
- **A shared/private file's `url` is the forum's own route**, not a signed
  external URL: `{basePath}/files/{id}` (and the `/fof/download/{uuid}` alias),
  authorized by the file's own visibility rule.

## 6. Verification

```bash
go test -count=1 -run 'TestUploads_' ./pb/flarum/
cd pb/flarum/frontend/packages/core && npx jest --config jest.config.local.cjs --testPathPattern 'pluginBundles'
bash scripts/build_flarum_spa.sh   # the bundle must be restaged or the shell serves the old one
```

The tests pin the resource's full attribute set, the multi-file upload and its
collection response, the permission gates (upload, shared library), the listing's
pagination and default sort, hiding, the delete-by-uuid 204, the download route,
the size setting, the moderator tier, and `fof-upload.canUpload` agreeing with
the endpoint.

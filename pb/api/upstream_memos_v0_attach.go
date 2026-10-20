package api

import (
	"bytes"
	"encoding/json"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/incurl/riff/pb/connect"
)

// The MoeMemos V0 client posts a memo that carries an
// attachment in three steps:
//
//  1. POST /api/v1/memo            — the memo with its text
//  2. POST /api/v1/resource/blob   — the file upload
//  3. PATCH /api/v1/memo/{id}      — the memo update whose
//     `resourceList` links the uploaded resource to the
//     memo
//
// Step 2 has no memo to attach the file to, so the upload
// lands **unlinked**: `attachments.memo` stays empty and the
// row's `creator` records the uploader. This is upstream's
// own model — `Attachment.memo` is `optional`, and
// `GetAttachment` documents "an unlinked attachment is
// visible only to its uploader and admins". Step 3 is what
// binds the file to the memo the user actually sees.
//
// Historically step 2 created a hidden stub memo and
// uploaded against it, because riff's `attachment.memo` was
// once required. Step 3 was also a no-op: the V0 PATCH
// shaper left `resourceList` in the request body, so it
// landed in the V1 `update_mask`, and the V1 `UpdateMemo`
// handler has no `resourceList` field. The file therefore
// stayed bound to the stub forever, and the feed showed two
// memos: the real one with the text, and an empty one
// carrying the attachment. The stub was a phantom `memos`
// row per upload that the follow-up PATCH had to clean up,
// which it only did when every file it held moved — a client
// that uploaded twice and bound one file left the stub
// behind holding the other.
//
// Both problems are gone: the upload no longer manufactures a
// memo row, so nothing has to be cleaned up, and step 3 is
// honoured explicitly below.

// v0ResourceIDFromLink reads the numeric V0 resource id out
// of one raw `resourceList` entry.
//
// The V0 client identifies a resource by the numeric V0
// Long id it was handed at upload time, so the id is the
// only load-bearing field in the entry; the display fields
// (`filename`, `type`, `externalLink`) are already stored
// on the attachment row.
//
// A malformed entry yields 0, which never matches a real
// attachment (the V0 id derivation never returns 0 — see
// `v0IDFromV1`).
func v0ResourceIDFromLink(entry map[string]any) int64 {
	switch v := entry["id"].(type) {
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0
		}
		return n
	case float64:
		return int64(v)
	case string:
		var n int64
		if err := json.Unmarshal([]byte(v), &n); err != nil {
			return 0
		}
		return n
	}
	return 0
}

// v0ResourceListIDs extracts the V0 resource ids from a
// decoded V0 memo body.
//
// The field is **`resourceIdList`**, a flat array of integer
// ids. This was wrong for the entire life of the V0 surface and
// is why uploads were never linked: the reader looked for
// `resourceList`, an array of *resource objects*, which is the
// **response** shape (`Memo.resourceList` in the client's
// OpenAPI spec) and is not a request field at all. MoeMemos'
// `CreateMemoRequest` and `PatchMemoRequest` both declare
// `resourceIdList: {type: array, items: {type: integer}}`, and
// the client populates it at MemosV0Service.swift:79 / :119
// from the resource `remoteId`s. The old reader therefore found
// nothing on every request and the PATCH bound nothing.
//
// `resourceList` (the object form) is still accepted, so a
// hand-rolled client or an older fork that posts it still binds.
//
// The list is optional: the client omits it on an edit that does
// not touch the attachments, so an absent or malformed list is
// not an error — it just means "no attachment changes".
func v0ResourceListIDs(inner map[string]any) []int64 {
	// The real request shape: a flat array of integer ids.
	// Elements may arrive as json.Number (the client decodes
	// 64-bit ids as Long, so the body is decoded with
	// UseNumber), float64, or a numeric string.
	if raw, ok := inner["resourceIdList"].([]any); ok {
		out := make([]int64, 0, len(raw))
		for _, item := range raw {
			if id := v0ResourceIDFromScalar(item); id != 0 {
				out = append(out, id)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	// The object form, kept for compatibility.
	raw, ok := inner["resourceList"].([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			// A flat integer in the object-form field.
			if id := v0ResourceIDFromScalar(item); id != 0 {
				out = append(out, id)
			}
			continue
		}
		if id := v0ResourceIDFromLink(m); id != 0 {
			out = append(out, id)
		}
	}
	return out
}

// v0ResourceIDFromScalar reads one resource id from a bare
// JSON value — the element type of `resourceIdList`.
//
// The client emits a `Long`, so the number arrives as a
// `json.Number`; a `float64` is accepted for hand-built bodies
// and a numeric string for clients that quote the id.
func v0ResourceIDFromScalar(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return i
	case float64:
		return int64(n)
	case string:
		var i int64
		if err := json.Unmarshal([]byte(n), &i); err != nil {
			return 0
		}
		return i
	}
	return 0
}

// v0ResourceListIDsFromBody extracts the V0 resource ids from
// a raw V0 memo PATCH body.
//
// The body is the V0 flat memo shape, so the list sits under
// the `resourceList` key. A body that is not a JSON object, or
// that carries no list, yields no ids — the PATCH then leaves
// the attachments alone, which is what an edit that does not
// touch them should do.
func v0ResourceListIDsFromBody(body []byte) []int64 {
	inner, err := decodeV0Object(body)
	if err != nil {
		return nil
	}
	return v0ResourceListIDs(inner)
}

// decodeV0Object decodes a V0 JSON object into a map,
// keeping the numbers as `json.Number`.
//
// `decodeFlatObject` (the shared V1 helper) decodes into
// `map[string]any`, so every number becomes a `float64`.
// The V0 resource ids are Kotlin `Long`s that run up to
// 2^63, which a float64 cannot represent exactly — the
// value comes back rounded, and the id no longer matches
// the attachment it names. This decoder keeps the literal
// digits so `json.Number.Int64` can read them back
// exactly.
//
// A body that is not a JSON object is an error, not a
// silent empty map: the caller treats a decode failure as
// "no resource list" and leaves the attachments alone.
func decodeV0Object(body []byte) (map[string]any, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, errors.Wrap(err, "invalid JSON body")
	}
	return out, nil
}

// v0ShapeSingleMemoAttachments re-derives a created memo's `attachments`
// list from the store, after `v0LinkResourcesToMemo` has bound the caller's
// uploaded resources onto it.
//
// The V1 `CreateMemo` response the shaper is about to shape was produced
// *before* the bind, so its `attachments` list is empty. Answering the client
// with that stale list is worse than answering 200: the client trusts it as
// the memo's contents, and on its next edit it sends a `resourceList` built
// from what it was told — an empty one — dropping the files it just
// successfully attached. Re-reading after the bind closes that loop.
//
// `e` is the originating request event; the user-facing origin
// it carries (scheme + host[:port]) is forwarded to every
// attachment row, producing absolute `externalLink`s the iOS
// MoeMemos client's host comparison trusts (see the
// request-origin comment in `pb/connect/connect.go`).
//
// The returned map is a fresh copy; the caller may keep using the original for
// its other fields.
func v0ShapeSingleMemoAttachments(app core.App, e *core.RequestEvent, memoID string, memo map[string]any) (map[string]any, error) {
	atts := connect.MemoAttachmentsAsProtoWithOriginString(app, memoID, requestOriginFromEvent(e))
	if len(atts) == 0 {
		return memo, nil
	}

	// The memo body is protojson-shaped (`map[string]any` decoded from the
	// connect response), so each attachment has to be rendered the same way
	// for `v0ShapeMemo`'s `m["attachments"].([]any)` read to see it.
	shaped := make([]any, 0, len(atts))
	for _, att := range atts {
		raw, err := protojson.Marshal(att)
		if err != nil {
			return nil, errors.Wrap(err, "marshal attachment")
		}
		var one map[string]any
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, errors.Wrap(err, "decode attachment json")
		}
		shaped = append(shaped, one)
	}

	out := make(map[string]any, len(memo)+1)
	for k, v := range memo {
		out[k] = v
	}
	out["attachments"] = shaped
	return out, nil
}

// v0LinkResourcesToMemo moves the attachments named by a V0
// memo's `resourceList` onto the target memo.
//
// The move is a pointer rewrite on the `attachments.memo`
// relation, not a re-upload: the file bytes never leave
// storage, and the attachment keeps its id, filename and
// blob.
//
// Two rules are enforced, both mirroring the V1
// orphan-bind path in `pb/connect/memo_bind_attachments.go`
// (and therefore upstream's `MemoAttachmentBindingOwnerAllowed`):
//
//   - Ownership. An unlinked row belongs to the uploader
//     recorded in its `creator` column. Only the memo's author
//     may bind it, so a client that learned another user's
//     resource id cannot pull the bytes onto its own memo.
//     A row already bound elsewhere is refused rather than
//     stolen; a stale id the client names but riff does not know
//     is skipped, because the memo edit itself is still valid.
//   - No orphan cleanup is needed. Uploads land unlinked
//     (`attachments.memo` empty), so there is no phantom memo
//     row to delete when the file moves away.
//
// The whole move runs in one transaction, so a rejected
// attachment cannot leave the files split across two memos.
func v0LinkResourcesToMemo(app core.App, authorID, targetMemoID string, v0ResourceIDs []int64) error {
	if len(v0ResourceIDs) == 0 {
		return nil
	}

	// Resolve every V0 resource id to an attachment row
	// before touching anything.
	attachments, err := app.FindCollectionByNameOrId("attachments")
	if err != nil {
		return errors.Wrap(err, "find attachments collection")
	}
	rows, err := app.FindRecordsByFilter(attachments, "", "-created", 0, 0)
	if err != nil {
		return errors.Wrap(err, "list attachments")
	}
	wanted := make(map[int64]*core.Record, len(v0ResourceIDs))
	for _, r := range rows {
		v0ID := v0IDFromV1(r.Id)
		if v0ID == 0 {
			continue
		}
		for _, want := range v0ResourceIDs {
			if v0ID == want {
				wanted[want] = r
				break
			}
		}
	}

	return app.RunInTransaction(func(txApp core.App) error {
		for _, v0ID := range v0ResourceIDs {
			rec, ok := wanted[v0ID]
			if !ok {
				// The client named a resource riff does
				// not know. Skip it: the memo edit is
				// still valid, and a stale id must not
				// fail the whole PATCH.
				continue
			}
			// Ownership, mirroring
			// `bindOrphanAttachmentsToMemo` /
			// upstream `MemoAttachmentBindingOwnerAllowed`:
			// an unlinked row belongs to the uploader in its
			// `creator` column, and only the memo's author may
			// bind it. An already-bound row keeps its existing
			// owner check.
			ownerMemoID := rec.GetString("memo")
			ownerAuthorID := ""
			if ownerMemoID != "" {
				owner, oerr := txApp.FindRecordById("memos", ownerMemoID)
				if oerr != nil {
					return errors.Wrap(oerr, "find owning memo")
				}
				ownerAuthorID = owner.GetString("author")
			} else {
				ownerAuthorID = rec.GetString("creator")
			}
			// An empty `creator` is a row written before the
			// column existed; the collection's own rules are the
			// only authority for those, so let them decide.
			if ownerAuthorID != "" && ownerAuthorID != authorID {
				return errors.New("attachment belongs to a different author")
			}
			rec.Set("memo", targetMemoID)
			if err := txApp.Save(rec); err != nil {
				return errors.Wrap(err, "rebind attachment to memo")
			}
		}
		return nil
	})
}

// isV0StubMemoRecord reports whether a memo record is a
// legacy upload stub — a phantom row that earlier builds
// created to satisfy the once-required `attachments.memo`
// column. riff no longer writes stubs, but rows created
// before that change are still in live databases, so the
// sentinel stays a filter in the list handlers and a
// marker for the cleanup migration.
//
// The stub's content is the zero-width-space sentinel; the
// V0 client never posts a memo with that exact content, so
// the sentinel alone is a reliable marker. The visibility
// check is deliberately NOT part of the test: a stub that an
// earlier edit flipped to PUBLIC is still a stub.
func isV0StubMemoRecord(rec *core.Record) bool {
	return connect.IsStubMemoContent(rec.GetString("content"))
}

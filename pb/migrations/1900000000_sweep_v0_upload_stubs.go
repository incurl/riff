package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
)

// LegacyStubMemoContent exposes the sentinel to other packages' tests.
//
// `pb/migrations` cannot import `pb/connect` (the dependency runs the other
// way), so the copy below cannot be compared against the original from inside
// this package's tests either — `go test` applies the same cycle rules. The
// only place both are importable is a package that already depends on both,
// which is why the cross-check lives in `pb/connect`. This accessor is what
// makes that possible.
func LegacyStubMemoContent() string { return legacyStubMemoContent }

// legacyStubMemoContent is the zero-width-space sentinel that marked every
// phantom upload stub.
//
// It is a copy of `pb/connect.StubMemoContent`, duplicated rather than
// imported because `pb/migrations` cannot import `pb/connect` — the
// dependency runs the other way (`pb/connect` test helpers pull in
// `pb/migrations` to build a fully-migrated app). The duplication is
// deliberate and the two must stay in sync; `TestSweepStubsSentinelMatchesThe
// ConnectConstant` in `1900000000_sweep_v0_upload_stubs_internal_test.go`
// fails if they ever drift.
const legacyStubMemoContent = "\u200b"

// legacyStubPayload is the JSON marker the stub creator stamped into
// `memos.payload`.
const legacyStubPayload = `{"_v0_stub":true}`

// sweepV0UploadStubs deletes the phantom memo rows that early riff builds
// created for the upstream-Memos V0 (Memos 0.21) upload path.
//
// Those builds required `UploadAttachment.spec.attachment.memo`, so
// `POST /api/v1/resource/blob` — which has no memo to name — created a hidden
// stub memo and uploaded against it. The stub was identified by the
// zero-width-space sentinel in `memos.content` (see
// `pb/connect/memo_stub.go`) and additionally carried a
// `payload = {"_v0_stub":true}` marker.
//
// The requirement is gone: an upload with no memo now lands as an unlinked
// attachment, matching upstream's `optional Attachment.memo`. Nothing writes
// stubs any more. This migration cleans up what earlier builds left behind —
// without it, those rows persist forever, filtered out of the feed but still
// occupying `memos` and still appearing to own uploaded bytes.
//
// **File bytes are never destroyed.** `attachments.memo` has
// `CascadeDelete: true`, so deleting a stub that still holds files would
// silently delete the user's uploads. Instead the pass walks each stub's
// attachments first and unlinks them — clearing `memo` and stamping `creator`
// with the stub's author — so the rows survive as ordinary unlinked
// attachments that the user can re-attach to a real memo. Only after that
// does it delete the now-empty stub.
//
// Detection requires BOTH markers. The content sentinel alone could in
// principle match a memo a user actually wrote, and the payload marker alone
// is a plain JSON column a user could have typed; together they are unique to
// the stub creator.
//
// **The zero-width space defeats PocketBase's filter parser.** An obvious
// `FindRecordsByFilter(memos, "content = {:sentinel}", ...)` matches nothing:
// the parser trims whitespace on both sides of `=`, and U+200B counts as
// whitespace to it, so the comparison is against an empty string. The failure
// is silent — zero rows, no error — which would leave every stub in place
// while looking like a successful migration. The sweep therefore lists the
// memos and matches in Go. (The companion `memo = {:memo}` lookup on
// `attachments` is fine: PocketBase ids are alphanumeric, never trimmed.)
//
// Idempotency: a second run finds no sentinel rows and does nothing. The
// migration no-ops on a fresh install where `memos` does not yet exist.
func init() {
	migrations.Register(sweepV0UploadStubs, nil)
}

// sweepV0UploadStubs is the migration body, kept as a named function so the
// tests can invoke it directly. PocketBase's migration runner records applied
// migrations by filename, so `RunAllMigrations` is a no-op for an
// already-migrated app — a test cannot seed phantom rows and then ask the
// runner to apply this one.
func sweepV0UploadStubs(txApp core.App) error {
	// PocketBase's filter parser trims whitespace from both sides of an
	// `=` comparison, and the zero-width space IS whitespace to it — an
	// `content = {:sentinel}` filter silently matches nothing. Selecting
	// with a filter at all is the problem, so this pass lists the memos and
	// matches in Go. The collection is bounded by the instance's memo count
	// and the sweep runs once, at migration time.
	memos, err := txApp.FindCollectionByNameOrId("memos")
	if err != nil {
		return nil
	}
	attachments, err := txApp.FindCollectionByNameOrId("attachments")
	if err != nil {
		return nil
	}

	rows, err := txApp.FindAllRecords(memos)
	if err != nil {
		return err
	}

	for _, stub := range rows {
		if !isLegacyUploadStub(stub) {
			continue
		}

		// Re-home the stub's files before deleting the row, so the
		// cascade does not take the user's uploads with it.
		held, err := txApp.FindRecordsByFilter(attachments,
			"memo = {:memo}", "", 0, 0, map[string]any{"memo": stub.Id})
		if err != nil {
			return err
		}
		for _, att := range held {
			att.Set("memo", "")
			if stub.GetString("author") != "" {
				att.Set("creator", stub.GetString("author"))
			}
			if err := txApp.Save(att); err != nil {
				return err
			}
		}

		if err := txApp.Delete(stub); err != nil {
			return err
		}
	}

	return nil
}

// isLegacyUploadStub reports whether a memo row is one of the phantom upload
// stubs.
//
// Both markers must match. `content` alone is only the zero-width-space
// sentinel, which a user could theoretically have submitted as a memo body;
// `payload` alone is a user-writable JSON column. Only the creator wrote the
// pair together.
func isLegacyUploadStub(rec *core.Record) bool {
	if rec.GetString("content") != legacyStubMemoContent {
		return false
	}
	return rec.GetString("payload") == legacyStubPayload
}

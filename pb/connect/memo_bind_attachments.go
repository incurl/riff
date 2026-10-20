package connect

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// bindOrphanAttachmentsToMemo rewires every attachment named on `wanted` to
// the memo id `memoID`. The binding only happens for rows that are still
// orphans (the `attachments.memo` column is empty) — that is the
// authoritative "the uploader has not bound this yet" signal. Rows that
// already belong to a different memo are rejected with INVALID_ARGUMENT
// instead of being silently moved; matches the `SetMemoAttachments` policy.
//
// Ownership gate (upstream parity): an unbound attachment may only be bound to
// a memo whose author is the uploader, or — for an instance administrator — by
// that administrator for their *own* uploads. This is upstream
// `store.MemoAttachmentBindingOwnerAllowed`, ported verbatim as
// `memoAttachmentBindingOwnerAllowed` below: "Ownership is never transferred
// between accounts." Note the comparison is against the MEMO AUTHOR, not the
// acting user.
//
// An orphan row's `creator` column is the uploader (set during
// `UploadAttachment`'s finishWrite pass — see `attachment_upload.go`), so
// without this gate a hostile SPA that ever learnt another user's
// attachment name could rebind the row onto its own memo. An orphan row's
// visibility rule resolves to NULL for everyone because `memo` is empty, so
// the standard rule engine cannot disambiguate the uploader; the `creator`
// column is the only signal we have.
//
// The failure mode is NOT_FOUND, not PERMISSION_DENIED. Upstream
// deliberately hides the row ("Hide another user's unlinked or foreign-memo
// attachment from the caller", `memo_attachment_service.go`): answering
// PERMISSION_DENIED confirms the name resolves to a real attachment, which
// is an existence oracle for another user's upload. NOT_FOUND says the same
// thing a genuinely absent row would.
//
// The whole pass runs in a single transaction so a rejected target cannot
// leave a partially-rebound memo behind.
func bindOrphanAttachmentsToMemo(ctx context.Context, app core.App, memoID, viewerID string, wanted []*v1.Attachment) error {
	if len(wanted) == 0 {
		return nil
	}

	memo, err := app.FindRecordById("memos", memoID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, "find memo: "+err.Error())
	}
	memoAuthorID := memo.GetString("author")
	actorIsAdmin := false
	if viewer := CurrentUser(ctx); viewer != nil {
		actorIsAdmin = viewer.IsSuperuser()
	}

	type resolved struct {
		id      string
		creator string
		owner   string
	}
	wantedIDs := make([]resolved, 0, len(wanted))
	seen := make(map[string]struct{}, len(wanted))
	for _, att := range wanted {
		_, rec, err := findAttachmentRecord(app, att.GetName())
		if err != nil {
			return connect.NewError(connect.CodeNotFound, "attachment not found: "+att.GetName())
		}
		ownerMemo := rec.GetString("memo")
		if ownerMemo != "" && ownerMemo != memoID {
			// Upstream answers FailedPrecondition here
			// ("attachment %s is already bound to another memo"). The
			// row is the caller's to see, so this does not leak a
			// foreign name: INVALID_ARGUMENT carries the same
			// "you asked for something impossible" reading on the
			// wire, and riff's connect surface has no
			// FailedPrecondition-shaped precedent.
			return connect.NewError(connect.CodeInvalidArgument,
				"attachment belongs to a different memo: "+att.GetName())
		}
		if _, dup := seen[rec.Id]; dup {
			continue
		}
		seen[rec.Id] = struct{}{}
		wantedIDs = append(wantedIDs, resolved{
			id:      rec.Id,
			creator: rec.GetString("creator"),
			owner:   ownerMemo,
		})
	}

	if err := app.RunInTransaction(func(txApp core.App) error {
		coll, err := txApp.FindCollectionByNameOrId("attachments")
		if err != nil {
			return connect.NewError(connect.CodeInternal, err.Error())
		}
		for _, w := range wantedIDs {
			rec, err := txApp.FindRecordById(coll, w.id)
			if err != nil {
				return connect.NewError(connect.CodeInternal, "find attachment: "+err.Error())
			}
			if rec.GetString("memo") == memoID {
				continue
			}
			// Ownership gate (upstream `MemoAttachmentBindingOwnerAllowed`):
			// an unbound attachment may only be bound to a memo whose author
			// is the uploader, or by an instance administrator for their *own*
			// uploads. Note the comparison is against the MEMO AUTHOR, not the
			// acting user — upstream deliberately lets an admin attach a
			// colleague's upload to the admin's own memo, but never lets
			// ownership transfer between accounts.
			//
			// Rows already bound to this memo are the no-op fast path above;
			// rows bound to a different memo never reach here (filtered out
			// in the resolve pass).
			if w.owner == "" && !memoAttachmentBindingOwnerAllowed(w.creator, memoAuthorID, viewerID, actorIsAdmin) {
				// NOT_FOUND, matching upstream: the name may be real but
				// foreign, and confirming that is an existence oracle for
				// another user's upload.
				return connect.NewError(connect.CodeNotFound,
					"attachment not found: attachments/"+w.id)
			}
			rec.Set("memo", memoID)
			if err := updateRecordRequest(ctx, txApp, rec); err != nil {
				return connect.NewError(connect.CodeInvalidArgument, err.Error())
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// memoAttachmentBindingOwnerAllowed reports whether an unbound attachment may
// be bound to a memo. It is a direct port of upstream's
// `store.MemoAttachmentBindingOwnerAllowed`: the attachment must be owned by
// the memo's author, or by the acting instance administrator. Ownership is
// never transferred between accounts.
//
// Note what the admin branch does and does not allow. It reads
// `attachmentCreatorID == actorUserID`, so it permits an administrator to
// attach **their own** upload to a memo somebody else authored. It does NOT
// permit moving a colleague's upload onto any memo — the first clause already
// requires the uploader to be the memo author, and the fallback re-anchors on
// the uploader being the actor.
//
// An empty `attachmentCreatorID` (a row written before the `creator` column
// existed, so the uploader was never recorded) is allowed through: the row is
// already reachable only through a rule that resolved it to this caller, and
// refusing it would strand uploads taken before the orphan-bind work landed.
func memoAttachmentBindingOwnerAllowed(attachmentCreatorID, memoAuthorID, actorUserID string, actorIsAdmin bool) bool {
	if attachmentCreatorID == "" {
		return true
	}
	if attachmentCreatorID == memoAuthorID {
		return true
	}
	return actorIsAdmin && attachmentCreatorID == actorUserID
}

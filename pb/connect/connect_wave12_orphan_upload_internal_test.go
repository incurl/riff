package connect

import (
	"bytes"
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/hooks"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// waitForMotionMediaWorkers blocks until every in-flight
// motion-media detection goroutine for `app` has finished.
// The hook fires detection as a detached goroutine
// (`pb/hooks/motion_media.go`); without this sync, the test's
// `rec.GetString(...)` reads race against the hook's
// `rec.Set("motion_media", ...)` write under `-race` — the
// detector touches the same row the test inspects.
func waitForMotionMediaWorkers(t *testing.T, app core.App) {
	t.Helper()
	hooks.WaitMotionMediaWorkers(app)
}

// orphanUploadSpecRequest builds a spec with `attachment.memo` empty
// (an "orphan" upload). The server used to reject this with
// `INVALID_ARGUMENT: memo is required for upload`; the gate now
// accepts it and writes the row with `attachments.memo` empty and
// a `creator` column stamped with the uploader. The SPA flows this
// shape from the inline-image and end-of-edit drag flows.
func orphanUploadSpecRequest(filename, mime string, totalSize int64) *v1.UploadAttachmentRequest {
	return &v1.UploadAttachmentRequest{
		Upload: &v1.UploadAttachmentRequest_Spec{
			Spec: &v1.UploadAttachmentSpec{
				Attachment: &v1.Attachment{
					Filename: filename,
					Type:     mime,
				},
				TotalSize: totalSize,
			},
		},
	}
}

// TestUploadAttachment_OrphanSpecSucceeds pins the relaxed server
// gate: a spec without `attachment.memo` is accepted, the row
// lands with the `memo` column empty, and the `creator` column is
// stamped with the caller's user-id. Without this shape the SPA's
// "upload files, then create memo" path fails with
// `INVALID_ARGUMENT` and the user sees a memo post whose
// attachment vanishes. The bind pass in
// `bindOrphanAttachmentsToMemo` runs inside the subsequent
// `CreateMemo` and re-binds the row to the new memo.
func TestUploadAttachment_OrphanSpecSucceeds(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	payload := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 32)
	spec := orphanUploadSpecRequest("kick.po33", "application/octet-stream", int64(len(payload)))
	resp, err := srv.UploadAttachment(ctx, spec)
	require.NoError(t, err, "an orphan spec must be accepted")
	require.NotEmpty(t, resp.GetUploadId())

	chunk := payload[:10]
	resp, err = srv.UploadAttachment(ctx, uploadContinueRequest(resp.GetUploadId(), 0, chunk, false))
	require.NoError(t, err)
	require.Equal(t, int64(10), resp.GetCommittedSize())

	resp, err = srv.UploadAttachment(ctx, uploadContinueRequest(resp.GetUploadId(), int64(len(chunk)), payload[len(chunk):], true))
	require.NoError(t, err)
	require.NotNil(t, resp.GetAttachment(), "finishWrite must return the attachment proto")

	waitForMotionMediaWorkers(t, app)

	patchID := resp.GetAttachment().GetName()
	require.NotEmpty(t, patchID)
	rec, err := app.FindRecordById("attachments", patchID[len("attachments/"):])
	require.NoError(t, err)
	assert.Empty(t, rec.GetString("memo"), "orphan row's memo column must be empty after finishWrite")
	assert.Equal(t, alice.Id, rec.GetString("creator"), "creator column must be stamped with the uploader id")
}

// TestCreateMemo_BindsOrphanAttachments pins the headline fix:
// when the SPA sends a `CreateMemo` whose
// `req.Memo.Attachments[]` carries names of orphan uploads from the
// same user, the server rewires the rows' `memo` column inside the
// same RPC. The user-facing symptom of the missing bind is a memo
// post whose attached file URL 404s — the row exists but has no
// `memo`, so the rule engine hides it from every viewer and the
// attachment storage path resolves the URL through the SPA's catch
// to `index.html`.
func TestCreateMemo_BindsOrphanAttachments(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")

	srv := NewAttachmentServer(app)
	ctx := withViewer(context.Background(), alice)
	payload := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 32)
	spec := orphanUploadSpecRequest("kick.po33", "application/octet-stream", int64(len(payload)))
	specResp, err := srv.UploadAttachment(ctx, spec)
	require.NoError(t, err)
	finishResp, err := srv.UploadAttachment(ctx, uploadContinueRequest(specResp.GetUploadId(), 0, payload, true))
	require.NoError(t, err)
	orphanName := finishResp.GetAttachment().GetName()

	memos := NewMemoServer(app)
	created, err := memos.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{
			Content:     "hi",
			Visibility:  v1.Visibility_PUBLIC,
			Attachments: []*v1.Attachment{{Name: orphanName}},
		},
	})
	require.NoError(t, err)

	waitForMotionMediaWorkers(t, app)

	rec, err := app.FindRecordById("attachments", orphanName[len("attachments/"):])
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())
	assert.Equal(t, memoID, rec.GetString("memo"), "CreateMemo must bind the orphan to the new memo in the same RPC")
}

// TestCreateMemo_RejectsForeignOrphan pins the cross-user rebind
// defence: bob cannot re-bind alice's orphan onto bob's own memo,
// even though bob knows the attachment name. Without the
// `creator` gate, a hostile SPA that ever learnt an attachment id
// could rebind the row onto its own memo and effectively steal the
// bytes (the orphan row's `attachments.memo` is empty so the
// visibility rule resolves to NULL for everyone, and the rule
// engine cannot disambiguate the uploader).
func TestCreateMemo_RejectsForeignOrphan(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")

	srv := NewAttachmentServer(app)
	payload := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 32)
	specResp, err := srv.UploadAttachment(withViewer(context.Background(), alice),
		orphanUploadSpecRequest("kick.po33", "application/octet-stream", int64(len(payload))))
	require.NoError(t, err)
	finishResp, err := srv.UploadAttachment(withViewer(context.Background(), alice),
		uploadContinueRequest(specResp.GetUploadId(), 0, payload, true))
	require.NoError(t, err)
	orphanName := finishResp.GetAttachment().GetName()

	memos := NewMemoServer(app)
	_, err = memos.CreateMemo(withViewer(context.Background(), bob), &v1.CreateMemoRequest{
		Memo: &v1.Memo{
			Content:     "trying to steal",
			Visibility:  v1.Visibility_PUBLIC,
			Attachments: []*v1.Attachment{{Name: orphanName}},
		},
	})
	require.Error(t, err, "bob must not be able to bind alice's orphan to his own memo")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"upstream answers NOT_FOUND here, not PERMISSION_DENIED: the name may be real but foreign, and confirming that is an existence oracle for another user's upload")

	waitForMotionMediaWorkers(t, app)

	rec, err := app.FindRecordById("attachments", orphanName[len("attachments/"):])
	require.NoError(t, err)
	assert.Empty(t, rec.GetString("memo"), "the rejected bind must leave the orphan row untouched")
}

// TestCreateMemo_RejectsForeignMemo guards the cross-memo
// rejection on the existing-memo branch: an attachment that
// already belongs to memo A cannot be rebound onto memo B by
// `CreateMemo({attachments: [...]})` — matches the
// `SetMemoAttachments` policy.
func TestCreateMemo_RejectsForeignMemo(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	ctx := withViewer(context.Background(), alice)

	memos := NewMemoServer(app)
	memoA, err := memos.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "memoA", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoB, err := memos.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "memoB", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)

	srv := NewAttachmentServer(app)
	payload := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 32)
	specResp, err := srv.UploadAttachment(ctx,
		uploadSpecRequest(memoA.GetName(), "kick.po33", "application/octet-stream", int64(len(payload))))
	require.NoError(t, err)
	finishResp, err := srv.UploadAttachment(ctx,
		uploadContinueRequest(specResp.GetUploadId(), 0, payload, true))
	require.NoError(t, err)
	attachedName := finishResp.GetAttachment().GetName()

	_, err = memos.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{
			Content:     "trying to steal",
			Visibility:  v1.Visibility_PUBLIC,
			Attachments: []*v1.Attachment{{Name: attachedName}},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "cross-memo rebind must be rejected as INVALID_ARGUMENT")

	waitForMotionMediaWorkers(t, app)

	rec, err := app.FindRecordById("attachments", attachedName[len("attachments/"):])
	require.NoError(t, err)
	assert.Equal(t, memoIDFromName(memoA.GetName()), rec.GetString("memo"),
		"the rejected rebind must leave the row attached to its original memo")

	_ = memoB
}

// TestCreateMemoComment_BindsOrphanAttachments pins the
// `CreateMemoComment` mirror of the orphan-bind pass: a reply that
// includes attachment names of the caller's own orphan uploads
// rebinds the rows into the same RPC. Without this branch the
// reply's attachments silently 404 — same symptom as the create
// path, but on the comment creation surface.
func TestCreateMemoComment_BindsOrphanAttachments(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	ctx := withViewer(context.Background(), alice)

	// Build a parent memo via the production handler so visibility
	// rules apply normally; replies can never be more visible than
	// the parent, so PUBLIC is the test-friendly visibility.
	memos := NewMemoServer(app)
	parent, err := memos.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "parent", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)

	srv := NewAttachmentServer(app)
	payload := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 32)
	specResp, err := srv.UploadAttachment(ctx,
		orphanUploadSpecRequest("kick.po33", "application/octet-stream", int64(len(payload))))
	require.NoError(t, err)
	finishResp, err := srv.UploadAttachment(ctx,
		uploadContinueRequest(specResp.GetUploadId(), 0, payload, true))
	require.NoError(t, err)
	orphanName := finishResp.GetAttachment().GetName()

	comment, err := memos.CreateMemoComment(ctx, &v1.CreateMemoCommentRequest{
		Name: parent.GetName(),
		Comment: &v1.Memo{
			Content:     "with attachment",
			Attachments: []*v1.Attachment{{Name: orphanName}},
		},
	})
	require.NoError(t, err)

	waitForMotionMediaWorkers(t, app)

	rec, err := app.FindRecordById("attachments", orphanName[len("attachments/"):])
	require.NoError(t, err)
	assert.Equal(t, memoIDFromName(comment.GetName()), rec.GetString("memo"),
		"CreateMemoComment must rebind the orphan to the reply in the same RPC")
}

// TestMemoAttachmentBindingOwnerAllowed pins the ownership gate against
// upstream's `store.MemoAttachmentBindingOwnerAllowed`
// (`/home/peter/github.com/memos/store/memo_attachment.go`) case by case.
//
// The comparison is uploader-vs-MEMO-AUTHOR, not uploader-vs-actor. That
// distinction is the whole point of the function: an administrator may bind
// a colleague's upload onto their own memo, but nobody may transfer
// ownership between two non-admin accounts.
func TestMemoAttachmentBindingOwnerAllowed(t *testing.T) {
	tests := []struct {
		name        string
		creator     string
		memoAuthor  string
		actor       string
		actorIsAmin bool
		want        bool
	}{
		{
			name:       "uploader owns the memo",
			creator:    "alice",
			memoAuthor: "alice",
			actor:      "alice",
			want:       true,
		},
		{
			name:       "different user owns the memo",
			creator:    "alice",
			memoAuthor: "bob",
			actor:      "bob",
			want:       false,
		},
		{
			name:        "admin binds their OWN upload onto someone else's memo",
			creator:     "root",
			memoAuthor:  "alice",
			actor:       "root",
			actorIsAmin: true,
			want:        true,
		},
		{
			name:        "admin cannot bind a colleague upload onto any memo",
			creator:     "alice",
			memoAuthor:  "bob",
			actor:       "root",
			actorIsAmin: true,
			want:        false,
		},
		{
			name:        "admin binding a colleague upload onto their OWN memo is still refused",
			creator:     "alice",
			memoAuthor:  "root",
			actor:       "root",
			actorIsAmin: true,
			want:        false,
		},
		{
			name:        "non-admin cannot bind a colleague upload even as the memo author",
			creator:     "alice",
			memoAuthor:  "bob",
			actor:       "bob",
			actorIsAmin: false,
			want:        false,
		},
		{
			name:       "legacy row with no recorded uploader is allowed through",
			creator:    "",
			memoAuthor: "bob",
			actor:      "bob",
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := memoAttachmentBindingOwnerAllowed(
				tt.creator, tt.memoAuthor, tt.actor, tt.actorIsAmin)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestCreateMemo_CannotStealAColleagueUpload pins the reachable denial
// for the ownership gate: a user who did not upload an orphan cannot bind it
// to their own memo.
//
// The gate is upstream's `MemoAttachmentBindingOwnerAllowed` — uploader must
// equal the memo author, with a fallback for an acting administrator. riff's
// `actorIsAdmin` branch is currently UNREACHABLE, because a memo's author is
// force-stamped from `e.Auth.Id` (`pb/hooks/memos.go`) against a `users`
// relation, so a `_superusers` record can neither author a memo nor pass
// `UpdateMemo`'s author check. The admin clause is ported for parity and
// guarded by `TestMemoAttachmentBindingOwnerAllowed`; this test pins the
// branch that real callers can actually reach.
//
// Before the gate was ported, riff compared the uploader against the *acting*
// user instead of the *memo author* and answered PERMISSION_DENIED — an
// existence oracle confirming a foreign attachment name resolves. The gate now
// answers NOT_FOUND.
func TestCreateMemo_CannotStealAColleagueUpload(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")

	srv := NewAttachmentServer(app)
	payload := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 32)
	specResp, err := srv.UploadAttachment(withViewer(context.Background(), alice),
		orphanUploadSpecRequest("kick.po33", "application/octet-stream", int64(len(payload))))
	require.NoError(t, err)
	finishResp, err := srv.UploadAttachment(withViewer(context.Background(), alice),
		uploadContinueRequest(specResp.GetUploadId(), 0, payload, true))
	require.NoError(t, err)
	orphanName := finishResp.GetAttachment().GetName()

	memos := NewMemoServer(app)
	_, err = memos.CreateMemo(withViewer(context.Background(), bob), &v1.CreateMemoRequest{
		Memo: &v1.Memo{
			Content:     "not mine",
			Visibility:  v1.Visibility_PUBLIC,
			Attachments: []*v1.Attachment{{Name: orphanName}},
		},
	})
	require.Error(t, err,
		"bob must not be able to move alice's upload onto bob's memo")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"the denial must be NOT_FOUND so the response does not confirm that a foreign attachment name exists")

	waitForMotionMediaWorkers(t, app)

	rec, err := app.FindRecordById("attachments", orphanName[len("attachments/"):])
	require.NoError(t, err)
	assert.Empty(t, rec.GetString("memo"),
		"the rejected bind must leave the orphan row untouched")
}

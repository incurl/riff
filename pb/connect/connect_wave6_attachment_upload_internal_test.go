// Wave 6 Slice 2 tests. These tests pin the chunked
// UploadAttachment RPC against a real PocketBase app;
// the manager is package-level and lives across tests,
// so each test resets its stage dir under the per-test
// temp data dir and clears any in-flight sessions.
package connect

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// resetUploadManagerForTest points the package-level
// upload manager at the test's per-app data dir and
// reaps every live session. The function is wrapped in
// a mutex so the parallel test runner cannot interleave
// two resets and leave a stale map behind.
var resetUploadManagerMu sync.Mutex

func resetUploadManagerForTest(t *testing.T, app core.App) {
	t.Helper()
	resetUploadManagerMu.Lock()
	defer resetUploadManagerMu.Unlock()
	mgr := uploadManagerGet()
	mgr.setDir(app.DataDir() + "/uploads")
	mgr.mu.Lock()
	for id, s := range mgr.sessions {
		s.closeAndCleanup()
		delete(mgr.sessions, id)
	}
	mgr.mu.Unlock()
}

// uploadSpecRequest is a small helper that builds a
// spec request with sensible defaults. Tests that need
// to omit a field pass an explicit zero.
func uploadSpecRequest(memo, filename, mime string, totalSize int64) *v1.UploadAttachmentRequest {
	return &v1.UploadAttachmentRequest{
		Upload: &v1.UploadAttachmentRequest_Spec{
			Spec: &v1.UploadAttachmentSpec{
				Attachment: &v1.Attachment{
					Filename: filename,
					Type:     mime,
					Memo:     &memo,
				},
				TotalSize: totalSize,
			},
		},
	}
}

func uploadContinueRequest(uploadID string, offset int64, data []byte, finish bool) *v1.UploadAttachmentRequest {
	return &v1.UploadAttachmentRequest{
		Upload:      &v1.UploadAttachmentRequest_UploadId{UploadId: uploadID},
		WriteOffset: offset,
		Data:        data,
		FinishWrite: finish,
	}
}

// seedUploadMemo creates a public memo and returns
// the canonical "memos/{id}" name the spec handler
// expects.
func seedUploadMemo(t *testing.T, app core.App, author *core.Record) string {
	t.Helper()
	memos := NewMemoServer(app)
	created, err := memos.CreateMemo(withViewer(context.Background(), author), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "upload test", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	return created.GetName()
}

// TestUploadAttachment_RequiresAuth asserts anonymous
// callers are rejected. A spec with no viewer should
// be CodeUnauthenticated, not Internal.
func TestUploadAttachment_RequiresAuth(t *testing.T) {
	app := newTestApp(t)
	resetUploadManagerForTest(t, app)

	srv := NewAttachmentServer(app)
	_, err := srv.UploadAttachment(context.Background(), uploadSpecRequest("memos/abc", "f.bin", "application/octet-stream", 0))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestUploadAttachment_SpecRejectsMissingFields is a
// belt-and-braces guard: every required field on the
// spec must produce CodeInvalidArgument before any
// stage file is opened.
func TestUploadAttachment_SpecRejectsMissingFields(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	cases := []struct {
		name string
		req  *v1.UploadAttachmentRequest
	}{
		{
			name: "no filename",
			req:  uploadSpecRequest(memoName, "", "application/octet-stream", 0),
		},
		{
			name: "no type",
			req:  uploadSpecRequest(memoName, "kick.po33", "", 0),
		},
		{
			name: "no memo",
			req:  uploadSpecRequest("", "kick.po33", "application/octet-stream", 0),
		},
		{
			name: "content in spec",
			req: &v1.UploadAttachmentRequest{
				Upload: &v1.UploadAttachmentRequest_Spec{
					Spec: &v1.UploadAttachmentSpec{
						Attachment: &v1.Attachment{
							Filename: "kick.po33",
							Type:     "application/octet-stream",
							Memo:     &memoName,
							Content:  []byte("hello"),
						},
					},
				},
			},
		},
		{
			name: "malformed memo",
			req:  uploadSpecRequest("not-a-memo", "kick.po33", "application/octet-stream", 0),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.UploadAttachment(ctx, tc.req)
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

// TestUploadAttachment_SpecRejectsMimeType verifies the
// unified `attachments` collection's MIME allow-list.
// The collection accepts a wide range of media types
// (image/png, audio/wav, application/zip, ...). A
// spec claiming application/x-msdownload (a Windows
// executable) is outside the allow-list, so the call
// must fail.
func TestUploadAttachment_SpecRejectsMimeType(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	_, err := srv.UploadAttachment(ctx, uploadSpecRequest(memoName, "evil.exe", "application/x-msdownload", 10))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestUploadAttachment_SpecRejectsOversize guards the
// 64 MiB hard cap. The patches field-level cap is
// 8 MB, but the upload path enforces the higher
// value at the spec stage so the bytes are never
// staged for an upload that will eventually fail
// after the user has waited for them to land.
func TestUploadAttachment_SpecRejectsOversize(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	_, err := srv.UploadAttachment(ctx, uploadSpecRequest(memoName, "huge.bin", "application/octet-stream", uploadMaxTotalSize+1))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}

// TestUploadAttachment_HappyPath is the canonical
// spec -> write -> write -> finish flow. Each chunk
// is the size the SPA sends (well under the 2 MiB
// server cap), the final response carries the
// committed size and the created attachment, and
// the patches row's `file` field has the same bytes
// the client wrote.
func TestUploadAttachment_HappyPath(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	// The `attachments` collection's `blob` FileField
	// validator accepts a wide range of file types.
	// We fill the payload with control bytes so the
	// detector lands on octet-stream.
	payload := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 64)
	spec := uploadSpecRequest(memoName, "kick.po33", "application/octet-stream", int64(len(payload)))
	resp, err := srv.UploadAttachment(ctx, spec)
	require.NoError(t, err)
	require.Equal(t, int32(uploadMaxChunkSize), resp.GetMaxChunkSize())
	require.Equal(t, int64(0), resp.GetCommittedSize())
	require.NotEmpty(t, resp.GetUploadId())
	require.Nil(t, resp.GetAttachment())

	chunk1 := payload[:10]
	resp, err = srv.UploadAttachment(ctx, uploadContinueRequest(resp.GetUploadId(), 0, chunk1, false))
	require.NoError(t, err)
	require.Equal(t, int64(10), resp.GetCommittedSize())
	require.Nil(t, resp.GetAttachment())

	chunk2 := payload[10:]
	resp, err = srv.UploadAttachment(ctx, uploadContinueRequest(resp.GetUploadId(), int64(len(chunk1)), chunk2, true))
	require.NoError(t, err)
	require.Equal(t, int64(len(payload)), resp.GetCommittedSize())
	require.NotNil(t, resp.GetAttachment())
	assert.Equal(t, "application/octet-stream", resp.GetAttachment().GetType())

	// The row exists and the file bytes match.
	patchID := strings.TrimPrefix(resp.GetAttachment().GetName(), "attachments/")
	rec, err := app.FindRecordById("attachments", patchID)
	require.NoError(t, err)
	fsys, err := app.NewFilesystem()
	require.NoError(t, err)
	r, err := fsys.GetReader(fmt.Sprintf("%s/%s/%s", rec.Collection().Id, rec.Id, rec.GetString("filename")))
	require.NoError(t, err)
	defer r.Close()
	got := make([]byte, len(payload))
	n, err := r.Read(got)
	require.NoError(t, err)
	require.Equal(t, len(payload), n, "read returned %d bytes, expected %d", n, len(payload))
	assert.Equal(t, payload, got[:n])
}

// TestUploadAttachment_SingleCall covers the SPA's
// edge case: a tiny upload (a favicon, an emoji)
// can carry the data in the spec call itself.
func TestUploadAttachment_SingleCall(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	// Use binary bytes so the mime detector accepts
	// the file as application/octet-stream.
	payload := []byte{0x00, 0x01, 0x02, 0x03}
	resp, err := srv.UploadAttachment(ctx, &v1.UploadAttachmentRequest{
		Upload: &v1.UploadAttachmentRequest_Spec{
			Spec: &v1.UploadAttachmentSpec{
				Attachment: &v1.Attachment{
					Filename: "tiny.po33",
					Type:     "application/octet-stream",
					Memo:     &memoName,
				},
				TotalSize: int64(len(payload)),
			},
		},
		WriteOffset: 0,
		Data:        payload,
		FinishWrite: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetAttachment())
	assert.Equal(t, int64(len(payload)), resp.GetCommittedSize())
}

// TestUploadAttachment_RejectsWrongOffset is the
// canonical "I lost the previous response and now
// I have the wrong offset" case. The handler must
// refuse to write at an offset that does not match
// the committed size.
func TestUploadAttachment_RejectsWrongOffset(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	spec := uploadSpecRequest(memoName, "kick.po33", "application/octet-stream", 100)
	resp, err := srv.UploadAttachment(ctx, spec)
	require.NoError(t, err)
	uid := resp.GetUploadId()

	// Skip offset 0 and try to write at 5.
	_, err = srv.UploadAttachment(ctx, uploadContinueRequest(uid, 5, []byte("12345"), false))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestUploadAttachment_RejectsOversizeChunk guards
// the 2 MiB chunk cap. The SPA caps its own chunks
// at the server-reported value, but a misbehaving
// client could try to send a 4 MiB block.
func TestUploadAttachment_RejectsOversizeChunk(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	spec := uploadSpecRequest(memoName, "big.po33", "application/octet-stream", int64(uploadMaxChunkSize*3))
	resp, err := srv.UploadAttachment(ctx, spec)
	require.NoError(t, err)
	uid := resp.GetUploadId()

	big := bytes.Repeat([]byte{'a'}, uploadMaxChunkSize+1)
	_, err = srv.UploadAttachment(ctx, uploadContinueRequest(uid, 0, big, false))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestUploadAttachment_RejectsCrossUser hides the
// existence of another user's session behind a
// NotFound. A permission error would leak the
// upload_id's existence to a guessing attacker.
func TestUploadAttachment_RejectsCrossUser(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	memoName := seedUploadMemo(t, app, alice)
	aliceCtx := withViewer(context.Background(), alice)
	bobCtx := withViewer(context.Background(), bob)

	srv := NewAttachmentServer(app)
	resp, err := srv.UploadAttachment(aliceCtx, uploadSpecRequest(memoName, "kick.po33", "application/octet-stream", 5))
	require.NoError(t, err)
	uid := resp.GetUploadId()

	_, err = srv.UploadAttachment(bobCtx, uploadContinueRequest(uid, 0, []byte("hi"), false))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestUploadAttachment_IdempotentFinish is the
// "I sent finishWrite but I lost the response"
// case. The proto guarantees that a retried
// finishWrite returns the same attachment.
func TestUploadAttachment_IdempotentFinish(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	payload := []byte{0x00, 0x01, 0x02, 0x03}
	spec := uploadSpecRequest(memoName, "kick.po33", "application/octet-stream", int64(len(payload)))
	resp, err := srv.UploadAttachment(ctx, spec)
	require.NoError(t, err)
	uid := resp.GetUploadId()

	// First finish.
	resp, err = srv.UploadAttachment(ctx, uploadContinueRequest(uid, 0, payload, true))
	require.NoError(t, err)
	require.NotNil(t, resp.GetAttachment())
	firstName := resp.GetAttachment().GetName()

	// Second finish with identical bytes and offset.
	resp, err = srv.UploadAttachment(ctx, uploadContinueRequest(uid, 0, payload, true))
	require.NoError(t, err)
	require.NotNil(t, resp.GetAttachment())
	assert.Equal(t, firstName, resp.GetAttachment().GetName())
}

// TestUploadAttachment_CrossUserHidden covers the
// corner case where the upload_id was not even
// minted by anyone (a random guess).
func TestUploadAttachment_UnknownUploadID(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	_, err := srv.UploadAttachment(ctx, uploadContinueRequest("not-a-real-id", 0, []byte("x"), false))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestUploadAttachment_MediaMetadataRoundTrip pins
// the spec's `media_metadata` round-trip: the value
// the SPA extracts at file-ingest time must
// survive the chunked upload and be returned in
// the proto response. The proto's MediaMetadata
// is oneof (photo | video); a `photo` variant is
// the common case.
func TestUploadAttachment_MediaMetadataRoundTrip(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	w, h := int32(640), int32(480)
	md := &v1.MediaMetadata{
		Width:  &w,
		Height: &h,
		Details: &v1.MediaMetadata_Photo{
			Photo: &v1.PhotoMetadata{
				CameraMake:  "TestCam",
				CameraModel: "Model X",
			},
		},
	}
	payload := []byte{0x00, 0x01, 0x02, 0x03}
	spec := &v1.UploadAttachmentRequest{
		Upload: &v1.UploadAttachmentRequest_Spec{
			Spec: &v1.UploadAttachmentSpec{
				Attachment: &v1.Attachment{
					Filename:      "kick.po33",
					Type:          "application/octet-stream",
					Memo:          &memoName,
					MediaMetadata: md,
				},
				TotalSize: int64(len(payload)),
			},
		},
		Data:        payload,
		FinishWrite: true,
	}
	resp, err := srv.UploadAttachment(ctx, spec)
	require.NoError(t, err)
	require.NotNil(t, resp.GetAttachment())
	got := resp.GetAttachment().GetMediaMetadata()
	require.NotNil(t, got, "media_metadata must round-trip")
	assert.Equal(t, int32(640), got.GetWidth())
	assert.Equal(t, int32(480), got.GetHeight())
	photo := got.GetPhoto()
	require.NotNil(t, photo, "photo details must round-trip")
	assert.Equal(t, "TestCam", photo.GetCameraMake())
}

// TestUploadAttachment_MotionMediaRoundTrip pins
// the motion_media spec field. The SPA sets it
// when the file is detected as a Motion Photo at
// ingest time; the server must persist it and
// return it on the final response.
func TestUploadAttachment_MotionMediaRoundTrip(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	mm := &v1.MotionMedia{
		Family:                  v1.MotionMediaFamily_ANDROID_MOTION_PHOTO,
		Role:                    v1.MotionMediaRole_CONTAINER,
		GroupId:                 "grp-1",
		PresentationTimestampUs: 12345,
		HasEmbeddedVideo:        true,
	}
	payload := []byte{0x00, 0x01, 0x02, 0x03}
	spec := &v1.UploadAttachmentRequest{
		Upload: &v1.UploadAttachmentRequest_Spec{
			Spec: &v1.UploadAttachmentSpec{
				Attachment: &v1.Attachment{
					Filename:    "photo.po33",
					Type:        "application/octet-stream",
					Memo:        &memoName,
					MotionMedia: mm,
				},
				TotalSize: int64(len(payload)),
			},
		},
		Data:        payload,
		FinishWrite: true,
	}
	resp, err := srv.UploadAttachment(ctx, spec)
	require.NoError(t, err)
	require.NotNil(t, resp.GetAttachment())
	got := resp.GetAttachment().GetMotionMedia()
	require.NotNil(t, got, "motion_media must round-trip")
	assert.Equal(t, v1.MotionMediaFamily_ANDROID_MOTION_PHOTO, got.GetFamily())
	assert.Equal(t, "grp-1", got.GetGroupId())
	assert.Equal(t, int64(12345), got.GetPresentationTimestampUs())
	assert.True(t, got.GetHasEmbeddedVideo())
}

// TestUploadAttachment_PerUserUploadCap guards the
// in-flight cap. A user with N-1 unfinished uploads
// can start the Nth; the N+1th is rejected.
func TestUploadAttachment_PerUserUploadCap(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	// Fill the cap with N unfinished sessions.
	for i := 0; i < uploadMaxUnfinishedPerUser; i++ {
		_, err := srv.UploadAttachment(ctx, uploadSpecRequest(memoName, fmt.Sprintf("a%d.po33", i), "application/octet-stream", 1))
		require.NoError(t, err, "upload %d should succeed", i)
	}
	// The N+1th call is rejected.
	_, err := srv.UploadAttachment(ctx, uploadSpecRequest(memoName, "overflow.po33", "application/octet-stream", 1))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}

// TestUploadAttachment_RejectsSpuriousFinishWithoutBytes
// pins the "finishWrite with no data but the spec
// said total_size > 0" case. The handler must
// refuse; otherwise a client could finalise an
// empty record by mistake.
func TestUploadAttachment_RejectsSpuriousFinishWithoutBytes(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	spec := uploadSpecRequest(memoName, "kick.po33", "application/octet-stream", 10)
	resp, err := srv.UploadAttachment(ctx, spec)
	require.NoError(t, err)
	uid := resp.GetUploadId()

	// Send a finish with no data and total_size=10.
	_, err = srv.UploadAttachment(ctx, uploadContinueRequest(uid, 0, nil, true))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestUploadAttachment_SampleAudio lands an audio file
// in the unified `attachments` collection. The proto has
// no collection discriminator, so the file just lands in
// `attachments` regardless of MIME type.
func TestUploadAttachment_SampleAudio(t *testing.T) {
	app := newHookedApp(t)
	resetUploadManagerForTest(t, app)
	alice := newUser(t, app, "alice")
	memoName := seedUploadMemo(t, app, alice)
	ctx := withViewer(context.Background(), alice)

	srv := NewAttachmentServer(app)
	payload := []byte("RIFF\x00\x00\x00\x00WAVEfmt ")
	spec := uploadSpecRequest(memoName, "kit.wav", "audio/wav", int64(len(payload)))
	resp, err := srv.UploadAttachment(ctx, &v1.UploadAttachmentRequest{
		Upload:      &v1.UploadAttachmentRequest_Spec{Spec: spec.GetUpload().(*v1.UploadAttachmentRequest_Spec).Spec},
		Data:        payload,
		FinishWrite: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetAttachment())
	assert.Equal(t, "audio/wav", resp.GetAttachment().GetType())
	id := resp.GetAttachment().GetName()[len("attachments/"):]
	rec, err := app.FindRecordById("attachments", id)
	require.NoError(t, err)
	assert.Equal(t, "kit.wav", rec.GetString("filename"))
}

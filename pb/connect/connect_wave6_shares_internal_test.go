package connect

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// Phase 4 Wave 6 — tests for the MemoShare RPCs. The
// handlers are methods on MemoServer, so this file lives
// next to memo_shares.go.

// TestCreateMemoShare_AuthorOnly covers the create-authorisation
// path: only the memo's author can mint a share link. A second
// user who can read the memo but did not write it gets
// CodePermissionDenied, and an unauthenticated caller gets
// CodeUnauthenticated.
func TestCreateMemoShare_AuthorOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	// Bob is denied.
	_, err = srv.CreateMemoShare(withViewer(context.Background(), bob), &v1.CreateMemoShareRequest{
		Parent:    "memos/" + memoID,
		MemoShare: &v1.MemoShare{},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// An anonymous caller is also denied (auth required).
	_, err = srv.CreateMemoShare(context.Background(), &v1.CreateMemoShareRequest{
		Parent:    "memos/" + memoID,
		MemoShare: &v1.MemoShare{},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// Alice is allowed.
	out, err := srv.CreateMemoShare(withViewer(context.Background(), alice), &v1.CreateMemoShareRequest{
		Parent:    "memos/" + memoID,
		MemoShare: &v1.MemoShare{},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.GetName())
	assert.NotNil(t, out.GetCreateTime())
	memoIDOut, code, ok := splitMemoShareName(out.GetName())
	require.True(t, ok)
	assert.Equal(t, memoID, memoIDOut)
	assert.Len(t, code, shareCodeLength)
}

// TestCreateMemoShare_GeneratesUniqueCode checks that 25
// successive creates produce 25 distinct codes.
func TestCreateMemoShare_GeneratesUniqueCode(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	const n = 25
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		out, err := srv.CreateMemoShare(withViewer(context.Background(), alice), &v1.CreateMemoShareRequest{
			Parent:    "memos/" + memoID,
			MemoShare: &v1.MemoShare{},
		})
		require.NoError(t, err)
		_, code, ok := splitMemoShareName(out.GetName())
		require.True(t, ok)
		require.Len(t, code, shareCodeLength)
		seen[code] = struct{}{}
	}
	assert.Len(t, seen, n, "every generated code must be unique")
}

// TestListMemoShares_AuthorSeesAll asserts the list path
// returns every share row for a memo.
func TestListMemoShares_AuthorSeesAll(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	const n = 3
	for i := 0; i < n; i++ {
		_, err := srv.CreateMemoShare(withViewer(context.Background(), alice), &v1.CreateMemoShareRequest{
			Parent:    "memos/" + memoID,
			MemoShare: &v1.MemoShare{},
		})
		require.NoError(t, err)
	}
	list, err := srv.ListMemoShares(withViewer(context.Background(), alice), &v1.ListMemoSharesRequest{
		Parent: "memos/" + memoID,
	})
	require.NoError(t, err)
	assert.Len(t, list.GetMemoShares(), n)
}

// TestListMemoShares_NonAuthorDenied covers the read gate.
func TestListMemoShares_NonAuthorDenied(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	_, err = srv.ListMemoShares(withViewer(context.Background(), bob), &v1.ListMemoSharesRequest{
		Parent: "memos/" + memoID,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestDeleteMemoShare_AuthorCanRevoke asserts a successful
// delete removes the row, a non-author is denied, and a
// repeat delete is a no-op (idempotent).
func TestDeleteMemoShare_AuthorCanRevoke(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	share, err := srv.CreateMemoShare(withViewer(context.Background(), alice), &v1.CreateMemoShareRequest{
		Parent:    "memos/" + memoID,
		MemoShare: &v1.MemoShare{},
	})
	require.NoError(t, err)

	// Delete with a non-author is denied.
	_, err = srv.DeleteMemoShare(withViewer(context.Background(), bob), &v1.DeleteMemoShareRequest{
		Name: share.GetName(),
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Author can delete.
	_, err = srv.DeleteMemoShare(withViewer(context.Background(), alice), &v1.DeleteMemoShareRequest{
		Name: share.GetName(),
	})
	require.NoError(t, err)

	// List is now empty.
	list, err := srv.ListMemoShares(withViewer(context.Background(), alice), &v1.ListMemoSharesRequest{
		Parent: "memos/" + memoID,
	})
	require.NoError(t, err)
	assert.Empty(t, list.GetMemoShares())

	// A repeat delete of the same name is a no-op, not an error.
	_, err = srv.DeleteMemoShare(withViewer(context.Background(), alice), &v1.DeleteMemoShareRequest{
		Name: share.GetName(),
	})
	require.NoError(t, err)
}

// TestGetSharedMemo_PublicUnauthenticated is the public-route
// assertion: a caller with no auth record in the context can
// resolve a share code to the memo it grants access to. This
// is the test that pins the no-auth shape of GetSharedMemo
// against the Change Routing "Public unauthenticated route"
// rule.
func TestGetSharedMemo_PublicUnauthenticated(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hello #World", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	share, err := srv.CreateMemoShare(withViewer(context.Background(), alice), &v1.CreateMemoShareRequest{
		Parent:    "memos/" + memoID,
		MemoShare: &v1.MemoShare{},
	})
	require.NoError(t, err)
	_, code, ok := splitMemoShareName(share.GetName())
	require.True(t, ok)

	// Public call: no viewer in the context.
	got, err := srv.GetSharedMemo(context.Background(), &v1.GetSharedMemoRequest{
		ShareToken: code,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello #World", got.GetContent())
	assert.Equal(t, v1.Visibility_PUBLIC, got.GetVisibility())
}

// TestGetSharedMemo_NotFound covers the bogus-token and the
// missing-token paths.
func TestGetSharedMemo_NotFound(t *testing.T) {
	app := newHookedApp(t)
	srv := NewMemoServer(app)

	// Missing token → InvalidArgument.
	_, err := srv.GetSharedMemo(context.Background(), &v1.GetSharedMemoRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Bogus token → NotFound.
	_, err = srv.GetSharedMemo(context.Background(), &v1.GetSharedMemoRequest{
		ShareToken: "no-such-code-exists",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestGetSharedMemo_ExpiredShare confirms a share with
// expire_time in the past is treated as missing.
func TestGetSharedMemo_ExpiredShare(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	// Create with a one-second TTL, then wait it out.
	share, err := srv.CreateMemoShare(withViewer(context.Background(), alice), &v1.CreateMemoShareRequest{
		Parent: "memos/" + memoID,
		MemoShare: &v1.MemoShare{
			ExpireTime: timestamppb.New(time.Now().Add(time.Second)),
		},
	})
	require.NoError(t, err)
	_, code, ok := splitMemoShareName(share.GetName())
	require.True(t, ok)

	// First read works.
	_, err = srv.GetSharedMemo(context.Background(), &v1.GetSharedMemoRequest{ShareToken: code})
	require.NoError(t, err)

	// Wait past the expiry.
	time.Sleep(1100 * time.Millisecond)

	_, err = srv.GetSharedMemo(context.Background(), &v1.GetSharedMemoRequest{ShareToken: code})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestGetSharedMemo_PrivateMemoShare is the visibility-leak
// guard: a private memo is private even when the holder of a
// share link tries to read it, because the bearer is by
// definition a non-member and canViewMemo's author check
// rejects them. The handler returns NotFound, never a leaked
// existence hint.
func TestGetSharedMemo_PrivateMemoShare(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "secret", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	share, err := srv.CreateMemoShare(withViewer(context.Background(), alice), &v1.CreateMemoShareRequest{
		Parent:    "memos/" + memoID,
		MemoShare: &v1.MemoShare{},
	})
	require.NoError(t, err)
	_, code, ok := splitMemoShareName(share.GetName())
	require.True(t, ok)

	_, err = srv.GetSharedMemo(context.Background(), &v1.GetSharedMemoRequest{ShareToken: code})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestMemoShare_CascadeDeleteOnMemoDelete is the cascade-rule
// guard: deleting a memo removes its share rows so a stale
// code cannot resolve to a tombstoned memo.
func TestMemoShare_CascadeDeleteOnMemoDelete(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hi", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoID := memoIDFromName(created.GetName())

	share, err := srv.CreateMemoShare(withViewer(context.Background(), alice), &v1.CreateMemoShareRequest{
		Parent:    "memos/" + memoID,
		MemoShare: &v1.MemoShare{},
	})
	require.NoError(t, err)
	_, code, ok := splitMemoShareName(share.GetName())
	require.True(t, ok)

	// Delete the memo.
	_, err = srv.DeleteMemo(withViewer(context.Background(), alice), &v1.DeleteMemoRequest{Name: "memos/" + memoID})
	require.NoError(t, err)

	// The share code now resolves to NotFound (not a leaked memo).
	_, err = srv.GetSharedMemo(context.Background(), &v1.GetSharedMemoRequest{ShareToken: code})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

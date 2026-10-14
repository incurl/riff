// Per-memo visibility tests for the new upstream-aligned auth model
// in `user_stats.go`. The wave-5 file already covers
// `ListAllUserStats_AnyCaller` and the existing `GetUserStats_GetForSelf`
// happy path; this file pins the per-caller visibility contract on
// `GetUserStats` end to end so a future refactor that drops the
// `CanAccessRecord` filter (e.g. by reverting to a global `computeUserStats`
// helper) would fail loud.
//
// The contract under test follows `memos.ViewRule` from
// `pb/migrations/1700000020_create_memos.go`:
//
//	author = @request.auth.id
//	  || visibility = "PROTECTED"
//	  || visibility = "PUBLIC"
//	  || (visibility = "SPACE" && ...space_members active)
//
// That means:
//   - An anonymous caller sees PUBLIC + PROTECTED memos of the
//     named user. PRIVATE is hidden. (This is a riff-specific
//     divergence from upstream Memos's `MemoAccessScope`, which
//     restricts PROTECTED to authenticated callers; riff's rule
//     was already unconditional before this change, so the new
//     `GetUserStats` inherits the existing rule verbatim.)
//   - A signed-in non-admin viewer named *differently* from the
//     target user sees the same PUBLIC + PROTECTED set, plus the
//     target's SPACE memos for spaces they actively belong to.
//     PRIVATE memos of the target user are still hidden.
//   - The named user themselves sees every memo of theirs (the
//     `author = @request.auth.id` branch).
package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestGetUserStats_RespectsVisibilityPerMemo pins the per-caller
// per-memo visibility contract. Alice posts a public note, a
// protected note, and a private note; bob is a stranger. Three
// callers ask for alice's stats:
//
//   - Anonymous: sees PUBLIC + PROTECTED (2 of 3), not PRIVATE.
//   - Bob (signed-in non-admin, not alice): same shape as the
//     anonymous caller (2 of 3) — the private memo is not his to
//     read regardless of authentication.
//   - Alice herself: sees all 3 (the `author = @request.auth.id`
//     branch of memos.ViewRule).
func TestGetUserStats_RespectsVisibilityPerMemo(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	memos := NewMemoServer(app)
	users := NewUserServer(app)

	aliceCtx := withViewer(context.Background(), alice)
	for _, body := range []v1.Visibility{v1.Visibility_PUBLIC, v1.Visibility_PROTECTED, v1.Visibility_PRIVATE} {
		_, err := memos.CreateMemo(aliceCtx, &v1.CreateMemoRequest{
			Memo: &v1.Memo{Content: "m-" + visibilityTag(body), Visibility: body},
		})
		require.NoError(t, err, "create %s", body)
	}

	// Anonymous: PUBLIC + PROTECTED are visible, PRIVATE is hidden.
	anon, err := users.GetUserStats(context.Background(), &v1.GetUserStatsRequest{Name: "users/alice"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), anon.GetTotalMemoCount(), "anonymous caller must see PUBLIC+PROTECTED, not PRIVATE")
	require.Len(t, anon.GetMemoCreatedTimestamps(), 2)

	// Bob (signed-in, not alice, not admin) sees the same PUBLIC+PROTECTED
	// set; the private memo is still not his to read.
	bobView, err := users.GetUserStats(withViewer(context.Background(), bob), &v1.GetUserStatsRequest{Name: "users/alice"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), bobView.GetTotalMemoCount(), "non-self non-admin viewer must not see alice's private memo")
	require.Len(t, bobView.GetMemoCreatedTimestamps(), 2)

	// Alice herself sees all three.
	self, err := users.GetUserStats(aliceCtx, &v1.GetUserStatsRequest{Name: "users/alice"})
	require.NoError(t, err)
	assert.Equal(t, int32(3), self.GetTotalMemoCount(), "self must still see every memo of theirs")
}

// TestGetUserStats_UnknownNameStillInvalidArgument guards the
// shape-only contract: a malformed or empty name must still fail
// with `InvalidArgument`, not a 200 with empty stats.
func TestGetUserStats_UnknownNameStillInvalidArgument(t *testing.T) {
	app := newHookedApp(t)
	users := NewUserServer(app)

	_, err := users.GetUserStats(context.Background(), &v1.GetUserStatsRequest{Name: ""})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// visibilityTag turns a Visibility enum into a stable suffix so the
// test memo contents are distinct without forcing the caller to
// repeat themselves.
func visibilityTag(v v1.Visibility) string {
	switch v {
	case v1.Visibility_PUBLIC:
		return "public"
	case v1.Visibility_PROTECTED:
		return "protected"
	case v1.Visibility_PRIVATE:
		return "private"
	default:
		return "unknown"
	}
}

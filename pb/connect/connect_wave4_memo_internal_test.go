package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestCreateMemo_RunsCreateHook asserts the create path runs
// OnRecordCreateRequest. Two things depend on it: the author is stamped from
// the caller, and the hashtags are parsed into `tags`. Without the hook the
// save fails validation outright, because the schema marks author Required.
func TestCreateMemo_RunsCreateHook(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	memo, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{
			Content:    "hello #World and #po33 and #World",
			Visibility: v1.Visibility_PUBLIC,
		},
	})
	require.NoError(t, err, "CreateMemo must succeed now that the hook chain runs")

	assert.Equal(t, "users/alice", memo.GetCreator())
	assert.Equal(t, []string{"world", "po33"}, memo.GetTags(), "hashtags must be parsed and deduplicated")

	rec := memoRecordByID(t, app, memoIDFromName(memo.GetName()))
	assert.Equal(t, alice.Id, rec.GetString("author"), "the hook must own the author field")
}

// TestCreateMemo_RequiresAuth asserts anonymous callers cannot create.
func TestCreateMemo_RequiresAuth(t *testing.T) {
	app := newHookedApp(t)
	srv := NewMemoServer(app)

	_, err := srv.CreateMemo(context.Background(), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "nope"},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestCreateMemo_SpaceVisibilityNeedsMembership asserts the hook's SPACE rule
// still applies through the bridge: only an active member may post into a
// space.
func TestCreateMemo_SpaceVisibilityNeedsMembership(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	spaces := NewSpaceServer(app)

	spaceID := mustCreateSpace(t, app, spaces, withViewer(context.Background(), alice), "Labs")
	spaceUID := lookupSpaceUID(app, spaceID)

	memos := NewMemoServer(app)
	space := "spaces/" + spaceUID

	_, err := memos.CreateMemo(withViewer(context.Background(), bob), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "intruder", Visibility: v1.Visibility_SPACE, Space: &space},
	})
	require.Error(t, err, "a non-member must not post into a space")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	created, err := memos.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "welcome", Visibility: v1.Visibility_SPACE, Space: &space},
	})
	require.NoError(t, err)
	require.NotNil(t, created.Space)
	assert.Equal(t, space, created.GetSpace())
}

// TestUpdateMemo_AuthorOnly asserts a memo can only be edited by its author,
// and that editing content re-parses the hashtags.
func TestUpdateMemo_AuthorOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "original #one", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)

	_, err = srv.UpdateMemo(withViewer(context.Background(), bob), &v1.UpdateMemoRequest{
		Memo: &v1.Memo{Name: created.GetName(), Content: "hijacked"},
	})
	require.Error(t, err, "a non-author must not update")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	updated, err := srv.UpdateMemo(withViewer(context.Background(), alice), &v1.UpdateMemoRequest{
		Memo: &v1.Memo{Name: created.GetName(), Content: "edited #two"},
	})
	require.NoError(t, err)
	assert.Equal(t, "edited #two", updated.GetContent())
	assert.Equal(t, []string{"two"}, updated.GetTags(), "tags must be re-parsed on content change")
}

// TestUpdateMemo_MaskLimitsFields asserts update_mask is honoured: a masked
// update may not touch a field the mask excludes.
func TestUpdateMemo_MaskLimitsFields(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "keep me", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)

	updated, err := srv.UpdateMemo(withViewer(context.Background(), alice), &v1.UpdateMemoRequest{
		Memo: &v1.Memo{
			Name:       created.GetName(),
			Content:    "should be ignored",
			Visibility: v1.Visibility_PUBLIC,
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"visibility"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "keep me", updated.GetContent(), "content is outside the mask")
	assert.Equal(t, v1.Visibility_PUBLIC, updated.GetVisibility(), "visibility is inside the mask")
}

// TestDeleteMemo_AuthorOnly asserts only the author can delete.
func TestDeleteMemo_AuthorOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)

	created, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "mine", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)

	_, err = srv.DeleteMemo(withViewer(context.Background(), bob), &v1.DeleteMemoRequest{Name: created.GetName()})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = srv.DeleteMemo(withViewer(context.Background(), alice), &v1.DeleteMemoRequest{Name: created.GetName()})
	require.NoError(t, err)

	_, err = app.FindRecordById("memos", memoIDFromName(created.GetName()))
	assert.Error(t, err, "the memo must be gone after deletion")
}

// TestCreateMemoComment_LinksToParent asserts a reply lands as an independent
// memo joined to its parent by a COMMENT relation, and that the parent comes
// back populated on the reply.
func TestCreateMemoComment_LinksToParent(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)
	ctx := withViewer(context.Background(), alice)

	parent, err := srv.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "parent", Visibility: v1.Visibility_PROTECTED},
	})
	require.NoError(t, err)

	comment, err := srv.CreateMemoComment(ctx, &v1.CreateMemoCommentRequest{
		Name:    parent.GetName(),
		Comment: &v1.Memo{Content: "a reply"},
	})
	require.NoError(t, err)

	assert.Equal(t, "users/alice", comment.GetCreator())
	assert.Equal(t, "a reply", comment.GetContent())
	assert.Equal(t, v1.Visibility_PROTECTED, comment.GetVisibility(),
		"a reply inherits the parent's audience so it is never more visible")
	require.NotNil(t, comment.Parent)
	assert.Equal(t, parent.GetName(), comment.GetParent())

	comments, err := srv.ListMemoComments(ctx, &v1.ListMemoCommentsRequest{Name: parent.GetName()})
	require.NoError(t, err)
	require.Len(t, comments.GetMemos(), 1)
	assert.Equal(t, comment.GetName(), comments.GetMemos()[0].GetName())
}

// TestCreateMemoComment_HidesParentFromOutsiders asserts a caller who cannot
// read the parent cannot enumerate its replies either.
func TestCreateMemoComment_HidesParentFromOutsiders(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)

	parent, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "private thoughts", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)

	bobCtx := withViewer(context.Background(), bob)
	_, err = srv.ListMemoComments(bobCtx, &v1.ListMemoCommentsRequest{Name: parent.GetName()})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = srv.CreateMemoComment(bobCtx, &v1.CreateMemoCommentRequest{
		Name: parent.GetName(), Comment: &v1.Memo{Content: "hello?"},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestReactions_UpsertListDelete covers the reaction lifecycle the memo card
// drives: add, stay idempotent on a repeat click, list, then withdraw.
func TestReactions_UpsertListDelete(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)

	memo, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "react to me", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)

	aliceCtx := withViewer(context.Background(), alice)
	first, err := srv.UpsertMemoReaction(aliceCtx, &v1.UpsertMemoReactionRequest{
		Name: memo.GetName(), Reaction: &v1.Reaction{ReactionType: "👍"},
	})
	require.NoError(t, err)
	assert.Equal(t, "users/alice", first.GetCreator())
	assert.Equal(t, "👍", first.GetReactionType())

	// Clicking the same emoji again must not duplicate the row.
	again, err := srv.UpsertMemoReaction(aliceCtx, &v1.UpsertMemoReactionRequest{
		Name: memo.GetName(), Reaction: &v1.Reaction{ReactionType: "👍"},
	})
	require.NoError(t, err)
	assert.Equal(t, first.GetName(), again.GetName(), "upsert must be idempotent")

	// A different emoji from the same user is a separate reaction.
	_, err = srv.UpsertMemoReaction(aliceCtx, &v1.UpsertMemoReactionRequest{
		Name: memo.GetName(), Reaction: &v1.Reaction{ReactionType: "❤️"},
	})
	require.NoError(t, err)

	// Bob adds his own.
	_, err = srv.UpsertMemoReaction(withViewer(context.Background(), bob), &v1.UpsertMemoReactionRequest{
		Name: memo.GetName(), Reaction: &v1.Reaction{ReactionType: "👍"},
	})
	require.NoError(t, err)

	list, err := srv.ListMemoReactions(aliceCtx, &v1.ListMemoReactionsRequest{Name: memo.GetName()})
	require.NoError(t, err)
	assert.Len(t, list.GetReactions(), 3)

	// The memo payload embeds the reactions the card renders.
	reloaded, err := srv.GetMemo(aliceCtx, &v1.GetMemoRequest{Name: memo.GetName()})
	require.NoError(t, err)
	assert.Len(t, reloaded.GetReactions(), 3, "recordToMemo must embed reactions")

	// Only the reaction's creator may withdraw it.
	_, err = srv.DeleteMemoReaction(withViewer(context.Background(), bob), &v1.DeleteMemoReactionRequest{Name: first.GetName()})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = srv.DeleteMemoReaction(aliceCtx, &v1.DeleteMemoReactionRequest{Name: first.GetName()})
	require.NoError(t, err)

	list, err = srv.ListMemoReactions(aliceCtx, &v1.ListMemoReactionsRequest{Name: memo.GetName()})
	require.NoError(t, err)
	assert.Len(t, list.GetReactions(), 2)
}

// TestReactions_HiddenMemoIsNotFound asserts a reaction cannot be attached to
// (or read from) a memo the caller cannot see.
func TestReactions_HiddenMemoIsNotFound(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)

	private, err := srv.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hidden", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)

	bobCtx := withViewer(context.Background(), bob)

	_, err = srv.UpsertMemoReaction(bobCtx, &v1.UpsertMemoReactionRequest{
		Name: private.GetName(), Reaction: &v1.Reaction{ReactionType: "👍"},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = srv.ListMemoReactions(bobCtx, &v1.ListMemoReactionsRequest{Name: private.GetName()})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestSetMemoRelations_ReplacesAndIsAuthorOnly asserts relations are replaced
// wholesale by their author, and that COMMENT relations are refused because
// CreateMemoComment owns those.
func TestSetMemoRelations_ReplacesAndIsAuthorOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	srv := NewMemoServer(app)
	aliceCtx := withViewer(context.Background(), alice)

	owner, err := srv.CreateMemo(aliceCtx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "linking #hub", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	first, err := srv.CreateMemo(aliceCtx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "first target", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	second, err := srv.CreateMemo(aliceCtx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "second target", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)

	_, err = srv.SetMemoRelations(aliceCtx, &v1.SetMemoRelationsRequest{
		Name: owner.GetName(),
		Relations: []*v1.MemoRelation{{
			RelatedMemo: &v1.MemoRelation_Memo{Name: first.GetName()},
			Type:        v1.MemoRelation_REFERENCE,
		}},
	})
	require.NoError(t, err)

	reloaded, err := srv.GetMemo(aliceCtx, &v1.GetMemoRequest{Name: owner.GetName()})
	require.NoError(t, err)
	require.Len(t, reloaded.GetRelations(), 1)
	assert.Equal(t, first.GetName(), reloaded.GetRelations()[0].GetRelatedMemo().GetName())

	// Replacing with a different target must drop the first one.
	_, err = srv.SetMemoRelations(aliceCtx, &v1.SetMemoRelationsRequest{
		Name: owner.GetName(),
		Relations: []*v1.MemoRelation{{
			RelatedMemo: &v1.MemoRelation_Memo{Name: second.GetName()},
			Type:        v1.MemoRelation_REFERENCE,
		}},
	})
	require.NoError(t, err)

	list, err := srv.ListMemoRelations(aliceCtx, &v1.ListMemoRelationsRequest{Name: owner.GetName()})
	require.NoError(t, err)
	require.Len(t, list.GetRelations(), 1)
	assert.Equal(t, second.GetName(), list.GetRelations()[0].GetRelatedMemo().GetName())

	// A non-author cannot rewrite them.
	_, err = srv.SetMemoRelations(withViewer(context.Background(), bob), &v1.SetMemoRelationsRequest{
		Name: owner.GetName(),
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// COMMENT relations belong to CreateMemoComment.
	_, err = srv.SetMemoRelations(aliceCtx, &v1.SetMemoRelationsRequest{
		Name: owner.GetName(),
		Relations: []*v1.MemoRelation{{
			RelatedMemo: &v1.MemoRelation_Memo{Name: first.GetName()},
			Type:        v1.MemoRelation_COMMENT,
		}},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestRecordToMemo_EmbedsDerivedCollections asserts the memo payload carries
// everything the React client reads off it: space, parent, relations and
// reactions.
func TestRecordToMemo_EmbedsDerivedCollections(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	memosSrv := NewMemoServer(app)
	spacesSrv := NewSpaceServer(app)
	aliceCtx := withViewer(context.Background(), alice)

	spaceID := mustCreateSpace(t, app, spacesSrv, aliceCtx, "Labs")
	spaceName := "spaces/" + lookupSpaceUID(app, spaceID)

	target, err := memosSrv.CreateMemo(aliceCtx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "link target", Visibility: v1.Visibility_SPACE, Space: &spaceName},
	})
	require.NoError(t, err)

	owner, err := memosSrv.CreateMemo(aliceCtx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hub memo", Visibility: v1.Visibility_SPACE, Space: &spaceName},
	})
	require.NoError(t, err)

	_, err = memosSrv.SetMemoRelations(aliceCtx, &v1.SetMemoRelationsRequest{
		Name: owner.GetName(),
		Relations: []*v1.MemoRelation{{
			RelatedMemo: &v1.MemoRelation_Memo{Name: target.GetName()},
			Type:        v1.MemoRelation_REFERENCE,
		}},
	})
	require.NoError(t, err)

	_, err = memosSrv.UpsertMemoReaction(aliceCtx, &v1.UpsertMemoReactionRequest{
		Name: owner.GetName(), Reaction: &v1.Reaction{ReactionType: "🔥"},
	})
	require.NoError(t, err)

	_, err = memosSrv.CreateMemoComment(aliceCtx, &v1.CreateMemoCommentRequest{
		Name: owner.GetName(), Comment: &v1.Memo{Content: "reply"},
	})
	require.NoError(t, err)

	reloaded, err := memosSrv.GetMemo(aliceCtx, &v1.GetMemoRequest{Name: owner.GetName()})
	require.NoError(t, err)
	require.NotNil(t, reloaded.Space)
	assert.Equal(t, spaceName, reloaded.GetSpace())
	assert.Len(t, reloaded.GetRelations(), 1, "REFERENCE relations are embedded")
	assert.Len(t, reloaded.GetReactions(), 1, "reactions are embedded")
	assert.Nil(t, reloaded.Parent, "a top-level memo has no parent")

	comments, err := memosSrv.ListMemoComments(aliceCtx, &v1.ListMemoCommentsRequest{Name: owner.GetName()})
	require.NoError(t, err)
	require.Len(t, comments.GetMemos(), 1)
	require.NotNil(t, comments.GetMemos()[0].Parent)
	assert.Equal(t, owner.GetName(), comments.GetMemos()[0].GetParent())

	// ListMemoAttachments works for a memo with no attachments.
	attachments, err := memosSrv.ListMemoAttachments(aliceCtx, &v1.ListMemoAttachmentsRequest{Name: owner.GetName()})
	require.NoError(t, err)
	assert.Empty(t, attachments.GetAttachments())
}

// TestDeleteMemo_PreservesCommentChain asserts the design-doc contract for
// DeleteMemo: only the named memo and the relations having it as an endpoint
// are removed. The comment subtree survives as orphaned but readable memos
// (docs/design/multi-spaces.md:135: "It does not traverse relations or
// delete other memos.").
//
// The hook still has to clear the incident memo_relations first, because
// memo_relations.related_memo is Required and does not cascade. Without the
// pre-clear PocketBase refuses the delete with "the record cannot be deleted
// because it is part of a required reference".
func TestDeleteMemo_PreservesCommentChain(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	srv := NewMemoServer(app)
	ctx := withViewer(context.Background(), alice)

	parent, err := srv.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "the original", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)

	reply, err := srv.CreateMemoComment(ctx, &v1.CreateMemoCommentRequest{
		Name: parent.GetName(), Comment: &v1.Memo{Content: "a reply"},
	})
	require.NoError(t, err)

	nested, err := srv.CreateMemoComment(ctx, &v1.CreateMemoCommentRequest{
		Name: reply.GetName(), Comment: &v1.Memo{Content: "a nested reply"},
	})
	require.NoError(t, err)

	referrer, err := srv.CreateMemo(ctx, &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "points at the original", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)

	_, err = srv.SetMemoRelations(ctx, &v1.SetMemoRelationsRequest{
		Name: referrer.GetName(),
		Relations: []*v1.MemoRelation{{
			RelatedMemo: &v1.MemoRelation_Memo{Name: parent.GetName()},
			Type:        v1.MemoRelation_REFERENCE,
		}},
	})
	require.NoError(t, err)

	_, err = srv.DeleteMemo(ctx, &v1.DeleteMemoRequest{Name: parent.GetName()})
	require.NoError(t, err, "a memo with replies and references must be deletable")

	// Parent is gone.
	_, err = app.FindRecordById("memos", memoIDFromName(parent.GetName()))
	assert.Error(t, err, "the deleted parent must be gone")

	// Replies survive as orphaned memos.
	for _, orphan := range []*v1.Memo{reply, nested} {
		_, err := app.FindRecordById("memos", memoIDFromName(orphan.GetName()))
		assert.NoError(t, err, "%s must survive its context memo's deletion", orphan.GetName())
	}

	// The COMMENT relations incident to the parent are gone (PocketBase's
	// `memo` side is cascading, so they vanished with the comment-side
	// context-free insert). The referrer's REFERENCE relation also went
	// away because the hook cleared related_memo = parent.
	rows, err := app.FindRecordsByFilter("memo_relations",
		"related_memo={:id}", "", 0, 0, map[string]any{"id": memoIDFromName(parent.GetName())})
	require.NoError(t, err)
	assert.Empty(t, rows, "no relation may still point at the deleted parent")

	// The referrer itself is fine; its relation is gone.
	survivor, err := srv.GetMemo(ctx, &v1.GetMemoRequest{Name: referrer.GetName()})
	require.NoError(t, err)
	assert.Empty(t, survivor.GetRelations(), "an incident relation must be removed")
}

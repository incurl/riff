package hooks

import (
	"github.com/pocketbase/pocketbase/core"
)

// registerNotifications binds the inbox write hooks. The matching
// read path (ListUserNotifications, UpdateUserNotification, …) lives
// in pb/connect/user_notifications.go.
//
// Four event sources feed the inbox:
//
//	memo_relations.AfterCreateSuccess
//	                            — emit MEMO_COMMENT to the parent
//	                              memo's author.
//	memos.AfterCreateSuccess / AfterUpdateSuccess
//	                            — extract @mentions from content and
//	                              emit MEMO_MENTION to each.
//	space_members.AfterUpdateSuccess
//	                            — detect INVITED → ACTIVE (accept)
//	                              and archive the matching
//	                              SPACE_INVITATION notification.
//	space_members.AfterDeleteSuccess
//	                            — fired by DeclineSpaceInvitation
//	                              and DeleteSpaceInvitation; clears
//	                              the SPACE_INVITATION inbox row.
//
// We use the model-level After*Success hook (not the request hook)
// because the request hook runs before commit, and a write that
// fails validation must not produce a side-effect row in the
// inbox. The After hook also runs after the transaction the
// OnRecordCreateRequest hook opened, so finding the parent memo
// does not see a half-committed state.
//
// For the mention dedupe on update, the OnRecordBeforeUpdate
// hook stashes the previous content in a package-level map keyed
// on record id so the AfterUpdateSuccess hook can compare the new
// mentions against the previously-notified ones. The map is
// best-effort: a missing entry means "treat this update as a
// fresh mention scan", which is the safe default.
func registerNotifications(app core.App) {
	// Reset the dedupe map for each new app boot. Register is
	// called once per process start.
	prevMemoContent = map[string]string{}

	app.OnRecordUpdate("memos").BindFunc(func(e *core.RecordEvent) error {
		// Stash the content as it sits in the DB right now
		// (before this update overwrites it). e.Record in the
		// model-level OnRecordUpdate hook is the row the caller
		// is about to write — in the Connect path, a fresh
		// core.NewRecord built from the request body — so the
		// previous state must be looked up explicitly.
		row, err := e.App.FindRecordById("memos", e.Record.Id)
		if err != nil {
			return e.Next()
		}
		prevMemoContent[e.Record.Id] = row.GetString("content")
		return e.Next()
	})
	app.OnRecordAfterCreateSuccess("memo_relations").BindFunc(func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		if e.Record.GetString("type") != "COMMENT" {
			return nil
		}
		parentID := e.Record.GetString("related_memo")
		commentID := e.Record.GetString("memo")
		if parentID == "" || commentID == "" {
			return nil
		}
		parent, err := e.App.FindRecordById("memos", parentID)
		if err != nil {
			return nil
		}
		comment, err := e.App.FindRecordById("memos", commentID)
		if err != nil {
			return nil
		}
		authorID := parent.GetString("author")
		if authorID == "" || authorID == comment.GetString("author") {
			// Author-self comment is the same as the inbox
			// "things you would otherwise miss" rule.
			return nil
		}
		receiver, err := e.App.FindRecordById("users", authorID)
		if err != nil {
			return nil
		}
		payload := map[string]any{
			"memo":                 "memos/" + commentID,
			"related_memo":         "memos/" + parentID,
			"memo_snippet":         snippetOf(comment.GetString("content"), 120),
			"related_memo_snippet": snippetOf(parent.GetString("content"), 120),
			"from_user": map[string]any{
				"id":       comment.GetString("author"),
				"username": lookupUsername(e.App, comment.GetString("author")),
			},
		}
		CreateNotificationBestEffort(e.App, receiver.Id, "MEMO_COMMENT", payload)
		return nil
	})

	app.OnRecordAfterCreateSuccess("memos").BindFunc(func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		emitMentionsForMemo(e.App, e.Record)
		return nil
	})

	app.OnRecordAfterUpdateSuccess("memos").BindFunc(func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		prevContent := prevMemoContent[e.Record.Id]
		// Stale stashes (e.g. from a prior boot, or after a
		// migration that wiped the map) get cleared here so
		// the next update of the same id sees a fresh state.
		delete(prevMemoContent, e.Record.Id)
		emitMentionsForMemoWithPrevious(e.App, e.Record, prevContent)
		return nil
	})

	app.OnRecordAfterUpdateSuccess("space_members").BindFunc(func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		prev := e.Record.Original()
		wasInvited := prev.GetString("status") == "INVITED"
		nowActive := e.Record.GetString("status") == "ACTIVE"
		if wasInvited && nowActive {
			archiveSpaceInvitationNotifications(e.App, e.Record.GetString("user"), e.Record.GetString("space"))
		}
		return nil
	})

	app.OnRecordAfterDeleteSuccess("space_members").BindFunc(func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		// DeclineSpaceInvitation and DeleteSpaceInvitation both
		// reach this hook via the record-request bridge, which is
		// the only path that should be removing an INVITED row.
		// A user-initiated self-leave (active → deleted) takes a
		// different route; the archive already happened at accept,
		// and there is no inbox row to clear.
		if e.Record.GetString("status") == "INVITED" {
			deleteSpaceInvitationNotifications(e.App, e.Record.GetString("user"), e.Record.GetString("space"))
		}
		return nil
	})
}

// emitMentionsForMemo is the create-path variant of
// emitMentionsForMemoWithPrevious: there is no previous version
// to dedupe against, so every mention is a fresh notification.
func emitMentionsForMemo(app core.App, memo *core.Record) {
	emitMentionsForMemoWithPrevious(app, memo, "")
}

// emitMentionsForMemoWithPrevious scans the memo's content for
// @username tokens via the markdown service and emits a
// MEMO_MENTION notification to each user who (a) still has an
// account, (b) can still read the memo, and (c) was not already
// mentioned by the previous version of the content (the empty
// string for prevContent means "no prior content to dedupe
// against" — every mention is fresh).
//
// The memo author is skipped — the inbox is for things you
// would otherwise miss, and the author already knows they wrote
// the memo.
func emitMentionsForMemoWithPrevious(app core.App, memo *core.Record, prevContent string) {
	usernames := mentionsInContent(memo.GetString("content"))
	if len(usernames) == 0 {
		return
	}

	alreadyNotified := map[string]struct{}{}
	if prevContent != "" {
		for _, u := range mentionsInContent(prevContent) {
			alreadyNotified[u] = struct{}{}
		}
	}

	for _, username := range usernames {
		if _, seen := alreadyNotified[username]; seen {
			continue
		}
		receiver, err := lookupUserByNameBestEffort(app, username)
		if err != nil || receiver == nil {
			continue
		}
		// Skip the memo author. The inbox is for things you
		// would otherwise miss — the author already knows they
		// wrote the memo.
		if receiver.Id == memo.GetString("author") {
			continue
		}
		if !canReceiverReadMemo(app, receiver, memo) {
			continue
		}
		parentRef := ""
		parentSnippet := ""
		if parentID := memoParentID(app, memo.Id); parentID != "" {
			parentRef = "memos/" + parentID
			if parent, err := app.FindRecordById("memos", parentID); err == nil {
				parentSnippet = snippetOf(parent.GetString("content"), 120)
			}
		}
		payload := map[string]any{
			"memo":                 "memos/" + memo.Id,
			"related_memo":         parentRef,
			"memo_snippet":         snippetOf(memo.GetString("content"), 120),
			"related_memo_snippet": parentSnippet,
			"from_user": map[string]any{
				"id":       memo.GetString("author"),
				"username": lookupUsername(app, memo.GetString("author")),
			},
		}
		CreateNotificationBestEffort(app, receiver.Id, "MEMO_MENTION", payload)
	}
}

// prevMemoContent maps a memo record id to its content as it sat
// in the DB at the time the OnRecordBeforeUpdate hook fired. The
// AfterUpdateSuccess hook reads from this map to know which
// mentions were already notified, then deletes the entry.
var prevMemoContent = map[string]string{}

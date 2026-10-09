package hooks

import (
	"errors"

	"github.com/pocketbase/pocketbase/core"
)

// registerMemoRelations binds hooks for the `memo_relations` collection.
//
// Per the Memos 0.31 design doc, a comment is an independent memo
// connected to a context memo by one immutable `COMMENT` relation
// created atomically with it. So this collection has stricter
// authorship and visibility constraints than the Phase 2 baseline.
func registerMemoRelations(app core.App) {
	app.OnRecordCreateRequest("memo_relations").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.Auth == nil {
			return errors.New("authentication required")
		}
		// Default type.
		if e.Record.GetString("type") == "" {
			e.Record.Set("type", "COMMENT")
		}

		commentMemoID := e.Record.GetString("memo")
		contextMemoID := e.Record.GetString("related_memo")
		if commentMemoID == "" || contextMemoID == "" {
			return errors.New("memo_relations requires both memo and related_memo")
		}
		commentMemo, err := e.App.FindRecordById("memos", commentMemoID)
		if err != nil {
			return err
		}
		contextMemo, err := e.App.FindRecordById("memos", contextMemoID)
		if err != nil {
			return err
		}

		// The comment's author must be the caller.
		if commentMemo.GetString("author") != e.Auth.Id {
			return errors.New("a comment's author must be the authenticated caller")
		}

		// The context memo must be visible to the caller.
		memos, _ := e.App.FindCollectionByNameOrId("memos")
		requestInfo, _ := e.RequestInfo()
		ok, err := e.App.CanAccessRecord(contextMemo, requestInfo, memos.ViewRule)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("you do not have access to the context memo")
		}

		// If context is SPACE, additionally require active membership.
		if contextMemo.GetString("visibility") == "SPACE" {
			space := contextMemo.GetString("space")
			if !IsActiveMember(e.App, e.Auth.Id, space) {
				return errors.New("only active space members can comment on SPACE-visibility memos")
			}
		}

		return e.Next()
	})
}

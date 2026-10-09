package hooks

import (
	"errors"

	"github.com/pocketbase/pocketbase/core"
)

// registerReactions binds hooks for the `reactions` collection.
//
// BeforeCreate:
//   - Force `user` to @request.auth.id.
//   - The parent memo must be visible to the caller.
//   - If the parent memo is SPACE-visibility, require active
//     membership.
//
// Per the design doc, an active reaction creator can withdraw
// their own reaction even after losing access. So we do NOT
// enforce the same read check on the BeforeDeleteRequest hook.
func registerReactions(app core.App) {
	app.OnRecordCreateRequest("reactions").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.Auth == nil {
			return errors.New("authentication required")
		}
		e.Record.Set("user", e.Auth.Id)

		memoID := e.Record.GetString("memo")
		if memoID == "" {
			return errors.New("reactions requires a memo")
		}
		memo, err := e.App.FindRecordById("memos", memoID)
		if err != nil {
			return err
		}
		memos, _ := e.App.FindCollectionByNameOrId("memos")
		requestInfo, _ := e.RequestInfo()
		ok, err := e.App.CanAccessRecord(memo, requestInfo, memos.ViewRule)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("you do not have access to the memo you are reacting to")
		}

		if memo.GetString("visibility") == "SPACE" {
			space := memo.GetString("space")
			if !IsActiveMember(e.App, e.Auth.Id, space) {
				return errors.New("only active space members can react to SPACE-visibility memos")
			}
		}

		return e.Next()
	})
}

package hooks

import (
	"encoding/json"
	"log/slog"

	"github.com/pocketbase/pocketbase/core"
)

// CreateNotificationBestEffort inserts a row into the
// `notifications` collection. It is the only authorized path:
// the collection's `createRule` is nil (superuser-only), so the
// Connect handlers and REST routes cannot write notifications
// directly, and every notification the system ever emits is
// shaped by one of the callers in this package.
//
// The error is logged but never returned: a failed notification
// insert is by design a soft failure (the underlying action
// — a comment, a mention, an accept — already succeeded, and the
// inbox is a best-effort surface).
//
// Exported so non-hook packages (notably pb/api/space_routes.go)
// can shape their own notifications through the same path.
func CreateNotificationBestEffort(app core.App, userID, kind string, payload any) {
	coll, err := app.FindCollectionByNameOrId("notifications")
	if err != nil {
		slog.Warn("notifications: failed to find collection",
			slog.String("kind", kind),
			slog.String("user_id", userID),
			slog.Any("err", err))
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("notifications: failed to marshal payload",
			slog.String("kind", kind),
			slog.Any("err", err))
		return
	}
	rec := core.NewRecord(coll)
	rec.Set("user", userID)
	rec.Set("type", kind)
	rec.Set("payload", string(raw))
	rec.Set("read", false)
	if err := app.Save(rec); err != nil {
		slog.Warn("notifications: failed to save",
			slog.String("kind", kind),
			slog.String("user_id", userID),
			slog.Any("err", err))
	}
}

// archiveSpaceInvitationNotifications marks every SPACE_INVITATION
// notification the receiver holds for the given space as archived
// (read=true). Called after AcceptSpaceInvitation so the inbox keeps
// the invitation as history while the membership is active — the
// state machine in the proto wants PENDING→ACCEPTED, never deleted.
func archiveSpaceInvitationNotifications(app core.App, receiverID, spaceID string) {
	forEachSpaceInvitationNotification(app, receiverID, spaceID, func(rec *core.Record) error {
		if rec.GetBool("read") {
			return nil
		}
		rec.Set("read", true)
		return app.Save(rec)
	})
}

// deleteSpaceInvitationNotifications removes the receiver's
// SPACE_INVITATION rows for the given space. Called after
// DeclineSpaceInvitation and DeleteSpaceInvitation: a declined or
// revoked offer leaves nothing the receiver can act on, so the inbox
// entry has no future state.
func deleteSpaceInvitationNotifications(app core.App, receiverID, spaceID string) {
	forEachSpaceInvitationNotification(app, receiverID, spaceID, func(rec *core.Record) error {
		return app.Delete(rec)
	})
}

// forEachSpaceInvitationNotification walks every SPACE_INVITATION
// notification the receiver holds for the given space, optionally
// filtered by an inline space-id check, and applies fn. The space
// filter is enforced in Go because the `payload` is a JSON column
// and PocketBase's filter language can't reach into it portably.
func forEachSpaceInvitationNotification(app core.App, receiverID, spaceID string, fn func(*core.Record) error) {
	rows, err := app.FindRecordsByFilter("notifications",
		"user={:uid} && type='SPACE_INVITATION'",
		"", 0, 0,
		map[string]any{"uid": receiverID},
	)
	if err != nil {
		slog.Warn("notifications: failed to list space invitations",
			slog.String("receiver_id", receiverID),
			slog.String("space_id", spaceID),
			slog.Any("err", err))
		return
	}
	for _, rec := range rows {
		payload := spaceInvitationPayloadMap(rec)
		if payload == nil {
			continue
		}
		if payload["space"] == nil {
			continue
		}
		space, ok := payload["space"].(map[string]any)
		if !ok {
			continue
		}
		id, _ := space["id"].(string)
		if id != spaceID {
			continue
		}
		if err := fn(rec); err != nil {
			slog.Warn("notifications: failed to apply",
				slog.String("receiver_id", receiverID),
				slog.String("space_id", spaceID),
				slog.Any("err", err))
		}
	}
}

// spaceInvitationPayloadMap unmarshals a SPACE_INVITATION row's
// JSON column. Returns nil on any decode failure so the caller
// silently skips the row — the worst case is a stale entry the
// archive/delete pass will leave in place, which is the safe
// failure mode for a "tidy the inbox" pass.
func spaceInvitationPayloadMap(rec *core.Record) map[string]any {
	raw := rec.GetString("payload")
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// canReceiverReadMemo reports whether the given receiver would be
// allowed to view the memo. It exists so the mention hook can
// avoid delivering a MEMO_MENTION to a user who can never read
// the memo anyway — a private memo whose author @-mentioned a
// non-friend, for example.
//
// The check evaluates the memos collection's list rule under a
// synthetic *core.RequestInfo with the receiver as the auth, which
// is exactly the shape the rule engine expects: a GET request
// from a logged-in user. Anonymous viewers (no auth) are
// deliberately not handled — the memo's list rule already covers
// the PUBLIC-visibility case for the same shape.
func canReceiverReadMemo(app core.App, receiver *core.Record, memo *core.Record) bool {
	if receiver == nil || memo == nil {
		return false
	}
	memos, err := app.FindCollectionByNameOrId("memos")
	if err != nil {
		return false
	}
	info := &core.RequestInfo{Method: "GET", Auth: receiver}
	ok, err := app.CanAccessRecord(memo, info, memos.ViewRule)
	return err == nil && ok
}

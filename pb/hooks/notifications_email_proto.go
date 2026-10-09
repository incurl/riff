package hooks

import "encoding/json"

// protoJSONUnmarshal is a thin wrapper over json.Unmarshal
// that the rest of the package uses to keep the import
// contained in one file. The "proto" prefix is a vestige of
// the protojson design we considered; the payloads are
// plain Go structs with `json:` tags, so encoding/json is
// the right tool. (protojson would also work; encoding/json
// is the lighter dep and matches what
// pb/connect/user_notifications.go already does for
// decoding the same shapes.)
func protoJSONUnmarshal(b []byte, m any) error {
	return json.Unmarshal(b, m)
}

// spaceInvitationPayload mirrors the JSON shape the
// pb/api/space_routes.go handler writes into the
// notifications.payload column for a SPACE_INVITATION row.
// The proto side (UserNotification_SpaceInvitationPayload)
// has the same fields under different names; we keep a
// riff-local struct so the dispatcher is not coupled to
// the proto.
type spaceInvitationPayload struct {
	Space    *spaceInvitationSpace    `json:"space"`
	FromUser *spaceInvitationFromUser `json:"from_user"`
}

type spaceInvitationSpace struct {
	ID    string `json:"id"`
	UID   string `json:"uid"`
	Title string `json:"title"`
	Icon  string `json:"icon"`
	Role  string `json:"role"`
}

type spaceInvitationFromUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// memoCommentPayload mirrors the JSON shape the after-create
// hook in pb/hooks/notifications.go writes for a MEMO_COMMENT
// row.
type memoCommentPayload struct {
	Memo               string                   `json:"memo"`
	RelatedMemo        string                   `json:"related_memo"`
	MemoSnippet        string                   `json:"memo_snippet"`
	RelatedMemoSnippet string                   `json:"related_memo_snippet"`
	FromUser           *spaceInvitationFromUser `json:"from_user"`
}

// memoMentionPayload mirrors the JSON shape the after-create
// hook in pb/hooks/notifications.go writes for a MEMO_MENTION
// row.
type memoMentionPayload struct {
	Memo               string                   `json:"memo"`
	RelatedMemo        string                   `json:"related_memo"`
	MemoSnippet        string                   `json:"memo_snippet"`
	RelatedMemoSnippet string                   `json:"related_memo_snippet"`
	FromUser           *spaceInvitationFromUser `json:"from_user"`
}

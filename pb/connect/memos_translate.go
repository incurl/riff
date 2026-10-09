package connect

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// recordToMemo converts a PocketBase `memos` record to the
// Memos `Memo` proto. The translation handles the field-name
// differences and embeds creator/space resource names in the
// Memos canonical form ("users/{username}" and "spaces/{uid}").
//
// The proto carries several derived collections, and the React
// client reads all of them off the Memo it receives:
//
//	reactions   — MemoReactionListView matches them against the
//	              signed-in user to highlight the buttons it has
//	              already pressed.
//	relations   — the editor (fromMemo) round-trips REFERENCE
//	              relations through the composer.
//	attachments — the editor round-trips the memo's patches and
//	              samples the same way.
//	parent      — the reaction view refetches a comment's parent
//	              when a comment reaction changes.
//	space       — the editor decides whether it is posting into a
//	              Space from this field.
//	payload     — server-computed property envelope (and the empty
//	              Location shell) populated by the memos save hook.
//	              See pb/hooks/memos.go for the column-side writer.
//
// Each of these costs one extra query per memo, which the feed can
// afford while it is small; a batched pre-join is the natural next
// step if list latency starts to matter.
func recordToMemo(app core.App, rec *core.Record) *v1.Memo {
	if rec == nil {
		return nil
	}
	username := lookupUsername(app, rec.GetString("author"))

	memo := &v1.Memo{
		Name:        "memos/" + rec.Id,
		State:       v1.State_NORMAL,
		Creator:     "users/" + username,
		CreateTime:  timestampToProto(rec.GetDateTime("created")),
		UpdateTime:  timestampToProto(rec.GetDateTime("updated")),
		Content:     rec.GetString("content"),
		Visibility:  visibilityToProto(rec.GetString("visibility")),
		Tags:        rec.GetStringSlice("tags"),
		Snippet:     snippetOf(rec.GetString("content"), 120),
		Reactions:   memoReactions(app, rec.Id),
		Relations:   memoReferenceRelations(app, rec.Id),
		Attachments: memoAttachments(app, rec.Id),
	}

	if spaceID := rec.GetString("space"); spaceID != "" {
		if uid := lookupSpaceUID(app, spaceID); uid != "" {
			space := "spaces/" + uid
			memo.Space = &space
		}
	}
	if parentID := memoParentID(app, rec.Id); parentID != "" {
		parent := "memos/" + parentID
		memo.Parent = &parent
	}
	if payload := memoPayloadFromRecord(rec); payload != nil {
		memo.Payload = payload
	}
	return memo
}

// memoPayloadFromRecord reads the `memos.payload` JSON column and
// decodes it into the upstream-shaped `MemoPayload` envelope. The
// column is written by the save hook in pb/hooks/memos.go; an empty
// or missing payload returns nil so the proto field stays unset
// (and clients reading the legacy flat `property`/`location` fields
// keep working unchanged).
//
// `Location` is left at its zero value for now: the column stores
// an empty shell, and riff has no geo feature to populate it with.
// The shape still matches the upstream proto so a future geo
// migration only has to populate the existing object.
func memoPayloadFromRecord(rec *core.Record) *v1.MemoPayload {
	raw := rec.Get("payload")
	if raw == nil {
		return nil
	}
	// PocketBase returns the JSON value as a generic `any` after
	// round-tripping through the JSON column. Accept the two
	// shapes we have actually seen in tests:
	//   - a Go struct (when we just wrote it via e.Record.Set),
	//   - a []byte (when the record came back from disk).
	var asStruct struct {
		Property struct {
			HasLink            bool   `json:"has_link"`
			HasTaskList        bool   `json:"has_task_list"`
			HasCode            bool   `json:"has_code"`
			HasIncompleteTasks bool   `json:"has_incomplete_tasks"`
			Title              string `json:"title"`
		} `json:"property"`
		Location struct {
			Placeholder string  `json:"placeholder"`
			Latitude    float64 `json:"latitude"`
			Longitude   float64 `json:"longitude"`
		} `json:"location"`
	}
	switch v := raw.(type) {
	case []byte:
		if len(v) == 0 || string(v) == "null" {
			return nil
		}
		if err := json.Unmarshal(v, &asStruct); err != nil {
			return nil
		}
	default:
		// PocketBase re-marshals the stored struct into JSON and
		// back when handing it to a caller, so we have to go
		// through JSON rather than type-asserting directly.
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		if len(b) == 0 || string(b) == "null" {
			return nil
		}
		if err := json.Unmarshal(b, &asStruct); err != nil {
			return nil
		}
	}
	out := &v1.MemoPayload{
		Property: &v1.MemoPayload_Property{
			HasLink:            asStruct.Property.HasLink,
			HasTaskList:        asStruct.Property.HasTaskList,
			HasCode:            asStruct.Property.HasCode,
			HasIncompleteTasks: asStruct.Property.HasIncompleteTasks,
			Title:              asStruct.Property.Title,
		},
		Location: &v1.Location{
			Placeholder: asStruct.Location.Placeholder,
			Latitude:    asStruct.Location.Latitude,
			Longitude:   asStruct.Location.Longitude,
		},
	}
	return out
}

// memoReactions loads the reactions attached to a memo.
func memoReactions(app core.App, memoID string) []*v1.Reaction {
	rows, err := app.FindRecordsByFilter("reactions",
		"memo={:id}", "", 0, 0, map[string]any{"id": memoID})
	if err != nil {
		return nil
	}
	out := make([]*v1.Reaction, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToReaction(app, memoID, r))
	}
	return out
}

// recordToReaction converts a `reactions` row to a Reaction proto. The
// resource name embeds the parent memo so the client can pass it
// straight back to DeleteMemoReaction.
func recordToReaction(app core.App, memoID string, rec *core.Record) *v1.Reaction {
	if rec == nil {
		return nil
	}
	username := lookupUsername(app, rec.GetString("user"))
	return &v1.Reaction{
		Name:         "memos/" + memoID + "/reactions/" + rec.Id,
		Creator:      "users/" + username,
		ReactionType: rec.GetString("emoji"),
		CreateTime:   timestampToProto(rec.GetDateTime("created")),
	}
}

// memoReferenceRelations loads the REFERENCE relations a memo owns.
// COMMENT relations model replies and surface as the memo's parent
// instead, so they are excluded here.
func memoReferenceRelations(app core.App, memoID string) []*v1.MemoRelation {
	rows, err := app.FindRecordsByFilter("memo_relations",
		"memo={:id} && type='REFERENCE'", "", 0, 0, map[string]any{"id": memoID})
	if err != nil {
		return nil
	}
	out := make([]*v1.MemoRelation, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToMemoRelation(app, r))
	}
	return out
}

// recordToMemoRelation converts a `memo_relations` row to the proto
// shape. Both endpoints are reported by resource name, with the
// target's snippet filled in so the editor can preview the link.
func recordToMemoRelation(app core.App, rec *core.Record) *v1.MemoRelation {
	if rec == nil {
		return nil
	}
	memoID := rec.GetString("memo")
	relatedID := rec.GetString("related_memo")
	return &v1.MemoRelation{
		Memo:        &v1.MemoRelation_Memo{Name: "memos/" + memoID},
		RelatedMemo: &v1.MemoRelation_Memo{Name: "memos/" + relatedID, Snippet: memoSnippet(app, relatedID)},
		Type:        relationTypeToProto(rec.GetString("type")),
	}
}

// relationTypeToProto maps our stored relation type to the proto enum.
func relationTypeToProto(t string) v1.MemoRelation_Type {
	switch t {
	case "REFERENCE":
		return v1.MemoRelation_REFERENCE
	case "COMMENT":
		return v1.MemoRelation_COMMENT
	default:
		return v1.MemoRelation_TYPE_UNSPECIFIED
	}
}

// memoParentID returns the id of the memo this memo is a comment on,
// or "" when it is a top-level memo.
func memoParentID(app core.App, memoID string) string {
	rows, err := app.FindRecordsByFilter("memo_relations",
		"memo={:id} && type='COMMENT'", "", 1, 0, map[string]any{"id": memoID})
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].GetString("related_memo")
}

// memoAttachments loads the patch and sample rows hanging off a memo
// and presents them as the flat Attachment list the proto expects.
func memoAttachments(app core.App, memoID string) []*v1.Attachment {
	out := []*v1.Attachment{}

	patches, err := app.FindRecordsByFilter("patches",
		"memo={:id}", "-created", 0, 0, map[string]any{"id": memoID})
	if err == nil {
		for _, p := range patches {
			if a := patchToAttachment(p); a != nil {
				out = append(out, a)
			}
		}
	}

	samples, err := app.FindRecordsByFilter("samples",
		"memo={:id}", "-created", 0, 0, map[string]any{"id": memoID})
	if err == nil {
		for _, sa := range samples {
			if a := sampleToAttachment(sa); a != nil {
				out = append(out, a)
			}
		}
	}

	return out
}

// memoSnippet returns a short plain-text preview of a memo's content.
func memoSnippet(app core.App, memoID string) string {
	rec, err := app.FindRecordById("memos", memoID)
	if err != nil {
		return ""
	}
	return snippetOf(rec.GetString("content"), 120)
}

// visibilityToProto maps a PocketBase visibility string to
// the proto enum. Unknown values map to UNSPECIFIED.
func visibilityToProto(v string) v1.Visibility {
	switch v {
	case "PRIVATE":
		return v1.Visibility_PRIVATE
	case "PROTECTED":
		return v1.Visibility_PROTECTED
	case "PUBLIC":
		return v1.Visibility_PUBLIC
	case "SPACE":
		return v1.Visibility_SPACE
	default:
		return v1.Visibility_VISIBILITY_UNSPECIFIED
	}
}

// visibilityFromProto is the reverse map. Returns "" if the
// visibility is UNSPECIFIED.
func visibilityFromProto(v v1.Visibility) string {
	switch v {
	case v1.Visibility_PRIVATE:
		return "PRIVATE"
	case v1.Visibility_PROTECTED:
		return "PROTECTED"
	case v1.Visibility_PUBLIC:
		return "PUBLIC"
	case v1.Visibility_SPACE:
		return "SPACE"
	default:
		return ""
	}
}

// snippetOf returns the first n bytes of content, trimmed at
// a word boundary if possible.
func snippetOf(content string, n int) string {
	if len(content) <= n {
		return content
	}
	cut := content[:n]
	if idx := lastSpace(cut); idx > n/2 {
		cut = cut[:idx]
	}
	return cut + "…"
}

func lastSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' || s[i] == '\n' || s[i] == '\t' {
			return i
		}
	}
	return -1
}

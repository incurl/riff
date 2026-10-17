package hooks

import (
	"errors"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/markdown"
)

// registerMemos binds hooks for the `memos` collection.
//
// BeforeCreate:
//   - Force author = @request.auth.id (never trust body).
//   - If visibility = SPACE, require space to be set and caller to
//     be an active member of that space. If visibility != SPACE
//     and space is set, clear space.
//   - Parse #hashtag tokens from content via the markdown service and
//     populate the tags field (deduplicated, lowercased). The service
//     walks the Goldmark AST, so hashtag recognition matches the rest
//     of the Memos-flavoured markdown (mentions, autolinks, GFM) and
//     inherits the upstream Unicode XID tag-character set.
//   - Materialise the MemoPayload envelope into the `payload` JSON
//     column via markdown.Service.ExtractProperties. The envelope
//     matches proto/gen/store.MemoPayload (property + location;
//     tags stay top-level). The location is an empty object — riff
//     has no geo feature yet. See DECISIONS.md and
//     pb/migrations/1700000021_add_memos_payload.go for the design.
//
// BeforeUpdate:
//   - Preserve author from the original record.
//   - If space is changing, caller must be active member of the
//     new space.
//   - If visibility is changing to SPACE, the same membership
//     check applies. Withdrawing (visibility -> non-SPACE) clears
//     space atomically.
//   - Re-parse hashtags if content changed.
//   - Re-materialise payload if content changed.
func registerMemos(app core.App) {
	// One markdown service per app boot. Tags and mentions are pure
	// functions of content, so the service is stateless and safe to
	// share across goroutines and requests.
	mdSvc := markdown.NewService(
		markdown.WithTagExtension(),
		markdown.WithMentionExtension(),
	)

	app.OnRecordCreateRequest("memos").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.Auth == nil {
			return errors.New("authentication required")
		}
		e.Record.Set("author", e.Auth.Id)

		visibility := e.Record.GetString("visibility")
		space := e.Record.GetString("space")
		switch visibility {
		case "SPACE":
			if space == "" {
				return errors.New("visibility=SPACE requires a space assignment")
			}
			if !IsActiveMember(e.App, e.Auth.Id, space) {
				return errors.New("only active space members can post with visibility=SPACE")
			}
		default:
			if space != "" {
				e.Record.Set("space", "")
			}
		}

		e.Record.Set("tags", extractHashtags(mdSvc, e.Record.GetString("content")))
		e.Record.Set("payload", materialisePayload(mdSvc, e.Record.GetString("content")))

		// Lifecycle / visibility interaction: a
		// sketch memo in DRAFT cannot be PUBLIC.
		// The publish path (PublishSketch RPC in
		// pb/connect/sketch_service_lifecycle.go)
		// flips DRAFT to NORMAL; a public draft
		// would skip the owner's review. The
		// check runs after the source / state
		// defaulting so a DRAFT sketch the
		// caller explicitly created lands in
		// the gated path even if the state
		// was not yet defaulted.
		if e.Record.GetString("state") == "DRAFT" &&
			e.Record.GetString("visibility") == "PUBLIC" {
			return errors.New("a DRAFT sketch cannot be PUBLIC; publish first")
		}

		// Default the `source` discriminator to "text"
		// when the caller did not set it. Every
		// existing memo (pre-PO-33 binding) is a
		// text memo, and a missing value would fail
		// the column's required check. The PO-33
		// upload handler always sets "sketch"
		// explicitly, so this default only applies
		// to text memos.
		if e.Record.GetString("source") == "" {
			e.Record.Set("source", "text")
		}
		// Default the `state` lifecycle field to
		// "NORMAL" when the caller did not set it.
		// The existing record (the migration that
		// added the column) backfills the value at
		// migration time; this defaulting rule
		// covers the pre-migration rows that the
		// migration's filter might miss (e.g. a
		// row created in the same transaction as
		// the migration) and any future caller
		// that forgets to set the column. The
		// PO-33 device upload handler always sets
		// "DRAFT" explicitly, so this default
		// only applies to text memos.
		if e.Record.GetString("state") == "" {
			e.Record.Set("state", "NORMAL")
		}
		return e.Next()
	})

	app.OnRecordUpdateRequest("memos").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.Auth == nil {
			return errors.New("authentication required")
		}
		// Author is immutable.
		e.Record.Set("author", e.Record.Original().GetString("author"))

		oldSpace := e.Record.Original().GetString("space")
		newSpace := e.Record.GetString("space")
		if newSpace != oldSpace && newSpace != "" {
			if !IsActiveMember(e.App, e.Auth.Id, newSpace) {
				return errors.New("only active members of the target space can move a memo into it")
			}
		}

		oldVis := e.Record.Original().GetString("visibility")
		newVis := e.Record.GetString("visibility")
		if newVis != oldVis && newVis == "SPACE" {
			if newSpace == "" {
				return errors.New("visibility=SPACE requires a space assignment")
			}
			if !IsActiveMember(e.App, e.Auth.Id, newSpace) {
				return errors.New("only active space members can change a memo to visibility=SPACE")
			}
		}

		oldContent := e.Record.Original().GetString("content")
		newContent := e.Record.GetString("content")
		if newContent != oldContent {
			e.Record.Set("tags", extractHashtags(mdSvc, newContent))
			e.Record.Set("payload", materialisePayload(mdSvc, newContent))
		}

		// Lifecycle / visibility interaction on
		// update: a memo that transitions into
		// DRAFT (rare, but possible if a future
		// admin tool un-archives) cannot be
		// PUBLIC. A memo that is currently
		// DRAFT and is being made PUBLIC is the
		// realistic case and is the one the
		// brief's lifecycle assumes: the
		// publish step must run first.
		newState := e.Record.GetString("state")
		if newState == "DRAFT" &&
			e.Record.GetString("visibility") == "PUBLIC" {
			return errors.New("a DRAFT sketch cannot be PUBLIC; publish first")
		}
		return e.Next()
	})

	app.OnRecordDeleteRequest("memos").BindFunc(func(e *core.RecordRequestEvent) error {
		memoID := e.Record.Id

		// A memo cannot be deleted while a required relation still points
		// at it. `memo_relations.related_memo` is Required and does not
		// cascade, because the design keeps a context memo's comments
		// alive when the context is gone (see
		// `pb/migrations/1700000030_create_memo_relations.go:70`). Without
		// this pre-clear, PocketBase refuses the delete with "the record
		// cannot be deleted because it is part of a required reference".
		//
		// The replies themselves are NOT deleted: per the design doc
		// (docs/design/multi-spaces.md:135), "deleting one memo deletes
		// that memo, its owned resources, and relations having it as an
		// endpoint. It does not traverse relations or delete other
		// memos." The reply remains as an orphaned but readable memo;
		// any further references pointing at the deleted parent are
		// removed the same way.
		//
		// Use the event app directly. The request may already be inside a
		// transaction (e.g. user force-delete purges their memos), and
		// nesting `RunInTransaction` on SQLite with `IMMEDIATE` locks
		// deadlocks; on other drivers it just nests a savepoint. Either
		// way, the surrounding transaction is what we want.
		if err := deleteByFilter(e.App, "memo_relations",
			"related_memo={:id}", map[string]any{"id": memoID}); err != nil {
			return err
		}
		return e.Next()
	})
}

// collectCommentSubtree and purgeMemoDependents were removed when the
// memo-delete hook was aligned with docs/design/multi-spaces.md. Deleting a
// memo no longer traverses its comment subtree; replies stay alive, only
// incident relations are cleared.

// extractHashtags asks the markdown service for #tag nodes and
// returns them as a deduplicated, lowercased list. We lowercase
// here because the service preserves original case (e.g. for
// display), but the `tags` JSON column is conventionally stored
// lowercase so facet filtering is case-insensitive.
//
// An empty/nil result from the service is normalised to a
// non-nil empty slice so PocketBase serialises it as `[]`
// rather than `null` (the JSON column has no default and would
// otherwise break clients that expect an array).
func extractHashtags(svc markdown.Service, content string) []string {
	if content == "" {
		return []string{}
	}
	tags, err := svc.ExtractTags([]byte(content))
	if err != nil {
		// A parse failure on user content shouldn't block the
		// memo save — fall back to an empty tag set and let
		// the markdown package's separate validation surface
		// deal with it (where applicable).
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(t)
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// payloadShape mirrors proto/gen/store.MemoPayload. It is the
// structure we write into the `memos.payload` JSON column on every
// save. The upstream proto's `tags` field is deliberately omitted:
// tags live in the top-level `memos.tags` JSON column so the
// `tags ?= {:tag}` filter in pb/api/feed.go keeps working.
//
// `Location` is an empty struct on every row for now. Riff has no
// geo feature; the field exists in the envelope so a future geo
// migration only has to populate the object, not redesign the
// column. See pb/migrations/1700000021_add_memos_payload.go and
// the DECISIONS.md entry "memos.payload column landed" for the
// full reasoning.
type payloadShape struct {
	Property payloadProperty `json:"property"`
	Location payloadLocation `json:"location"`
}

type payloadProperty struct {
	HasLink            bool   `json:"has_link"`
	HasTaskList        bool   `json:"has_task_list"`
	HasCode            bool   `json:"has_code"`
	HasIncompleteTasks bool   `json:"has_incomplete_tasks"`
	Title              string `json:"title"`
}

type payloadLocation struct {
	Placeholder string  `json:"placeholder"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

// materialisePayload runs markdown.Service.ExtractProperties against
// the memo content and packages the result into the upstream-shaped
// MemoPayload envelope. The hook caller writes the result to the
// `payload` JSON column on the record.
//
// A parse failure is treated the same way `extractHashtags` treats
// one: the memo save is not blocked, the payload is materialised
// with an all-false property. The markdown package has its own
// validation surface that can surface the underlying error to the
// caller separately; this hook is the metadata path, not the
// content-validation path.
//
// An empty `content` returns the zero payload (all-false property,
// empty location) — every field of MemoPayload has a defined
// zero value, so the JSON column never stores a partial shape.
func materialisePayload(svc markdown.Service, content string) payloadShape {
	out := payloadShape{}
	if content == "" {
		return out
	}
	prop, err := svc.ExtractProperties([]byte(content))
	if err != nil {
		return out
	}
	if prop != nil {
		out.Property = payloadProperty{
			HasLink:            prop.HasLink,
			HasTaskList:        prop.HasTaskList,
			HasCode:            prop.HasCode,
			HasIncompleteTasks: prop.HasIncompleteTasks,
			Title:              prop.Title,
		}
	}
	return out
}

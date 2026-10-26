package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// makeSpacesPrivate scopes the `spaces` collection to its ACTIVE
// members.
//
// The collection shipped with PUBLIC rules (""), which in PocketBase
// means "everyone can read". The Memos-shaped REST surface reached it
// through the `pb_records_crud` flavour, which never consults the
// rule engine, so an anonymous caller could list every space in the
// instance.
//
// The rule below is the same back-relation traversal the `memos`
// collection already uses for its SPACE visibility branch
// (`space.space_members_via_space.user.id ?= @request.auth.id`),
// applied from the space record instead of through it:
//
//	space_members_via_space.user.id ?= @request.auth.id
//	&& space_members_via_space.status = "ACTIVE"
//
// The owner is always an ACTIVE member — the create hook adds the
// creator as an ACTIVE ADMIN (pb/hooks/spaces.go) — so the owner
// needs no separate clause.
//
// The write rules are relaxed from "owner only" to "owner or an
// ACTIVE ADMIN", which is what the Memos 0.31 multi-spaces design
// describes: a space is administered by its admins, not solely by
// whoever created it.
//
// The `memos` and `attachments` rules are deliberately UNCHANGED. A
// memo's visibility is independent of its space's privacy, so a
// PUBLIC memo in a private space stays public — the space RELATION
// is stripped for a non-member by the converter instead (see
// pb/connect/memos_translate.go).
//
// Idempotency: a no-op when the rules are already the private ones.
func init() {
	migrations.Register(func(txApp core.App) error {
		col, err := txApp.FindCollectionByNameOrId("spaces")
		if err != nil {
			// The collection does not exist yet on a fresh install;
			// its own create migration owns the schema.
			return nil
		}

		memberRule := `space_members_via_space.user.id ?= @request.auth.id` +
			` && space_members_via_space.status = "ACTIVE"`
		adminRule := `owner = @request.auth.id` +
			` || (space_members_via_space.user.id ?= @request.auth.id` +
			` && space_members_via_space.status = "ACTIVE"` +
			` && space_members_via_space.role = "ADMIN")`

		if col.ListRule != nil && *col.ListRule == memberRule {
			return nil
		}

		col.ListRule = types.Pointer(memberRule)
		col.ViewRule = types.Pointer(memberRule)
		// Creating a space requires an authenticated caller; the
		// create hook then forces `owner` to be that caller.
		col.CreateRule = types.Pointer("")
		col.UpdateRule = types.Pointer(adminRule)
		col.DeleteRule = types.Pointer(adminRule)

		return txApp.Save(col)
	}, nil)
}

package api

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/hooks"
)

// memoMoveHandler serves POST /api/po33/memos/:id/move.
//
// Body: { "space": "<space-uid-or-id-or-null>", "visibility": "..." }
//
// The :id is a memo id. Caller must be the author. The move
// enforces the design doc semantics:
//   - visibility=SPACE requires the new space to be set and the
//     author to be an active member of it.
//   - Withdrawing (visibility != SPACE) clears the space in the
//     same update.
//   - The visibility and space fields must be consistent.
func memoMoveHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return NewAPIError(http.StatusUnauthorized, "authentication required")
	}
	id := e.Request.PathValue("id")
	memo, err := e.App.FindRecordById("memos", id)
	if err != nil {
		return NewAPIError(http.StatusNotFound, "memo not found")
	}
	if memo.GetString("author") != e.Auth.Id {
		return NewAPIError(http.StatusForbidden, "only the author can move a memo")
	}

	var body struct {
		Space      string `json:"space"`
		Visibility string `json:"visibility"`
	}
	if err := e.BindBody(&body); err != nil {
		return NewAPIError(http.StatusBadRequest, "invalid request body")
	}

	// Resolve space: body.Space may be "" (withdraw), a uid, or an id.
	resolvedSpaceID := ""
	if body.Space != "" {
		row, err := e.App.FindRecordById("spaces", body.Space)
		if err == nil {
			resolvedSpaceID = row.Id
		} else {
			rows, err2 := e.App.FindRecordsByFilter("spaces", "uid={:u}", "", 1, 0, map[string]any{"u": body.Space})
			if err2 != nil || len(rows) == 0 {
				return NewAPIError(http.StatusNotFound, "space not found")
			}
			resolvedSpaceID = rows[0].Id
		}
	}

	switch {
	case body.Visibility == "SPACE":
		if resolvedSpaceID == "" {
			return NewAPIError(http.StatusBadRequest, "visibility=SPACE requires a space assignment")
		}
		if !hooks.IsActiveMember(e.App, e.Auth.Id, resolvedSpaceID) {
			return NewAPIError(http.StatusForbidden, "only active space members can move a memo into the space")
		}
		memo.Set("space", resolvedSpaceID)
		memo.Set("visibility", "SPACE")

	case body.Visibility == "PRIVATE" || body.Visibility == "PROTECTED" || body.Visibility == "PUBLIC":
		// Withdrawing or moving to a non-SPACE visibility.
		memo.Set("visibility", body.Visibility)
		memo.Set("space", "") // always clear space on non-SPACE visibility

	default:
		// No visibility change: just space change.
		if resolvedSpaceID != "" {
			if !hooks.IsActiveMember(e.App, e.Auth.Id, resolvedSpaceID) {
				return NewAPIError(http.StatusForbidden, "only active space members can assign a memo to the space")
			}
			if memo.GetString("visibility") != "SPACE" {
				return NewAPIError(http.StatusBadRequest, "assigning a space requires visibility=SPACE")
			}
			memo.Set("space", resolvedSpaceID)
		} else {
			// Withdrawing requires visibility change in same update.
			return NewAPIError(http.StatusBadRequest, "withdrawing a space requires changing visibility to a non-SPACE value in the same request")
		}
	}

	if err := e.App.Save(memo); err != nil {
		return err
	}
	return e.JSON(http.StatusOK, memo.PublicExport())
}

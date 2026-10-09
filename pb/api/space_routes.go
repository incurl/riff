package api

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/hooks"
)

// resolveSpace looks up a space by either its id or its uid. The
// :id path param is treated as a uid first (since that's what the
// frontend sends in URLs), then falls back to id. Returns the
// record or a 404.
func resolveSpace(e *core.RequestEvent) (*core.Record, error) {
	idOrUid := e.Request.PathValue("id")
	if idOrUid == "" {
		return nil, NewAPIError(http.StatusNotFound, "space not found")
	}
	// Try by id first.
	if row, err := e.App.FindRecordById("spaces", idOrUid); err == nil {
		return row, nil
	}
	// Try by uid.
	rows, err := e.App.FindRecordsByFilter("spaces", "uid={:u}", "", 1, 0, map[string]any{"u": idOrUid})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, NewAPIError(http.StatusNotFound, "space not found")
	}
	return rows[0], nil
}

// spaceMemosHandler serves GET /api/po33/spaces/:id/memos.
//
// The :id may be a space uid (URL slug) or record id. Returns 404
// if the space doesn't exist or the caller is not an active member
// (we don't differentiate per the design doc's "NotFound for
// non-members" rule).
func spaceMemosHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return NewAPIError(http.StatusUnauthorized, "authentication required")
	}
	space, err := resolveSpace(e)
	if err != nil {
		return err
	}

	// Active-member gate (per design doc: pending invitees get
	// NotFound, not Forbidden).
	if !hooks.IsActiveMember(e.App, e.Auth.Id, space.Id) {
		return NewAPIError(http.StatusNotFound, "space not found")
	}

	memos, err := e.App.FindCollectionByNameOrId("memos")
	if err != nil {
		return err
	}
	page, perPage, err := paginationFromQuery(e, 30, 100)
	if err != nil {
		return NewAPIError(http.StatusBadRequest, err.Error())
	}

	records, err := e.App.FindRecordsByFilter(
		memos, "space={:sid}", "-created", perPage, (page-1)*perPage,
		map[string]any{"sid": space.Id},
	)
	if err != nil {
		return err
	}

	out := make([]map[string]any, 0, len(records))
	for _, r := range records {
		out = append(out, r.PublicExport())
	}
	return e.JSON(http.StatusOK, FeedResponse{
		Items: out, Page: page, PerPage: perPage, TotalItems: len(out),
	})
}

// spaceInviteHandler serves POST /api/po33/spaces/:id/invite.
//
// Body: { "username": "<username>", "role": "USER" | "ADMIN" }
//
// Caller must be an active ADMIN of the space. Creates (or
// replaces) a space_members row with status=INVITED and the
// requested role, and emits a SPACE_INVITATION notification.
func spaceInviteHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return NewAPIError(http.StatusUnauthorized, "authentication required")
	}
	space, err := resolveSpace(e)
	if err != nil {
		return err
	}

	// Caller must be an active ADMIN.
	if !hooks.IsActiveAdmin(e.App, e.Auth.Id, space.Id) {
		return NewAPIError(http.StatusForbidden, "only active space ADMINs can invite")
	}

	var body struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := e.BindBody(&body); err != nil {
		return NewAPIError(http.StatusBadRequest, "invalid request body")
	}
	if body.Username == "" {
		return NewAPIError(http.StatusBadRequest, "username is required")
	}
	if body.Role != "ADMIN" && body.Role != "USER" {
		return NewAPIError(http.StatusBadRequest, "role must be ADMIN or USER")
	}

	// Look up the invitee by username.
	invitees, err := e.App.FindRecordsByFilter("users", "username={:un}", "", 1, 0, map[string]any{"un": body.Username})
	if err != nil {
		return err
	}
	if len(invitees) == 0 {
		return NewAPIError(http.StatusNotFound, "user not found")
	}
	invitee := invitees[0]

	// Idempotency: if a row already exists, update it; else create.
	smCol, _ := e.App.FindCollectionByNameOrId("space_members")
	existing, _ := e.App.FindRecordsByFilter(
		"space_members",
		"space={:sid} && user={:uid}",
		"", 1, 0,
		map[string]any{"sid": space.Id, "uid": invitee.Id},
	)
	var member *core.Record
	if len(existing) > 0 {
		member = existing[0]
		member.Set("status", "INVITED")
		member.Set("role", body.Role)
	} else {
		member = core.NewRecord(smCol)
		member.Set("space", space.Id)
		member.Set("user", invitee.Id)
		member.Set("status", "INVITED")
		member.Set("role", body.Role)
	}
	if err := e.App.Save(member); err != nil {
		return err
	}

	// Create a notification (best-effort).
	notifCol, _ := e.App.FindCollectionByNameOrId("notifications")
	if notifCol != nil {
		notif := core.NewRecord(notifCol)
		notif.Set("user", invitee.Id)
		notif.Set("type", "SPACE_INVITATION")
		notif.Set("payload", map[string]any{
			"space": map[string]any{
				"id":    space.Id,
				"uid":   space.GetString("uid"),
				"title": space.GetString("title"),
				"icon":  space.GetString("icon"),
				"role":  body.Role,
			},
			"from_user": map[string]any{
				"id":       e.Auth.Id,
				"username": e.Auth.GetString("username"),
			},
		})
		_ = e.App.Save(notif)
	}

	return e.JSON(http.StatusOK, member.PublicExport())
}

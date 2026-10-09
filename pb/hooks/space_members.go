package hooks

import (
	"errors"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
)

// registerSpaceMembers binds hooks for the `space_members` junction.
//
// Authorization model (per the Memos 0.31 design doc):
//
//	create:  only the invited user (accepting an INVITED row they own).
//	         Admin-initiated invites go through the custom REST route
//	         /api/po33/spaces/:id/invite, NOT the collection API.
//	update:  the row's own user (accept/decline) OR an active ADMIN of
//	         the same space (role change, removal).
//	delete:  same as update.
//	Invariant: a Space must always have at least one ACTIVE ADMIN.
//	         Any change that would violate this is rejected.
func registerSpaceMembers(app core.App) {
	app.OnRecordCreateRequest("space_members").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.Auth == nil {
			return errors.New("authentication required")
		}
		rowUser := e.Record.GetString("user")
		if rowUser != e.Auth.Id {
			return errors.New("a space_members row can only be created by the invited user (use the invite route for admin-initiated invitations)")
		}
		if e.Record.GetString("status") != "ACTIVE" {
			return errors.New("a space_members row can only be created in ACTIVE status (use the invite route for INVITED rows)")
		}
		return e.Next()
	})

	app.OnRecordUpdateRequest("space_members").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.Auth == nil {
			return errors.New("authentication required")
		}
		spaceID := e.Record.GetString("space")
		rowUser := e.Record.GetString("user")

		isOwner := rowUser == e.Auth.Id
		isAdmin := IsActiveAdmin(e.App, e.Auth.Id, spaceID)
		if !isOwner && !isAdmin {
			return errors.New("only the invited user or an active space ADMIN can modify a space_members row")
		}

		newRole := e.Record.GetString("role")
		oldRole := e.Record.Original().GetString("role")
		if newRole != oldRole && !isAdmin {
			return fmt.Errorf("only an active space ADMIN can change a member's role")
		}

		newStatus := e.Record.GetString("status")
		oldStatus := e.Record.Original().GetString("status")
		if newStatus != oldStatus {
			if !isOwner {
				return fmt.Errorf("only the invited user can change their own status")
			}
			if oldStatus != "INVITED" || newStatus != "ACTIVE" {
				return fmt.Errorf("status can only transition from INVITED to ACTIVE")
			}
		}

		if err := enforceLastAdminInvariant(e.App, spaceID, e.Record.Id, newRole, newStatus); err != nil {
			return err
		}
		return e.Next()
	})

	app.OnRecordDeleteRequest("space_members").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.Auth == nil {
			return errors.New("authentication required")
		}
		spaceID := e.Record.GetString("space")
		rowUser := e.Record.GetString("user")

		isOwner := rowUser == e.Auth.Id
		isAdmin := IsActiveAdmin(e.App, e.Auth.Id, spaceID)
		if !isOwner && !isAdmin {
			return errors.New("only the row's user or an active space ADMIN can delete a space_members row")
		}

		role := e.Record.GetString("role")
		status := e.Record.GetString("status")
		if role == "ADMIN" && status == "ACTIVE" {
			others, err := countActiveAdmins(e.App, spaceID, e.Record.Id)
			if err != nil {
				return err
			}
			if others < 1 {
				return errors.New("cannot remove the last active ADMIN of a space")
			}
		}
		return e.Next()
	})
}

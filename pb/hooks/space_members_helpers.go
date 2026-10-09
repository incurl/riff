package hooks

import (
	"errors"

	"github.com/pocketbase/pocketbase/core"
)

// countActiveAdmins counts ACTIVE ADMIN rows for a space, optionally
// excluding a specific row (used during the last-ADMIN check on
// update/delete).
func countActiveAdmins(app core.App, spaceID, excludeRowID string) (int, error) {
	rows, err := app.FindRecordsByFilter(
		"space_members",
		"space={:sid} && status='ACTIVE' && role='ADMIN' && id!={:eid}",
		"", 0, 0,
		map[string]any{"sid": spaceID, "eid": excludeRowID},
	)
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}

// enforceLastAdminInvariant checks that a pending update would not
// leave a space with zero ACTIVE ADMINs.
func enforceLastAdminInvariant(app core.App, spaceID, rowID, newRole, newStatus string) error {
	original, err := app.FindRecordById("space_members", rowID)
	if err != nil {
		return err
	}
	wasAdmin := original.GetString("role") == "ADMIN" && original.GetString("status") == "ACTIVE"
	stillAdmin := newRole == "ADMIN" && newStatus == "ACTIVE"
	if wasAdmin && !stillAdmin {
		others, err := countActiveAdmins(app, spaceID, rowID)
		if err != nil {
			return err
		}
		if others < 1 {
			return errors.New("cannot demote or deactivate the last active ADMIN of a space")
		}
	}
	return nil
}

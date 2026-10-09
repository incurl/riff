package hooks

import "github.com/pocketbase/pocketbase/core"

// IsActiveAdmin reports whether the user is currently an ACTIVE
// ADMIN of the given space. Exported so the pb/api routes can use
// it without duplicating the logic.
func IsActiveAdmin(app core.App, userID, spaceID string) bool {
	if userID == "" || spaceID == "" {
		return false
	}
	rows, err := app.FindRecordsByFilter(
		"space_members",
		"user={:uid} && space={:sid} && status='ACTIVE' && role='ADMIN'",
		"", 0, 0,
		map[string]any{"uid": userID, "sid": spaceID},
	)
	if err != nil {
		return false
	}
	return len(rows) > 0
}

// IsActiveMember reports whether the user is currently an ACTIVE
// member of the given space (any role). Exported.
func IsActiveMember(app core.App, userID, spaceID string) bool {
	if userID == "" || spaceID == "" {
		return false
	}
	rows, err := app.FindRecordsByFilter(
		"space_members",
		"user={:uid} && space={:sid} && status='ACTIVE'",
		"", 0, 0,
		map[string]any{"uid": userID, "sid": spaceID},
	)
	if err != nil {
		return false
	}
	return len(rows) > 0
}

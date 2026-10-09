// Package hooks registers all riff event hooks on a PocketBase app.
//
// The init() function binds every hook this package owns. main.go
// imports this package with a blank import for the side effect.
package hooks

import "github.com/pocketbase/pocketbase/core"

// Register wires all riff event hooks onto the given app. Call this
// from main.go after pocketbase.NewWithConfig and before app.Start().
//
// Each subpackage (spaces, memos, ...) provides its own Register
// function; this top-level Register orchestrates them.
func Register(app core.App) {
	registerSpaces(app)
	registerSpaceMembers(app)
	registerMemos(app)
	registerMemoRelations(app)
	registerReactions(app)
	registerRealtime(app)
	registerMemoLinks(app)
}

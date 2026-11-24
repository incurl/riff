package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
	"github.com/incurl/riff/pb/flarum/session"
)

// The admin surface: the routes the admin application calls to change
// the forum's configuration.
//
// Every route here mirrors an upstream Flarum v2.0.0 controller, and the
// wire format is upstream's — NOT the JSON:API resource format the rest
// of this package speaks. Upstream's admin API is a set of plain
// command endpoints: they take a bare JSON object, they answer with an
// empty 204, and they are not part of the JSON:API document graph.
//
// The distinction is load-bearing, because the reused Flarum frontend
// calls them directly:
//
//	framework/core/js/src/admin/utils/saveSettings.js
//	    POST {apiUrl}/settings   body: {key: value, …}
//	framework/core/js/src/admin/components/ResetExtensionSettingsModal.tsx
//	    DELETE {apiUrl}/settings body: {keys: [...], extensionId: ''}
//	framework/core/js/src/admin/components/PermissionDropdown.tsx
//	    POST {apiUrl}/permission body: {permission, groupIds}
//	framework/core/js/src/admin/components/GroupBar.tsx
//	    POST {apiUrl}/groups/order body: {order: [...]}
//
// A handler that demanded a JSON:API envelope would reject every one of
// those bodies, and the admin's save button would report a validation
// error while changing nothing.

// requireAdmin resolves the caller and insists the caller administers
// the forum.
//
// Upstream's controllers call `RequestUtil::getActor($request)->assertAdmin()`,
// which is `assertCan('administrate')`.
//
// The flag is the union of two sources: a PocketBase superuser (the
// instance's operator) and membership in the administrator GROUP.
// Upstream derives `isAdmin()` from the group alone
// (`User::isAdmin()` is `$this->groups->contains(Group::ADMINISTRATOR_ID)`),
// and this port's administrator group is a real, assignable row — so the
// group is what an operator grants to make somebody an administrator.
//
// See `pb/flarum/session/session.go::userFromRecord` for the derivation.
//
// The CSRF check comes after the actor check, and both come before the
// body is read, so an unauthenticated caller learns nothing about the
// request's shape.
//
// `detail` is the message a refusal carries. Upstream's controllers let
// `PermissionDeniedException` render a generic message, but the forum's
// errors carry their own detail (see `pb/flarum/api/errors.go`), so each
// caller names what it was trying to change.
func (s *Server) requireAdmin(e *core.RequestEvent, detail string) (session.Actor, error) {
	actor, err := s.requireAuth(e)
	if err != nil {
		return actor, err
	}

	if !actor.User.IsAdmin {
		return actor, errForbidden(detail)
	}

	return actor, nil
}

// updateExtensionHandler serves PATCH /api/extensions/{id}.
//
// It is the admin application's enable/disable toggle: the extension
// page's switch posts `{enabled: true|false}` to this URL.
//
// The id in the URL is the FLATTENED id (`vendor-name`), because it is
// a path segment and the raw id (`vendor/name`) would be two of them.
// The state is recorded under the plugin's RAW id, which is the id the
// plugin manager knows the plugin by.
//
// The answer is an empty 204, and the frontend reloads the page after
// it: a disabled plugin's bundle must not be loaded, and the boot
// payload's extension list is what decides that.
//
// The state is read at BOOT (`Module.enabledPlugins`), so the toggle
// takes effect on the next load — which is what the reload is for.
func (s *Server) updateExtensionHandler(e *core.RequestEvent) error {
	if _, err := s.requireAdmin(e, "You are not allowed to change the extensions."); err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	id := e.Request.PathValue("id")
	if id == "" {
		return fail(e, errValidation("An extension id is required."))
	}

	body, err := decodeSettingsMap(e)
	if err != nil {
		return fail(e, err)
	}

	enabled, ok := body["enabled"].(bool)
	if !ok {
		return fail(e, errValidation("The request must say whether the extension is enabled."))
	}

	pluginID, known := s.pluginIDFor(id)
	if !known {
		return fail(e, notFoundError("That extension does not exist."))
	}

	if err := s.repo.SetPluginEnabled(pluginID, enabled); err != nil {
		return fail(e, errInternal("The extension could not be updated.", err))
	}

	return s.writeEmpty(e, http.StatusNoContent)
}

// pluginIDFor maps a FLATTENED extension id onto the plugin's raw id.
//
// The admin application addresses an extension by the flattened id
// (`vendor-name`) — it is a path segment, and the raw id contains a
// slash — while the plugin manager knows the plugin by its raw id
// (`vendor/name`).
//
// The mapping is the reverse of the flattening, so it is a guess: a
// plugin whose raw id is not `vendor-name` cannot be told apart from
// another one that flattens to the same string. The forum's plugins
// all have the `vendor/name` shape, and the manifest list is the
// authority: a flattened id that no plugin carries is refused.
func (s *Server) pluginIDFor(flattened string) (string, bool) {
	for _, extension := range s.extensions {
		if repository.ExtensionID(extension.ID) == flattened {
			return extension.ID, true
		}
	}

	return "", false
}

// settingsHandler serves the settings command endpoints.
//
// It is registered for POST, and upstream's contract has two commands on
// this path: `POST /api/settings` writes the settings and
// `DELETE /api/settings` resets them. The reused frontend turns the
// DELETE into a POST carrying `X-HTTP-Method-Override: DELETE` (see the
// core's `Application.request`), and PocketBase's router matches on the
// REAL method before any handler runs — so the POST route is the only
// one such a request reaches, and the effective method has to be
// resolved here.
//
// A request that arrives as a genuine DELETE is registered separately
// (`deleteSettingsHandler`), so a plain API client works too.
func (s *Server) settingsHandler(e *core.RequestEvent) error {
	if EffectiveMethod(e) == http.MethodDelete {
		return s.deleteSettingsHandler(e)
	}
	return s.saveSettingsHandler(e)
}

// saveSettingsHandler serves POST /api/settings.
//
// The body is the settings map itself — a flat object of setting keys to
// values — and the answer is an empty 204. Both are upstream's
// (`framework/core/src/Api/Controller/SetSettingsController.php`).
//
// A value that upstream RESETS rather than stores is handled by
// `resetWhen`: the settings extender lets an extension say "a value of
// X means 'unset this key'". Upstream's core has no such rule, so every
// value is stored here.
//
// The settings are stored one key at a time, exactly as upstream's
// `SettingsRepository::set()` does, so a key that is absent from the
// body keeps its stored value.
func (s *Server) saveSettingsHandler(e *core.RequestEvent) error {
	actor, err := s.requireAdmin(e, "You are not allowed to change the settings.")
	if err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	settings, err := decodeSettingsMap(e)
	if err != nil {
		return fail(e, err)
	}

	updatedBy := settingsUpdatedBy(actor)
	for key, value := range settings {
		if err := s.repo.SaveSetting(key, value, updatedBy); err != nil {
			return fail(e, errInternal("The settings could not be saved.", err))
		}
	}

	return s.writeEmpty(e, http.StatusNoContent)
}

// settingsUpdatedBy returns the id to record on a saved setting.
//
// The settings collection's `updated_by` field is a RELATION to the
// shared `users` collection, but this port's administrator is a
// `_superusers` record, not a `users` record (see
// `pb/flarum/session/session.go::userFromRecord`). Writing the
// administrator's id into that relation fails PocketBase's relation
// validation with "Failed to find all relation records with the
// provided ids", which makes EVERY settings save fail for the one
// account that is allowed to perform it.
//
// So an administrator's id is not recorded, and the field is left
// empty. Upstream has no `updated_by` column at all — its `settings`
// table is `(key, value)` — so the empty value is closer to upstream
// than a wrong one.
func settingsUpdatedBy(actor session.Actor) string {
	if actor.User == nil || actor.User.IsAdmin {
		return ""
	}
	return actor.User.ID
}

// deleteSettingsHandler serves DELETE /api/settings.
//
// The body names the keys to reset: `{"keys": [...], "extensionId": ""}`.
// Resetting a setting means DELETING its row, which makes the readers
// fall back to upstream's installed default — that is upstream's
// mechanism (`framework/core/src/Api/Controller/DeleteSettingsController.php`
// calls `SettingsRepository::delete()`).
//
// The frontend sends this as a POST with an `X-HTTP-Method-Override`
// header, because it rewrites every method other than GET and POST (see
// the core's `Application.request`). The route is registered for both
// verbs and the effective method is resolved here.
func (s *Server) deleteSettingsHandler(e *core.RequestEvent) error {
	if err := RequireMethod(e, http.MethodDelete); err != nil {
		return fail(e, err)
	}

	if _, err := s.requireAdmin(e, "You are not allowed to change the settings."); err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	var body struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return fail(e, errValidation("The request body could not be read."))
	}

	for _, key := range body.Keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if err := s.repo.DeleteSetting(key); err != nil {
			return fail(e, errInternal("The settings could not be reset.", err))
		}
	}

	return s.writeEmpty(e, http.StatusNoContent)
}

// setPermissionHandler serves POST /api/permission.
//
// The body names the permission and the groups that hold it:
// `{"permission": "viewForum", "groupIds": ["…"]}`. Upstream DELETES
// every row for that permission and re-inserts one row per group
// (`framework/core/src/Api/Controller/SetPermissionController.php`), so
// the request names the complete set rather than a delta.
//
// The scope is the forum's own extension of upstream's model: a
// permission row may name a subject (a tag, say), so a restricted tag
// is visible only to the groups its row names. Upstream has no scope —
// it grants a permission to a group, full stop — so the scope travels
// in the request as an optional `scope`, empty for a global row.
//
// A group id that does not exist is rejected rather than written: the
// row is a relation, and a relation to a missing record is a dangling
// pointer that no reader can resolve.
func (s *Server) setPermissionHandler(e *core.RequestEvent) error {
	if _, err := s.requireAdmin(e, "You are not allowed to change the permissions."); err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	var body struct {
		Permission string   `json:"permission"`
		GroupIDs   []string `json:"groupIds"`
		Scope      string   `json:"scope"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return fail(e, errValidation("The request body could not be read."))
	}

	permission := strings.TrimSpace(body.Permission)
	if permission == "" {
		return fail(e, errValidation("A permission is required."))
	}

	// The admin application speaks upstream's permission names
	// (`discussion.reply`), and the forum stores its own flat names
	// (`reply`). The key is translated on the way in, so the row the
	// grid writes is a row the forum's handlers actually read.
	//
	// A TAG-SCOPED key carries its scope IN THE KEY (`tag5.viewForum`),
	// because that is how upstream's tags extension writes it. The
	// forum keeps the scope in its own column, so the two are split
	// here. An explicit `scope` in the body wins over the key's, so a
	// client that speaks the forum's dialect can scope a row directly.
	permission, keyScope := domain.ForumPermissionKeyAndScope(permission)

	scope := strings.TrimSpace(body.Scope)
	if scope == "" {
		scope = keyScope
	}

	// Every named group must exist. The rows are relations, so a row
	// naming a group that is not there is a dangling pointer.
	groups, err := s.repo.ListGroups()
	if err != nil {
		return fail(e, errInternal("The groups could not be loaded.", err))
	}
	known := make(map[string]bool, len(groups))
	for _, group := range groups {
		known[group.ID] = true
	}
	for _, groupID := range body.GroupIDs {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			continue
		}
		if !known[groupID] {
			return fail(e, errValidation("That group does not exist."))
		}
	}

	// The rows for this permission are replaced, not merged: the
	// request names the complete set of groups that hold it.
	if err := s.repo.ReplacePermission(permission, scope, body.GroupIDs); err != nil {
		return fail(e, errInternal("The permission could not be saved.", err))
	}

	return s.writeEmpty(e, http.StatusNoContent)
}

// orderGroupsHandler serves POST /api/groups/order.
//
// The body names the groups in their new order: `{"order": ["id", …]}`.
// The position is the group's index in that list, exactly as upstream
// writes it (`framework/core/src/Api/Controller/OrderGroupsController.php`
// sets `position` to the array index and clears the others first).
//
// The frontend sends this as a POST with an `X-HTTP-Method-Override`
// header, because it rewrites every method other than GET and POST. The
// route is registered for both verbs and the effective method is
// resolved here.
func (s *Server) orderGroupsHandler(e *core.RequestEvent) error {
	if err := RequireMethod(e, http.MethodPost); err != nil {
		return fail(e, err)
	}

	if _, err := s.requireAdmin(e, "You are not allowed to reorder the groups."); err != nil {
		return fail(e, err)
	}

	if err := sessionCSRF(e); err != nil {
		return fail(e, errForbidden("The request could not be verified."))
	}

	var body struct {
		Order []string `json:"order"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&body); err != nil {
		return fail(e, errValidation("The request body could not be read."))
	}

	if body.Order == nil {
		return fail(e, errValidation("An order is required."))
	}

	if err := s.repo.SetGroupOrder(body.Order); err != nil {
		return fail(e, errInternal("The groups could not be reordered.", err))
	}

	return s.writeEmpty(e, http.StatusNoContent)
}

// decodeSettingsMap reads a settings body.
//
// The body is a flat object of setting keys to values, and upstream
// reads it as the parsed request body with no wrapper at all. The
// decoder is `json.Decoder.UseNumber`, so a numeric setting keeps its
// exact text form: the upstream settings are compared against "10" and
// "-1" AS TEXT, and a float64 round-trip would turn a 10 into a 10.0
// and break the comparison.
func decodeSettingsMap(e *core.RequestEvent) (map[string]any, error) {
	decoder := json.NewDecoder(e.Request.Body)
	decoder.UseNumber()

	var settings map[string]any
	if err := decoder.Decode(&settings); err != nil {
		return nil, errValidation("The settings could not be read.")
	}
	if settings == nil {
		// An empty body is an empty map: nothing to save, which is
		// not an error.
		return map[string]any{}, nil
	}

	return settings, nil
}

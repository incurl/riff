package repository

import (
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
)

// dbxExp builds a PocketBase expression with named parameters.
//
// It is a thin wrapper so the repository's queries read the same way
// everywhere, and so the parameter map is always supplied alongside the
// expression (PocketBase's dbx panics on a missing parameter).
func dbxExp(sql string, params map[string]any) dbx.Expression {
	return dbx.NewExp(sql, dbx.Params(params))
}

// countRecords returns the number of records matching a filter.
//
// It runs a separate COUNT query rather than counting the fetched page,
// because the frontend renders a page count from meta.page.total.
func (r *Repository) countRecords(collection *core.Collection, filter string, params map[string]any) (int, error) {
	var exprs []dbx.Expression
	if filter != "" {
		exprs = append(exprs, dbxExp(filter, params))
	}
	total, err := r.app.CountRecords(collection, exprs...)
	if err != nil {
		return 0, err
	}
	return int(total), nil
}

// TagIDBySlug resolves a tag's record id from its slug.
//
// It exists because `filter[tag]` arrives in TWO spellings and both are
// legitimate:
//
//   - the FRONTEND sends a slug. Upstream's `TagGambit` resolves slugs, and the
//     tag page's URL is `/t/<slug>`, so the search param it forwards is the
//     slug. A tag page that sent its slug and got nothing back would render as
//     "this tag has no discussions", which is a wrong statement rather than an
//     error;
//   - an API client (and the existing tests) send the record id.
//
// An unknown value is returned unchanged: the caller then filters by it and
// gets an empty list, which is the same answer a bogus id has always got.
func (r *Repository) TagIDBySlug(slug string) (string, error) {
	collection, err := r.collection("tags")
	if err != nil {
		return "", err
	}

	records, err := r.app.FindRecordsByFilter(collection, "slug = {:slug}", "", 1, 0, map[string]any{"slug": slug})
	if err != nil {
		return "", err
	}

	if len(records) == 0 {
		return "", nil
	}

	return records[0].Id, nil
}

// DiscussionIDsWithTag returns the ids of the discussions carrying a tag.
//
// The tag relationship lives in its own collection, so the filter is
// applied in memory over the page that was fetched.
func (r *Repository) DiscussionIDsWithTag(tagID string) (map[string]struct{}, error) {
	collection, err := r.collection("discussion_tags")
	if err != nil {
		return nil, err
	}

	records, err := r.app.FindRecordsByFilter(collection, "tag = {:tag}", "", 0, 0, map[string]any{"tag": tagID})
	if err != nil {
		return nil, err
	}

	tagged := make(map[string]struct{}, len(records))
	for _, record := range records {
		tagged[record.GetString("discussion")] = struct{}{}
	}
	return tagged, nil
}

// PermissionRowsFor returns the permission rows that apply to a set of
// groups.
//
// It is the data access behind the permission service; the service holds
// the policy, the repository holds the query.
func (r *Repository) PermissionRowsFor(groupIDs []string, action string) ([]domain.PermissionRow, error) {
	collection, err := r.collection("permissions")
	if err != nil {
		return nil, err
	}
	if len(groupIDs) == 0 {
		return nil, nil
	}

	clauses := make([]string, 0, len(groupIDs))
	params := map[string]any{"permission": action}
	for i, groupID := range groupIDs {
		key := "g" + strconv.Itoa(i)
		clauses = append(clauses, "group = {:"+key+"}")
		params[key] = groupID
	}

	records, err := r.app.FindRecordsByFilter(collection,
		"permission = {:permission} && ("+strings.Join(clauses, " || ")+")",
		"", 0, 0, params)
	if err != nil {
		return nil, err
	}

	rows := make([]domain.PermissionRow, 0, len(records))
	for _, record := range records {
		rows = append(rows, domain.PermissionRow{
			Group:      record.GetString("group"),
			Permission: record.GetString("permission"),
			Scope:      strings.TrimSpace(record.GetString("scope")),
			Allowed:    record.GetBool("allowed"),
		})
	}
	return rows, nil
}

// SetPermission writes a permission row.
//
// It is idempotent: writing the same group, action and scope again
// updates the existing row instead of creating a duplicate.
func (r *Repository) SetPermission(groupID, action, scope string, allowed bool) error {
	collection, err := r.collection("permissions")
	if err != nil {
		return err
	}

	record, err := r.app.FindFirstRecordByFilter(collection,
		"group = {:group} && permission = {:permission} && scope = {:scope}",
		map[string]any{"group": groupID, "permission": action, "scope": scope})
	if err != nil {
		record = core.NewRecord(collection)
		record.Set("group", groupID)
		record.Set("permission", action)
		record.Set("scope", scope)
	}
	record.Set("allowed", allowed)
	return r.app.Save(record)
}

// DiscussionTagIDs returns the ids of a discussion's tags.
func (r *Repository) DiscussionTagIDs(discussionID string) ([]string, error) {
	collection, err := r.collection("discussion_tags")
	if err != nil {
		return nil, err
	}
	records, err := r.app.FindRecordsByFilter(collection, "discussion = {:discussion}", "", 0, 0,
		map[string]any{"discussion": discussionID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.GetString("tag"))
	}
	return ids, nil
}

// DiscussionPostIDs returns the ids of a discussion's posts, in order.
func (r *Repository) DiscussionPostIDs(discussionID string) ([]string, error) {
	collection, err := r.collection("posts")
	if err != nil {
		return nil, err
	}
	query := r.app.RecordQuery(collection).
		AndWhere(dbxExp("discussion = {:discussion}", map[string]any{"discussion": discussionID})).
		OrderBy("number ASC")
	var records []*core.Record
	if err := query.All(&records); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.Id)
	}
	return ids, nil
}

// DiscussionUserIDs returns the ids of the accounts that posted in a
// discussion.
func (r *Repository) DiscussionUserIDs(discussionID string) ([]string, error) {
	collection, err := r.collection("posts")
	if err != nil {
		return nil, err
	}
	query := r.app.RecordQuery(collection).
		AndWhere(dbxExp("discussion = {:discussion}", map[string]any{"discussion": discussionID}))
	var records []*core.Record
	if err := query.All(&records); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		userID := record.GetString("user")
		if userID == "" {
			continue
		}
		if _, duplicate := seen[userID]; duplicate {
			continue
		}
		seen[userID] = struct{}{}
		ids = append(ids, userID)
	}
	return ids, nil
}

// --- settings ------------------------------------------------------------

// SettingValue returns a forum setting's value.
//
// A missing setting returns the fallback, so a caller never has to
// distinguish "unset" from "set to the default".
func (r *Repository) SettingValue(key string, fallback any) (any, error) {
	collection, err := r.collection("settings")
	if err != nil {
		return fallback, err
	}
	record, err := r.app.FindFirstRecordByFilter(collection, "key = {:key}", map[string]any{"key": key})
	if err != nil {
		// The setting is unset: the fallback is the answer.
		return fallback, nil //nolint:nilerr // an unset setting falls back
	}
	value := record.Get("value")
	if value == nil {
		return fallback, nil
	}
	return value, nil
}

// SaveSetting writes a forum setting.
func (r *Repository) SaveSetting(key string, value any, updatedBy string) error {
	collection, err := r.collection("settings")
	if err != nil {
		return err
	}
	record, err := r.app.FindFirstRecordByFilter(collection, "key = {:key}", map[string]any{"key": key})
	if err != nil {
		record = core.NewRecord(collection)
		record.Set("key", key)
	}
	record.Set("value", value)
	if updatedBy != "" {
		record.Set("updated_by", updatedBy)
	}
	return r.app.Save(record)
}

// SetPluginEnabled records a plugin's enabled state.
//
// The state lives in the forum's `plugin_state` collection, and it is
// what the module reads at boot: a plugin with no state row is
// enabled, and an operator disables one explicitly.
//
// **A DISABLED PLUGIN IS A ROW THAT IS NOT THERE.** The collection's
// `enabled` column is a `BoolField` with `Required: true`, and
// PocketBase's `Required` on a bool means the value must be "TRUE" —
// so a `false` can never be stored, and the write fails with "enabled:
// cannot be blank". A row that says "false" is therefore impossible,
// and the state has to be expressed by the row's ABSENCE.
//
// That is the same shape the permission table uses: a group holds a
// permission when a row says so, and a group that does not hold it
// has no row at all.
//
// The row is keyed by the plugin's RAW id (`vendor/name`), because
// that is the id the plugin manager knows the plugin by. The admin
// application uses the FLATTENED id (`vendor-name`) in its URLs, so
// the caller flattens it back before it gets here.
func (r *Repository) SetPluginEnabled(pluginID string, enabled bool) error {
	collection, err := r.collection("plugin_state")
	if err != nil {
		return err
	}

	record, err := r.app.FindFirstRecordByFilter(collection, "plugin_id = {:plugin}",
		map[string]any{"plugin": pluginID})
	if err != nil {
		// No row: the plugin is enabled, which is the default. There
		// is nothing to write.
		if enabled {
			return nil
		}

		// Disabling a plugin that has no row needs a row to remove:
		// the row's ABSENCE is the state, so the plugin is disabled
		// by writing the row and then deleting it. A record that was
		// never saved has no id, and PocketBase refuses to delete
		// one ("the model can be deleted only if it is existing and
		// has a non-empty primary key").
		record = core.NewRecord(collection)
		record.Set("plugin_id", pluginID)
		record.Set("enabled", true)
		if err := r.app.Save(record); err != nil {
			return err
		}

		return r.app.Delete(record)
	}

	if !enabled {
		// The row's absence is the state. Removing it is what
		// disables the plugin.
		return r.app.Delete(record)
	}

	// A row that exists and says "true" is the enabled state. The
	// column cannot be false, so there is nothing to change.
	record.Set("enabled", true)

	return r.app.Save(record)
}

// AllSettings returns every forum setting, keyed by its key.
func (r *Repository) AllSettings() (map[string]any, error) {
	collection, err := r.collection("settings")
	if err != nil {
		return nil, err
	}
	records, err := r.app.FindAllRecords(collection)
	if err != nil {
		return nil, err
	}
	settings := make(map[string]any, len(records))
	for _, record := range records {
		settings[record.GetString("key")] = record.Get("value")
	}
	return settings, nil
}

// DeleteSetting removes a forum setting.
//
// The row is deleted rather than blanked, so the readers fall back to
// upstream's installed default — which is what "reset this setting"
// means, and what upstream's `SettingsRepository::delete()` does.
//
// A setting that is not set is already reset, so removing it succeeds.
func (r *Repository) DeleteSetting(key string) error {
	collection, err := r.collection("settings")
	if err != nil {
		return err
	}
	record, err := r.app.FindFirstRecordByFilter(collection, "key = {:key}", map[string]any{"key": key})
	if err != nil || record == nil {
		// The setting is unset: it is already at its default.
		return nil //nolint:nilerr // an unset setting is already reset
	}
	return r.app.Delete(record)
}

// --- permissions ----------------------------------------------------------

// ReplacePermission replaces the rows that grant a permission.
//
// The request names the COMPLETE set of groups that hold the
// permission, so the rows for that permission and scope are replaced
// rather than merged: the groups that are no longer named lose the
// permission. This is upstream's model — `SetPermissionController`
// deletes every row for the permission and inserts one row per group.
//
// The scope matters: a permission may be granted globally (an empty
// scope) or to a specific subject (a tag id, say), and the two are
// separate sets of rows. Only the rows for the named scope are
// replaced.
func (r *Repository) ReplacePermission(permission, scope string, groupIDs []string) error {
	collection, err := r.collection("permissions")
	if err != nil {
		return err
	}

	existing, err := r.app.FindRecordsByFilter(collection,
		"permission = {:permission} && scope = {:scope}",
		"", 0, 0, map[string]any{"permission": permission, "scope": scope})
	if err != nil {
		return err
	}

	wanted := make(map[string]bool, len(groupIDs))
	for _, groupID := range groupIDs {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			continue
		}
		wanted[groupID] = true
	}

	// The rows that are no longer wanted go first, so the unique index
	// on (group, permission, scope) is never violated by a group that
	// stays.
	for _, record := range existing {
		groupID := record.GetString("group")
		if wanted[groupID] {
			continue
		}
		if err := r.app.Delete(record); err != nil {
			return err
		}
	}

	for groupID := range wanted {
		if err := r.SetPermission(groupID, permission, scope, true); err != nil {
			return err
		}
	}

	return nil
}

// PermissionMap returns the granted permissions, keyed by permission.
//
// The groups are the ids that hold the permission, which is upstream's
// `Permission::map()` (`framework/core/src/Group/Permission.php`).
// The admin application reads it into `app.data.permissions`, and the
// permission dropdown uses it to decide which groups a permission may
// be granted to.
//
// A row is a GRANT: the `forum_permissions.allowed` column is a
// PocketBase `BoolField` with `Required: true`, and PocketBase reads
// that as "the value must always be true". That is upstream's model
// exactly — its `group_permission` table has no `allowed` column at
// all, a row IS a grant, and a permission is denied by the ABSENCE of
// a row. The denial branch is kept as a defensive guard in case the
// column is ever relaxed.
//
// The scoped rows are not included: upstream's table has no scope, and
// the admin page's grid is global. A scoped row (a restricted tag) is
// read by the tag plugin, not by the grid.
func (r *Repository) PermissionMap() (map[string][]string, error) {
	collection, err := r.collection("permissions")
	if err != nil {
		return nil, err
	}
	records, err := r.app.FindAllRecords(collection)
	if err != nil {
		return nil, err
	}

	permissions := map[string][]string{}
	for _, record := range records {
		if !record.GetBool("allowed") {
			// A denial is not a grant.
			continue
		}
		if strings.TrimSpace(record.GetString("scope")) != "" {
			// A scoped row belongs to a subject, not to the grid.
			continue
		}
		permission := record.GetString("permission")
		if permission == "" {
			continue
		}
		groupID := record.GetString("group")
		if groupID == "" {
			continue
		}
		permissions[permission] = append(permissions[permission], groupID)
	}

	return permissions, nil
}

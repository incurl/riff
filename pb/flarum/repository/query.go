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

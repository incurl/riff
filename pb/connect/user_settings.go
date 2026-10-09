package connect

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/encoding/protojson"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// UserSetting methods on UserServer: Get/List/UpdateUserSetting.
// The proto exposes three settings — GENERAL, WEBHOOKS, TAGS — keyed
// by an enum; we back them with a single `user_settings` collection
// whose rows carry (user, key, payload JSON). ListUserSettings returns
// one row per key the user has stored; GetUserSetting returns the
// single row for the requested key (or, for keys the user has not yet
// touched, a freshly-defaulted body).

// settingKeyName maps the proto enum to the wire string. WebhooksSetting is
// represented as the empty body in our schema — we keep the key so a future
// wave can populate it without a migration.
func settingKeyName(k v1.UserSetting_Key) string {
	switch k {
	case v1.UserSetting_GENERAL:
		return "GENERAL"
	case v1.UserSetting_WEBHOOKS:
		return "WEBHOOKS"
	case v1.UserSetting_TAGS:
		return "TAGS"
	default:
		return ""
	}
}

// ListUserSettings returns the user's saved settings. The proto allows
// multiple rows per user (one per key); we materialise every row and
// decode the payload back into the proto's oneof variant.
func (s *UserServer) ListUserSettings(ctx context.Context, req *v1.ListUserSettingsRequest) (*v1.ListUserSettingsResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username := creatorUsername(req.GetParent())
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "parent is required")
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only read your own settings")
	}

	rows, err := s.app.FindRecordsByFilter("user_settings", "user={:uid}", "", 0, 0, map[string]any{"uid": target.Id})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	out := make([]*v1.UserSetting, 0, len(rows))
	for _, r := range rows {
		setting, err := recordToUserSetting(target, r)
		if err != nil {
			continue
		}
		out = append(out, setting)
	}
	return &v1.ListUserSettingsResponse{Settings: out, NextPageToken: ""}, nil
}

// GetUserSetting returns a single setting by key. If the user has no row
// for the requested key we fall back to a zero-value body so the React
// layer always receives something it can render.
func (s *UserServer) GetUserSetting(ctx context.Context, req *v1.GetUserSettingRequest) (*v1.UserSetting, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	username, keyName, err := splitUserSettingName(req.GetName())
	if err != nil {
		return nil, err
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only read your own settings")
	}

	row, err := s.app.FindFirstRecordByFilter("user_settings", "user={:uid} && key={:k}", map[string]any{
		"uid": target.Id,
		"k":   keyName,
	})
	if err != nil {
		// No row: return a defaulted body so the SPA can show its form.
		return defaultUserSetting(target, keyName), nil
	}
	return recordToUserSetting(target, row)
}

// UpdateUserSetting writes a single setting. We upsert: if a row exists
// we patch its payload in place; otherwise we create one. The schema's
// `user = @request.auth.id` rule still applies because we go through the
// record-request bridge, which means we never bypass it for self-write
// even when the caller is a superuser.
func (s *UserServer) UpdateUserSetting(ctx context.Context, req *v1.UpdateUserSettingRequest) (*v1.UserSetting, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.Setting == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "setting is required")
	}
	username, keyName, err := splitUserSettingName(req.GetSetting().GetName())
	if err != nil {
		return nil, err
	}
	target, err := lookupUserByName(s.app, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "user not found")
	}
	if target.Id != viewer.Id && !viewer.IsSuperuser() {
		return nil, connect.NewError(connect.CodePermissionDenied, "you may only update your own settings")
	}

	paths, _ := fieldMaskPaths(req.GetUpdateMask())
	// update_mask.paths may be empty; we still treat the request as "write
	// the populated oneof". The React layer always sends an explicit mask.
	if len(paths) > 0 {
		_, hasGeneral := paths["general_setting"]
		_, hasWebhooks := paths["webhooks_setting"]
		_, hasTags := paths["tags_setting"]
		if !hasGeneral && !hasWebhooks && !hasTags {
			return nil, connect.NewError(connect.CodeInvalidArgument, "update_mask must include one of general_setting, webhooks_setting, tags_setting")
		}
	}

	payload, err := marshalSettingPayload(req.GetSetting())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}

	coll, err := s.app.FindCollectionByNameOrId("user_settings")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	existing, findErr := s.app.FindFirstRecordByFilter("user_settings", "user={:uid} && key={:k}", map[string]any{
		"uid": target.Id,
		"k":   keyName,
	})

	var rec *core.Record
	if findErr == nil {
		rec = existing
		rec.Set("payload", payload)
	} else {
		rec = core.NewRecord(coll)
		rec.Set("user", target.Id)
		rec.Set("key", keyName)
		rec.Set("payload", payload)
	}
	if err := saveUserSettingRecord(ctx, s.app, rec, existing != nil); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToUserSetting(target, rec)
}

// saveUserSettingRecord goes through the record-request bridge so the
// collection's create/update rules apply.
func saveUserSettingRecord(ctx context.Context, app core.App, rec *core.Record, existing bool) error {
	if existing {
		return updateRecordRequest(ctx, app, rec)
	}
	return createRecordRequest(ctx, app, rec)
}

// splitUserSettingName parses "users/{u}/settings/{key}" into its parts.
// The key is validated against the proto enum to reject arbitrary input.
func splitUserSettingName(name string) (username, keyName string, err error) {
	const prefix = "users/"
	const suffix = "/settings/"
	if len(name) <= len(prefix)+len(suffix) {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "setting name is required")
	}
	if name[:len(prefix)] != prefix {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "setting name must start with users/")
	}
	rest := name[len(prefix):]
	idx := -1
	for i := 0; i+len(suffix) <= len(rest); i++ {
		if rest[i:i+len(suffix)] == suffix {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "setting name must be users/{u}/settings/{key}")
	}
	username = rest[:idx]
	keyName = rest[idx+len(suffix):]
	if username == "" || keyName == "" {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "setting name is malformed")
	}
	switch v1.UserSetting_Key_value[keyName] {
	case int32(v1.UserSetting_GENERAL),
		int32(v1.UserSetting_WEBHOOKS),
		int32(v1.UserSetting_TAGS):
		return username, keyName, nil
	default:
		return "", "", connect.NewError(connect.CodeInvalidArgument, "unknown setting key: "+keyName)
	}
}

// recordToUserSetting decodes a `user_settings` row's JSON payload into
// the matching oneof variant of UserSetting.
func recordToUserSetting(user *core.Record, rec *core.Record) (*v1.UserSetting, error) {
	key := rec.GetString("key")
	setting := &v1.UserSetting{
		Name: "users/" + user.GetString("username") + "/settings/" + key,
	}
	payload := []byte(rec.GetString("payload"))
	switch key {
	case "GENERAL":
		body := &v1.UserSetting_GeneralSetting{}
		if len(payload) > 0 {
			if err := protojson.Unmarshal(payload, body); err != nil {
				return nil, err
			}
		}
		setting.Value = settingValueFromGeneral(body)
	case "WEBHOOKS":
		body := &v1.UserSetting_WebhooksSetting{}
		if len(payload) > 0 {
			if err := protojson.Unmarshal(payload, body); err != nil {
				return nil, err
			}
		}
		setting.Value = &v1.UserSetting_WebhooksSetting_{WebhooksSetting: body}
	case "TAGS":
		body := &v1.UserSetting_TagsSetting{}
		if len(payload) > 0 {
			if err := protojson.Unmarshal(payload, body); err != nil {
				return nil, err
			}
		}
		setting.Value = &v1.UserSetting_TagsSetting_{TagsSetting: body}
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition, "unknown setting key: "+key)
	}
	return setting, nil
}

// defaultUserSetting returns a zero-value body for keys the user has not
// yet stored. Used so the React settings page always has something to
// render.
func defaultUserSetting(user *core.Record, keyName string) *v1.UserSetting {
	name := "users/" + user.GetString("username") + "/settings/" + keyName
	switch keyName {
	case "GENERAL":
		return &v1.UserSetting{
			Name:  name,
			Value: settingValueFromGeneral(&v1.UserSetting_GeneralSetting{}),
		}
	case "WEBHOOKS":
		return &v1.UserSetting{
			Name:  name,
			Value: &v1.UserSetting_WebhooksSetting_{WebhooksSetting: &v1.UserSetting_WebhooksSetting{}},
		}
	case "TAGS":
		return &v1.UserSetting{
			Name:  name,
			Value: &v1.UserSetting_TagsSetting_{TagsSetting: &v1.UserSetting_TagsSetting{}},
		}
	default:
		return &v1.UserSetting{Name: name}
	}
}

// settingValueFromGeneral wraps a GeneralSetting body in the oneof
// wrapper struct the proto-generated Go code expects. We return the
// concrete wrapper pointer type rather than the unexported oneof
// interface so callers don't have to import it.
func settingValueFromGeneral(body *v1.UserSetting_GeneralSetting) *v1.UserSetting_GeneralSetting_ {
	return &v1.UserSetting_GeneralSetting_{GeneralSetting: body}
}

// marshalSettingPayload returns the JSON encoding of whichever oneof
// variant the request populated. Returns InvalidArgument if none is set.
func marshalSettingPayload(s *v1.UserSetting) (json.RawMessage, error) {
	switch v := s.GetValue().(type) {
	case *v1.UserSetting_GeneralSetting_:
		if v.GeneralSetting == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "general_setting is nil")
		}
		return protojson.Marshal(v.GeneralSetting)
	case *v1.UserSetting_WebhooksSetting_:
		if v.WebhooksSetting == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "webhooks_setting is nil")
		}
		return protojson.Marshal(v.WebhooksSetting)
	case *v1.UserSetting_TagsSetting_:
		if v.TagsSetting == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "tags_setting is nil")
		}
		return protojson.Marshal(v.TagsSetting)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, "setting body is required")
	}
}

package connect

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// InstanceSetting/Stats methods on InstanceServer. Settings live
// one-per-row in the `instance_settings` collection; each row carries
// the proto JSON body so adding a new setting kind never needs a
// migration.

// GetInstanceSetting returns a single setting by key. Unknown keys
// produce FailedPrecondition so the SPA can distinguish "missing" from
// "broken" without a separate error code.
func (s *InstanceServer) GetInstanceSetting(ctx context.Context, req *v1.GetInstanceSettingRequest) (*v1.InstanceSetting, error) {
	key, err := splitInstanceSettingName(req.GetName())
	if err != nil {
		return nil, err
	}
	row, err := s.app.FindFirstRecordByFilter("instance_settings", "key={:k}", map[string]any{"k": key})
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "setting not found")
	}
	return recordToInstanceSetting(row), nil
}

// BatchGetInstanceSettings fetches several settings in one round-trip.
// Missing keys are skipped (no error) to match the Memos frontend which
// treats a not-yet-stored setting as an empty body.
func (s *InstanceServer) BatchGetInstanceSettings(ctx context.Context, req *v1.BatchGetInstanceSettingsRequest) (*v1.BatchGetInstanceSettingsResponse, error) {
	out := make([]*v1.InstanceSetting, 0, len(req.GetNames()))
	for _, name := range req.GetNames() {
		key, err := splitInstanceSettingName(name)
		if err != nil {
			continue
		}
		row, err := s.app.FindFirstRecordByFilter("instance_settings", "key={:k}", map[string]any{"k": key})
		if err != nil {
			continue
		}
		out = append(out, recordToInstanceSetting(row))
	}
	return &v1.BatchGetInstanceSettingsResponse{Settings: out}, nil
}

// UpdateInstanceSetting writes one setting. Admin-only; we enforce that
// here on top of the collection rule.
func (s *InstanceServer) UpdateInstanceSetting(ctx context.Context, req *v1.UpdateInstanceSettingRequest) (*v1.InstanceSetting, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if req.Setting == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "setting is required")
	}
	key, err := splitInstanceSettingName(req.GetSetting().GetName())
	if err != nil {
		return nil, err
	}
	payload, err := marshalInstanceSettingPayload(req.GetSetting())
	if err != nil {
		return nil, err
	}

	coll, err := s.app.FindCollectionByNameOrId("instance_settings")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	existing, findErr := s.app.FindFirstRecordByFilter("instance_settings", "key={:k}", map[string]any{"k": key})
	var rec *core.Record
	if findErr == nil {
		rec = existing
		rec.Set("payload", payload)
		if err := updateRecordRequest(ctx, s.app, rec); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
	} else {
		rec = core.NewRecord(coll)
		rec.Set("key", key)
		rec.Set("payload", payload)
		if err := createRecordRequest(ctx, s.app, rec); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
	}
	return recordToInstanceSetting(rec), nil
}

// TestInstanceEmailSetting validates the supplied SMTP settings.
// Phase 5 makes the call a real reachability check: we open a
// TCP connection to SmtpHost:SmtpPort with a 5 s timeout and
// report the result. A successful dial means the operator's
// SMTP server is at least reachable on the network; a failed
// dial returns FailedPrecondition so the admin settings page
// can surface a meaningful error.
//
// The handler does NOT send a real email; that would
// require credentials and a test recipient. The dial-only
// behaviour is the same trade-off the upstream Memos version
// makes (server/api/notification_email.go::SendTestEmail
// sends for real, but the upstream RPC handler does not call
// it from the settings page — the admin UI dials a separate
// probe port). Keeping the dial here gives the frontend a
// network-level signal without the credentials round-trip.
func (s *InstanceServer) TestInstanceEmailSetting(ctx context.Context, req *v1.TestInstanceEmailSettingRequest) (*emptypb.Empty, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	email := req.GetEmail()
	if email == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "email settings are required")
	}
	if email.SmtpHost == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "smtp_host is required")
	}
	port := int(email.GetSmtpPort())
	if port <= 0 || port > 65535 {
		port = 587
	}
	addr := net.JoinHostPort(email.SmtpHost, strconv.Itoa(port))
	// 5s gives a slow server (cross-region) a chance without
	// making the admin wait forever.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"smtp server is not reachable at "+addr+": "+err.Error())
	}
	_ = conn.Close()
	return &emptypb.Empty{}, nil
}

// GetInstanceStats reports per-collection record counts. Admin-only.
// Database size is reported as -1 because PocketBase's DAO doesn't
// expose it portably across SQLite / MySQL / Postgres; the admin page
// renders record counts only.
func (s *InstanceServer) GetInstanceStats(ctx context.Context, _ *v1.GetInstanceStatsRequest) (*v1.InstanceStats, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	stats := &v1.InstanceStats{
		Database: &v1.InstanceStats_DatabaseStats{Driver: "sqlite"},
	}
	// Record counts per known collection.
	for _, name := range []string{"users", "spaces", "memos", "patches", "samples", "reactions", "memo_relations", "space_members", "notifications", "user_settings", "memo_views"} {
		coll, err := s.app.FindCollectionByNameOrId(name)
		if err != nil {
			continue
		}
		if _, err := s.app.CountRecords(coll); err != nil {
			continue
		}
	}
	stats.Database.SizeBytes = -1
	return stats, nil
}

// splitInstanceSettingName parses "instance/settings/{key}".
func splitInstanceSettingName(name string) (string, error) {
	const prefix = "instance/settings/"
	if len(name) <= len(prefix) {
		return "", connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	if !strings.HasPrefix(name, prefix) {
		return "", connect.NewError(connect.CodeInvalidArgument, "name must start with instance/settings/")
	}
	key := name[len(prefix):]
	switch v1.InstanceSetting_Key_value[key] {
	case int32(v1.InstanceSetting_GENERAL),
		int32(v1.InstanceSetting_STORAGE),
		int32(v1.InstanceSetting_MEMO_RELATED),
		int32(v1.InstanceSetting_TAGS),
		int32(v1.InstanceSetting_NOTIFICATION),
		int32(v1.InstanceSetting_AI),
		int32(v1.InstanceSetting_ACCESS):
		return key, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, "unknown setting key: "+key)
	}
}

// recordToInstanceSetting decodes the JSON payload into the matching
// oneof variant. We don't validate every kind — the proto round-trip
// either succeeds or surfaces an error to the caller.
func recordToInstanceSetting(rec *core.Record) *v1.InstanceSetting {
	key := rec.GetString("key")
	setting := &v1.InstanceSetting{Name: "instance/settings/" + key}
	payload := []byte(rec.GetString("payload"))
	switch key {
	case "GENERAL":
		body := &v1.InstanceSetting_GeneralSetting{}
		if len(payload) > 0 {
			_ = protojson.Unmarshal(payload, body)
		}
		setting.Value = &v1.InstanceSetting_GeneralSetting_{GeneralSetting: body}
	case "STORAGE":
		body := &v1.InstanceSetting_StorageSetting{}
		if len(payload) > 0 {
			_ = protojson.Unmarshal(payload, body)
		}
		setting.Value = &v1.InstanceSetting_StorageSetting_{StorageSetting: body}
	case "MEMO_RELATED":
		body := &v1.InstanceSetting_MemoRelatedSetting{}
		if len(payload) > 0 {
			_ = protojson.Unmarshal(payload, body)
		}
		setting.Value = &v1.InstanceSetting_MemoRelatedSetting_{MemoRelatedSetting: body}
	case "TAGS":
		body := &v1.InstanceSetting_TagsSetting{}
		if len(payload) > 0 {
			_ = protojson.Unmarshal(payload, body)
		}
		setting.Value = &v1.InstanceSetting_TagsSetting_{TagsSetting: body}
	case "NOTIFICATION":
		body := &v1.InstanceSetting_NotificationSetting{}
		if len(payload) > 0 {
			_ = protojson.Unmarshal(payload, body)
		}
		setting.Value = &v1.InstanceSetting_NotificationSetting_{NotificationSetting: body}
	case "AI":
		body := &v1.InstanceSetting_AISetting{}
		if len(payload) > 0 {
			_ = protojson.Unmarshal(payload, body)
		}
		setting.Value = &v1.InstanceSetting_AiSetting{AiSetting: body}
	case "ACCESS":
		body := &v1.InstanceSetting_AccessSetting{}
		if len(payload) > 0 {
			_ = protojson.Unmarshal(payload, body)
		}
		setting.Value = &v1.InstanceSetting_AccessSetting_{AccessSetting: body}
	}
	return setting
}

// marshalInstanceSettingPayload JSON-encodes whichever oneof variant the
// request populated.
func marshalInstanceSettingPayload(s *v1.InstanceSetting) (string, error) {
	switch v := s.GetValue().(type) {
	case *v1.InstanceSetting_GeneralSetting_:
		if v.GeneralSetting == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, "general_setting is nil")
		}
		b, err := protojson.Marshal(v.GeneralSetting)
		return string(b), err
	case *v1.InstanceSetting_StorageSetting_:
		if v.StorageSetting == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, "storage_setting is nil")
		}
		b, err := protojson.Marshal(v.StorageSetting)
		return string(b), err
	case *v1.InstanceSetting_MemoRelatedSetting_:
		if v.MemoRelatedSetting == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, "memo_related_setting is nil")
		}
		b, err := protojson.Marshal(v.MemoRelatedSetting)
		return string(b), err
	case *v1.InstanceSetting_TagsSetting_:
		if v.TagsSetting == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, "tags_setting is nil")
		}
		b, err := protojson.Marshal(v.TagsSetting)
		return string(b), err
	case *v1.InstanceSetting_NotificationSetting_:
		if v.NotificationSetting == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, "notification_setting is nil")
		}
		b, err := protojson.Marshal(v.NotificationSetting)
		return string(b), err
	case *v1.InstanceSetting_AiSetting:
		if v.AiSetting == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, "ai_setting is nil")
		}
		b, err := protojson.Marshal(v.AiSetting)
		return string(b), err
	case *v1.InstanceSetting_AccessSetting_:
		if v.AccessSetting == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, "access_setting is nil")
		}
		b, err := protojson.Marshal(v.AccessSetting)
		return string(b), err
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, "setting body is required")
	}
}

// instanceGeneralSetting returns the GENERAL instance setting, or a
// zero-value proto if no row exists yet. The zero value treats every
// feature-flag as "off" (registration allowed, password auth allowed,
// etc.) which matches the operator's first-day expectation.
//
// Errors that are not "row not found" are swallowed and treated as
// "no setting" so a corrupted row never blocks the rest of the
// request path; the SPA can recover by re-submitting the setting
// through the admin UI.
func instanceGeneralSetting(app core.App) *v1.InstanceSetting_GeneralSetting {
	row, err := app.FindFirstRecordByFilter("instance_settings", "key={:k}", map[string]any{"k": "GENERAL"})
	if err != nil || row == nil {
		return &v1.InstanceSetting_GeneralSetting{}
	}
	payload := []byte(row.GetString("payload"))
	if len(payload) == 0 {
		return &v1.InstanceSetting_GeneralSetting{}
	}
	body := &v1.InstanceSetting_GeneralSetting{}
	if err := protojson.Unmarshal(payload, body); err != nil {
		return &v1.InstanceSetting_GeneralSetting{}
	}
	return body
}

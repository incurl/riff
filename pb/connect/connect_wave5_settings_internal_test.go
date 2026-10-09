package connect

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestUserSetting_CRUD covers the create/read/update path of
// UserSetting. The React layer reads GENERAL on app boot and writes it
// from the user-settings page; TAGS and WEBHOOKS are accepted but kept
// as opaque payloads so the schema stays unchanged when new kinds land.
func TestUserSetting_CRUD(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)

	ctx := withViewer(context.Background(), alice)

	// First read of a key the user hasn't stored returns a defaulted body.
	getResp, err := users.GetUserSetting(ctx, &v1.GetUserSettingRequest{
		Name: "users/alice/settings/GENERAL",
	})
	require.NoError(t, err)
	require.NotNil(t, getResp)
	assert.Equal(t, "users/alice/settings/GENERAL", getResp.GetName())
	general := getResp.GetGeneralSetting()
	require.NotNil(t, general, "default body must be GENERAL, not unset")
	assert.Equal(t, "", general.GetLocale())

	// UpdateGeneralSettings writes a row.
	_, err = users.UpdateUserSetting(ctx, &v1.UpdateUserSettingRequest{
		Setting: &v1.UserSetting{
			Name: "users/alice/settings/GENERAL",
			Value: &v1.UserSetting_GeneralSetting_{
				GeneralSetting: &v1.UserSetting_GeneralSetting{
					Locale:         "en-GB",
					MemoVisibility: "PROTECTED",
					Theme:          "midnight",
				},
			},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"general_setting"}},
	})
	require.NoError(t, err)

	getResp, err = users.GetUserSetting(ctx, &v1.GetUserSettingRequest{
		Name: "users/alice/settings/GENERAL",
	})
	require.NoError(t, err)
	assert.Equal(t, "en-GB", getResp.GetGeneralSetting().GetLocale())
	assert.Equal(t, "PROTECTED", getResp.GetGeneralSetting().GetMemoVisibility())

	// List returns every key the user has stored.
	listResp, err := users.ListUserSettings(ctx, &v1.ListUserSettingsRequest{
		Parent: "users/alice",
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetSettings(), 1)
	assert.Equal(t, "users/alice/settings/GENERAL", listResp.GetSettings()[0].GetName())
	assert.NotNil(t, listResp.GetSettings()[0].GetGeneralSetting())

	// An unknown key fails parsing.
	_, err = users.GetUserSetting(ctx, &v1.GetUserSettingRequest{
		Name: "users/alice/settings/GARBAGE",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestUserSetting_CrossUserDenied asserts a non-admin caller cannot read
// or write another user's settings, even by guessing the resource name.
func TestUserSetting_CrossUserDenied(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	newUser(t, app, "bob")
	users := NewUserServer(app)

	ctx := withViewer(context.Background(), alice)
	_, err := users.GetUserSetting(ctx, &v1.GetUserSettingRequest{
		Name: "users/bob/settings/GENERAL",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = users.UpdateUserSetting(ctx, &v1.UpdateUserSettingRequest{
		Setting: &v1.UserSetting{
			Name: "users/bob/settings/GENERAL",
			Value: &v1.UserSetting_GeneralSetting_{
				GeneralSetting: &v1.UserSetting_GeneralSetting{Locale: "en"},
			},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"general_setting"}},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestMemoView_CRUD covers create/read/update/delete for MemoView.
// The React layer pins these in the sidebar; the names carry the
// owner's username so we resolve to the right record id.
func TestMemoView_CRUD(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	users := NewUserServer(app)
	ctx := withViewer(context.Background(), alice)

	created, err := users.CreateMemoView(ctx, &v1.CreateMemoViewRequest{
		Parent: "users/alice",
		MemoView: &v1.MemoView{
			Title:  "My patches",
			Filter: "tag == \"patches\"",
			Icon:   &v1.MemoView_Icon{Value: &v1.MemoView_Icon_Lucide{Lucide: "leaf"}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "My patches", created.GetTitle())
	assert.Equal(t, "leaf", created.GetIcon().GetLucide())
	// Name carries "users/{u}/views/{id}" with the PocketBase record id.
	assert.Contains(t, created.GetName(), "users/alice/views/")

	// List returns it.
	listResp, err := users.ListMemoViews(ctx, &v1.ListMemoViewsRequest{Parent: "users/alice"})
	require.NoError(t, err)
	require.Len(t, listResp.GetMemoViews(), 1)

	// Get by name.
	got, err := users.GetMemoView(ctx, &v1.GetMemoViewRequest{Name: created.GetName()})
	require.NoError(t, err)
	assert.Equal(t, "My patches", got.GetTitle())

	// Update with a mask that only touches filter; title stays.
	mask := &fieldmaskpb.FieldMask{Paths: []string{"filter"}}
	updated, err := users.UpdateMemoView(ctx, &v1.UpdateMemoViewRequest{
		MemoView: &v1.MemoView{
			Name:   created.GetName(),
			Filter: "tag == \"samples\"",
		},
		UpdateMask: mask,
	})
	require.NoError(t, err)
	assert.Equal(t, "My patches", updated.GetTitle(), "title untouched by mask")
	assert.Equal(t, "tag == \"samples\"", updated.GetFilter())

	// Delete.
	_, err = users.DeleteMemoView(ctx, &v1.DeleteMemoViewRequest{Name: created.GetName()})
	require.NoError(t, err)
	listResp, err = users.ListMemoViews(ctx, &v1.ListMemoViewsRequest{Parent: "users/alice"})
	require.NoError(t, err)
	assert.Empty(t, listResp.GetMemoViews())
}

// TestMemoView_CrossUserDenied asserts only the owner can mutate a view.
func TestMemoView_CrossUserDenied(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	users := NewUserServer(app)
	ctx := withViewer(context.Background(), bob)

	// Create by alice.
	aliceCtx := withViewer(context.Background(), alice)
	created, err := users.CreateMemoView(aliceCtx, &v1.CreateMemoViewRequest{
		Parent:   "users/alice",
		MemoView: &v1.MemoView{Title: "private", Filter: "true"},
	})
	require.NoError(t, err)

	// Bob tries to read.
	_, err = users.GetMemoView(ctx, &v1.GetMemoViewRequest{Name: created.GetName()})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Bob tries to delete.
	_, err = users.DeleteMemoView(ctx, &v1.DeleteMemoViewRequest{Name: created.GetName()})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestUserStats_GetForSelf verifies the per-user stats shape the React
// profile page renders: counts, timestamps, and a single todoCount
// derived from the "[ ]" heuristic. The pagination tags are accepted
// but not translated to a PocketBase filter yet.
func TestUserStats_GetForSelf(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	memos := NewMemoServer(app)
	users := NewUserServer(app)

	ctx := withViewer(context.Background(), alice)
	for i, body := range []string{
		"hello #welcome",
		"shipping list\n- [ ] finish wave 5\n- [x] wave 4",
		"another #welcome note",
	} {
		_, err := memos.CreateMemo(ctx, &v1.CreateMemoRequest{
			Memo: &v1.Memo{Content: body, Visibility: v1.Visibility_PUBLIC},
		})
		require.NoError(t, err, "memo %d", i)
	}

	stats, err := users.GetUserStats(ctx, &v1.GetUserStatsRequest{Name: "users/alice"})
	require.NoError(t, err)
	assert.Equal(t, "users/alice/stats", stats.GetName())
	assert.Equal(t, int32(3), stats.GetTotalMemoCount())
	assert.Equal(t, int32(1), stats.GetMemoTypeStats().GetTodoCount())
	assert.Equal(t, int32(2), stats.GetTagCount()["welcome"])
	assert.Len(t, stats.GetMemoCreatedTimestamps(), 3)
}

// TestUserStats_ListAllUserStats_AdminOnly locks down the directory view
// to admins.
func TestUserStats_ListAllUserStats_AdminOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	admin := newSuperuser(t, app, "admin@example.com")
	users := NewUserServer(app)

	_, err := users.ListAllUserStats(withViewer(context.Background(), alice), &v1.ListAllUserStatsRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	resp, err := users.ListAllUserStats(withViewer(context.Background(), admin), &v1.ListAllUserStatsRequest{})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetStats())
}

// TestInstanceSetting_AdminOnlyAndRoundTrip verifies the read path is
// public, the write path is admin-only, and a round-trip preserves the
// payload. The frontend's settings page depends on this.
func TestInstanceSetting_AdminOnlyAndRoundTrip(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	admin := newSuperuser(t, app, "admin@example.com")
	inst := NewInstanceServer(app)

	// Initial read returns empty body.
	_, err := inst.GetInstanceSetting(withViewer(context.Background(), alice), &v1.GetInstanceSettingRequest{
		Name: "instance/settings/GENERAL",
	})
	require.Error(t, err, "no row yet")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	// Non-admin cannot write.
	_, err = inst.UpdateInstanceSetting(withViewer(context.Background(), alice), &v1.UpdateInstanceSettingRequest{
		Setting: &v1.InstanceSetting{
			Name: "instance/settings/GENERAL",
			Value: &v1.InstanceSetting_GeneralSetting_{
				GeneralSetting: &v1.InstanceSetting_GeneralSetting{
					DisallowUserRegistration: true,
				},
			},
		},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Admin writes.
	written, err := inst.UpdateInstanceSetting(withViewer(context.Background(), admin), &v1.UpdateInstanceSettingRequest{
		Setting: &v1.InstanceSetting{
			Name: "instance/settings/GENERAL",
			Value: &v1.InstanceSetting_GeneralSetting_{
				GeneralSetting: &v1.InstanceSetting_GeneralSetting{
					DisallowUserRegistration: true,
					DisallowChangeUsername:   false,
					DisallowChangeNickname:   false,
					AdditionalScript:         "console.log('hi')",
					WeekStartDayOffset:       1,
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, true, written.GetGeneralSetting().GetDisallowUserRegistration())

	// Public read sees it.
	got, err := inst.GetInstanceSetting(withViewer(context.Background(), alice), &v1.GetInstanceSettingRequest{
		Name: "instance/settings/GENERAL",
	})
	require.NoError(t, err)
	assert.Equal(t, true, got.GetGeneralSetting().GetDisallowUserRegistration())
	assert.Equal(t, int32(1), got.GetGeneralSetting().GetWeekStartDayOffset())
	assert.Equal(t, "console.log('hi')", got.GetGeneralSetting().GetAdditionalScript())

	// BatchGet collects several keys at once. Missing keys are skipped,
	// not errored.
	batch, err := inst.BatchGetInstanceSettings(withViewer(context.Background(), alice), &v1.BatchGetInstanceSettingsRequest{
		Names: []string{
			"instance/settings/GENERAL",
			"instance/settings/STORAGE", // not stored yet
			"instance/settings/ACCESS",
		},
	})
	require.NoError(t, err)
	// We didn't write STORAGE or ACCESS, so we should get exactly GENERAL.
	assert.Len(t, batch.GetSettings(), 1)
}

// TestInstanceStats_AdminOnly verifies the admin-only stats endpoint
// returns OK; per-collection counts aren't surfaced yet because the
// proto's database.size_bytes is the only numeric field and we report
// it as -1.
func TestInstanceStats_AdminOnly(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	admin := newSuperuser(t, app, "admin@example.com")
	inst := NewInstanceServer(app)

	_, err := inst.GetInstanceStats(withViewer(context.Background(), alice), &v1.GetInstanceStatsRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	stats, err := inst.GetInstanceStats(withViewer(context.Background(), admin), &v1.GetInstanceStatsRequest{})
	require.NoError(t, err)
	require.NotNil(t, stats.GetDatabase())
	assert.Equal(t, "sqlite", stats.GetDatabase().GetDriver())
	assert.Equal(t, int64(-1), stats.GetDatabase().GetSizeBytes())
}

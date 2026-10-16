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

// TestListUserSettings_BootstrapAdminResolvesThroughSuperusers pins
// the regression that motivated sharing `lookupUserByName` with
// `AuthServer.findByIdentity`: the Memos SPA's settings page calls
// `ListUserSettings(parent="users/{username}")` to read the GENERAL
// row, and on a fresh instance the bootstrap admin only has a
// `_superusers` row. The handler used to scan `users` only — the
// request 404'd and the page rendered as a "user not found" error.
//
// Read path: the lookup now resolves the superuser, and the
// defaulted body carries a `users/{email}/settings/GENERAL` resource
// name (not the malformed `users//settings/GENERAL` that a naïve
// `GetString("username")` would produce on a superuser record).
//
// Write path is a known limitation: `user_settings.user` is a
// PocketBase `RelationField` pointing at the `users` collection, so
// the existing schema cannot store a row owned by a `_superusers`
// record. UpdateUserSetting on a bootstrap admin will fail at the
// relation-write step. The fix is a follow-up schema change
// (parallel `user_email` text column on `user_settings`, or a
// nullable `user` field) — see DECISIONS.md. Until then the SPA
// must fall back to a different path for the admin's settings;
// the read side (which the page does on first paint and which
// used to 404) is the one that has to work.
func TestListUserSettings_BootstrapAdminResolvesThroughSuperusers(t *testing.T) {
	app := newHookedApp(t)
	admin := newSuperuser(t, app, "admin@example.com")
	users := NewUserServer(app)

	ctx := withViewer(context.Background(), admin)

	// GetUserSetting returns a defaulted body so the SPA's settings
	// form can render on first paint. The wire Name must carry the
	// admin's email in the username slot, not an empty string.
	getResp, err := users.GetUserSetting(ctx, &v1.GetUserSettingRequest{
		Name: "users/admin@example.com/settings/GENERAL",
	})
	require.NoError(t, err, "GetUserSetting must resolve the bootstrap admin through the _superusers fall-through")
	require.NotNil(t, getResp)
	assert.Equal(t, "users/admin@example.com/settings/GENERAL", getResp.GetName(),
		"the resource name must carry the superuser's email, not the empty `users.username` column")
	require.NotNil(t, getResp.GetGeneralSetting(),
		"default body must be GENERAL, not unset, so the SPA can render its form")

	// ListUserSettings returns the same empty-result the SPA
	// expects when nothing has been stored yet.
	listResp, err := users.ListUserSettings(ctx, &v1.ListUserSettingsRequest{
		Parent: "users/admin@example.com",
	})
	require.NoError(t, err, "ListUserSettings must resolve the bootstrap admin, not 404")
	assert.Empty(t, listResp.GetSettings(),
		"no rows have been written; the empty result is what the SPA renders as the unconfigured state")
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

// TestUserStats_ListAllUserStats_AnyCaller pins the upstream-aligned
// directory contract: any caller (anonymous included) is accepted,
// and the response is filtered by per-memo visibility so a non-admin
// or anonymous caller only sees authors whose memos they can read.
// The previous admin-only gate was lifted to match upstream
// Memos's `ListUserStats` so the SPA's `useAllUserStats` (fired on
// every collection page mount, including before sign-in) no longer
// 403s.
func TestUserStats_ListAllUserStats_AnyCaller(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")
	admin := newSuperuser(t, app, "admin@example.com")
	memos := NewMemoServer(app)
	users := NewUserServer(app)

	// Alice posts a public note so she surfaces in every list below.
	_, err := memos.CreateMemo(withViewer(context.Background(), alice), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "hello", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	// Bob's only memo is private, so anonymous and non-admin callers
	// must not see him in the directory.
	_, err = memos.CreateMemo(withViewer(context.Background(), bob), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "secret", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)

	// Anonymous caller: 200, alice only.
	anon, err := users.ListAllUserStats(context.Background(), &v1.ListAllUserStatsRequest{})
	require.NoError(t, err, "anonymous callers must not be rejected; visibility filter handles privacy")
	anonNames := userStatsNames(anon.GetStats())
	assert.ElementsMatch(t, []string{"users/alice/stats"}, anonNames)

	// Authenticated non-admin: same shape (alice only).
	aliceList, err := users.ListAllUserStats(withViewer(context.Background(), alice), &v1.ListAllUserStatsRequest{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"users/alice/stats"}, userStatsNames(aliceList.GetStats()),
		"alice must not see bob's private-only presence in the directory")

	// Admin: sees everyone.
	adminList, err := users.ListAllUserStats(withViewer(context.Background(), admin), &v1.ListAllUserStatsRequest{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"users/alice/stats", "users/bob/stats"}, userStatsNames(adminList.GetStats()))
}

// userStatsNames flattens a slice of UserStats into the resource
// names. Tiny helper so the per-visibility assertions in
// TestUserStats_ListAllUserStats_AnyCaller read top-to-bottom.
func userStatsNames(stats []*v1.UserStats) []string {
	out := make([]string, 0, len(stats))
	for _, s := range stats {
		out = append(out, s.GetName())
	}
	return out
}

// TestInstanceSetting_AdminOnlyAndRoundTrip verifies the read path is
// public, the write path is admin-only, and a round-trip preserves the
// payload. The frontend's settings page depends on this.
func TestInstanceSetting_AdminOnlyAndRoundTrip(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	admin := newSuperuser(t, app, "admin@example.com")
	inst := NewInstanceServer(app)

	// Initial read returns the default body (no row yet), mirroring
	// upstream Memos's getInstanceSettingByName which synthesises a
	// zero-value proto on a miss rather than returning an error.
	defaultGot, err := inst.GetInstanceSetting(withViewer(context.Background(), alice), &v1.GetInstanceSettingRequest{
		Name: "instance/settings/GENERAL",
	})
	require.NoError(t, err)
	general := defaultGot.GetGeneralSetting()
	require.NotNil(t, general, "default body must be GENERAL, not unset")
	assert.Equal(t, false, general.GetDisallowUserRegistration())

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

	// BatchGet collects several keys at once. Known keys with no
	// stored row are filled in with the default body, so the
	// response carries one entry per requested known key.
	batch, err := inst.BatchGetInstanceSettings(withViewer(context.Background(), alice), &v1.BatchGetInstanceSettingsRequest{
		Names: []string{
			"instance/settings/GENERAL",
			"instance/settings/STORAGE", // not stored yet — must come back as a default
			"instance/settings/ACCESS",  // not stored yet — must come back as a default
		},
	})
	require.NoError(t, err)
	require.Len(t, batch.GetSettings(), 3)
	// GENERAL carries the value the admin wrote above; STORAGE and
	// ACCESS are the zero-value defaults, but each must populate
	// the matching oneof variant (not nil).
	byName := map[string]*v1.InstanceSetting{}
	for _, s := range batch.GetSettings() {
		byName[s.GetName()] = s
	}
	assert.Equal(t, true, byName["instance/settings/GENERAL"].GetGeneralSetting().GetDisallowUserRegistration())
	assert.Equal(t, int32(1), byName["instance/settings/GENERAL"].GetGeneralSetting().GetWeekStartDayOffset())
	assert.NotNil(t, byName["instance/settings/STORAGE"].GetStorageSetting(), "missing STORAGE must still come back with a default body")
	assert.NotNil(t, byName["instance/settings/ACCESS"].GetAccessSetting(), "missing ACCESS must still come back with a default body")
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

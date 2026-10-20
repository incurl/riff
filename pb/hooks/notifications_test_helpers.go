package hooks

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/testutil"
	_ "github.com/incurl/riff/pb/migrations"
)

// newHookedAppForHooks builds a migrated app with the production
// hook chain registered. It exists alongside the equivalent helper
// in pb/connect/connect_wave4_internal_test.go because the hooks
// package cannot import pb/connect — registering the hooks from
// a different package was the only way to get an isolated
// "hooked app" for the inbox tests.
//
// Each call allocates a fresh temp DataDir so the SQLite databases
// do not share state across tests.
func newHookedAppForHooks(t *testing.T) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_hooks_notifications_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })

	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed")
	testutil.DisableLogPersistence(app)
	require.NoError(t, app.RunAllMigrations(), "RunAllMigrations should succeed")
	Register(app)
	return app
}

// withHookCtx returns a context with the given user attached as
// the actor, mirroring what the Connect auth interceptor does in
// production. The hooks read e.Auth where they need it; this is
// here for symmetry, not for any current consumer.
func withHookCtx(user *core.Record) context.Context {
	return context.WithValue(context.Background(), hookCtxKey{}, user)
}

type hookCtxKey struct{}

// newUserForHook creates a `users` auth record with a hashed
// password. Mirrors pb/connect/connect_internal_test.go::newUser;
// duplicated here because the hooks package cannot import pb/connect.
func newUserForHook(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(users)
	rec.Set("username", username)
	rec.Set("email", username+"@example.com")
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))
	return rec
}

// lookupUserByNameBest resolves a username to a record and
// fatals if missing — the tests always know which users exist.
func lookupUserByNameBest(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()
	rows, err := app.FindRecordsByFilter("users", "username={:u}", "", 1, 0, map[string]any{"u": username})
	require.NoError(t, err)
	require.Len(t, rows, 1, "expected exactly one row for %q", username)
	return rows[0]
}

// notifRow fetches the most recent notification of a given kind
// for the given user. Returns nil when no row matches; tests then
// assert on the row's payload, which the production path would
// have populated.
func notifRow(t *testing.T, app core.App, userID, kind string) *core.Record {
	t.Helper()
	rows, err := app.FindRecordsByFilter("notifications",
		"user={:uid} && type={:k}", "-created", 1, 0,
		map[string]any{"uid": userID, "k": kind},
	)
	require.NoError(t, err)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// decodeNotificationPayload decodes a notification row's JSON
// payload into a generic map so tests can assert on its shape.
func decodeNotificationPayload(t *testing.T, rec *core.Record) map[string]any {
	t.Helper()
	raw := rec.GetString("payload")
	require.NotEmpty(t, raw, "the notification row must carry a JSON payload")
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	return out
}

// bootstrapMemoAndRelations returns the two collections the inbox
// tests write to.
func bootstrapMemoAndRelations(t *testing.T, app core.App) (memos, relations *core.Collection) {
	t.Helper()
	var err error
	memos, err = app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)
	relations, err = app.FindCollectionByNameOrId("memo_relations")
	require.NoError(t, err)
	return memos, relations
}

// mustInsertMemo writes a memo through the record-request bridge
// so the OnRecordCreateRequest rules run, and the post-save hooks
// (notably the MEMO_MENTION extractor) fire.
func mustInsertMemo(t *testing.T, app core.App, memos *core.Collection, content, visibility, authorID, spaceID string) *core.Record {
	t.Helper()
	user := lookupUser(t, app, authorID)
	rec := core.NewRecord(memos)
	rec.Set("content", content)
	rec.Set("visibility", visibility)
	rec.Set("author", authorID)
	if spaceID != "" {
		rec.Set("space", spaceID)
	}
	require.NoError(t, runHookCreate(app, "memos", rec, user))
	return rec
}

// mustInsertRelation writes a memo_relations row through the
// request bridge so the OnRecordAfterCreateSuccess hook that
// emits MEMO_COMMENT fires.
func mustInsertRelation(t *testing.T, app core.App, relations *core.Collection, memoID, relatedID, relType string) {
	t.Helper()
	authorID := lookupMemoAuthor(t, app, memoID)
	user := lookupUser(t, app, authorID)
	rec := core.NewRecord(relations)
	rec.Set("memo", memoID)
	rec.Set("related_memo", relatedID)
	rec.Set("type", relType)
	require.NoError(t, runHookCreate(app, "memo_relations", rec, user))
}

// lookupMemoAuthor returns the author id of a memo, used to
// determine the actor for a relation insert.
func lookupMemoAuthor(t *testing.T, app core.App, memoID string) string {
	t.Helper()
	rec, err := app.FindRecordById("memos", memoID)
	require.NoError(t, err)
	return rec.GetString("author")
}

// bootstrapSpacesAndMembers returns the spaces and space_members
// collections. The tests for accept/decline exercise the
// Space Invitation path.
func bootstrapSpacesAndMembers(t *testing.T, app core.App) (spaces, sm *core.Collection) {
	t.Helper()
	var err error
	spaces, err = app.FindCollectionByNameOrId("spaces")
	require.NoError(t, err)
	sm, err = app.FindCollectionByNameOrId("space_members")
	require.NoError(t, err)
	return spaces, sm
}

// mustInsertSpace writes a space via the bridge. Returns the
// persisted row. The uid is required and is normally derived from
// the title in pb/connect/space.go::CreateSpace; we slug it the
// same way here so the test does not depend on the connect
// handler.
func mustInsertSpace(t *testing.T, app core.App, spaces *core.Collection, title, ownerID string) *core.Record {
	t.Helper()
	user := lookupUser(t, app, ownerID)
	rec := core.NewRecord(spaces)
	rec.Set("title", title)
	rec.Set("owner", ownerID)
	rec.Set("uid", slugifyTitle(title))
	require.NoError(t, runHookCreate(app, "spaces", rec, user))
	out, err := app.FindRecordById("spaces", rec.Id)
	require.NoError(t, err)
	return out
}

// slugifyTitle produces a Memos-compatible uid from a title,
// matching the slugify function in pb/connect/space.go.
func slugifyTitle(title string) string {
	var b [40]byte
	i := 0
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b[i] = byte(r)
			i++
		case r >= 'A' && r <= 'Z':
			b[i] = byte(r + 32) // tolower
			i++
		case r == ' ' || r == '-' || r == '_':
			b[i] = '-'
			i++
		}
	}
	return string(b[:i])
}

// mustInsertSpaceInvite writes a space_members row with
// status=INVITED. The BeforeCreate hook on space_members would
// normally refuse this (it expects the row's user to equal the
// caller), so we go around it via app.Save; we then seed the
// matching inbox row directly so the test's "accept" and
// "decline" paths have something to find.
func mustInsertSpaceInvite(t *testing.T, app core.App, sm *core.Collection, spaceID, inviteeID, role string) {
	t.Helper()
	rec := core.NewRecord(sm)
	rec.Set("space", spaceID)
	rec.Set("user", inviteeID)
	rec.Set("status", "INVITED")
	rec.Set("role", role)
	require.NoError(t, app.Save(rec))
	CreateNotificationBestEffort(app, inviteeID, "SPACE_INVITATION", map[string]any{
		"space": map[string]any{
			"id":    spaceID,
			"uid":   "labs",
			"title": "Labs",
			"icon":  "",
			"role":  role,
		},
		"from_user": map[string]any{
			"id":       inviteeID,
			"username": "test",
		},
	})
}

// mustAcceptInvite flips the (space, user) row to ACTIVE, which
// is the transition the OnRecordAfterUpdateSuccess hook listens
// for.
func mustAcceptInvite(t *testing.T, app core.App, sm *core.Collection, spaceID, userID string) {
	t.Helper()
	rec := mustMemberRow(t, app, sm, spaceID, userID)
	rec.Set("status", "ACTIVE")
	require.NoError(t, runHookUpdate(app, "space_members", rec, lookupUser(t, app, userID)))
}

// mustDeclineInvite deletes the (space, user) row with
// status=INVITED, which is the path the OnRecordAfterDeleteSuccess
// hook listens for.
func mustDeclineInvite(t *testing.T, app core.App, sm *core.Collection, spaceID, userID string) {
	t.Helper()
	rec := mustMemberRow(t, app, sm, spaceID, userID)
	require.NoError(t, runHookDelete(app, "space_members", rec, lookupUser(t, app, userID)))
}

func mustMemberRow(t *testing.T, app core.App, _ *core.Collection, spaceID, userID string) *core.Record {
	t.Helper()
	rows, err := app.FindRecordsByFilter("space_members",
		"space={:sid} && user={:uid}", "", 1, 0,
		map[string]any{"sid": spaceID, "uid": userID},
	)
	require.NoError(t, err)
	require.Len(t, rows, 1, "expected exactly one space_members row")
	return rows[0]
}

// lookupUser loads a user record by id.
func lookupUser(t *testing.T, app core.App, id string) *core.Record {
	t.Helper()
	rec, err := app.FindRecordById("users", id)
	require.NoError(t, err)
	return rec
}

// runHookCreate, runHookUpdate, runHookDelete wrap the
// record-request bridge for the hooks test. The bridge builds a
// synthetic RequestEvent (see pb/connect/records.go for the full
// reasoning) and runs the hook chain; the post-save hooks fire
// as if the call came over HTTP. The bridge helpers are
// intentionally not exported (they belong to the connect package),
// so we inline the three calls here. Each function is small and
// the duplication is the price of keeping the hooks package free
// of an import cycle.
func runHookCreate(app core.App, _ string, rec *core.Record, user *core.Record) error {
	req, err := http.NewRequest(http.MethodPost, "/", nil)
	if err != nil {
		return err
	}
	return app.OnRecordCreateRequest().Trigger(&core.RecordRequestEvent{
		RequestEvent: newHookBridgeEvent(app, req, user),
		Collection:   rec.Collection(),
		Record:       rec,
	}, func(e *core.RecordRequestEvent) error {
		return e.App.Save(e.Record)
	})
}

func runHookUpdate(app core.App, _ string, rec *core.Record, user *core.Record) error {
	req, err := http.NewRequest(http.MethodPatch, "/", nil)
	if err != nil {
		return err
	}
	return app.OnRecordUpdateRequest().Trigger(&core.RecordRequestEvent{
		RequestEvent: newHookBridgeEvent(app, req, user),
		Collection:   rec.Collection(),
		Record:       rec,
	}, func(e *core.RecordRequestEvent) error {
		return e.App.Save(e.Record)
	})
}

func runHookDelete(app core.App, _ string, rec *core.Record, user *core.Record) error {
	req, err := http.NewRequest(http.MethodDelete, "/", nil)
	if err != nil {
		return err
	}
	return app.OnRecordDeleteRequest().Trigger(&core.RecordRequestEvent{
		RequestEvent: newHookBridgeEvent(app, req, user),
		Collection:   rec.Collection(),
		Record:       rec,
	}, func(e *core.RecordRequestEvent) error {
		return e.App.Delete(e.Record)
	})
}

// newHookBridgeEvent mirrors the discardResponseWriter-shaped event
// the connect package builds in pb/connect/records.go. We need
// the same shape so the BeforeCreate / BeforeUpdate / BeforeDelete
// hooks on the touched collections see a request event with the
// test user as Auth, which is what the production path looks like.
func newHookBridgeEvent(app core.App, req *http.Request, user *core.Record) *core.RequestEvent {
	return &core.RequestEvent{
		App:  app,
		Auth: user,
		Event: router.Event{
			Request:  req,
			Response: hookDiscardResponseWriter{header: http.Header{}},
		},
	}
}

type hookDiscardResponseWriter struct{ header http.Header }

func (w hookDiscardResponseWriter) Header() http.Header {
	if w.header == nil {
		return http.Header{}
	}
	return w.header
}
func (w hookDiscardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w hookDiscardResponseWriter) WriteHeader(_ int)           {}

package pattoken

import (
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"

	// The blank import registers PocketBase's system migrations, which create
	// the _collections / _migrations tables the bootstrap needs. Without it
	// Bootstrap fails with "no such table: _collections".
	_ "github.com/pocketbase/pocketbase/migrations"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/incurl/riff/internal/testutil"
)

// newTestApp builds a PocketBase app with the two collections Verify touches:
// `users` (PocketBase's own, for the owner lookup) and a hand-made
// `personal_access_tokens` with the columns the migration creates.
func newTestApp(t *testing.T) core.App {
	t.Helper()

	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_pattoken_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap())
	testutil.DisableLogPersistence(app)

	collection := core.NewBaseCollection(Collection)
	collection.Fields.Add(
		&core.RelationField{Name: "user", CollectionId: "_pb_users_auth_", MaxSelect: 1, Required: true},
		&core.TextField{Name: "description"},
		&core.TextField{Name: "token_hash"},
		&core.DateField{Name: "last_used_at"},
		&core.DateField{Name: "expires_at"},
	)
	require.NoError(t, app.Save(collection))

	return app
}

func seedUser(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	record := core.NewRecord(users)
	record.Set("username", username)
	record.Set("email", username+"@example.com")
	record.Set("password", "passw0rd!")
	require.NoError(t, app.Save(record))
	return record
}

func seedToken(t *testing.T, app core.App, userID, raw string, expires time.Time) *core.Record {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	require.NoError(t, err)

	collection, err := app.FindCollectionByNameOrId(Collection)
	require.NoError(t, err)

	row := core.NewRecord(collection)
	row.Set("user", userID)
	row.Set("description", "test")
	row.Set("token_hash", string(hash))
	if !expires.IsZero() {
		row.Set("expires_at", expires)
	}
	require.NoError(t, app.Save(row))
	return row
}

func TestVerify_NonPATValueIsNotChecked(t *testing.T) {
	app := newTestApp(t)

	record, err := Verify(app, "eyJhbGciOiJIUzI1NiJ9.a-jwt")
	require.NoError(t, err)
	require.Nil(t, record, "a non-PAT value is simply not this credential")
}

func TestVerify_UnknownPATAuthenticatesNobody(t *testing.T) {
	app := newTestApp(t)
	user := seedUser(t, app, "owner")
	seedToken(t, app, user.Id, Prefix+strings.Repeat("A", 32), time.Time{})

	record, err := Verify(app, Prefix+strings.Repeat("B", 32))
	require.NoError(t, err)
	require.Nil(t, record, "a token that matches no row is not an error, it is no credential")
}

func TestVerify_KnownPATReturnsItsOwner(t *testing.T) {
	app := newTestApp(t)
	user := seedUser(t, app, "owner2")
	raw := Prefix + strings.Repeat("C", 32)
	row := seedToken(t, app, user.Id, raw, time.Time{})

	record, err := Verify(app, raw)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, user.Id, record.Id)

	// The audit stamp is best-effort but expected on the happy path.
	reloaded, err := app.FindRecordById(Collection, row.Id)
	require.NoError(t, err)
	require.False(t, reloaded.GetDateTime("last_used_at").IsZero(), "last_used_at is stamped")
}

func TestVerify_ExpiredPATIsRefused(t *testing.T) {
	app := newTestApp(t)
	user := seedUser(t, app, "owner3")
	raw := Prefix + strings.Repeat("D", 32)
	seedToken(t, app, user.Id, raw, time.Now().Add(-time.Hour))

	record, err := Verify(app, raw)
	require.Nil(t, record)
	require.ErrorContains(t, err, "expired")
}

func TestVerify_MissingCollectionIsNoCredential(t *testing.T) {
	// An install whose migration never ran has no PAT table. That is "no such
	// credential", not a 500 on every request that happens to carry a
	// memos_pat_ prefix.
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_pattoken_missing",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap())
	testutil.DisableLogPersistence(app)

	record, err := Verify(app, Prefix+strings.Repeat("E", 32))
	require.NoError(t, err)
	require.Nil(t, record)
}

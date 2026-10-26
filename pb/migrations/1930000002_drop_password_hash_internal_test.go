package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDropPasswordHash_ColumnIsGone pins that the duplicate bcrypt
// digest column no longer exists.
func TestDropPasswordHash_ColumnIsGone(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	for _, name := range []string{"users", core.CollectionNameSuperusers} {
		col, err := app.FindCollectionByNameOrId(name)
		require.NoError(t, err, name)
		assert.Nil(t, col.Fields.GetByName("password_hash"),
			"%s: the password_hash column must be gone", name)
	}
}

// TestDropPasswordHash_PocketBasePasswordStillVerifies is the
// load-bearing test of the whole change: a record fetched from the
// database can still be verified with PocketBase's own
// Record.ValidatePassword.
//
// It is the assumption the migration rests on — the
// `PasswordField.Intercept` hook clears the PLAIN value after a
// save, but it must leave the HASH in place.
func TestDropPasswordHash_PocketBasePasswordStillVerifies(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	rec := core.NewRecord(users)
	rec.Set("username", "pbpass")
	rec.Set("email", "pbpass@example.test")
	rec.SetPassword("audit-password-123")
	require.NoError(t, app.Save(rec))

	// Re-fetch: the saved record's plain value is wiped, the hash
	// is not.
	fetched, err := app.FindRecordById("users", rec.Id)
	require.NoError(t, err)

	assert.True(t, fetched.ValidatePassword("audit-password-123"),
		"a fetched record must verify against the correct password")
	assert.False(t, fetched.ValidatePassword("wrong-password"),
		"a fetched record must reject an incorrect password")
}

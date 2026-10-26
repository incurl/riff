package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTightenSecretCollectionRules_OwnerScoped pins that the three
// secret-bearing collections are scoped to their owner.
//
// The rule was nil before, which in PocketBase means superuser-only.
// That protected the PocketBase REST surface but NOT the Memos-shaped
// `pb_records_crud` surface, which never consults the rule engine.
func TestTightenSecretCollectionRules_OwnerScoped(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	const ownerRule = "user = @request.auth.id"
	for _, name := range []string{
		"personal_access_tokens",
		"user_webhooks",
		"linked_identities",
	} {
		col, err := app.FindCollectionByNameOrId(name)
		require.NoError(t, err, name)

		require.NotNil(t, col.ListRule, "%s: the list rule must not be nil", name)
		assert.Equal(t, ownerRule, *col.ListRule, "%s: the list rule must be owner-scoped", name)
		require.NotNil(t, col.ViewRule, "%s: the view rule must not be nil", name)
		assert.Equal(t, ownerRule, *col.ViewRule, "%s: the view rule must be owner-scoped", name)
		require.NotNil(t, col.CreateRule, "%s: the create rule must not be nil", name)
		assert.Equal(t, ownerRule, *col.CreateRule, "%s: the create rule must be owner-scoped", name)
		require.NotNil(t, col.UpdateRule, "%s: the update rule must not be nil", name)
		assert.Equal(t, ownerRule, *col.UpdateRule, "%s: the update rule must be owner-scoped", name)
		require.NotNil(t, col.DeleteRule, "%s: the delete rule must not be nil", name)
		assert.Equal(t, ownerRule, *col.DeleteRule, "%s: the delete rule must be owner-scoped", name)
	}
}

// TestTightenSecretCollectionRules_Idempotent pins that re-running
// the migration changes nothing.
func TestTightenSecretCollectionRules_Idempotent(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	col, err := app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, err)
	before := *col.ListRule

	require.NoError(t, app.RunAllMigrations(), "a second run must succeed")

	col, err = app.FindCollectionByNameOrId("personal_access_tokens")
	require.NoError(t, err)
	require.NotNil(t, col.ListRule)
	assert.Equal(t, before, *col.ListRule)
}

// TestHidePasswordHash pins that the bcrypt digest column is Hidden
// on both collections, so no export carries it.
func TestHidePasswordHash(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)

	for _, name := range []string{"users", core.CollectionNameSuperusers} {
		col, err := app.FindCollectionByNameOrId(name)
		require.NoError(t, err, name)
		field := col.Fields.GetByName("password_hash")
		if field == nil {
			continue
		}
		assert.True(t, field.GetHidden(), "%s: password_hash must be Hidden", name)
	}
}

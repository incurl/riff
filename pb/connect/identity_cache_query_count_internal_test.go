package connect

import (
	"testing"

	"github.com/pocketbase/dbx"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// countingApp wraps a core.App and counts the FindRecordById calls
// that reach the database, so a test can assert that the identity
// cache actually removes queries rather than merely returning the
// right value.
type countingApp struct {
	core.App
	userLookups  int
	spaceLookups int
}

// FindRecordById counts the lookup and delegates. Only the two
// collections the identity cache serves are counted — anything else
// would make the number depend on unrelated machinery.
// FindRecordById overrides the embedded App's method. The name must
// match the interface exactly or the override is never called, so the
// revive var-naming rule (which prefers FindRecordByID) is suppressed
// here rather than obeyed.
//
//nolint:revive // the name must match core.App's method to override it
func (c *countingApp) FindRecordById(collectionModelOrIdentifier any, recordID string, optFilters ...func(q *dbx.SelectQuery) error) (*core.Record, error) {
	switch collectionModelOrIdentifier {
	case "users":
		c.userLookups++
	case "spaces":
		c.spaceLookups++
	default:
		// Not a collection the identity cache serves.
	}
	return c.App.FindRecordById(collectionModelOrIdentifier, recordID, optFilters...)
}

// TestIdentityCache_ReducesTheUserLookups is the payoff test: the
// number of user lookups for a page of memos must not grow with the
// number of memos.
//
// Without the cache, `recordToMemo` resolves the author of every memo
// and every reaction, so a page of N memos by one author costs N+
// lookups of the same row. With the cache it costs one.
func TestIdentityCache_ReducesTheUserLookups(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")

	// Five memos, all by the same author — the common case.
	for i := 0; i < 5; i++ {
		newFilterMemo(t, app, alice.Id, "memo body", nil)
	}

	counting := &countingApp{App: app}
	srv := &MemoServer{app: counting}
	srv.SetFTSSearch(true)

	res, err := srv.ListMemos(t.Context(), &v1.ListMemosRequest{})
	require.NoError(t, err)
	require.Len(t, res.GetMemos(), 5)

	assert.Equal(t, 1, counting.userLookups,
		"five memos by one author must cost one user lookup, not five")
}

// TestIdentityCache_LookupIsStillCorrect is the other half: the cache
// must not change what the feed reports. A cache that returned the
// wrong name would be worse than no cache.
func TestIdentityCache_LookupIsStillCorrect(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	bob := newUser(t, app, "bob")

	newFilterMemo(t, app, alice.Id, "alice memo", nil)
	newFilterMemo(t, app, bob.Id, "bob memo", nil)

	srv := NewMemoServer(app)
	res, err := srv.ListMemos(t.Context(), &v1.ListMemosRequest{})
	require.NoError(t, err)
	require.Len(t, res.GetMemos(), 2)

	creators := map[string]bool{}
	for _, m := range res.GetMemos() {
		creators[m.GetCreator()] = true
	}
	assert.True(t, creators["users/alice"], "alice's memo must report alice")
	assert.True(t, creators["users/bob"], "bob's memo must report bob")
}

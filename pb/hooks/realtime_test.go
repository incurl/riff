package hooks_test

import (
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/hooks"
	_ "github.com/incurl/riff/pb/migrations"
)

// TestRealtime_PrivateMemoIsNotBroadcastToAnonymous pins that a
// PRIVATE memo's id never reaches a subscriber that is not allowed
// to read it.
//
// The broadcaster used to send every create to every subscriber, so
// a PRIVATE memo's id reached anonymous clients. The check is now
// the collection's ListRule evaluated per client, the same way
// PocketBase's own realtime does it.
func TestRealtime_PrivateMemoIsNotBroadcastToAnonymous(t *testing.T) {
	t.Parallel()
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	RegisterAll(app)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	owner := core.NewRecord(users)
	owner.Set("username", "rtowner")
	owner.Set("email", "rtowner@example.com")
	owner.Set("password", "ownerpassw0rd")
	require.NoError(t, app.Save(owner))

	memos, err := app.FindCollectionByNameOrId("memos")
	require.NoError(t, err)

	privateMemo := core.NewRecord(memos)
	privateMemo.Set("author", owner.Id)
	privateMemo.Set("content", "a private memo")
	privateMemo.Set("visibility", "PRIVATE")
	require.NoError(t, app.Save(privateMemo))

	publicMemo := core.NewRecord(memos)
	publicMemo.Set("author", owner.Id)
	publicMemo.Set("content", "a public memo")
	publicMemo.Set("visibility", "PUBLIC")
	require.NoError(t, app.Save(publicMemo))

	// An anonymous subscriber.
	anon := subscriptions.NewDefaultClient()
	anon.Subscribe("memos")
	app.SubscriptionsBroker().Register(anon)
	t.Cleanup(func() { app.SubscriptionsBroker().Unregister(anon.Id()) })

	// The owner.
	ownerClient := subscriptions.NewDefaultClient()
	ownerClient.Subscribe("memos")
	ownerClient.Set(hooks.RealtimeClientAuthKey, owner)
	app.SubscriptionsBroker().Register(ownerClient)
	t.Cleanup(func() { app.SubscriptionsBroker().Unregister(ownerClient.Id()) })

	// A PUBLIC memo reaches both.
	got := broadcastAndDrainBoth(t, app, "memos", publicMemo, anon, ownerClient)
	anonGot, ownerGot := got[0], got[1]
	assert.Equal(t, hooks.RealtimeEventName, anonGot,
		"an anonymous client may see a PUBLIC memo")
	assert.Equal(t, hooks.RealtimeEventName, ownerGot,
		"the owner may see their own PUBLIC memo")

	// A PRIVATE memo reaches only the owner.
	got = broadcastAndDrainBoth(t, app, "memos", privateMemo, anon, ownerClient)
	anonGot, ownerGot = got[0], got[1]
	assert.Empty(t, anonGot,
		"an anonymous client must NOT receive a PRIVATE memo's id")
	assert.Equal(t, hooks.RealtimeEventName, ownerGot,
		"the owner must receive their own PRIVATE memo")
}

// broadcastAndDrainBoth broadcasts a create event while readers are
// parked on BOTH clients' channels, and returns the message name
// each one received ("" when it received nothing).
//
// The clients' channels are UNBUFFERED
// (tools/subscriptions/client.go: `make(chan Message)`), so the
// broadcaster's non-blocking send only lands when a reader is
// already waiting. Every client that may receive the event must
// therefore have a reader parked BEFORE the broadcast — hence the
// goroutine per client.
func broadcastAndDrainBoth(t *testing.T, app core.App, collection string, record *core.Record, clients ...*subscriptions.DefaultClient) []string {
	t.Helper()
	names := make([]string, len(clients))
	results := make([]chan string, len(clients))

	for i, client := range clients {
		results[i] = make(chan string, 1)
		go func(ch chan string, c *subscriptions.DefaultClient) {
			select {
			case msg := <-c.Channel():
				ch <- msg.Name
			case <-time.After(500 * time.Millisecond):
				ch <- ""
			}
		}(results[i], client)
	}

	// Give the readers a moment to park on their channels, then
	// broadcast.
	time.Sleep(50 * time.Millisecond)
	hooks.BroadcastCreateForTest(app, collection, record)

	for i := range clients {
		names[i] = <-results[i]
	}
	return names
}

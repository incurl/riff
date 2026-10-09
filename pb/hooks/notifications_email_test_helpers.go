package hooks

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/email"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// fakeSender is an EmailSender implementation that records the
// (config, message) pairs it was called with so tests can
// assert on them without ever opening a TCP connection.
type fakeSender struct {
	mu    sync.Mutex
	calls []fakeCall
}

type fakeCall struct {
	Config  *email.Config
	Message *email.Message
}

func (f *fakeSender) Send(c *email.Config, m *email.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{Config: c, Message: m})
}

func (f *fakeSender) Last() (fakeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

func (f *fakeSender) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// seedNotificationSetting writes a row into instance_settings
// with key=NOTIFICATION and the supplied body, so the
// dispatcher finds the email setting.
func seedNotificationSetting(t *testing.T, app core.App, setting *v1.InstanceSetting_NotificationSetting) {
	t.Helper()
	coll, err := app.FindCollectionByNameOrId("instance_settings")
	require.NoError(t, err)
	raw, err := json.Marshal(setting)
	require.NoError(t, err)
	row, err := app.FindFirstRecordByFilter("instance_settings", "key='NOTIFICATION'")
	if err == nil {
		row.Set("payload", string(raw))
		require.NoError(t, app.Save(row))
		return
	}
	row = core.NewRecord(coll)
	row.Set("key", "NOTIFICATION")
	row.Set("payload", string(raw))
	require.NoError(t, app.Save(row))
}

// notifFor inserts a notifications row with the given kind
// and payload, so the dispatcher has something to operate on.
func notifFor(t *testing.T, app core.App, userID, kind, payload string) *core.Record {
	t.Helper()
	coll, err := app.FindCollectionByNameOrId("notifications")
	require.NoError(t, err)
	row := core.NewRecord(coll)
	row.Set("user", userID)
	row.Set("type", kind)
	row.Set("payload", payload)
	row.Set("read", false)
	require.NoError(t, app.Save(row))
	return row
}

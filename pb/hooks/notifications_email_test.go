package hooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/profile"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// TestEmailDispatcher_NoInstanceURL_Noop confirms the
// dispatcher is a no-op when the profile has no InstanceURL.
// Even with a valid email setting, an empty URL means the
// message body would have a broken link, so the dispatcher
// skips.
func TestEmailDispatcher_NoInstanceURL_Noop(t *testing.T) {
	app := newHookedAppForHooks(t)
	sender := &fakeSender{}
	d := NewEmailDispatcher(&profile.Profile{}, app, sender.Send)
	require.NoError(t, d.DispatchInboxEmail(context.Background(), nil))
	assert.Equal(t, 0, sender.Count(), "no sender call when InstanceURL is empty")
}

// TestEmailDispatcher_Disabled_Noop: when the email setting
// is not enabled, the dispatcher must not send.
func TestEmailDispatcher_Disabled_Noop(t *testing.T) {
	app := newHookedAppForHooks(t)
	seedNotificationSetting(t, app, &v1.InstanceSetting_NotificationSetting{
		Email: &v1.InstanceSetting_NotificationSetting_EmailSetting{
			Enabled: false,
		},
	})
	sender := &fakeSender{}
	d := NewEmailDispatcher(&profile.Profile{InstanceURL: "https://riff.example.com"}, app, sender.Send)
	alice := newUserForHook(t, app, "alice")
	rec := notifFor(t, app, alice.Id, "MEMO_COMMENT", `{"memo":"memos/a","related_memo":"memos/b"}`)
	require.NoError(t, d.DispatchInboxEmail(context.Background(), rec))
	assert.Equal(t, 0, sender.Count(), "no sender call when setting is disabled")
}

// TestEmailDispatcher_Enabled_BuildsAndSends: a complete
// happy path. The dispatcher reads the email setting,
// finds the receiver, builds the message, and hands it to
// the sender. We assert the sender was called once and
// the message body contains the right pieces. We use the
// SPACE_INVITATION kind for this test because the
// MEMO_COMMENT and MEMO_MENTION kinds need real memo rows
// to resolve; the invitation kind is a single payload.
func TestEmailDispatcher_Enabled_BuildsAndSends(t *testing.T) {
	app := newHookedAppForHooks(t)
	seedNotificationSetting(t, app, &v1.InstanceSetting_NotificationSetting{
		Email: &v1.InstanceSetting_NotificationSetting_EmailSetting{
			Enabled:   true,
			SmtpHost:  "smtp.example.com",
			SmtpPort:  587,
			FromEmail: "noreply@riff.example.com",
			FromName:  "Riff",
		},
	})
	alice := newUserForHook(t, app, "alice")
	rec := notifFor(t, app, alice.Id, "SPACE_INVITATION", `{"space":{"id":"s1","uid":"labs","title":"Labs","role":"USER"},"from_user":{"id":"u1","username":"bob"}}`)
	sender := &fakeSender{}
	d := NewEmailDispatcher(&profile.Profile{InstanceURL: "https://riff.example.com"}, app, sender.Send)
	require.NoError(t, d.DispatchInboxEmail(context.Background(), rec))
	call, called := sender.Last()
	require.True(t, called, "the sender must be called when settings + receiver are good")
	assert.Equal(t, "smtp.example.com", call.Config.SMTPHost)
	assert.Equal(t, 587, call.Config.SMTPPort)
	assert.Equal(t, "noreply@riff.example.com", call.Config.FromEmail)
	assert.Equal(t, []string{"alice@example.com"}, call.Message.To)
	assert.Contains(t, call.Message.Subject, "bob", "subject should reference the inviter")
	assert.Contains(t, call.Message.Subject, "Labs", "subject should reference the space title")
	assert.Contains(t, call.Message.Body, "riff.example.com", "body should include a URL to the instance")
}

// TestEmailDispatcher_InvalidConfig_ReturnsError: when the
// settings row stores a FromEmail that is empty, config
// validation fails and DispatchInboxEmail returns an error
// (the caller — the after-create hook — logs it).
func TestEmailDispatcher_InvalidConfig_ReturnsError(t *testing.T) {
	app := newHookedAppForHooks(t)
	seedNotificationSetting(t, app, &v1.InstanceSetting_NotificationSetting{
		Email: &v1.InstanceSetting_NotificationSetting_EmailSetting{
			Enabled:   true,
			SmtpHost:  "smtp.example.com",
			SmtpPort:  587,
			FromEmail: "",
		},
	})
	alice := newUserForHook(t, app, "alice")
	rec := notifFor(t, app, alice.Id, "SPACE_INVITATION", `{"space":{"id":"s1","uid":"labs","title":"Labs","role":"USER"},"from_user":{"id":"u1","username":"bob"}}`)
	sender := &fakeSender{}
	d := NewEmailDispatcher(&profile.Profile{InstanceURL: "https://riff.example.com"}, app, sender.Send)
	err := d.DispatchInboxEmail(context.Background(), rec)
	require.Error(t, err, "config with empty FromEmail must error")
	assert.Contains(t, err.Error(), "from email is required")
	assert.Equal(t, 0, sender.Count(), "no sender call on invalid config")
}

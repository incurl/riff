package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/email"
	"github.com/incurl/riff/internal/profile"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// EmailSender is the seam the dispatcher uses to hand a
// prepared (config, message) pair to a transport. It matches
// the notification.EmailSender shape from upstream Memos and
// lets tests substitute a stub that records the call without
// opening a TCP connection.
type EmailSender func(*email.Config, *email.Message)

// EmailDispatcher turns a notifications row into an SMTP
// message, when the admin has configured SMTP and the receiver
// has an email address. It is owned by the hooks package and
// installed as the side effect of every notification create.
//
// The dispatcher holds no PocketBase DAO of its own; every
// lookup goes through app so it sees the same auth state as
// the rest of the request path. The `profile` and `sender`
// fields are the only knobs: `profile.InstanceURL` is the
// canonical base URL we link to in the message body, and
// `sender` is the async send function (which defaults to
// email.SendAsync when nil).
type EmailDispatcher struct {
	profile *profile.Profile
	app     core.App
	sender  EmailSender
}

// NewEmailDispatcher builds a dispatcher. A nil sender falls
// back to email.SendAsync, matching the Memos upstream default.
func NewEmailDispatcher(p *profile.Profile, app core.App, sender EmailSender) *EmailDispatcher {
	if sender == nil {
		sender = email.SendAsync
	}
	return &EmailDispatcher{profile: p, app: app, sender: sender}
}

// DispatchInboxEmail reads the notification row, looks up the
// receiver, builds the right message body for the row's kind,
// and hands it to the sender. It is best-effort: every
// recoverable condition (no settings, no email address, no
// instance URL, invalid SMTP config) returns nil so the caller
// never has to inspect the error path.
func (d *EmailDispatcher) DispatchInboxEmail(ctx context.Context, rec *core.Record) error {
	if rec == nil {
		return nil
	}
	emailSetting, err := d.notificationEmailSetting(ctx)
	if err != nil {
		return errors.Wrap(err, "notifications_email: failed to load email setting")
	}
	if emailSetting == nil || !emailSetting.GetEnabled() {
		return nil
	}
	if d.baseURL() == "" {
		slog.Warn("notifications_email: skipping email, instance URL not set",
			slog.String("notification_id", rec.Id),
			slog.String("receiver_id", rec.GetString("user")))
		return nil
	}
	receiver, err := d.app.FindRecordById("users", rec.GetString("user"))
	if err != nil {
		return errors.Wrap(err, "notifications_email: failed to load receiver")
	}
	receiverEmail := strings.TrimSpace(receiver.GetString("email"))
	if receiverEmail == "" {
		return nil
	}

	// Build a per-kind message. Unknown kinds are a silent
	// no-op so the dispatcher never needs to be updated when
	// a new notification type is introduced in the future.
	msg, err := d.buildMessage(ctx, rec, receiver)
	if err != nil {
		return err
	}
	if msg == nil {
		return nil
	}
	msg.ReplyTo = emailSetting.GetReplyTo()

	config := d.configFromSetting(emailSetting)
	if err := config.Validate(); err != nil {
		return errors.Wrap(err, "notifications_email: invalid notification email setting")
	}
	d.sender(config, msg)
	return nil
}

// DispatchTestEmail sends a one-off "test email from your
// notification settings" message to the address the admin
// specified (or the admin's own email, if they passed none).
// It is the implementation behind the InstanceService
// .TestInstanceEmailSetting RPC; the Connect handler is in
// pb/connect/instance_settings.go.
func (d *EmailDispatcher) DispatchTestEmail(_ context.Context, setting *v1.InstanceSetting_NotificationSetting_EmailSetting, recipientEmail string) error {
	if setting == nil || !setting.GetEnabled() {
		return errors.New("email notifications are not enabled")
	}
	if d.baseURL() == "" {
		return errors.New("instance URL is not configured")
	}
	if recipientEmail == "" {
		// Caller did not pass a recipient — fall back to the
		// current admin's email so the smoke test still has
		// somewhere to land. The Connect handler reads e.Auth
		// and passes the admin's email when the request body
		// omitted the recipient_email field.
		return errors.New("no recipient email address")
	}
	config := d.configFromSetting(setting)
	if err := config.Validate(); err != nil {
		return errors.Wrap(err, "invalid email setting")
	}
	msg := &email.Message{
		To:      []string{recipientEmail},
		Subject: "[Riff] Test email",
		Body:    "This is a test email from your Riff notification settings.",
		ReplyTo: setting.GetReplyTo(),
	}
	d.sender(config, msg)
	return nil
}

// buildMessage routes on the row's type. The memos each
// dispatcher variant needs are looked up by id; a missing
// memo is a silent no-op so a deleted memo (e.g. the user
// removed it between the inbox write and the email
// dispatch) does not become an error.
func (d *EmailDispatcher) buildMessage(ctx context.Context, rec *core.Record, receiver *core.Record) (*email.Message, error) {
	switch rec.GetString("type") {
	case "SPACE_INVITATION":
		return d.buildSpaceInvitationMessage(ctx, rec, receiver)
	case "MEMO_COMMENT":
		return d.buildMemoCommentMessage(ctx, rec, receiver)
	case "MEMO_MENTION":
		return d.buildMemoMentionMessage(ctx, rec, receiver)
	default:
		return nil, nil
	}
}

// notificationEmailSetting reads the stored
// InstanceSetting.NotificationSetting row. A missing row is
// the same as "feature off" (returns nil, no error).
func (d *EmailDispatcher) notificationEmailSetting(_ context.Context) (*v1.InstanceSetting_NotificationSetting_EmailSetting, error) {
	row, err := d.app.FindFirstRecordByFilter("instance_settings", "key='NOTIFICATION'")
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	raw := row.GetString("payload")
	if raw == "" {
		return nil, nil
	}
	body := &v1.InstanceSetting_NotificationSetting{}
	if err := protoJSONUnmarshal([]byte(raw), body); err != nil {
		return nil, err
	}
	return body.GetEmail(), nil
}

func (d *EmailDispatcher) configFromSetting(s *v1.InstanceSetting_NotificationSetting_EmailSetting) *email.Config {
	if s == nil {
		return &email.Config{}
	}
	return &email.Config{
		SMTPHost:     s.GetSmtpHost(),
		SMTPPort:     int(s.GetSmtpPort()),
		SMTPUsername: s.GetSmtpUsername(),
		SMTPPassword: s.GetSmtpPassword(),
		FromEmail:    s.GetFromEmail(),
		FromName:     s.GetFromName(),
		UseTLS:       s.GetUseTls(),
		UseSSL:       s.GetUseSsl(),
	}
}

// buildSpaceInvitationMessage renders a one-line plain text
// invitation email. The subject and the body both reference
// the space title the admin offered, and link back to the
// inbox so the receiver can accept in one click.
func (d *EmailDispatcher) buildSpaceInvitationMessage(_ context.Context, rec *core.Record, receiver *core.Record) (*email.Message, error) {
	raw := rec.GetString("payload")
	if raw == "" {
		return nil, nil
	}
	body := &spaceInvitationPayload{}
	if err := protoJSONUnmarshal([]byte(raw), body); err != nil {
		return nil, errors.Wrap(err, "notifications_email: invalid SPACE_INVITATION payload")
	}
	if body.Space == nil {
		return nil, nil
	}
	senderName := ""
	if fu := body.FromUser; fu != nil {
		senderName = fu.Username
	}
	roleName := "a member"
	if body.Space.Role == "ADMIN" {
		roleName = "an administrator"
	}
	inboxURL := d.inboxURL()
	if inboxURL == "" {
		return nil, nil
	}
	text := strings.Join([]string{
		fmt.Sprintf("Hi %s,", displayNameOrUsername(receiver)),
		"",
		fmt.Sprintf("%s invited you to join the Space \"%s\" as %s.", senderName, body.Space.Title, roleName),
		"",
		"Review the invitation in Riff:",
		inboxURL,
		"",
		"You are receiving this because an administrator of this Space invited you.",
	}, "\n")
	return &email.Message{
		To:      []string{receiver.GetString("email")},
		Subject: fmt.Sprintf("[Riff] %s invited you to join \"%s\"", senderName, body.Space.Title),
		Body:    text,
	}, nil
}

// buildMemoCommentMessage renders the "X commented on your
// memo" email. We look up both the comment memo and the
// context memo to ensure the receiver still has access; if
// either is missing or hidden, the dispatcher returns a
// nil message (the inbox row stays, but the email is
// suppressed).
func (d *EmailDispatcher) buildMemoCommentMessage(_ context.Context, rec *core.Record, receiver *core.Record) (*email.Message, error) {
	raw := rec.GetString("payload")
	if raw == "" {
		return nil, nil
	}
	body := &memoCommentPayload{}
	if err := protoJSONUnmarshal([]byte(raw), body); err != nil {
		return nil, errors.Wrap(err, "notifications_email: invalid MEMO_COMMENT payload")
	}
	commentID := memoIDFromRef(body.Memo)
	relatedID := memoIDFromRef(body.RelatedMemo)
	if commentID == "" || relatedID == "" {
		return nil, nil
	}
	comment, err := d.app.FindRecordById("memos", commentID)
	if err != nil {
		return nil, nil
	}
	related, err := d.app.FindRecordById("memos", relatedID)
	if err != nil {
		return nil, nil
	}
	if !memoVisibleTo(comment, receiver) || !memoVisibleTo(related, receiver) {
		return nil, nil
	}
	url := d.memoCommentURL(related.Id, comment.Id)
	if url == "" {
		return nil, nil
	}
	senderName := ""
	if fu := body.FromUser; fu != nil {
		senderName = fu.Username
	}
	text := strings.Join([]string{
		fmt.Sprintf("Hi %s,", displayNameOrUsername(receiver)),
		"",
		fmt.Sprintf("%s commented on your memo.", senderName),
		"",
		"Open in Riff:",
		url,
		"",
		"You are receiving this because you own this memo.",
	}, "\n")
	return &email.Message{
		To:      []string{receiver.GetString("email")},
		Subject: fmt.Sprintf("[Riff] %s commented on your memo", senderName),
		Body:    text,
	}, nil
}

// buildMemoMentionMessage is the "X mentioned you in a memo"
// variant. The related_memo is the conversational parent
// when the mention was posted in a comment; when set, we
// require visibility on it too.
func (d *EmailDispatcher) buildMemoMentionMessage(_ context.Context, rec *core.Record, receiver *core.Record) (*email.Message, error) {
	raw := rec.GetString("payload")
	if raw == "" {
		return nil, nil
	}
	body := &memoMentionPayload{}
	if err := protoJSONUnmarshal([]byte(raw), body); err != nil {
		return nil, errors.Wrap(err, "notifications_email: invalid MEMO_MENTION payload")
	}
	memoID := memoIDFromRef(body.Memo)
	if memoID == "" {
		return nil, nil
	}
	memo, err := d.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, nil
	}
	if !memoVisibleTo(memo, receiver) {
		return nil, nil
	}
	if relatedID := memoIDFromRef(body.RelatedMemo); relatedID != "" {
		related, err := d.app.FindRecordById("memos", relatedID)
		if err != nil {
			return nil, nil
		}
		if !memoVisibleTo(related, receiver) {
			return nil, nil
		}
	}
	url := d.memoURL(memo.Id)
	if url == "" {
		return nil, nil
	}
	senderName := ""
	if fu := body.FromUser; fu != nil {
		senderName = fu.Username
	}
	text := strings.Join([]string{
		fmt.Sprintf("Hi %s,", displayNameOrUsername(receiver)),
		"",
		fmt.Sprintf("%s mentioned you in a memo.", senderName),
		"",
		"Open in Riff:",
		url,
		"",
		"You are receiving this because you were mentioned in this memo.",
	}, "\n")
	return &email.Message{
		To:      []string{receiver.GetString("email")},
		Subject: fmt.Sprintf("[Riff] %s mentioned you in a memo", senderName),
		Body:    text,
	}, nil
}

func (d *EmailDispatcher) baseURL() string {
	if d.profile == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(d.profile.InstanceURL), "/")
}

func (d *EmailDispatcher) inboxURL() string {
	b := d.baseURL()
	if b == "" {
		return ""
	}
	return b + "/inbox"
}

func (d *EmailDispatcher) memoURL(id string) string {
	b := d.baseURL()
	if b == "" || id == "" {
		return ""
	}
	return b + "/memos/" + id
}

func (d *EmailDispatcher) memoCommentURL(relatedID, commentID string) string {
	b := d.baseURL()
	if b == "" || relatedID == "" || commentID == "" {
		return ""
	}
	return fmt.Sprintf("%s/memos/%s#%s", b, relatedID, commentID)
}

// displayNameOrUsername picks the friendliest of display name
// or username. Empty when both are blank, which is a defensive
// case the SMTP layer refuses to send anyway.
func displayNameOrUsername(u *core.Record) string {
	if u == nil {
		return "there"
	}
	if n := strings.TrimSpace(u.GetString("name")); n != "" {
		return n
	}
	if n := strings.TrimSpace(u.GetString("display_name")); n != "" {
		return n
	}
	if n := strings.TrimSpace(u.GetString("username")); n != "" {
		return n
	}
	return "there"
}

// memoVisibleTo reports whether the receiver can read the memo.
// The current visibility model is private-or-public: a memo
// is readable by everyone when visibility=PUBLIC, by no one
// else. The schema's list rule is more nuanced (PROTECTED
// allows signed-in users, SPACE restricts to members), but
// for the email suppression rule we accept a conservative
// approximation that never leaks a private memo and never
// sends a "you were mentioned" email about a memo the
// receiver can no longer read.
func memoVisibleTo(memo *core.Record, _ *core.Record) bool {
	if memo == nil {
		return false
	}
	v := memo.GetString("visibility")
	return v == "PUBLIC" || v == "PROTECTED"
}

// memoIDFromRef parses a "memos/{id}" resource name into the
// bare id; returns "" for any other shape.
func memoIDFromRef(name string) string {
	const prefix = "memos/"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	return strings.TrimPrefix(name, prefix)
}

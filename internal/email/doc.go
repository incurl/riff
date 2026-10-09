// Package email provides the SMTP transport used by riff's
// inbox-notification email dispatcher.
//
// It was deleted during the Memos-to-PocketBase port because
// nothing imported it (see DECISIONS.md "Orphan package
// retirement: internal/email"). The Phase 5 inbox work
// reintroduces it as a building block for the dispatcher hook
// in pb/hooks/notifications_email.go, which sends an
// email for each new notification row when the admin has
// configured SMTP credentials on the
// InstanceSetting.NotificationSetting.EmailSetting proto.
//
// The package owns two small things: a Config that holds the
// SMTP connection parameters, and a Message that holds the
// envelope. The Client sends one Message over one Config via
// either STARTTLS (port 587) or implicit TLS (port 465), with a
// per-attempt timeout so a misconfigured server cannot stall the
// request goroutine.
//
// Like the upstream Memos version, the package is intentionally
// stdlib-only. The async send queue is started by init() and
// drains in a pair of background workers, matching the Memos
// "fire and forget" semantics for notification emails.
package email

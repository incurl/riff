package hooks

import (
	"context"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/profile"
)

// InstallEmailDispatcher binds a post-save hook to the
// `notifications` collection that fires
// EmailDispatcher.DispatchInboxEmail for every new row. It is
// the wiring point main.go uses to opt the running server in
// to email delivery.
//
// A nil dispatcher (the zero value before InstallEmailDispatcher
// runs) is the safe no-op state. Tests that exercise the hook
// chain without going through main.go can leave it nil and the
// hook simply skips the email send, so the inbox path keeps
// working in test environments.
func InstallEmailDispatcher(p *profile.Profile, app core.App) {
	dispatcher = NewEmailDispatcher(p, app, nil)
}

// dispatcher is the package-level email dispatcher installed by
// InstallEmailDispatcher. The after-create hook on the
// `notifications` collection reads from it; a nil value is
// the no-op case.
var dispatcher *EmailDispatcher

// registerNotificationsEmailHook binds the after-create hook
// that calls the dispatcher. The dispatcher is installed
// separately by main.go so the boot sequence stays explicit.
func registerNotificationsEmailHook(app core.App) {
	app.OnRecordAfterCreateSuccess("notifications").BindFunc(func(e *core.RecordEvent) error {
		defer func() { _ = e.Next() }()
		if dispatcher == nil {
			return nil
		}
		// We deliberately swallow the error. The inbox row
		// already committed; an email send failure is a
		// warn-only path, never a reason to roll anything
		// back.
		_ = dispatcher.DispatchInboxEmail(context.Background(), e.Record)
		return nil
	})
}

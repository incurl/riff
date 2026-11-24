// The Suspend extension's forum initialiser.
//
// It mirrors the parts of upstream `flarum/suspend` that this port serves:
// a suspended badge on the account, a moderation control that opens the
// suspend dialog, and the suspension notice shown to a suspended account.
//
// The dialog collects a duration, an optional reason (moderators only) and
// an optional message for the account. The backend owns the rules — who may
// suspend, that an administrator cannot be suspended, that a suspension
// revokes every group permission — so the UI only reflects `canSuspend`.
import app from 'flarum/forum/app';
import { extend as extendMethod } from 'flarum/common/extend';
import User from 'flarum/common/models/User';
import UserControls from 'flarum/forum/utils/UserControls';
import Badge from 'flarum/common/components/Badge';
import Button from 'flarum/common/components/Button';
import FormModal from 'flarum/common/components/FormModal';

/** The forum's API root, e.g. `/forum/api`. */
function apiUrl() {
  return app.forum.attribute('apiUrl');
}

/** Whether an account is suspended. */
function suspendedUntil(user) {
  return user.attribute('suspendedUntil') || null;
}

/** Whether the caller may suspend the account. */
function canSuspend(user) {
  return !!user.attribute('canSuspend');
}

/**
 * The suspend dialog.
 *
 * The date is a plain text field rather than a date picker: the backend
 * accepts an RFC 3339 timestamp, and the components this port vendors do
 * not include a date picker. An empty value means "indefinitely".
 */
class SuspendUserModal extends FormModal {
  oninit(vnode) {
    super.oninit(vnode);

    this.user = this.attrs.user;
    this.reason = '';
    this.message = '';
    this.until = '';
  }

  className() {
    return 'SuspendUserModal Modal--small';
  }

  title() {
    return app.translator.trans('flarum-suspend.forum.suspend_user.title', { user: this.user });
  }

  content() {
    return (
      <div className="Modal-body">
        <div className="Form">
          <div className="Form-group">
            <label>{app.translator.trans('flarum-suspend.forum.suspend_user.suspended_until_label')}</label>
            <input
              className="FormControl"
              placeholder={app.translator.trans('flarum-suspend.forum.suspend_user.suspended_until_placeholder')}
              value={this.until}
              oninput={(e) => (this.until = e.target.value)}
            />
            <div className="helpText">{app.translator.trans('flarum-suspend.forum.suspend_user.suspended_until_help')}</div>
          </div>

          <div className="Form-group">
            <label>{app.translator.trans('flarum-suspend.forum.suspend_user.reason_label')}</label>
            <input className="FormControl" value={this.reason} oninput={(e) => (this.reason = e.target.value)} />
          </div>

          <div className="Form-group">
            <label>{app.translator.trans('flarum-suspend.forum.suspend_user.message_label')}</label>
            <textarea className="FormControl" value={this.message} oninput={(e) => (this.message = e.target.value)} />
          </div>

          <div className="Form-group">
            <Button className="Button Button--primary" type="submit" loading={this.loading}>
              {app.translator.trans('flarum-suspend.forum.suspend_user.submit_button')}
            </Button>
          </div>
        </div>
      </div>
    );
  }

  onsubmit(e) {
    e.preventDefault();

    this.loading = true;

    // An empty date is sent as an empty string, which the backend reads as
    // an indefinite suspension.
    app
      .request({
        method: 'POST',
        url: apiUrl() + '/users/' + this.user.id() + '/suspend',
        body: {
          data: {
            attributes: {
              suspendedUntil: this.until,
              suspendReason: this.reason,
              suspendMessage: this.message,
            },
          },
        },
      })
      .then((payload) => {
        this.user.pushAttributes(payload.data.attributes);
        app.modal.close();
        m.redraw();
      })
      .finally(() => {
        this.loading = false;
        m.redraw();
      });
  }
}

/**
 * Reinstate an account.
 */
function unsuspend(user) {
  app
    .request({ method: 'DELETE', url: apiUrl() + '/users/' + user.id() + '/suspend' })
    .then((payload) => {
      user.pushAttributes(payload.data.attributes);
      m.redraw();
    })
    .catch(() => {});
}

export const extend = [
  {
    extend() {
      extendMethod(User.prototype, 'badges', function (badges) {
        if (!suspendedUntil(this)) return;

        badges.add(
          'suspended',
          <Badge
            type="suspended"
            icon="fas fa-ban"
            label={app.translator.trans('flarum-suspend.forum.user_badge.suspended_tooltip')}
            tabindex="0"
          />,
          30
        );
      });

      extendMethod(UserControls, 'moderationControls', function (items, user) {
        if (!canSuspend(user)) return;

        if (suspendedUntil(user)) {
          items.add(
            'unsuspend',
            <Button icon="fas fa-undo" onclick={() => unsuspend(user)}>
              {app.translator.trans('flarum-suspend.forum.user_controls.unsuspend_button')}
            </Button>
          );
          return;
        }

        items.add(
          'suspend',
          <Button icon="fas fa-ban" onclick={() => app.modal.show(SuspendUserModal, { user })}>
            {app.translator.trans('flarum-suspend.forum.user_controls.suspend_button')}
          </Button>
        );
      });
    },
  },
];

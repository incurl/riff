import app from '../app';
import Component from '../../common/Component';
import type Mithril from 'mithril';
import ItemList from '../../common/utils/ItemList';
import Alert from '../../common/components/Alert';
import Button from '../../common/components/Button';
import Icon from '../../common/components/Icon';

export default class Notices extends Component {
  view(): Mithril.Children {
    return <div className="App-notices">{this.items().toArray()}</div>;
  }

  items(): ItemList<Mithril.Children> {
    const items = new ItemList<Mithril.Children>();

    const user = app.session.user;

    // The email-confirmation prompt is NOT shown, and that is a
    // decision rather than an omission.
    //
    // Upstream shows it for an account that has not confirmed its
    // address, and the prompt is NOT dismissible: the account is
    // expected to confirm, so dismissing the reminder would only hide
    // something the reader still has to do. The prompt's button asks
    // the server to send the confirmation again.
    //
    // This port has NEITHER half of that. The account's confirmation
    // belongs to the host's auth layer — the forum has no `/register`,
    // no `/confirm` and no `/send-confirmation` endpoint (see
    // `docs/design/flarum/shared-users.md`) — and the user resource
    // carries no `isEmailConfirmed` attribute, because reading
    // PocketBase's reserved `verified` field is exactly what that
    // document forbids.
    //
    // An ATTRIBUTE THAT IS ABSENT reads as `undefined`, and
    // `!undefined` is `true`: the prompt would therefore be shown to
    // EVERY signed-in member, it could not be dismissed, and its
    // button would answer 404. An undismissable alert that asks for
    // something the forum cannot give is the worst of the three
    // outcomes, so the prompt is not rendered.
    //
    // The account's own address is still shown on its profile and in
    // the admin's user list; nothing about the account is hidden by
    // this.

    if (user && user.emailBouncedAt()) {
      items.add(
        'emailBounced',
        <Alert
          type="error"
          dismissible={false}
          controls={[
            <Button className="Button Button--link" onclick={() => app.modal.show(() => import('./ChangeEmailModal'))}>
              {app.translator.trans('core.forum.user_email_bounced.update_button')}
            </Button>,
          ]}
          className="Alert--emailBounced"
          containerClassName="container"
        >
          {app.translator.trans('core.forum.user_email_bounced.alert_message', { email: <strong>{user.email()}</strong> })}
        </Alert>,
        95
      );
    }

    if (app.data.bisecting) {
      items.add(
        'bisecting',
        <Alert
          type="error"
          dismissible={false}
          className="Alert--bisecting"
          containerClassName="container"
          controls={[
            <a className="Button Button--link" target="_blank" href={app.forum.attribute('adminUrl') + '#/advanced?modal=extension-bisect'}>
              {app.translator.trans('core.lib.notices.bisecting_continue')}
            </a>,
          ]}
        >
          {app.translator.trans('core.lib.notices.bisecting')}
        </Alert>,
        90
      );
    }

    if (app.data.maintenanceMode) {
      items.add(
        'maintenanceMode',
        <Alert type="error" dismissible={false} className="Alert--maintenanceMode" containerClassName="container">
          {app.translator.trans('core.lib.notices.maintenance_mode_' + app.data.maintenanceMode)}
        </Alert>,
        80
      );
    }

    return items;
  }
}

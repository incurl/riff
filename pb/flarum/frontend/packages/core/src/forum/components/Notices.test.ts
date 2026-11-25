/**
 * The forum's NOTICES area must not show an alert the forum cannot act
 * on.
 *
 * The area renders alerts that are NOT dismissible — upstream's
 * email-confirmation prompt, the bounced-address notice, the
 * maintenance-mode notice. That is deliberate upstream: each one asks
 * the reader to do something, so dismissing the reminder would only
 * hide something they still have to do.
 *
 * The email-confirmation prompt is the one this port must NOT show.
 * It asks for a confirmation the forum cannot deliver:
 *
 *   - the account's confirmation belongs to the host's auth layer, so
 *     the forum has no `/confirm` and no `/send-confirmation`
 *     endpoint (see `docs/design/flarum/shared-users.md`);
 *   - the user resource carries no `isEmailConfirmed` attribute,
 *     because reading PocketBase's reserved `verified` field is what
 *     that document forbids.
 *
 * An ATTRIBUTE THAT IS ABSENT reads as `undefined`, and
 * `!undefined` is `true` — so the prompt would be shown to EVERY
 * signed-in member, it could not be dismissed, and its button would
 * answer 404.
 *
 * The contract is the GUARD, so the test reads the SOURCE rather
 * than mounting the area: importing the component pulls in the forum
 * application, whose constructor mounts a dozen components — and the
 * redraw that follows touches them all, which is not a component
 * test's business.
 */
const fs = require('fs');
const path = require('path');

const source = fs.readFileSync(path.resolve(__dirname, 'Notices.tsx'), 'utf8');

describe('Notices', () => {
  it('does not guard on the account\'s confirmation state', () => {
    // The guard upstream is `if (user && !user.isEmailConfirmed())`.
    // It must be gone: an absent attribute reads as `undefined`, and
    // `!undefined` is `true`.
    expect(source).not.toContain('isEmailConfirmed()');
  });

  it('does not render the confirmation prompt', () => {
    expect(source).not.toContain('Alert--emailConfirmation');
  });

  it('does not ask the server to send a confirmation', () => {
    // The prompt's button called `/users/{id}/send-confirmation`,
    // which this port does not have. The check is on the CALL, so
    // the explanation in the comment above does not trip it.
    expect(source).not.toMatch(/url:.*send-confirmation/);
  });

  it('still guards the bounced-address notice', () => {
    // The bounced-address alert is guarded by a POSITIVE test, so an
    // absent attribute does not trigger it — the opposite of the
    // confirmation prompt's guard, and the reason one of them had to
    // go and the other did not.
    expect(source).toContain('user.emailBouncedAt()');
  });

  it('still renders the notices area', () => {
    expect(source).toContain('App-notices');
  });
});

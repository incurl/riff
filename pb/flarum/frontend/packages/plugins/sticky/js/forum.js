// The Sticky extension's forum initialiser.
//
// It mirrors upstream `flarum/sticky`: a sticky badge on a pinned
// discussion, and a moderation control that toggles the flag for a caller
// holding the `sticky` permission.
//
// The badge carries priority 10 so it sorts ahead of the other badges, as
// upstream's does: a pinned discussion should read as pinned before
// anything else about it.
import app from 'flarum/forum/app';
import { extend as extendMethod } from 'flarum/common/extend';
import Discussion from 'flarum/common/models/Discussion';
import DiscussionControls from 'flarum/forum/utils/DiscussionControls';
import Badge from 'flarum/common/components/Badge';
import Button from 'flarum/common/components/Button';

/** The forum's API root, e.g. `/forum/api`. */
function apiUrl() {
  return app.forum.attribute('apiUrl');
}

/** Whether a discussion is pinned. */
function isSticky(discussion) {
  return !!discussion.attribute('isSticky');
}

/** Whether the caller may pin it. */
function canSticky(discussion) {
  return !!discussion.attribute('canSticky');
}

/**
 * Toggle the flag.
 *
 * The list is refreshed after the toggle on the index page, because the
 * ORDER changes as well as the badge: a newly pinned discussion belongs at
 * the top, and leaving the page as it was would show it in place until the
 * next navigation.
 */
function toggleSticky(discussion) {
  const method = isSticky(discussion) ? 'DELETE' : 'POST';

  app
    .request({ method, url: apiUrl() + '/discussions/' + discussion.id() + '/sticky' })
    .then((payload) => {
      discussion.pushAttributes(payload.data.attributes);

      if (app.current && app.current.matches && app.current.get('discussions')) {
        app.current.get('discussions').refresh().catch(() => {});
      }

      m.redraw();
    })
    .catch(() => {});
}

export const extend = [
  {
    extend() {
      extendMethod(Discussion.prototype, 'badges', function (badges) {
        if (!isSticky(this)) return;

        badges.add(
          'sticky',
          <Badge
            type="sticky"
            icon="fas fa-thumbtack"
            label={app.translator.trans('flarum-sticky.forum.badge.sticky_tooltip')}
            tabindex="0"
          />,
          10
        );
      });

      extendMethod(DiscussionControls, 'moderationControls', function (items, discussion) {
        if (!canSticky(discussion)) return;

        items.add(
          'sticky',
          <Button icon="fas fa-thumbtack" onclick={() => toggleSticky(discussion)}>
            {app.translator.trans(
              'flarum-sticky.forum.discussion_controls.' + (isSticky(discussion) ? 'unsticky' : 'sticky') + '_button'
            )}
          </Button>
        );
      });
    },
  },
];

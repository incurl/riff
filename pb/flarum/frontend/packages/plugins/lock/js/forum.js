// The Lock extension's forum initialiser.
//
// It mirrors upstream `flarum/lock`: a lock badge on a locked discussion,
// and a moderation control that toggles the flag for a caller holding the
// `lock` permission.
//
// NOTHING may touch `app.forum`, `app.session` or `app.store` at module
// scope: the shell runs `app.load(payload)`, then
// `app.bootExtensions(...)`, and only then `app.boot()` — and `app.boot()`
// is what assigns `app.forum` and `app.session`. The URL is therefore built
// inside the handlers, not at load time.
//
// The two flags are read with `attribute()` rather than through model
// methods: this port's Discussion model does not declare `isLocked` /
// `canLock`, and upstream's extensions add those methods through an
// extender that a hand-authored bundle does not need.
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

/** Whether a discussion is locked. */
function isLocked(discussion) {
  return !!discussion.attribute('isLocked');
}

/** Whether the caller may lock it. */
function canLock(discussion) {
  return !!discussion.attribute('canLock');
}

/**
 * Toggle the flag.
 *
 * The discussion model is updated from the response rather than refetched:
 * the endpoint answers the new `isLocked`, and a refetch would reload the
 * whole post stream for a one-word change.
 */
function toggleLock(discussion) {
  const method = isLocked(discussion) ? 'DELETE' : 'POST';

  app
    .request({ method, url: apiUrl() + '/discussions/' + discussion.id() + '/lock' })
    .then((payload) => {
      discussion.pushAttributes(payload.data.attributes);
      m.redraw();
    })
    .catch(() => {});
}

export const extend = [
  {
    extend() {
      extendMethod(Discussion.prototype, 'badges', function (badges) {
        if (!isLocked(this)) return;

        badges.add(
          'locked',
          <Badge
            type="locked"
            icon="fas fa-lock"
            label={app.translator.trans('flarum-lock.forum.badge.locked_tooltip')}
            tabindex="0"
          />,
          20
        );
      });

      extendMethod(DiscussionControls, 'moderationControls', function (items, discussion) {
        if (!canLock(discussion)) return;

        items.add(
          'lock',
          <Button icon="fas fa-lock" onclick={() => toggleLock(discussion)}>
            {app.translator.trans(
              'flarum-lock.forum.discussion_controls.' + (isLocked(discussion) ? 'unlock' : 'lock') + '_button'
            )}
          </Button>
        );
      });
    },
  },
];

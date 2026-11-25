// The Likes extension's forum initialiser.
//
// The post resource already carries `likesCount`, `isLiked` and
// `canLike` (the plugin contributes them as attributes), so the button
// needs no extra fetch to render. Toggling posts to the plugin's own
// route, and the post is then re-read so the count and the flag come
// from the server rather than from a local guess.
//
// NOTHING may touch `app.forum` or `app.session` in the extender: the
// shell runs `app.load(payload)`, then `app.bootExtensions(...)`, and
// only then `app.boot()` — and `app.boot()` is what assigns them.
// `app.request` reads `this.session.csrfToken`, so a request fired
// from an extender would throw on an undefined session.
//
// The extender only registers a hook, and the requests happen later, on
// a click — long after the app is booted.
import app from 'flarum/forum/app';
import Extend from 'flarum/common/extenders';
import { extend as extendMethod } from 'flarum/common/extend';
import classList from 'flarum/common/utils/classList';
import Icon from 'flarum/common/components/Icon';
import CommentPost from 'flarum/forum/components/CommentPost';
import Post from 'flarum/common/models/Post';
import Notification from 'flarum/forum/components/Notification';
import NotificationModel from 'flarum/common/models/Notification';

/**
 * The notification that tells an author somebody liked their post.
 *
 * It is upstream's `PostLikedNotification`: the thumb icon, a link to
 * the post, and a sentence naming who liked it.
 */
class PostLikedNotification extends Notification {
  icon() {
    return 'far fa-thumbs-up';
  }

  href() {
    const notification = this.attrs.notification;
    const post = notification.post();

    if (!post) {
      return '#';
    }

    return app.route.post(post);
  }

  content() {
    const user = this.attrs.notification.fromUser();

    return user ? `${user.displayName()} liked your post.` : 'Someone liked your post.';
  }

  excerpt() {
    // The server sends the summary in `excerpt` — the post's own
    // text, trimmed.
    return this.attrs.notification.excerpt() || null;
  }
}

// pending holds the post ids whose toggle request is in flight, so the
// button can disable itself instead of accepting a double click.
const pending = new Set();

/**
 * Re-read a post.
 *
 * The plugin's attributes are computed server-side, so a refetch is the
 * only way to get an authoritative count; pushing the payload then
 * updates the model the component is already rendering.
 */
function refreshPost(post) {
  return app.request({ url: app.forum.attribute('apiUrl') + '/posts/' + post.id() }).then((payload) => app.store.pushPayload(payload));
}

/**
 * Toggle the like state of a post.
 */
function toggleLike(post) {
  const id = post.id();

  if (pending.has(id)) return;

  pending.add(id);
  m.redraw();

  const method = post.isLiked() ? 'DELETE' : 'POST';

  app
    .request({ method, url: app.forum.attribute('apiUrl') + '/posts/' + id + '/like' })
    .then(() => refreshPost(post))
    .catch(() => {})
    .then(() => {
      pending.delete(id);
      m.redraw();
    });
}

/**
 * The like button for a post.
 */
function likeButton(post) {
  const id = post.id();
  const count = post.likesCount() || 0;
  const liked = !!post.isLiked();
  const canLike = !!post.canLike();
  const busy = pending.has(id);

  return (
    <button
      type="button"
      className={classList('Button Button--link Post-like', { 'Post-like--liked': liked })}
      onclick={() => toggleLike(post)}
      disabled={!canLike || busy}
      aria-pressed={liked ? 'true' : 'false'}
      aria-label={liked ? 'Unlike' : 'Like'}
      title={liked ? 'Unlike' : 'Like'}
    >
      <Icon icon={liked ? 'fas fa-thumbs-up' : 'far fa-thumbs-up'} />
      {count > 0 ? <span className="Post-like-count">{count}</span> : null}
    </button>
  );
}

export const extend = [
  // The notification's `post` relationship, and its excerpt.
  //
  // The server sends both, and the notification component links to the
  // post — but the core's Notification model declares no accessor for
  // either, so `notification.post()` and `notification.excerpt()` do
  // not exist without this.
  new Extend.Model(NotificationModel).hasOne('post').attribute('excerpt'),

  // The notification component for the like kind.
  //
  // The name is the kind the SERVER sends: the notifications
  // collection's `type` field carries `postLiked` (upstream's
  // `PostLikedBlueprint::getType()`), and the serializer publishes it
  // as `contentType`.
  //
  // WITHOUT this the notification renders as an EMPTY ROW: the SPA
  // looks the component up by that name
  // (`NotificationType.tsx` reads `notification.contentType()` and
  // uses it as the key into `app.notificationComponents`), and an
  // unregistered kind has no component — while the unread badge still
  // counts it, because the badge reads `isRead`. The symptom is a
  // badge with a blank list.
  new Extend.Notification().add('postLiked', PostLikedNotification),

  // The post carries the likes, the count and the flag.
  //
  // They have to be registered as MODEL METHODS: the server sends
  // them as plain attributes, and `post.isLiked()` is a method
  // call — without the registration the method does not exist on the
  // model and the render throws.
  //
  // `isLiked` is the one that is easy to forget, because the button also
  // reads `likesCount` and `canLike` and the Go side sends all three. It
  // was missing, and the consequence was not a wrong count: `post.isLiked`
  // was `undefined`, the call threw inside the `extend` callback, and
  // `extend()` swallows that (it reports it once through
  // `app.handleErrorOnce`) — so the like button never rendered at all.
  new Extend.Model(Post).hasMany('likes').attribute('isLiked').attribute('likesCount').attribute('canLike'),

  {
    extend() {
      extendMethod(CommentPost.prototype, 'footerItems', function (items) {
        const post = this.attrs.post;

        if (!post || post.contentType() !== 'comment') return;

        items.add('like', likeButton(post), 10);
      });

      // The row on the settings page's notification grid.
      //
      // Without it the like notification arrives and the reader has no
      // control over it — and no way to learn it exists. The key the grid
      // writes is `notify_postLiked_alert`, which the server publishes in
      // the user's `preferences` and honours when it writes the
      // notification, so the checkbox and the write path agree.
      //
      // The component is named by PATH, not imported: `NotificationGrid` lives
      // in a lazy chunk (`flarum.reg.addChunkModule("298", "3711", "core",
      // "forum/components/NotificationGrid")`), so a static import would
      // resolve to `undefined` at bundle-evaluation time and the `extend` call
      // would throw. Naming the path defers the registration through
      // `flarum.reg.onLoad` until the chunk lands — which is what upstream's
      // own `extend('flarum/forum/components/NotificationGrid', ...)` does.
      //
      // The label is untranslated English: this port ships no locale bundle
      // for the extension, and a missing key renders literally.
      extendMethod('flarum/forum/components/NotificationGrid', 'notificationTypes', function (items) {
        items.add('postLiked', {
          name: 'postLiked',
          icon: 'far fa-thumbs-up',
          label: 'Notify me when someone likes my post',
        });
      });
    },
  },
];

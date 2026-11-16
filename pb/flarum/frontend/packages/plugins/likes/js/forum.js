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
import { extend as extendMethod } from 'flarum/common/extend';
import classList from 'flarum/common/utils/classList';
import Icon from 'flarum/common/components/Icon';
import CommentPost from 'flarum/forum/components/CommentPost';

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

  const method = post.attribute('isLiked') ? 'DELETE' : 'POST';

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
  const count = post.attribute('likesCount') || 0;
  const liked = !!post.attribute('isLiked');
  const canLike = !!post.attribute('canLike');
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
  {
    extend() {
      extendMethod(CommentPost.prototype, 'footerItems', function (items) {
        const post = this.attrs.post;

        if (!post || post.contentType() !== 'comment') return;

        items.add('like', likeButton(post), 10);
      });
    },
  },
];

// The Gamification extension's forum initialiser.
//
// It mirrors upstream `fof/gamification`'s vote controls: an upvote, the
// score and a downvote on a post.
//
// The vote is a ROUTE in this port (`POST /api/posts/{id}/vote`) rather
// than a writable field on the post, because the permission that governs
// it (`votePosts`) is the extension's own and the core's post update
// handler does not consult it. The button therefore posts to the route and
// applies the answer to the model.
//
// NOTHING may touch `app.forum` or `app.session` at module scope: the
// shell boots the extensions before `app.boot()` assigns them.
import app from 'flarum/forum/app';
import { extend as extendMethod } from 'flarum/common/extend';
import CommentPost from 'flarum/forum/components/CommentPost';
import Button from 'flarum/common/components/Button';
import classList from 'flarum/common/utils/classList';

/** The forum's API root, e.g. `/forum/api`. */
function apiUrl() {
  return app.forum.attribute('apiUrl');
}

/**
 * Record a vote.
 *
 * `vote` is "up", "down" or null to withdraw. The response carries the
 * new score and the new state, so the model is updated from it rather
 * than refetched.
 */
function saveVote(post, vote) {
  return app
    .request({
      method: 'POST',
      url: apiUrl() + '/posts/' + post.id() + '/vote',
      body: { data: { attributes: { vote } } },
    })
    .then((payload) => {
      post.pushAttributes(payload.data.attributes);
      m.redraw();
    })
    .catch(() => {});
}

export const extend = [
  {
    extend() {
      extendMethod(CommentPost.prototype, 'actionItems', function (items) {
        const post = this.attrs.post;

        // Render NOTHING where voting does not apply: a disabled control
        // still tells the reader that voting exists here.
        if (!post.canSeeVotes()) return;

        // A deleted post keeps no vote controls.
        if (post.isHidden()) return;

        const hasUpvoted = !!post.hasUpvoted();
        const hasDownvoted = !!post.hasDownvoted();

        // A guest may click, and is sent to the login — the control is
        // how a reader discovers that voting needs an account.
        const canVote = !app.session.user || post.canVote();

        const up = (
          <Button
            type="button"
            icon="fas fa-thumbs-up"
            className={classList('Post-vote Post-vote-button--up', hasUpvoted && 'is-voted')}
            disabled={!canVote}
            aria-label="Upvote"
            title="Upvote"
            onclick={() => saveVote(post, hasUpvoted ? null : 'up')}
          />
        );

        const down = (
          <Button
            type="button"
            icon="fas fa-thumbs-down"
            className={classList('Post-vote Post-vote-button--down', hasDownvoted && 'is-voted')}
            disabled={!canVote}
            aria-label="Downvote"
            title="Downvote"
            onclick={() => saveVote(post, hasDownvoted ? null : 'down')}
          />
        );

        items.add(
          'votes',
          <div className="Post-vote">
            {up}
            <span className="Post-vote-score">{post.votes()}</span>
            {down}
          </div>,
          10
        );
      });
    },
  },
];

// The Gamification extension's forum initialiser.
//
// It mirrors upstream `fof/gamification`'s forum half:
//
//   vote buttons       an upvote, the score and a downvote on a post
//   /rankings          the leaderboard page (components/RankingsPage.js)
//   rank badges        a member's ranks next to their name (addUserInfo.js)
//   the list's score   the first post's tally on a discussion row
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
import Extend from 'flarum/common/extenders';
import { extend as extendMethod } from 'flarum/common/extend';
import CommentPost from 'flarum/forum/components/CommentPost';
import Post from 'flarum/common/models/Post';
import Discussion from 'flarum/common/models/Discussion';
import User from 'flarum/common/models/User';
import Button from 'flarum/common/components/Button';
import classList from 'flarum/common/utils/classList';

import { Rank, LeaderboardEntry } from './models';
import RankingsPage from './components/RankingsPage';
import { addUserInfo } from './addUserInfo';
import { addUpvotesToDiscussion } from './addUpvotesToDiscussion';

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
  // The store must know the two plugin-owned types, or `pushObject` throws
  // "Pushing object of type `ranks` not allowed" and the response is
  // dropped — a rank badge and a leaderboard row that never appear.
  new Extend.Store().add('ranks', Rank).add('leaderboard-entries', LeaderboardEntry),

  // `/rankings` is upstream's route name and path.
  new Extend.Routes().add('rankings', '/rankings', RankingsPage),

  // The post's vote attributes have to be registered as MODEL METHODS.
  //
  // The server sends them as plain attributes, but `post.canSeeVotes()`
  // is a method call: without the registration the method does not
  // exist on the model and the render throws. The raw attributes alone
  // do not create the methods.
  new Extend.Model(Post)
    .attribute('votes')
    .attribute('canSeeVotes')
    .attribute('canVote')
    .attribute('hasUpvoted')
    .attribute('hasDownvoted')
    .attribute('seeVoters'),

  // The discussion carries the first post's tally, which the list renders.
  new Extend.Model(Discussion)
    .attribute('votes')
    .attribute('seeVotes')
    .attribute('canVote')
    .attribute('hasUpvoted')
    .attribute('hasDownvoted'),

  // The account's score and the ranks it has earned. `ranks` is a
  // RELATIONSHIP: its resources travel in `included`, and `ranksToShow`
  // drops an identifier whose resource has not arrived rather than
  // throwing on it.
  new Extend.Model(User).attribute('points').attribute('votes').attribute('rank').hasMany('ranks'),

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

      addUserInfo();
      addUpvotesToDiscussion();
    },
  },
];

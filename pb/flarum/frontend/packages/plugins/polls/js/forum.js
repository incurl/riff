// The Polls extension's forum initialiser.
//
// It mirrors upstream `fof/polls`'s post poll: the poll is rendered
// under the post's content, its options are radio buttons (or checkboxes
// when the poll allows several), and picking one votes.
//
// The vote is a ROUTE in this port (`PATCH /api/polls/{id}/votes`)
// rather than a writable field on the poll, because the permission that
// governs it (`polls.vote`) is the extension's own and the poll update
// handler does not consult it.
//
// The body carries the voter's whole SELECTION rather than a delta, so
// the client and the server cannot disagree about what "my vote" is.
//
// NOTHING may touch `app.forum` or `app.session` at module scope: the
// shell boots the extensions before `app.boot()` assigns them.
import app from 'flarum/forum/app';
import Extend from 'flarum/common/extenders';
import { extend as extendMethod } from 'flarum/common/extend';
import CommentPost from 'flarum/forum/components/CommentPost';
import Post from 'flarum/common/models/Post';
import Icon from 'flarum/common/components/Icon';
import { Poll, PollOption, PollVote } from './models';

/** The forum's API root, e.g. `/forum/api`. */
function apiUrl() {
  return app.forum.attribute('apiUrl');
}

/**
 * The option ids the account has voted for.
 *
 * The votes travel in the poll's `myVotes` relationship, and each vote
 * names its option. A poll that allows several votes holds one vote per
 * option, so the ids are collected from every one of them.
 */
function selectedOptionIds(poll) {
  const ids = [];

  for (const vote of poll.myVotes()) {
    const option = vote.option();
    if (option && option.id()) {
      ids.push(option.id());
    }
  }

  return ids;
}

/**
 * Record a selection.
 *
 * The response carries the poll with its new counts and the voter's own
 * votes, so the store is updated from it rather than refetched.
 */
function saveVote(poll, optionIds) {
  return app
    .request({
      method: 'PATCH',
      url: apiUrl() + '/polls/' + poll.id() + '/votes',
      body: { data: { optionIds } },
    })
    .then((payload) => {
      app.store.pushPayload(payload);
      m.redraw();
    })
    .catch(() => {
      m.redraw();
    });
}

/**
 * Toggle an option in the current selection.
 *
 * A poll that allows one vote replaces the selection; a poll that allows
 * several adds to it, and un-ticking an option removes it.
 */
function toggleOption(poll, optionId, selected) {
  const ids = selected.slice();

  if (poll.allowMultipleVotes()) {
    const at = ids.indexOf(optionId);
    if (at === -1) {
      ids.push(optionId);
    } else {
      ids.splice(at, 1);
    }
  } else if (ids.length === 1 && ids[0] === optionId) {
    // Un-ticking the only ticked option withdraws the vote.
    ids.length = 0;
  } else {
    ids.length = 0;
    ids.push(optionId);
  }

  saveVote(poll, ids);
}

/**
 * The poll under a post.
 *
 * It renders the question, the options and the total.
 */
function PostPoll({ poll, post }) {
  const selected = selectedOptionIds(poll);
  const voted = selected.length > 0;
  const ended = poll.hasEnded();
  const canVote = poll.canVote() && !ended;
  const showCount = poll.voteCount() !== undefined;

  return (
    <div className="Post-poll">
      <h3 className="Post-poll-title">{poll.question()}</h3>

      {poll.subtitle() ? <p className="Post-poll-subtitle helpText">{poll.subtitle()}</p> : null}

      <fieldset className="Post-poll-options">
        <legend className="sr-only">{poll.question()}</legend>

        {poll.options().map((option) => {
          if (!option) return null;

          const checked = selected.indexOf(option.id()) !== -1;
          const disabled = !canVote || (voted && !poll.canChangeVote());

          return (
            <label className="Post-poll-option" key={option.id()}>
              <input
                type={poll.allowMultipleVotes() ? 'checkbox' : 'radio'}
                name={'poll' + poll.id()}
                value={option.id()}
                checked={checked}
                disabled={disabled}
                onchange={() => toggleOption(poll, option.id(), selected)}
              />
              <span className="Post-poll-answer">{option.answer()}</span>
              {showCount && option.voteCount() !== undefined ? (
                <span className="Post-poll-count">{option.voteCount()}</span>
              ) : null}
            </label>
          );
        })}
      </fieldset>

      {showCount ? (
        <p className="Post-poll-total helpText">
          <Icon name="fas fa-poll" className="fa-fw" />
          {app.translator.trans('polls.forum.total_votes', { count: poll.voteCount() })}
        </p>
      ) : null}

      {ended ? (
        <p className="Post-poll-ended helpText">
          <Icon name="fas fa-clock" className="fa-fw" />
          {app.translator.trans('polls.forum.poll_ended')}
        </p>
      ) : null}
    </div>
  );
}

export const extend = [
  // The store needs to know the three types, or the frontend cannot
  // build a model out of the payload: `pushObject` throws "Pushing
  // object of type `polls` not allowed" for an unregistered type.
  new Extend.Store().add('polls', Poll).add('poll_options', PollOption).add('poll_votes', PollVote),

  // The post carries the polls.
  new Extend.Model(Post).hasMany('polls'),

  {
    extend() {
      extendMethod(CommentPost.prototype, 'content', function (content) {
        const post = this.attrs.post;

        if (!post || post.isHidden()) return;

        const polls = post.polls();
        if (!polls || !polls.length) return;

        for (const poll of polls) {
          if (!poll) continue;
          content.push(<PostPoll poll={poll} post={post} />);
        }
      });
    },
  },
];

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

  // The COUNT is shown to a reader who may see it: the server OMITS the
  // attribute when the caller may not, and the frontend reads the
  // absence as "hidden". A zero would read as "nobody has voted".
  const canSeeVoteCount = typeof poll.voteCount() === 'number';

  // A GUEST gets a LIVE control: clicking it asks them to log in.
  //
  // Upstream's `PollState.canSelect` returns true for a guest on
  // purpose — the control is offered, and the click opens the login
  // modal. Refusing to render the control instead leaves the reader
  // with a poll they cannot interact with and no explanation.
  const guest = !app.session.user;

  const disabled = !guest && (!canVote || (voted && !poll.canChangeVote()));

  return (
    <div className="Post-poll">
      <h3 className="Post-poll-title">{poll.question()}</h3>

      {poll.subtitle() ? <p className="Post-poll-subtitle helpText">{poll.subtitle()}</p> : null}

      <fieldset className="Post-poll-options">
        <legend className="sr-only">{poll.question()}</legend>

        {poll.options().map((option) => {
          if (!option) return null;

          const checked = selected.indexOf(option.id()) !== -1;

          return (
            <label className="Post-poll-option" key={option.id()}>
              <input
                type={poll.allowMultipleVotes() ? 'checkbox' : 'radio'}
                name={'poll' + poll.id()}
                value={option.id()}
                checked={checked}
                disabled={disabled}
                onchange={(event) => {
                  if (guest) {
                    // The tick is undone: the guest has not voted, and
                    // the control is offered only to ask them to log in.
                    //
                    // `flarum.reg.asyncModuleImport`, NOT `import(...)`: the
                    // shared webpack config only rewrites `import('flarum/…')`
                    // to that call for sources under `src/`, and an extension's
                    // sources live in `js/` — so the bare `import()` compiles to
                    // the config's SYNCHRONOUS external, `flarum.reg.get(...)`,
                    // which is undefined for a lazily-chunked core component.
                    // `ModalManagerState.show` awaits a function that resolves
                    // to `{ default: Modal }`, which is exactly what the async
                    // loader hands back.
                    event.target.checked = false;
                    app.modal.show(() => flarum.reg.asyncModuleImport('flarum/forum/components/LogInModal'));
                    return;
                  }
                  toggleOption(poll, option.id(), selected);
                }}
              />
              <span className="Post-poll-answer">{option.answer()}</span>
              {canSeeVoteCount && option.voteCount() !== undefined ? (
                <span className="Post-poll-count">{option.voteCount()}</span>
              ) : null}
            </label>
          );
        })}
      </fieldset>

      <ul className="Post-poll-info helpText">
        {app.session.user && !canVote && !ended ? (
          <li>
            <Icon name="fas fa-times-circle" className="fa-fw" />
            {app.translator.trans('fof-polls.forum.no_permission')}
          </li>
        ) : null}

        {ended ? (
          <li>
            <Icon name="fas fa-clock" className="fa-fw" />
            {app.translator.trans('fof-polls.forum.poll_ended')}
          </li>
        ) : null}

        {canVote && !ended && !voted ? (
          <li>
            <Icon name="fas fa-poll" className="fa-fw" />
            {app.translator.trans('fof-polls.forum.max_votes_allowed', { max: maxVotes(poll) })}
          </li>
        ) : null}

        {canSeeVoteCount && (ended || voted) ? (
          <li>
            <Icon name="fas fa-poll" className="fa-fw" />
            {app.translator.trans('fof-polls.forum.poll.total_votes', { count: poll.voteCount() })}
          </li>
        ) : null}
      </ul>
    </div>
  );
}

/**
 * How many options a poll lets a voter pick.
 *
 * A poll that allows one vote allows exactly one. A poll that allows
 * several caps them, and a cap of ZERO means "as many as there are
 * options" — upstream's reading of `max_votes = 0`.
 */
function maxVotes(poll) {
  let max = poll.allowMultipleVotes() ? poll.maxVotes() : 1;

  if (max === 0) max = poll.options().length;

  return max;
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

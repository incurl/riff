// The Polls extension's models.
//
// They mirror upstream `fof/polls`'s `Poll`, `PollOption` and `PollVote`
// models: the attribute names the server sends are the names the
// frontend reads.
//
// The models are PLAIN classes extending the core's `Model`, not
// TypeScript: the bundle is built by webpack against the vendored
// Flarum source, and a `.ts` file would need the extension's own
// tsconfig, which the other riff bundles do not carry either.
import Model from 'flarum/common/Model';

/**
 * A poll.
 *
 * The server sends `question`, `hasEnded`, `canVote`, `voteCount` and
 * the rest as plain attributes, so the model is a thin reader over
 * them.
 */
export class Poll extends Model {
  question() {
    return Model.attribute('question').call(this);
  }

  subtitle() {
    return Model.attribute('subtitle').call(this);
  }

  hasEnded() {
    return Model.attribute('hasEnded').call(this);
  }

  endDate() {
    return Model.attribute('endDate', Model.transformDate).call(this);
  }

  publicPoll() {
    return Model.attribute('publicPoll').call(this);
  }

  hideVotes() {
    return Model.attribute('hideVotes').call(this);
  }

  allowChangeVote() {
    return Model.attribute('allowChangeVote').call(this);
  }

  allowMultipleVotes() {
    return Model.attribute('allowMultipleVotes').call(this);
  }

  maxVotes() {
    return Model.attribute('maxVotes').call(this);
  }

  voteCount() {
    return Model.attribute('voteCount').call(this);
  }

  canVote() {
    return Model.attribute('canVote').call(this);
  }

  canEdit() {
    return Model.attribute('canEdit').call(this);
  }

  canDelete() {
    return Model.attribute('canDelete').call(this);
  }

  canSeeVoters() {
    return Model.attribute('canSeeVoters').call(this);
  }

  canChangeVote() {
    return Model.attribute('canChangeVote').call(this);
  }

  isGlobal() {
    return Model.attribute('isGlobal').call(this);
  }

  options() {
    const options = Model.hasMany('options').call(this);
    return options || [];
  }

  votes() {
    const votes = Model.hasMany('votes').call(this);
    return votes || [];
  }

  myVotes() {
    const votes = Model.hasMany('myVotes').call(this);
    return votes || [];
  }

  post() {
    return Model.hasOne('post').call(this);
  }

  user() {
    return Model.hasOne('user').call(this);
  }
}

/**
 * One option of a poll.
 */
export class PollOption extends Model {
  answer() {
    return Model.attribute('answer').call(this);
  }

  voteCount() {
    return Model.attribute('voteCount').call(this);
  }

  poll() {
    return Model.hasOne('poll').call(this);
  }
}

/**
 * One vote: an account's vote for an option.
 */
export class PollVote extends Model {
  /**
   * The poll the vote belongs to.
   *
   * Upstream's `PollVote` declares `pollId` and `optionId` as
   * ATTRIBUTES, and the frontend reads them by name. The port's server
   * sends them, and this model reads them.
   */
  pollId() {
    return Model.attribute('pollId').call(this);
  }

  optionId() {
    return Model.attribute('optionId').call(this);
  }

  poll() {
    return Model.hasOne('poll').call(this);
  }

  option() {
    return Model.hasOne('option').call(this);
  }

  user() {
    return Model.hasOne('user').call(this);
  }
}

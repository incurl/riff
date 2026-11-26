// The vote tally on a discussion in the LIST.
//
// It is upstream's `addUpvotesToDiscussion`: the discussion list shows the
// first post's score next to the reply count, so a reader sees how a
// discussion is received before opening it.
//
// The server serves the five fields on the DISCUSSION resource
// (`votes`, `seeVotes`, `canVote`, `hasUpvoted`, `hasDownvoted`), all of
// them the state of the discussion's FIRST post. `votes` is ABSENT for a
// reader who may not see the tally, which is upstream's `visible()`, so the
// control is hidden rather than shown as zero.
import { extend } from 'flarum/common/extend';
import DiscussionListItem from 'flarum/forum/components/DiscussionListItem';

export function addUpvotesToDiscussion() {
  extend(DiscussionListItem.prototype, 'infoItems', function (items) {
    const discussion = this.attrs.discussion;

    if (typeof discussion.seeVotes !== 'function' || !discussion.seeVotes()) return;
    if (discussion.votes() === undefined || discussion.votes() === null) return;

    items.add(
      'votes',
      <span className="DiscussionListItem-votes">
        <i className="fas fa-thumbs-up" /> {discussion.votes()}
      </span>,
      40
    );
  });
}

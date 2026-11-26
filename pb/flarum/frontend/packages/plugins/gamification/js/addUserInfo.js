// The rank badges and the points a member has earned.
//
// It is upstream's `addUserInfo`, which puts a rank stack next to a name in
// both places a member appears: on a post (`PostUser`) and on the user card.
//
// The badges come from `user.ranks()`, which is a RELATIONSHIP: the server
// sends the identifiers AND the resources (`included`), so the store has the
// names and colours. A relationship whose resources did not travel would
// read as an empty stack here, with no error anywhere.
//
// Upstream does DOM surgery on the rendered vnode to insert the badges; this
// port adds them through the ITEM LIST each component already offers, which
// is the same result without reaching into another component's markup.
import app from 'flarum/forum/app';
import { extend } from 'flarum/common/extend';
import PostUser from 'flarum/forum/components/PostUser';
import UserCard from 'flarum/forum/components/UserCard';

import { ranksToShow, rankLabel } from './models';

export function addUserInfo() {
  extend(PostUser.prototype, 'userViewItems', function (items, user) {
    if (!user) return items;

    const ranks = ranksToShow(user, app.forum);
    if (ranks.length === 0) return items;

    items.add('ranks', <span className="Post-Ranks">{ranks.map((rank) => rankLabel(rank))}</span>, 89);

    return items;
  });

  extend(UserCard.prototype, 'infoItems', function (items) {
    const user = this.attrs.user;
    if (!user || typeof user.points !== 'function' || user.points() === undefined) return items;

    items.add(
      'points',
      <span className="UserCard-points">
        <i className="fas fa-medal" /> {user.points()} points
      </span>,
      50
    );

    return items;
  });

  extend(UserCard.prototype, 'contentItems', function (items) {
    const user = this.attrs.user;

    const ranks = user ? ranksToShow(user, app.forum) : [];
    if (ranks.length === 0) return items;

    // The card already renders a `badges` list (the group badges); the rank
    // stack goes right below it, which is where upstream's DOM surgery puts
    // it.
    items.add(
      'ranks',
      <ul className="UserCard-ranks badges">{ranks.map((rank) => <li>{rankLabel(rank)}</li>)}</ul>,
      85
    );

    return items;
  });
}

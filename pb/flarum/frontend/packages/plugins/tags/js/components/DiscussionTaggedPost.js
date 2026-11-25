// The post that records that a discussion's tags changed.
//
// Upstream's `flarum/tags`'s `DiscussionTaggedPost`. It extends the core's
// `EventPost` — the same base the `discussionRenamed` post uses — and says
// which tags were added and which removed.
//
// The post's content is `[oldTagIds, newTagIds]`: two ARRAYS of ids, not
// two strings. `post.content()[0]` is the first element, so a string would
// make it the first CHARACTER and the diff would find nothing.
//
// The tags are resolved through the STORE, by id. A tag that has not been
// loaded resolves to `undefined`, and the diff SKIPS it rather than
// rendering "undefined" — a tag that was deleted is not a tag the reader can
// be shown.
import app from 'flarum/forum/app';
import EventPost from 'flarum/forum/components/EventPost';

import { tagsLabel } from '../helpers';

export default class DiscussionTaggedPost extends EventPost {
  oninit(vnode) {
    super.oninit(vnode);

    const content = this.attrs.post.content();
    const oldTags = Array.isArray(content) ? content[0] || [] : [];
    const newTags = Array.isArray(content) ? content[1] || [] : [];

    this.tagsAdded = this.diffTags(newTags, oldTags);
    this.tagsRemoved = this.diffTags(oldTags, newTags);
  }

  /**
   * The tags in `tags` that are not in `other`.
   */
  diffTags(tags, other) {
    return tags
      .filter((id) => !other.includes(id))
      .map((id) => app.store.getById('tags', id))
      .filter(Boolean);
  }

  icon() {
    return 'fas fa-tag';
  }

  description() {
    const parts = [];

    if (this.tagsAdded.length) {
      parts.push(
        <span className="DiscussionTaggedPost-added">
          Added {tagsLabel(this.tagsAdded, { link: true })}
        </span>
      );
    }

    if (this.tagsRemoved.length) {
      parts.push(
        <span className="DiscussionTaggedPost-removed">
          Removed {tagsLabel(this.tagsRemoved, { link: true })}
        </span>
      );
    }

    if (parts.length === 0) {
      return <span>The tags did not change.</span>;
    }

    return <span>{parts.join(' ')}</span>;
  }
}

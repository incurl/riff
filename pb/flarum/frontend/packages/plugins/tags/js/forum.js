// The Tags extension's forum initialiser.
//
// A Flarum extension bundle exports `{ extend: [...] }`. The boot shell
// loads the bundle, hands `flarum.extensions` to `app.bootExtensions`,
// and each entry's `extend(app, { name, exports })` runs before mount.
//
// The tag list comes from the plugin's own endpoint (`GET /api/tags`),
// because a discussion's `tags` relationship carries only resource
// identifiers — the tag names, colours and slugs are not included in
// the discussion payload. The response is pushed into the store, so
// `store.getById('tags', id)` resolves the identifiers the relationship
// holds.
import app from 'flarum/forum/app';
import { extend as extendMethod } from 'flarum/common/extend';
import classList from 'flarum/common/utils/classList';
import DiscussionListItem from 'flarum/forum/components/DiscussionListItem';

// tagsRequest is the in-flight (or settled) tag-list request. It is a
// single shared promise so a list of 20 discussions issues one request,
// not 20.
let tagsRequest = null;

/**
 * Load the tag list into the store.
 *
 * A failure is not fatal: the discussions still render, just without
 * tags. The promise is cleared on failure so a later redraw retries.
 */
export function loadTags() {
  if (!tagsRequest) {
    tagsRequest = app
      .request({ url: app.forum.attribute('apiUrl') + '/tags' })
      .then((payload) => {
        app.store.pushPayload(payload);

        return payload;
      })
      .catch((error) => {
        tagsRequest = null;
        throw error;
      });
  }

  return tagsRequest;
}

/**
 * The tag models of a discussion, in the order the server listed them.
 *
 * The relationship carries identifiers only, so each one is resolved
 * through the store; a tag that has not loaded yet is skipped rather
 * than rendering as a blank badge.
 */
function tagsOf(discussion) {
  const identifiers = discussion.data.relationships?.tags?.data;

  if (!Array.isArray(identifiers)) return [];

  return identifiers.map((identifier) => app.store.getById('tags', identifier.id)).filter(Boolean);
}

/**
 * A single tag badge.
 *
 * There is no tag page in this port, so the badge is a label rather
 * than a link: linking to a route the router does not have would send
 * the reader to the fallback page.
 */
function tagLabel(tag) {
  const color = tag.attribute('color');
  const icon = tag.attribute('icon');

  return (
    <span className="TagsLabel-item" title={tag.attribute('description') || null}>
      {color ? <span className="TagsLabel-dot" style={{ background: color }} /> : null}
      {icon ? <i className={icon} aria-hidden="true" /> : null}
      {tag.attribute('name')}
    </span>
  );
}

export const extend = [
  {
    extend() {
      // Seed the store before the list renders, so the first paint
      // already carries the badges.
      loadTags().catch(() => {});

      extendMethod(DiscussionListItem.prototype, 'infoItems', function (items) {
        const tags = tagsOf(this.attrs.discussion);

        if (tags.length === 0) return;

        items.add(
          'tags',
          <ul className={classList('TagsLabel', 'DiscussionListItem-tags')}>{tags.map((tag) => <li>{tagLabel(tag)}</li>)}</ul>,
          50
        );
      });
    },
  },
];

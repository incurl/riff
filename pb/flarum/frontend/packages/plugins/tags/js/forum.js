// The Tags extension's forum initialiser.
//
// A Flarum extension bundle exports `{ extend: [...] }`. The boot shell
// loads the bundle, hands `flarum.extensions` to `app.bootExtensions`,
// and each entry's `extend(app, { name, exports })` runs before mount.
//
// NOTHING may touch `app.forum`, `app.session` or `app.store` here: the
// shell runs `app.load(payload)`, then `app.bootExtensions(...)`, and
// only then `app.boot()` — and `app.boot()` is what assigns `app.forum`
// and `app.session`. `app.request` reads `this.session.csrfToken`, so a
// request fired from an extender throws on an undefined session.
//
// The work is therefore deferred to `app.beforeMount`, which runs inside
// `app.boot()` after the forum and the session exist.
//
// The tag list comes from the plugin's own endpoint (`GET /api/tags`),
// because a discussion's `tags` relationship carries only resource
// identifiers — the tag names, colours and slugs are not included in
// the discussion payload.
//
// The response is pushed into the STORE, and the `tags` type is
// registered as a model, exactly as upstream's extension does
// (`new Extend.Store().add('tags', Tag)`). Registering the type is
// what makes the store accept it: `pushObject` throws "Pushing object
// of type `tags` not allowed" for an unregistered type.
import app from 'flarum/forum/app';
import Extend from 'flarum/common/extenders';
import { extend as extendMethod } from 'flarum/common/extend';
import classList from 'flarum/common/utils/classList';
import DiscussionListItem from 'flarum/forum/components/DiscussionListItem';
import { Tag } from './models';

// tagsRequest is the in-flight (or settled) tag-list request. It is a
// single shared promise so a list of 20 discussions issues one request,
// not 20.
let tagsRequest = null;

/**
 * Load the tag list.
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
 * The tags of a discussion, in the order the server listed them.
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
  const color = tag.color();
  const icon = tag.icon();

  return (
    <span className="TagsLabel-item" title={tag.description() || null}>
      {color ? <span className="TagsLabel-dot" style={{ background: color }} /> : null}
      {icon ? <i className={icon} aria-hidden="true" /> : null}
      {tag.name()}
    </span>
  );
}

export const extend = [
  // The store needs to know the `tags` type, or it refuses the
  // payload: `pushObject` throws "Pushing object of type `tags` not
  // allowed" for a type that is not registered as a model.
  new Extend.Store().add('tags', Tag),

  {
    extend() {
      extendMethod(DiscussionListItem.prototype, 'infoItems', function (items) {
        const tags = tagsOf(this.attrs.discussion);

        if (tags.length === 0) return;

        items.add(
          'tags',
          <ul className={classList('TagsLabel', 'DiscussionListItem-tags')}>{tags.map((tag) => <li>{tagLabel(tag)}</li>)}</ul>,
          50
        );
      });

      // The request happens here, not in the extender: `app.forum` and
      // `app.session` do not exist yet.
      app.beforeMount(() => {
        loadTags()
          .then(() => m.redraw())
          .catch(() => {});
      });
    },
  },
];

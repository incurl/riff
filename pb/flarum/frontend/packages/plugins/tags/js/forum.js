// The Tags extension's forum initialiser.
//
// This mirrors upstream `flarum/tags`'s forum half, which registers SIX
// behaviours (`js/src/forum/index.ts`) plus a route model:
//
//   addTagList()     the shared tag list
//   addTagFilter()   the tag page's hero, title, button colour and the
//                    `filter[tag]` the discussion list sends
//   addTagLabels()   labels on list items, and on a discussion's hero
//   addTagControl()  (NOT ported — see the note at the end of this file)
//   addTagComposer() (NOT ported — see the note at the end of this file)
//   extendRealtime() (NOT ported: this port has no realtime extension)
//
// plus `Extend.Routes()` for `/tags` and `/t/:tags`, and
// `Extend.Model(Discussion)` for the `tags` relation and `canTag`.
//
// A Flarum extension bundle exports `{ extend: [...] }`. The boot shell loads
// the bundle, hands `flarum.extensions` to `app.bootExtensions`, and each
// entry's `extend(app, { name, exports })` runs before mount.
//
// NOTHING may touch `app.forum`, `app.session` or `app.store` at module scope:
// the shell runs `app.load(payload)`, then `app.bootExtensions(...)`, and only
// then `app.boot()` — and `app.boot()` is what assigns `app.forum` and
// `app.session`. `app.request` reads `this.session.csrfToken`, so a request
// fired from an extender throws on an undefined session.
//
// The work is therefore deferred to `app.beforeMount`, which runs inside
// `app.boot()` after the forum and the session exist.
import app from 'flarum/forum/app';
import Extend from 'flarum/common/extenders';
import { extend as extendMethod, override } from 'flarum/common/extend';
import classList from 'flarum/common/utils/classList';
import textContrastClass from 'flarum/common/helpers/textContrastClass';
import Discussion from 'flarum/common/models/Discussion';
import DiscussionHero from 'flarum/forum/components/DiscussionHero';
import DiscussionListItem from 'flarum/forum/components/DiscussionListItem';
import IndexPage from 'flarum/forum/components/IndexPage';
import IndexSidebar from 'flarum/forum/components/IndexSidebar';
import DiscussionListState from 'flarum/forum/states/DiscussionListState';
import GlobalSearchState from 'flarum/forum/states/GlobalSearchState';

import { Tag } from './models';
import { loadTags, sortTags, tagsLabel } from './helpers';
import TagHero from './components/TagHero';
import TagsIndexPage from './components/TagsIndexPage';

/** The cached tag for the current route. */
let activeTag = null;

/** Whether a tag-list request is in flight. */
let loadingTags = false;

/**
 * Find a loaded tag by slug, case-insensitively.
 *
 * @param {string} slug
 * @returns {object | null}
 */
function findBySlug(slug) {
  return (
    app.store.all('tags').find((tag) => (tag.slug() || '').localeCompare(slug, undefined, { sensitivity: 'base' }) === 0) || null
  );
}

/**
 * The tag the current route points at, if any.
 *
 * Upstream hangs this off the application (`app.currentTag()`); this keeps it
 * module-local, because nothing else needs it and a module-local cache cannot
 * leak a stale tag into a route that has none.
 *
 * The lookup is by SLUG against the store, which `loadTags()` fills. It
 * deliberately does not call `GET /api/tags/{slug}`: the port's show route
 * takes an ID, so a slug lookup would 404.
 *
 * @returns {object | null}
 */
export function currentTag() {
  const slug = m.route.param('tags');

  if (!slug) {
    activeTag = null;

    return null;
  }

  if (activeTag && (activeTag.slug() || '').localeCompare(slug, undefined, { sensitivity: 'base' }) === 0) {
    return activeTag;
  }

  const found = findBySlug(slug);

  if (found) {
    activeTag = found;

    return found;
  }

  // Not in the store yet: on a cold load of `/t/<slug>` the boot payload
  // carries the discussions but not necessarily this tag. Load the list once,
  // then redraw so the hero and the title appear.
  if (!loadingTags) {
    loadingTags = true;

    loadTags()
      .then(() => {
        activeTag = findBySlug(slug);
        m.redraw();
      })
      .catch(() => {})
      .finally(() => {
        loadingTags = false;
      });
  }

  return null;
}

export const extend = [
  // The store needs to know the `tags` type, or it refuses the payload:
  // `pushObject` throws "Pushing object of type `tags` not allowed" for a type
  // that is not registered as a model.
  new Extend.Store().add('tags', Tag),

  // `/tags` lists the taxonomy; `/t/:slug` is the ordinary index page filtered
  // to one tag — upstream's arrangement. The helper makes the pretty URL the
  // one every link uses.
  new Extend.Routes()
    .add('tags', '/tags', TagsIndexPage)
    .add('tag', '/t/:tags', IndexPage)
    .helper('tag', (tag) => app.route('tag', { tags: tag.slug() })),

  // A discussion carries its tags and the caller's tagging permission. Both
  // are already on the wire; declaring them here is what turns them into
  // `discussion.tags()` and `discussion.canTag()`.
  new Extend.Model(Discussion).hasMany('tags').attribute('canTag'),

  {
    extend() {
      addTagLabels();
      addTagFilter();

      // One shared tag-list request for the whole page load: the labels, the
      // sidebar and `/tags` all read from it.
      //
      // Guarded because `beforeMount` only exists once the application has
      // booted: the extension's own bundle is also evaluated by harnesses (the
      // Jest bundle test drives every extender against a stub app), and an
      // unguarded call would make the whole bundle throw instead of
      // registering.
      if (typeof app.beforeMount === 'function') {
        app.beforeMount(() => {
          loadTags()
            .then(() => m.redraw())
            .catch(() => {});
        });
      }
    },
  },
];

/**
 * Labels on a discussion, in both places upstream puts them.
 *
 * Upstream's `addTagLabels` extends THREE things, and the label on a list item
 * is only the first. The other two are what make a discussion page wear its
 * tag: the tag stack above the title, and the hero's background colour taken
 * from the first tag.
 */
function addTagLabels() {
  // The discussion list.
  extendMethod(DiscussionListItem.prototype, 'infoItems', function (items) {
    const tags = this.attrs.discussion.tags();

    if (tags && tags.length) {
      items.add('tags', tagsLabel(tags), 10);
    }
  });

  // The discussion's own hero: the tags go ABOVE the title, and each one
  // links to its tag page.
  extendMethod(DiscussionHero.prototype, 'items', function (items) {
    const tags = this.attrs.discussion.tags();

    if (tags && tags.length) {
      items.add('tags', tagsLabel(tags, { link: true }), 5);
    }
  });

  // The hero's colour comes from the FIRST tag, sorted — the primary one.
  //
  // Upstream writes this onto the vnode's attrs (`view.attrs.style`). That
  // does not work here: this port's `Hero.view()` renders `style={this.style()}`
  // and `classList('Hero', this.className())`, so attrs are never consulted.
  // The same information is returned from the two methods the port does read.
  override(DiscussionHero.prototype, 'style', function (original) {
    const tags = sortTags(this.attrs.discussion.tags() || []);

    if (tags.length && tags[0].color()) {
      return { '--hero-bg': tags[0].color() };
    }

    return original();
  });

  override(DiscussionHero.prototype, 'className', function (original) {
    const tags = sortTags(this.attrs.discussion.tags() || []);
    const color = tags.length ? tags[0].color() : null;

    if (!color) return original();

    return classList(original(), 'DiscussionHero--colored', textContrastClass(color));
  });
}

/**
 * The tag a page is filtered to.
 *
 * Three things have to agree for a tag page to work, and upstream wires all
 * three:
 *
 *   1. the route param reaches the search state, so the list request carries
 *      it;
 *   2. the request sends `filter[tag]`, which this port's `GET /api/discussions`
 *      already understands (`DiscussionFilter.TagID`);
 *   3. the page says WHICH tag it is — hero, title, and the colour of the
 *      "start a discussion" button.
 */
function addTagFilter() {
  // 1. The route param becomes a search param.
  extendMethod(GlobalSearchState.prototype, 'params', function (params) {
    params.tags = m.route.param('tags');
  });

  // 2. The search param becomes a filter.
  //
  //    Upstream ALSO pushes `tags,parent` into `include` here. This port
  //    cannot: a tag resource is serialised by the tags PLUGIN, and the
  //    core's `included` array only builds the resources IT knows — users,
  //    posts, groups. The store is filled instead by `loadTags()` (the
  //    plugin's own `GET /api/tags`), which loads EVERY tag once per page
  //    load, so `discussion.tags()` resolves the same way. Asking for an
  //    include the server cannot answer would be a request whose answer
  //    silently lacks what was asked for.
  extendMethod(DiscussionListState.prototype, 'requestParams', function (params) {
    const tags = this.params ? this.params.tags : null;

    if (tags) {
      params.filter = params.filter || {};
      params.filter.tag = tags;
    }
  });

  // 3a. A tag page wears the tag's hero instead of the welcome hero.
  override(IndexPage.prototype, 'hero', function (original) {
    const tag = currentTag();

    return tag ? <TagHero model={tag} /> : original();
  });

  // 3b. The document title is the tag's name.
  extendMethod(IndexPage.prototype, 'setTitle', function () {
    const tag = currentTag();

    if (tag) app.setTitle(tag.name());
  });

  // 3c. The index's title control names the tag when none of its nav items is
  //     active — the case for a secondary tag outside the first few.
  extendMethod(IndexPage.prototype, 'view', function () {
    const tag = currentTag();

    if (tag) app.current.set('titleControlLabel', tag.name());
  });

  // 3d. The "start a discussion" button takes the tag's colour, and is
  //     disabled when the caller may not start a discussion there.
  extendMethod(IndexSidebar.prototype, 'items', function (items) {
    const tag = currentTag();

    if (!tag) return;

    const color = tag.color();
    const canStart = tag.canStartDiscussion() || !app.session.user;
    const button = items.get('newDiscussion');

    if (!button) return;

    if (color) {
      button.attrs.className = classList([button.attrs.className, 'Button--tagColored', textContrastClass(color)]);
      button.attrs.style = { '--color': color };
    }

    button.attrs.disabled = !canStart;
    button.children = canStart ? 'Start a Discussion' : 'You cannot start a discussion here';
  });
}

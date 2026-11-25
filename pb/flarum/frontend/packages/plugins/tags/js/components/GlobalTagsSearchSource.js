// The Tags section of the header's search dropdown.
//
// The server half of this is upstream's `TagSearcher`: `GET /api/tags` honours
// `filter[q]` as a PREFIX match on a tag's name or slug. Upstream ships the
// searcher without a client-side source (its core dropdown lists discussions,
// posts and users), so the dropdown section is added HERE — otherwise tags are
// only findable from the `/tags` page, and a tag with no discussions yet is
// findable by nothing at all. See `docs/design/flarum/deviations.md` §16b.
//
// The source is shaped like the core's `GlobalUsersSearchSource`, which is the
// other "ask the API and also match what the store already holds" source: the
// store usually has the whole taxonomy from `loadTags()`, so the local prefix
// match makes typing feel instant while the request is in flight.
import app from 'flarum/forum/app';
import LinkButton from 'flarum/common/components/LinkButton';

import { sortTags, tagIcon, tagPath } from '../helpers';

export default class GlobalTagsSearchSource {
  results = new Map();

  resource = 'tags';

  title() {
    // Untranslated: this port ships no locale bundle for the extension, and a
    // missing key renders literally rather than falling back.
    return 'Tags';
  }

  isCached(query) {
    return this.results.has(query.toLowerCase());
  }

  async search(query, limit) {
    query = query.toLowerCase();

    this.results.set(query, []);

    return app.store.find('tags', { filter: { q: query }, page: { limit } }).then((results) => {
      // The tag endpoint is not paginated — `loadTags()` relies on it
      // returning the whole taxonomy — so `page[limit]` is only a hint there
      // and the slice happens here. Without it a one-letter query lists every
      // tag in the modal.
      this.results.set(query, limit ? results.slice(0, limit) : results);

      m.redraw();
    });
  }

  view(query) {
    query = query.toLowerCase();

    const found = this.results.get(query) || [];

    // A tag the store already holds whose name or slug starts with the query
    // is shown even before its request answers — and it is de-duplicated by
    // id, because the payload merges into the same store.
    const seen = new Set(found.map((tag) => tag.id()));

    const local = sortTags(app.store.all('tags') || []).filter(
      (tag) => !seen.has(tag.id()) && [tag.name(), tag.slug()].some((value) => (value || '').toLowerCase().startsWith(query))
    );

    const tags = [...found, ...local];

    if (!tags.length) return [];

    return tags.map((tag) => {
      const count = tag.discussionCount();

      return (
        <li className="TagSearchResult" data-index={'tags' + tag.id()} data-id={tag.id()}>
          <a className="TagSearchResult-link" href={tagPath(tag)}>
            {tagIcon(tag, { className: 'TagSearchResult-icon' })}
            <span className="TagSearchResult-name">{tag.name()}</span>
            {typeof count === 'number' ? <span className="TagSearchResult-count">{count}</span> : null}
          </a>
        </li>
      );
    });
  }

  customGrouping() {
    return false;
  }

  fullPage() {
    return (
      <li>
        <LinkButton icon="fas fa-th-large" href={app.route('tags')}>
          All tags
        </LinkButton>
      </li>
    );
  }

  gotoItem(id) {
    const tag = app.store.getById('tags', id);

    if (!tag) return null;

    return app.route.tag(tag);
  }
}

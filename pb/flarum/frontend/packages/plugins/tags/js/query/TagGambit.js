// The `tag:` gambit.
//
// Upstream's `flarum/tags` `js/src/common/query/discussions/TagGambit.ts`:
// typing `tag:slug` in the search box narrows the discussion list to one tag,
// `tag:a,b` to either, and `-tag:slug` excludes one. The values reach the API
// as `filter[tag]`, which is the same filter the tag page's route param
// produces — so there is one server-side implementation of "filter by tag",
// not two.
//
// The KEYWORD is untranslated on purpose. Upstream translates it
// (`flarum-tags.lib.gambits.discussions.tag.key`), but this port ships no
// locale bundle for the extension, so a translated key would render literally
// in the pattern. `key()` and `canonicalKey()` therefore agree, which is what
// makes English always work.
//
// `predicates = true` is the load-bearing flag: it tells `GambitManager.apply`
// to CONCATENATE the values of repeated gambits instead of overwriting, so
// `tag:a tag:b` sends `{tag: ['a', 'b']}` — two AND-ed OR-groups — rather than
// just the last one.
import app from 'flarum/forum/app';
import { KeyValueGambit } from 'flarum/common/query/IGambit';

import { loadTags } from '../helpers';

/** How long to wait before re-trying a failed tag load. */
const RETRY_COOLDOWN_MS = 10000;

/** Whether a tag-list request is in flight. */
let loadingTags = false;

/** Whether the tag list has been loaded for the autocomplete. */
let loadedTags = false;

/** When the last tag-list request failed, or 0. */
let lastLoadFailedAt = 0;

export default class TagGambit extends KeyValueGambit {
  predicates = true;

  key() {
    return 'tag';
  }

  hint() {
    return 'Filter by tag slug';
  }

  filterKey() {
    return 'tag';
  }

  /**
   * The slugs the autocomplete offers.
   *
   * It loads the tag list once, with a cooldown after a failure so a broken
   * request is not re-sent on every keystroke.
   */
  values() {
    const recentlyFailed = lastLoadFailedAt && Date.now() - lastLoadFailedAt < RETRY_COOLDOWN_MS;

    if (!loadedTags && !loadingTags && !recentlyFailed) {
      loadingTags = true;

      loadTags()
        .then(() => {
          loadedTags = true;
          m.redraw();
        })
        .catch(() => {
          lastLoadFailedAt = Date.now();
        })
        .finally(() => {
          loadingTags = false;
        });
    }

    return (app.store.all('tags') || [])
      .map((tag) => ({ value: tag.slug(), label: tag.name() }))
      .filter((option) => !!option.value);
  }

  /**
   * The filter value for one `tag:` occurrence.
   *
   * It is an ARRAY of one so that `GambitManager.apply`'s concatenation
   * accumulates the occurrences into the OR-group list the server reads.
   */
  gambitValueToFilterValue(value) {
    return [value];
  }

  /**
   * The search text for a filter value.
   *
   * An array is several gambits, not one comma-separated value: upstream
   * joins them with a SPACE, which is what makes them AND-ed when the text is
   * parsed again. A comma-separated value stays one OR-group.
   */
  fromFilter(value, negate) {
    if (Array.isArray(value)) {
      return value.map((entry) => this.fromFilter(String(entry), negate)).join(' ');
    }

    return `${negate ? '-' : ''}${this.key()}:${value}`;
  }
}

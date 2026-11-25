// The tags extension's composer integration.
//
// Upstream's `addTagComposer` does four things, and this mirrors them:
//
//   1. the composer offers a tag picker, and the chosen tags are shown;
//   2. the chosen tags travel with the discussion when it is created;
//   3. a tag page's tag is PRESELECTED, so a discussion started from a tag
//      page carries that tag;
//   4. the minimums are enforced before submitting — the modal opens instead
//      of a request the server would refuse.
//
// The chosen tags live outside `composer.fields`, which is upstream's
// arrangement but not this port's: `composer.fields` is typed
// `Record<string, Stream<any>>`, so a plain array in that map would be read
// as a Stream and every read would be undefined.
import app from 'flarum/forum/app';
import { extend as extendMethod, override } from 'flarum/common/extend';
import IndexSidebar from 'flarum/forum/components/IndexSidebar';
import classList from 'flarum/common/utils/classList';
import Button from 'flarum/common/components/Button';

import { currentTag, loadTags, tagsLabel } from './helpers';
import TagSelectionModal, { isPrimaryTag, tagLimits } from './components/TagSelectionModal';

/**
 * The tags chosen for the discussion being composed.
 *
 * Module-level because the composer's `fields` map holds Streams, and a tag
 * is not a stream. It is cleared when the composer closes, so a later
 * discussion does not inherit the previous one's tags.
 */
let chosenTags = [];

/**
 * The tags currently chosen.
 *
 * @returns {object[]}
 */
export function composerTags() {
  return chosenTags;
}

/**
 * Set the chosen tags.
 *
 * @param {object[]} tags
 */
export function setComposerTags(tags) {
  chosenTags = tags;
}

/**
 * The tags the author may choose from.
 *
 * Upstream's `getSelectableTags`: a tag the caller may NOT start a
 * discussion with is not offered.
 *
 * @returns {object[]}
 */
export function selectableTags() {
  return (app.store.all('tags') || []).filter((tag) => tag.canStartDiscussion());
}

/**
 * Open the tag picker.
 *
 * @param {object[]} selected
 * @param {Function} onsubmit
 */
export function showTagModal(selected, onsubmit) {
  app.modal.show(TagSelectionModal, {
    selectedTags: selected,
    onsubmit,
  });
}

/**
 * The tag picker button for the composer's header.
 *
 * It is a BUTTON rather than a dropdown: the composer header has room for a
 * label, and the picker is a modal.
 */
function tagsHeaderItem(items) {
  const tags = composerTags();

  // The composer disables its body while minimized, when the whole of it is
  // one button that restores it: it shows the chosen tags then, but not the
  // prompt or the button to choose them.
  if (this.attrs.disabled) {
    if (tags.length) {
      items.add('tags', <span className="DiscussionComposer-changeTags">{tagsLabel(tags)}</span>, 10);
    }

    return;
  }

  const selectable = selectableTags();

  items.add(
    'tags',
    <Button
      className={classList(['DiscussionComposer-changeTags', 'Button Button--ua-reset', !selectable.length && 'disabled'])}
      disabled={!selectable.length}
      onclick={() => this.chooseTags()}
    >
      {tags.length ? (
        tagsLabel(tags)
      ) : (
        <span className="TagLabel untagged">Choose tags</span>
      )}
    </Button>,
    10
  );
}

export function addTagComposer() {
  // The composer offers a picker, and the chosen tags travel with the
  // discussion.
  extendMethod('flarum/forum/components/DiscussionComposer', 'headerItems', tagsHeaderItem);

  extendMethod('flarum/forum/components/DiscussionComposer', 'data', function (data) {
    // The relationship is named the way the WRITE side expects it: the
    // server's writer takes the tag ids (`writeDiscussionTags`), and the
    // store serialises a model array into `{type, id}` identifiers.
    data.relationships = data.relationships || {};
    data.relationships.tags = composerTags();
  });

  // The composer needs a `chooseTags` method for the header button. It is
  // added to the prototype rather than to an instance, because the header
  // item's `this` is the composer.
  extendMethod('flarum/forum/components/DiscussionComposer', 'oninit', function () {
    this.chooseTags = () => {
      const selectable = selectableTags();

      if (!selectable.length) return;

      showTagModal(composerTags().slice(0), (chosen) => {
        setComposerTags(chosen);
        m.redraw();
      });
    };

    // The tag list, before the picker can offer anything.
    loadTags()
      .then(() => m.redraw())
      .catch(() => {});
  });

  // A tag page PRESELECTS its tag: a discussion started from `/t/<slug>`
  // carries that tag, and (upstream) its parent too.
  extendMethod(IndexSidebar.prototype, 'newDiscussionAction', function (promise) {
    const tag = currentTag();

    if (tag) {
      const parent = tag.parent();
      const tags = parent ? [parent, tag] : [tag];

      // A guest is asked to log in instead, which rejects the promise.
      // Whoever started the discussion handles that; this chain must handle
      // it too.
      promise.then(
        (composer) => {
          setComposerTags(tags);
          m.redraw();
        },
        () => {}
      );
    } else {
      setComposerTags([]);
    }
  });

  // The composer clears its fields when it closes, so the chosen tags must
  // be cleared with them: a later discussion would otherwise inherit the
  // previous one's tags.
  extendMethod('flarum/common/components/Composer', 'onhide', function () {
    setComposerTags([]);
  });

  // The minimums are enforced BEFORE submitting: the modal opens instead of
  // a request the server would refuse.
  override('flarum/forum/components/DiscussionComposer', 'onsubmit', function (original) {
    const tags = composerTags();
    const primary = tags.filter(isPrimaryTag).length;
    const secondary = tags.length - primary;
    const { min, max } = tagLimits();
    const selectable = selectableTags();

    const tooFew = primary < min.primary || secondary < min.secondary;
    const tooMany = primary > max.primary || secondary > max.secondary;

    if ((tooFew || tooMany) && selectable.length) {
      showTagModal(tags, (chosen) => {
        setComposerTags(chosen);
        original();
      });

      return;
    }

    original();
  });
}

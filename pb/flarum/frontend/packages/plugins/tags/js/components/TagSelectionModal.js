// The tag selection modal.
//
// Upstream's `flarum/tags` has two modals: `TagSelectionModal` (the generic
// picker, with limits, keyboard navigation and a filter) and
// `TagDiscussionModal` (the discussion-specific wrapper). This is ONE modal
// serving both, because the two differ only in the limits they apply, the
// title they carry, and whether a discussion is being edited (which is what
// decides which tags may be offered — see `utils/selectableTags`).
//
// It is a MODAL rather than an inline dropdown because the composer's header
// has no room for a searchable list, and because the selection has to be
// visible while the author writes.
//
// The LIMITS are the server's, read from the forum document: the server
// enforces the same counts (`validateTagCounts`), so a modal that ignored
// them would let an author compose a discussion the server then refuses.
//
// The LIST is filtered too, for the same reason: a tag the caller may not
// use is not offered (`selectableTags`), because the server really does
// refuse it — a restricted tag's permission is checked, so an unfiltered
// picker produces a selection whose save comes back refused.
import app from 'flarum/forum/app';
import FormModal from 'flarum/common/components/FormModal';
import Button from 'flarum/common/components/Button';
import LoadingIndicator from 'flarum/common/components/LoadingIndicator';
import classList from 'flarum/common/utils/classList';
import Stream from 'flarum/common/utils/Stream';

import { loadTags, sortTags, tagIcon } from '../helpers';
import { selectableTags } from '../utils/selectableTags';

/**
 * The limits on a selection.
 *
 * They are read from the forum document, which carries the four count
 * settings the tags plugin declares.
 *
 * @returns {{min: {primary: number, secondary: number}, max: {primary: number, secondary: number}}}
 */
export function tagLimits() {
  const number = (name, fallback) => {
    const raw = app.forum.attribute(name);

    const parsed = parseInt(raw, 10);

    return Number.isNaN(parsed) ? fallback : parsed;
  };

  return {
    min: {
      primary: number('minPrimaryTags', 0),
      secondary: number('minSecondaryTags', 0),
    },
    max: {
      primary: number('maxPrimaryTags', 0),
      secondary: number('maxSecondaryTags', 0),
    },
  };
}

/**
 * A tag is PRIMARY when it has a position and is not a child of another tag.
 *
 * Upstream's rule, and the server's: a child carries a position of its own,
 * so counting by position alone lets a child satisfy the primary minimum.
 *
 * @param {object} tag
 * @returns {boolean}
 */
export function isPrimaryTag(tag) {
  return tag.position() !== null && !tag.isChild();
}

export default class TagSelectionModal extends FormModal {
  oninit(vnode) {
    super.oninit(vnode);

    this.selected = (this.attrs.selectedTags || []).slice(0);
    this.filter = Stream('');
    this.limits = this.attrs.limits || tagLimits();
  }

  className() {
    return classList('TagSelectionModal', this.attrs.className);
  }

  title() {
    return this.attrs.title || 'Choose Tags';
  }

  content() {
    const tags = app.store.all('tags');

    // The LOADING check is on the RAW list: filtering first would make "no
    // tag is usable by this caller" look identical to "the list has not
    // arrived", and the modal would spin forever instead of saying so.
    if (!tags.length) return <LoadingIndicator />;

    const selectable = selectableTags(tags, this.attrs.discussion);

    const filter = this.filter().trim().toLowerCase();
    const visible = sortTags(selectable).filter((tag) => !filter || (tag.name() || '').toLowerCase().includes(filter));

    return (
      <div className="TagSelectionModal-container">
        <div className="TagSelectionModal-filter">
          <input
            className="FormControl"
            type="search"
            placeholder="Filter tags"
            value={this.filter()}
            oninput={(event) => this.filter(event.target.value)}
          />
        </div>

        {this.selected.length ? <div className="TagSelectionModal-selected">{this.selected.map((tag) => this.chip(tag))}</div> : null}

        {visible.length ? (
          <ul className="TagSelectionModal-list">
            {visible.map((tag) => (
              <li className={classList('TagSelectionModal-item', this.isSelected(tag) && 'selected')}>
                <button type="button" className="TagSelectionModal-button" onclick={() => this.toggle(tag)}>
                  {tagIcon(tag, { className: 'TagSelectionModal-icon' })}
                  <span className="TagSelectionModal-name">{tag.name()}</span>
                  {tag.description() ? <span className="TagSelectionModal-description">{tag.description()}</span> : null}
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <div className="TagSelectionModal-empty">{selectable.length ? 'No tags match the filter.' : 'No tags are available to you here.'}</div>
        )}

        {this.hint()}
      </div>
    );
  }

  /**
   * A selected tag, as a removable chip.
   */
  chip(tag) {
    return (
      <span className="TagSelectionModal-chip">
        {tag.name()}
        <button type="button" className="TagSelectionModal-remove" aria-label={`Remove ${tag.name()}`} onclick={() => this.toggle(tag)}>
          &times;
        </button>
      </span>
    );
  }

  /**
   * What the author still has to choose, or what they have chosen too many of.
   */
  hint() {
    const primary = this.selected.filter(isPrimaryTag).length;
    const secondary = this.selected.length - primary;
    const { min, max } = this.limits;

    if (primary > max.primary) {
      return <div className="TagSelectionModal-hint TagSelectionModal-hint--error">Too many primary tags — the maximum is {max.primary}.</div>;
    }

    if (secondary > max.secondary) {
      return <div className="TagSelectionModal-hint TagSelectionModal-hint--error">Too many secondary tags — the maximum is {max.secondary}.</div>;
    }

    if (primary < min.primary) {
      return <div className="TagSelectionModal-hint">Choose at least {min.primary} primary tag{min.primary === 1 ? '' : 's'}.</div>;
    }

    if (secondary < min.secondary) {
      return <div className="TagSelectionModal-hint">Choose at least {min.secondary} secondary tag{min.secondary === 1 ? '' : 's'}.</div>;
    }

    return null;
  }

  isSelected(tag) {
    return this.selected.some((selected) => selected.id() === tag.id());
  }

  toggle(tag) {
    if (this.isSelected(tag)) {
      this.selected = this.selected.filter((selected) => selected.id() !== tag.id());
    } else {
      this.selected = [...this.selected, tag];
    }

    if (this.attrs.onSelect) {
      this.attrs.onSelect(tag, this.selected);
    }

    m.redraw();
  }

  onsubmit(e) {
    e.preventDefault();

    if (this.attrs.onsubmit) {
      this.attrs.onsubmit(this.selected);
    }

    this.hide();
  }
}

// The hero of a tag's page.
//
// Upstream's `js/src/forum/components/TagHero.js`: the tag's name, its
// description, and its colour as the hero background.
//
// The colour is returned from `style()` rather than written onto the vnode's
// attrs, because that is what THIS port's `Hero` reads (`Hero.view()` renders
// `style={this.style()}`). Upstream's `addTagFilter` sets `vnode.attrs.style`,
// which works against upstream's Hero and would be silently dropped here.
import Hero from 'flarum/forum/components/Hero';
import classList from 'flarum/common/utils/classList';
import textContrastClass from 'flarum/common/helpers/textContrastClass';

import { tagIcon } from '../helpers';

export default class TagHero extends Hero {
  className() {
    const tag = this.attrs.model;
    const color = tag.color();

    return classList('TagHero', color ? classList('TagHero--colored', textContrastClass(color)) : '');
  }

  style() {
    const color = this.attrs.model.color();

    return color ? { '--hero-bg': color } : undefined;
  }

  bodyItems() {
    const tag = this.attrs.model;

    return [
      <h1 className="TagHero-title">
        {tagIcon(tag, { className: 'TagHero-icon' })}
        {tag.name()}
      </h1>,
      tag.description() ? <div className="TagHero-description">{tag.description()}</div> : null,
      // The count is upstream's: how many discussions the tag holds, which is
      // what makes a tag page feel populated before the list scrolls in.
      //
      // A PLAIN STRING rather than `app.translator.trans(...)`: this port
      // vendors no extension locale file, and core's catalogue has no
      // discussion-count key — a translation call would render the raw key.
      <div className="TagHero-meta">
        {tag.discussionCount()} {tag.discussionCount() === 1 ? 'discussion' : 'discussions'}
      </div>,
    ];
  }
}

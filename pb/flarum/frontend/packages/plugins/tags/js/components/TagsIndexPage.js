// The `/tags` page: every tag the caller may see, as tiles.
//
// Upstream's `js/src/forum/components/TagsPage.js` groups the tags into
// primary tags and their children, renders a tile per primary tag with its
// children beneath, and links each one to its own page. It exists so a reader
// can browse the taxonomy without knowing a slug, and it is the one page the
// extension adds beyond the tag pages themselves.
//
// The tiles are built from the STORE, which `loadTags()` fills — the same
// request the labels use, so arriving here costs nothing extra.
import app from 'flarum/forum/app';
import Page from 'flarum/common/components/Page';
import PageStructure from 'flarum/forum/components/PageStructure';
import LoadingIndicator from 'flarum/common/components/LoadingIndicator';

import { loadTags, sortTags, tagIcon, tagPath } from '../helpers';

export default class TagsIndexPage extends Page {
  oninit(vnode) {
    super.oninit(vnode);

    this.bodyClass = 'App--tags';
    this.loading = true;

    loadTags()
      .catch(() => {
        // A failed load is not fatal: the page renders its empty state, and
        // the reader can retry by navigating back.
      })
      .then(() => {
        this.loading = false;
        m.redraw();
      });
  }

  view() {
    return (
      <PageStructure className="TagsPage" hero={() => this.hero()} sidebar={() => this.sidebar()} loading={this.loading}>
        {this.loading ? <LoadingIndicator /> : this.content()}
      </PageStructure>
    );
  }

  hero() {
    return (
      <header className="Hero TagsPage-hero">
        <div className="container">
          <h1 className="TagsPage-title">Tags</h1>
        </div>
      </header>
    );
  }

  sidebar() {
    return null;
  }

  content() {
    const tags = app.store.all('tags');
    const primaries = sortTags(tags.filter((tag) => !tag.parent()));

    if (primaries.length === 0) {
      return <div className="TagsPage-empty">No tags yet.</div>;
    }

    return (
      <div className="TagsPage-content">
        {primaries.map((primary) => (
          <section className="TagsPage-group">
            {this.tile(primary, 'TagTile TagTile--primary')}
            {this.children(primary)}
          </section>
        ))}
      </div>
    );
  }

  children(primary) {
    const children = sortTags(app.store.all('tags').filter((tag) => tag.parent() === primary));

    if (children.length === 0) return null;

    return <div className="TagsPage-children">{children.map((child) => this.tile(child, 'TagTile TagTile--child'))}</div>;
  }

  tile(tag, className) {
    const color = tag.color();

    return (
      <a
        className={className}
        href={tagPath(tag)}
        // `oncreate` rather than the Link component: a tile is a whole block,
        // and a real <a> keeps middle-click and copy-link working.
        oncreate={(vnode) => {
          vnode.dom.addEventListener('click', (event) => {
            if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;

            event.preventDefault();
            m.route.set(tagPath(tag));
          });
        }}
        style={color ? { '--tag-color': color } : undefined}
      >
        <span className="TagTile-icon">{tagIcon(tag, { useColor: true })}</span>
        <span className="TagTile-name">{tag.name()}</span>
        <span className="TagTile-count">{tag.discussionCount()}</span>
        {tag.description() ? <span className="TagTile-description">{tag.description()}</span> : null}
      </a>
    );
  }
}

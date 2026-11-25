// The Tags extension's admin page.
//
// It mirrors upstream `flarum/tags`'s `js/src/admin/components/TagsPage.js`:
// the tag TREE (primary and secondary, with create buttons and drag-to-
// reorder) on one tab, and the COUNT SETTINGS on another.
//
// The page is registered with `Extend.Admin().page(TagsPage)`, and the
// admin application renders a custom page INSTEAD of the plain
// settings list — so this file is where the tree lives.
//
// The settings half is upstream's too: four number fields, the two
// pairs being a minimum and a maximum.
import app from 'flarum/admin/app';
import ExtensionPage from 'flarum/admin/components/ExtensionPage';
import Button from 'flarum/common/components/Button';
import LoadingIndicator from 'flarum/common/components/LoadingIndicator';
import Form from 'flarum/common/components/Form';
import FormSection from 'flarum/admin/components/FormSection';
import FormSectionGroup from 'flarum/admin/components/FormSectionGroup';
import ItemList from 'flarum/common/utils/ItemList';
import EditTagModal from './EditTagModal';
import { sortTags, tagIcon } from '../helpers';

/**
 * One tag in the tree.
 */
function tagItem(tag) {
  return (
    <li data-id={tag.id()} style={{ color: tag.color() }}>
      <div className="TagListItem-info">
        {tagIcon(tag)}
        <span className="TagListItem-name">{tag.name()}</span>
        <Button
          className="Button Button--link"
          icon="fas fa-pencil-alt"
          aria-label={'Edit tag ' + tag.name()}
          onclick={() => app.modal.show(EditTagModal, { model: tag })}
        />
      </div>
      {!tag.isChild() && tag.position() !== null && (
        <ol className="TagListItem-children TagList">
          {sortTags(tag.children() || []).map(tagItem)}
        </ol>
      )}
    </li>
  );
}

export default class TagsPage extends ExtensionPage {
  /**
   * The tag tree is loaded on demand: the admin payload does not carry
   * the tags, and the page is only opened by an administrator who
   * asked for it.
   */
  loading = true;

  /**
   * A regular redraw will not work for the tree, because sortable
   * reorders the DOM and that confuses Mithril's diffing. The key is
   * forced instead, so the list is re-rendered from scratch.
   */
  forcedRefreshKey = 0;

  oninit(vnode) {
    super.oninit(vnode);

    // The sortable library is ~120KB, so it is loaded only when this
    // page is opened.
    Promise.all([this.loadTags(), import('flarum/admin/utils/loadSortable')]).then(([, sortableModule]) => {
      this.sortable = sortableModule.default;
      this.loading = false;

      m.redraw();
    });
  }

  /**
   * Load the tag list into the store.
   */
  loadTags() {
    return app.request({ url: app.forum.attribute('apiUrl') + '/tags' }).then((payload) => app.store.pushPayload(payload));
  }

  /**
   * The page's tabs: the tag tree first, then the settings, then the
   * core's permissions tab.
   */
  pageTabItems() {
    const items = super.pageTabItems();
    const unsaved = this.isChanged() > 0;

    items.add('tags', { id: 'tags', label: 'Tags', icon: 'fas fa-tags' }, 100);
    items.add(
      'settings',
      {
        id: 'settings',
        label: 'Settings',
        icon: 'fas fa-sliders-h',
        indicator: unsaved,
        indicatorLabel: unsaved ? 'Unsaved changes' : undefined,
      },
      50
    );

    return items;
  }

  pageTabContent(tab, vnode) {
    if (tab === 'tags') return this.content(vnode);
    if (tab === 'settings') return this.settingsContent();

    return super.pageTabContent(tab, vnode);
  }

  /**
   * The tag tree: the primary tags and the secondary tags, each with a
   * create button.
   */
  content() {
    if (this.loading) {
      return <LoadingIndicator />;
    }

    const tags = sortTags(app.store.all('tags').filter((tag) => !tag.parent()));

    return (
      <div className="TagsContent">
        <div className="TagsContent-list" key={this.forcedRefreshKey} oncreate={this.onListOnCreate.bind(this)}>
          <FormSectionGroup>
            <FormSection className="TagGroup" label="Primary tags">
              <ol className="TagList TagList--primary">
                {tags
                  .filter((tag) => tag.position() !== null && !tag.isChild())
                  .map(tagItem)}
              </ol>
              <Button className="Button TagList-button" icon="fas fa-plus" onclick={() => app.modal.show(EditTagModal, { primary: true })}>
                Create primary tag
              </Button>
            </FormSection>

            <FormSection className="TagGroup TagGroup--secondary" label="Secondary tags">
              <ul className="TagList TagList--secondary">
                {tags
                  .filter((tag) => tag.position() === null)
                  .sort((a, b) => a.name().localeCompare(b.name()))
                  .map(tagItem)}
              </ul>
              <Button className="Button TagList-button" icon="fas fa-plus" onclick={() => app.modal.show(EditTagModal, { primary: false })}>
                Create secondary tag
              </Button>
            </FormSection>
          </FormSectionGroup>
        </div>
      </div>
    );
  }

  /**
   * The settings tab: the four count settings, saved with the page's
   * own settings streams.
   */
  settingsContent() {
    return (
      <div className="ExtensionPage-settings TagSettings">
        <Form className="AdminPage-settingsForm AdminPage-settingsForm--sectioned">
          {this.settingsItems().toArray()}
          <div className="Form-group Form-controls">
            {this.submitButton()}
            {this.resetButton()}
          </div>
        </Form>
      </div>
    );
  }

  /**
   * The settings on the Settings tab.
   */
  settingsItems() {
    const items = new ItemList();

    const primaryLabel = 'Primary tags';
    const secondaryLabel = 'Secondary tags';

    const minPrimaryTags = this.setting('flarum-tags.min_primary_tags', 0, primaryLabel);
    const maxPrimaryTags = this.setting('flarum-tags.max_primary_tags', 0, primaryLabel);

    const minSecondaryTags = this.setting('flarum-tags.min_secondary_tags', 0, secondaryLabel);
    const maxSecondaryTags = this.setting('flarum-tags.max_secondary_tags', 0, secondaryLabel);

    items.add(
      'requirements',
      <div className="Form-group">
        <label>{primaryLabel}</label>
        <div className="helpText">How many primary tags a discussion must and may carry.</div>
        <div className="TagSettings-rangeInput">
          <input
            className="FormControl"
            type="number"
            min="0"
            value={minPrimaryTags()}
            oninput={this.setMinTags.bind(this, minPrimaryTags, maxPrimaryTags)}
          />
          <span className="TagSettings-rangeSeparator">to</span>
          <input className="FormControl" type="number" min={minPrimaryTags()} bidi={maxPrimaryTags} />
        </div>
      </div>,
      100
    );

    // Upstream's `addTagChangePermission` puts this on the PERMISSION GRID,
    // as a `<SettingDropdown key="allow_tag_change">`. This port has no tag
    // section on the grid, so the control lives on the extension's own page —
    // but the KEY is upstream's flat one, not a `flarum-tags.`-prefixed key,
    // because the server reads the flat name (`api.go`'s
    // `allowTagChangeSetting`) and the admin payload publishes it there too.
    //
    // The options and their values are upstream's. The default is "0"
    // (never), which is also the server's fallback: an unset setting permits
    // nothing, and a setting that silently permitted everything would be a
    // hole.
    items.add(
      'tag-change',
      <div className="Form-group">
        <label>Allow tag change</label>
        <div className="helpText">When an author may still change a discussion’s tags after posting it.</div>
        <select className="FormControl TagSettings-select" bidi={this.setting('allow_tag_change', '0')}>
          <option value="-1">Allow indefinitely</option>
          <option value="10">Allow for 10 minutes</option>
          <option value="reply">Allow until the first reply</option>
          <option value="0">Never</option>
        </select>
      </div>,
      90
    );

    items.add(
      'requirements-secondary',
      <div className="Form-group">
        <label>{secondaryLabel}</label>
        <div className="helpText">How many secondary tags a discussion must and may carry.</div>
        <div className="TagSettings-rangeInput">
          <input
            className="FormControl"
            type="number"
            min="0"
            value={minSecondaryTags()}
            oninput={this.setMinTags.bind(this, minSecondaryTags, maxSecondaryTags)}
          />
          <span className="TagSettings-rangeSeparator">to</span>
          <input className="FormControl" type="number" min={minSecondaryTags()} bidi={maxSecondaryTags} />
        </div>
      </div>,
      100
    );

    return items;
  }

  /**
   * Raise the maximum when the minimum passes it.
   *
   * The rule is a RANGE, so a minimum above the maximum would be a set
   * no discussion could satisfy — and the server would refuse every
   * tag set.
   */
  setMinTags(minTags, maxTags, e) {
    const value = parseInt(e.target.value, 10);

    if (isNaN(value)) return;

    minTags(value);
    maxTags(Math.max(value, maxTags()));
  }

  /**
   * Make the tree sortable once it is in the DOM.
   */
  onListOnCreate(vnode) {
    if (!this.sortable) return;

    this.$('.TagList')
      .get()
      .map((e) => {
        this.sortable.create(e, {
          group: 'tags',
          // Secondary tags have no stored order: they are always listed
          // by name. They can still be dragged to and from the other
          // lists.
          sort: !e.classList.contains('TagList--secondary'),
          delay: 50,
          delayOnTouchOnly: true,
          touchStartThreshold: 5,
          animation: 150,
          swapThreshold: 0.65,
          dragClass: 'TagListItem--dragging',
          ghostClass: 'TagListItem--ghost',
          onSort: this.onSortUpdate.bind(this),
        });
      });
  }

  /**
   * Persist a new order.
   */
  onSortUpdate(e) {
    const order = [];

    this.$('.TagList--primary > li')
      .map((element) => {
        const id = element.getAttribute('data-id');

        order.push({
          id: id,
          children: Array.from(element.querySelectorAll('.TagListItem-children > li'))
            .map((child) => child.getAttribute('data-id'))
            .get(),
        });
      })
      .get();

    app.request({
      url: app.forum.attribute('apiUrl') + '/tags/order',
      method: 'POST',
      body: { order: order },
    });

    this.forcedRefreshKey++;
    m.redraw();
  }
}

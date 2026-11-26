// The Tags extension's edit/create-tag modal.
//
// It mirrors upstream `flarum/tags`'s
// `js/src/admin/components/EditTagModal.tsx`: the same fields, in the
// same order, with the same validation rules.
//
// ONE FIELD IS NOT HERE, because it is absent on the server too: the icon
// IMAGE (upstream's `IconImageInput`, which uploads a picture). The port's tag
// resource carries no icon-image attributes, so the input would upload to an
// endpoint that does not exist.
//
// **THE PARENT FIELD IS HERE, AND UPSTREAM'S IS NOT.** Upstream's modal has no
// parent field at all: a secondary tag is created UNPLACED and then dragged
// under a primary tag on the Tags page. That leaves the one thing an
// administrator wants when they create a secondary tag — to say WHICH primary
// tag it belongs to — to a drag, and the created tag's `parent` is null until
// they make it. This port's tree is the same two levels, so the field offers
// exactly what the server accepts: a ROOT tag (`parentOptions` in
// `tagParent.js`), or none at all.
//
// The rest — name, slug, description, colour, icon, hidden, default sort — is
// upstream's own, and the server accepts all of them.
import app from 'flarum/admin/app';
import FormModal from 'flarum/common/components/FormModal';
import Button from 'flarum/common/components/Button';
import ColorPreviewInput from 'flarum/common/components/ColorPreviewInput';
import IconInput from 'flarum/common/components/IconInput';
import Select from 'flarum/common/components/Select';
import ItemList from 'flarum/common/utils/ItemList';
import Stream from 'flarum/common/utils/Stream';
import Form from 'flarum/common/components/Form';

import { parentOptions, parentValue, showParentField } from './tagParent';

/**
 * The `EditTagModal` shows a modal which creates or edits a tag.
 */
export default class EditTagModal extends FormModal {
  /**
   * The tag being edited, or a fresh one.
   */
  tag;

  name;
  slug;
  description;
  color;
  icon;
  isHidden;
  isPrimary;
  parentId;
  defaultSort;

  /**
   * Whether typing a name should still suggest the slug: only for a
   * new tag whose slug field has not been edited. An existing tag's
   * slug is in links, so it changes only when the admin edits it.
   */
  suggestSlug;

  oninit(vnode) {
    super.oninit(vnode);

    this.tag = this.attrs.model || app.store.createRecord('tags');

    this.name = Stream(this.tag.name() || '');
    this.slug = Stream(this.tag.storedSlug() || '');
    this.description = Stream(this.tag.description() || '');
    this.color = Stream(this.tag.color() || '');
    this.icon = Stream(this.tag.attribute('icon') || '');
    this.isHidden = Stream(this.tag.isHidden() || false);
    this.isPrimary = Stream(this.attrs.primary || false);
    // The parent is the PRIMARY TAG the tag sits under, empty for none. A new
    // tag starts with none: choosing one is how a secondary tag is created
    // already in its place.
    this.parentId = Stream(this.attrs.parentId || parentValue(this.tag));
    this.defaultSort = Stream(this.tag.defaultSort() || '');

    this.suggestSlug = !this.tag.exists;
  }

  className() {
    return 'EditTagModal Modal--small';
  }

  title() {
    return this.name() ? this.name() : 'Create tag';
  }

  content() {
    return (
      <div className="Modal-body">
        <Form>{this.fields().toArray()}</Form>
      </div>
    );
  }

  fields() {
    const items = new ItemList();

    items.add(
      'name',
      <div className="Form-group">
        <label>Name</label>
        <input
          className="FormControl"
          placeholder="Tag name"
          value={this.name()}
          oninput={(e) => {
            const target = e.target;
            this.name(target.value);
            if (this.suggestSlug) this.slug(slugify(target.value));
          }}
        />
      </div>,
      50
    );

    items.add(
      'slug',
      <div className="Form-group">
        <label>Slug</label>
        <input
          className="FormControl"
          value={this.slug()}
          oninput={(e) => {
            const target = e.target;
            this.slug(target.value);
            // Clearing the field hands it back to the suggestion.
            this.suggestSlug = !this.tag.exists && target.value === '';
          }}
        />
      </div>,
      40
    );

    if (this.canHaveParent()) {
      items.add(
        'parent',
        <div className="Form-group">
          <label>Parent tag</label>
          <div className="helpText">
            The primary tag this one sits under. Leave it empty to keep the tag at the root — unplaced, in the
            &quot;Secondary tags&quot; list — until you drag it onto a primary tag.
          </div>
          <Select value={this.parentId()} options={parentOptions(app.store.all('tags'), this.tag.id())} onchange={this.parentId} />
        </div>,
        35
      );
    }

    items.add(
      'description',
      <div className="Form-group">
        <label>Description</label>
        <textarea className="FormControl" bidi={this.description} />
      </div>,
      30
    );

    items.add(
      'color',
      <div className="Form-group">
        <label>Color</label>
        <ColorPreviewInput className="FormControl" placeholder="#aaaaaa" bidi={this.color} />
      </div>,
      20
    );

    items.add(
      'icon',
      <div className="Form-group">
        <label>Icon</label>
        <div className="helpText">The Font Awesome classes of the tag&apos;s icon, e.g. fas fa-bolt.</div>
        <IconInput placeholder="fas fa-bolt" bidi={this.icon} />
      </div>,
      10
    );

    items.add(
      'hidden',
      <div className="Form-group">
        <div>
          <label className="checkbox">
            <input type="checkbox" bidi={this.isHidden} />
            Hide this tag from the All Discussions list
          </label>
        </div>
      </div>,
      10
    );

    items.add('defaultSort', this.defaultSortField(), 5);

    items.add(
      'submit',
      <div className="Form-group Form-controls">
        <Button type="submit" className="Button Button--primary EditTagModal-save" loading={this.loading}>
          Save tag
        </Button>

        {this.tag.exists && (
          <button type="button" className="Button EditTagModal-delete" onclick={this.delete.bind(this)}>
            Delete tag
          </button>
        )}
      </div>,
      -10
    );

    return items;
  }

  /**
   * The sorts a discussion list can be ordered by.
   *
   * The list is the FORUM's own: the sort keys the index page uses,
   * mapped to the labels the core already carries. Reading them from
   * the forum document rather than listing them here means a sort the
   * forum adds is offered without this file changing.
   */
  sortOptions() {
    const sorts = app.data.sortMaps ? app.data.sortMaps.discussions : null;

    const options = {
      '': 'No preference',
    };

    if (sorts) {
      Object.keys(sorts).forEach((key) => {
        options[key] = key;
      });
    }

    // A sort saved earlier that the forum no longer offers is kept in
    // the list, so that it is not silently replaced by whatever sits at
    // the top of the dropdown when the tag is next edited.
    const saved = this.defaultSort();

    if (saved && !(saved in options)) {
      options[saved] = saved;
    }

    return options;
  }

  defaultSortField() {
    return (
      <div className="Form-group">
        <label>Default sort</label>
        <div className="helpText">The sort a discussion list in this tag opens with.</div>
        <Select value={this.defaultSort() ?? ''} options={this.sortOptions()} onchange={this.defaultSort} />
      </div>
    );
  }

  /**
   * Whether this tag may carry a parent.
   *
   * A NEW primary tag may not (a primary tag is a root), and neither may an
   * existing primary root: it changes kind by being dragged onto another
   * primary tag's list, which is the page's own statement about where it goes.
   * Everything else — a new secondary tag, an existing child, an existing
   * unplaced tag — may.
   */
  canHaveParent() {
    // The rule is `tagParent.js`'s, so it has a unit test: hidden only when
    // the modal is CREATING a primary tag, shown for every existing tag.
    return showParentField(this.tag.exists, this.isPrimary());
  }

  submitData() {
    return {
      name: this.name(),
      slug: this.slug(),
      description: this.description(),
      color: this.color(),
      icon: this.icon(),
      isHidden: this.isHidden(),
      // Only a new tag is made primary or secondary here: an existing
      // one changes kind by being dragged between the lists on the
      // Tags page.
      ...(this.tag.exists ? {} : { isPrimary: this.isPrimary() }),
      // The parent travels as an ATTRIBUTE: `Model.save` drops a null
      // relationship, and "no parent" is a value this form can produce.
      // Naming a parent also makes the tag a child — the server keeps
      // `isPrimary`, `position` and `parent` in agreement.
      ...(this.canHaveParent() ? { parent: this.parentId() } : {}),
      // An empty selection means "no preference".
      defaultSort: this.defaultSort() || null,
    };
  }

  onsubmit(e) {
    e.preventDefault();

    this.loading = true;

    this.tag
      .save(this.submitData())
      .then((tag) => {
        // A new tag's save resolves to another instance. Keep it, so
        // that saving again does not create a second tag.
        this.tag = tag;

        // The tag now exists at its slug: if the modal stays open,
        // renaming it there must not move it.
        this.suggestSlug = false;

        this.hide();
      })
      .catch(() => {
        this.loading = false;
        m.redraw();
      });
  }

  delete() {
    if (confirm('Delete this tag?')) {
      this.tag.delete().then(() => {
        m.redraw();
      });

      this.hide();
    }
  }
}

/**
 * Slugify a tag name.
 *
 * It is upstream's `slug(value, SluggingMode.UTF8_FOLDED)` reduced to
 * the ASCII form the port's slug driver produces: lower case, and
 * anything that is not a letter or a digit becomes a dash.
 */
function slugify(value) {
  return value
    .toString()
    .toLowerCase()
    .trim()
    .replace(/\s+/g, '-')
    .replace(/[^\p{L}\p{N}-]+/gu, '')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '');
}

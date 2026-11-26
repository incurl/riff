// The Tags extension's models.
//
// They mirror upstream `flarum/tags`'s `Tag` model: the attribute names
// the server sends are the names the frontend reads.
//
// The model is registered with the STORE (`new Extend.Store().add(
// 'tags', Tag)`), which is what upstream does — and what lets the
// discussion's `tags` relationship resolve into real models instead
// of raw identifiers.
import Model from 'flarum/common/Model';

/**
 * A tag.
 */
export class Tag extends Model {
  name() {
    return Model.attribute('name').call(this);
  }

  slug() {
    return Model.attribute('slug').call(this);
  }

  storedSlug() {
    return Model.attribute('storedSlug').call(this);
  }

  description() {
    return Model.attribute('description').call(this);
  }

  color() {
    return Model.attribute('color').call(this);
  }

  icon() {
    return Model.attribute('icon').call(this);
  }

  position() {
    return Model.attribute('position').call(this);
  }

  parent() {
    return Model.hasOne('parent').call(this);
  }

  // NOT `children()`. The API carries a tag's `parent` relationship and
  // nothing that points back, so a `hasMany('children')` accessor read a
  // relationship that is never sent: it returned false, and the admin tree
  // (which is the only reader) rendered a nested tag nowhere at all. The
  // tree filters the STORE on `parent()` instead, which is upstream's
  // approach.

  defaultSort() {
    return Model.attribute('defaultSort').call(this);
  }

  isChild() {
    return Model.attribute('isChild').call(this);
  }

  isHidden() {
    return Model.attribute('isHidden').call(this);
  }

  isPrimary() {
    return Model.attribute('isPrimary').call(this);
  }

  isRestricted() {
    return Model.attribute('isRestricted').call(this);
  }

  discussionCount() {
    return Model.attribute('discussionCount').call(this);
  }

  lastPostedAt() {
    return Model.attribute('lastPostedAt', Model.transformDate).call(this);
  }

  lastPostedDiscussion() {
    return Model.hasOne('lastPostedDiscussion').call(this);
  }

  canStartDiscussion() {
    return Model.attribute('canStartDiscussion').call(this);
  }

  canAddToDiscussion() {
    return Model.attribute('canAddToDiscussion').call(this);
  }
}

// The Tags extension's admin initialiser.
//
// Upstream's `flarum/tags` uses a CUSTOM PAGE — the tag TREE (primary
// and secondary, with create buttons and drag-to-reorder) on one tab,
// and the count settings on another. It is registered with
// `Extend.Admin().page(TagsPage)`, and the admin application renders a
// custom page INSTEAD of the plain settings list.
//
// The store needs to know the `tags` type here too: the page pushes the
// tag list into the store, and `pushObject` throws "Pushing object of
// type `tags` not allowed" for a type that is not registered as a
// model.
//
// The label and the help text are plain strings rather than
// `app.translator.trans(...)` calls: this port vendors no upstream
// extension locale files, so a translation key would render as the
// key itself.
import Extend from 'flarum/common/extenders';
import { Tag } from './models';
import TagsPage from './admin/TagsPage';
import addTagsPermissionScope from './admin/addTagsPermissionScope';

export const extend = [
  // The permission scope: a RESTRICTED tag gets its own column in the
  // admin permission grid, so a permission can be granted for that tag
  // alone. The Go layer enforces the scope; without this the grid could
  // not GRANT one.
  addTagsPermissionScope(),

  // The store needs to know the `tags` type, or the page's tag list is
  // refused.
  new Extend.Store().add('tags', Tag),

  new Extend.Admin().page(TagsPage),
];

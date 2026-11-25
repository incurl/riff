// The tags extension's admin permission scope.
//
// Upstream's `addTagsPermissionScope`: a RESTRICTED tag gets its own
// column in the permission grid, so a permission can be granted for that
// tag alone.
//
// The grid's extension points are upstream's own (`scopeItems` and
// `scopeControlItems`), and the port's grid has both — the port's
// `PermissionGrid` renders `scopeItems()` as the grid's columns and
// `scopeControlItems()` as the control above them.
//
// The scope is carried IN THE KEY, as upstream does: the dropdown is given
// `tag{id}.{permission}`, and the server's `ForumPermissionKeyAndScope`
// splits the key into the forum's own permission name and the tag's id,
// which it stores in the row's own scope column. A grid that sent a bare
// permission name would write a GLOBAL row — and a restricted tag would
// then grant access to everybody.
//
// A tag becomes RESTRICTED through the dropdown above the grid, and the
// row it already holds is what makes it restricted: the scope column is
// non-empty.
import app from 'flarum/admin/app';
import { extend as extendMethod, override } from 'flarum/common/extend';
import PermissionGrid from 'flarum/admin/components/PermissionGrid';
import PermissionDropdown from 'flarum/admin/components/PermissionDropdown';
import Dropdown from 'flarum/common/components/Dropdown';
import Button from 'flarum/common/components/Button';
import LoadingIndicator from 'flarum/common/components/LoadingIndicator';

import { loadTags, sortTags, tagIcon, tagLabel } from '../helpers';
import { SCOPED_PERMISSIONS, globalPermission, scopedKey, scopedRequired } from './scope';

/**
 * Add the tags extension's admin permission scope to the grid.
 *
 * It is upstream's `addTagsPermissionScope`: a RESTRICTED tag gets its own
 * column in the permission grid, and a control above the grid makes a tag
 * restricted in the first place.
 *
 * The pure halves live in `./scope` so they can be tested without a
 * browser — the scoped key and the required-permission rewrite are where a
 * mistake turns a restriction into a grant.
 */
export default function addTagsPermissionScope() {
  // The grid waits for the tag list: a restricted tag's column is part of
  // the grid, so the grid cannot be drawn before the tags are known.
  extendMethod(PermissionGrid.prototype, 'oninit', function () {
    this.loading = true;
  });

  extendMethod(PermissionGrid.prototype, 'oncreate', function () {
    loadTags()
      .then(() => {
        this.loading = false;
        m.redraw();
      })
      .catch(() => {
        this.loading = false;
        m.redraw();
      });
  });

  override(PermissionGrid.prototype, 'view', function (original, vnode) {
    if (this.loading) {
      return <LoadingIndicator />;
    }

    return original(vnode);
  });

  // A tag-scoped permission requires the same permissions as the
  // unscoped one, so the grid's "required permissions" lookup has to strip
  // the scope first — otherwise it looks for a permission named
  // `tag5.viewForum`, which is not a permission at all.
  override(app, 'getRequiredPermissions', function (original, permission) {
    return scopedRequired(original(globalPermission(permission)), permission);
  });

  // The grid's COLUMNS: one per restricted tag.
  extendMethod(PermissionGrid.prototype, 'scopeItems', function (items) {
    sortTags(app.store.all('tags'))
      .filter((tag) => tag.isRestricted())
      .forEach((tag) => {
        items.add(
          'tag' + tag.id(),
          {
            label: tagLabel(tag),
            onremove: () => tag.save({ isRestricted: false }),
            render: (item) => {
              // A setting has no permission to scope.
              if ('setting' in item) return null;

              if (
                SCOPED_PERMISSIONS.includes(item.permission) ||
                (item.permission?.startsWith('discussion.') && item.tagScoped !== false) ||
                item.tagScoped
              ) {
                return <PermissionDropdown permission={scopedKey(tag.id(), item.permission)} allowGuest={item.allowGuest} />;
              }

              return null;
            },
          }
        );
      });
  });

  // The control ABOVE the grid: the tags that are not restricted yet, so
  // one can be made restricted.
  extendMethod(PermissionGrid.prototype, 'scopeControlItems', function (items) {
    const tags = sortTags(app.store.all('tags').filter((tag) => !tag.isRestricted()));

    if (!tags.length) return;

    items.add(
      'tag',
      <Dropdown
        className="Dropdown--restrictByTag"
        buttonClassName="Button Button--link"
        label="Restrict by tag"
        icon="fas fa-plus"
        caretIcon={null}
      >
        {tags.map((tag) => (
          <Button
            icon={tagIcon(tag, { className: 'Button-icon' })}
            onclick={() => tag.save({ isRestricted: true })}
          >
            {tag.name()}
          </Button>
        ))}
      </Dropdown>
    );
  });
}

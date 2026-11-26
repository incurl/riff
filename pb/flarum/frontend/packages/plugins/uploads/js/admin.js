// The Uploads extension's admin initialiser.
//
// It registers the six `fof-upload.*` permissions so the admin's permission
// grid can show them. Upstream does the same from its `js/src/admin/index.ts`,
// and without it the permissions are only grantable through the API: the grid
// renders the rows a bundle registered and nothing else, so an operator could
// not turn uploads off — or hand somebody the media-manager tier — from the
// page they are looking at.
//
// The `permission` value is the UPSTREAM name, because that is the string the
// server stores and the grid posts back; `domain.ForumPermissionKey` translates
// it to the forum's own flat key on the way in. A flat name here would be
// stored as a permission no handler reads.
//
// The labels are plain strings rather than `app.translator.trans(...)` calls, for
// the reason the likes bundle gives: this port vendors no upstream extension
// locale files, so a translation key would render as the key itself.
import Extend from 'flarum/common/extenders';

export const extend = [
  new Extend.Admin()
    // Who may attach a file at all. Upstream's README asks the operator to
    // grant it; this port seeds it for members, so the row is where it is
    // taken away again.
    .permission(() => ({ icon: 'fas fa-paperclip', label: 'Upload files', permission: 'fof-upload.upload' }), 'start', 90)
    // The media-manager tier: everybody's files, and the shared library.
    .permission(
      () => ({ icon: 'fas fa-trash', label: "Delete other users' uploads", permission: 'fof-upload.deleteUserUploads' }),
      'moderate',
      80
    )
    .permission(
      () => ({ icon: 'fas fa-trash', label: 'Delete own uploads', permission: 'fof-upload.deleteOwnUploads' }),
      'moderate',
      70
    )
    .permission(
      () => ({ icon: 'fas fa-user', label: "View other users' uploads", permission: 'fof-upload.viewUserUploads' }),
      'moderate',
      60
    )
    .permission(
      () => ({ icon: 'fas fa-folder-open', label: 'Access shared files', permission: 'fof-upload.accessSharedFiles' }),
      'moderate',
      50
    )
    .permission(
      () => ({
        icon: 'fas fa-folder-plus',
        label: 'Upload, hide and delete shared files',
        permission: 'fof-upload.uploadSharedFiles',
      }),
      'moderate',
      40
    ),
];

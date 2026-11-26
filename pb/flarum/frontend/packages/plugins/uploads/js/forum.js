// The Uploads extension's forum initialiser.
//
// It does TWO things, and they are the two upstream's `fof/upload` does:
//
//   1. it renders the files attached to a post; and
//   2. it lets the author ATTACH one: a button in the composer's control
//      row opens a file picker, posts the files to the extension's upload
//      route, and inserts the embed the server answers with at the cursor.
//
// A Flarum extension bundle exports `{ extend: [...] }`. The boot shell
// loads the bundle, hands `flarum.extensions` to `app.bootExtensions`,
// and each entry's `extend(app, { name, exports })` runs before mount.
//
// NOTHING may touch `app.forum`, `app.session` or `app.store` here: the
// shell runs `app.load(payload)`, then `app.bootExtensions(...)`, and
// only then `app.boot()` — and `app.boot()` is what assigns `app.forum`
// and `app.session`. `app.request` reads `this.session.csrfToken`, so a
// request fired from an extender throws on an undefined session.
//
// The work is therefore deferred to `app.beforeMount`, which runs inside
// `app.boot()` after the forum and the session exist — or to a component
// hook, which runs after both as well.
import app from 'flarum/forum/app';
import { extend as extendMethod } from 'flarum/common/extend';
import CommentPost from 'flarum/forum/components/CommentPost';
import TextEditor from 'flarum/common/components/TextEditor';
import Component from 'flarum/common/Component';
import Icon from 'flarum/common/components/Icon';
import classList from 'flarum/common/utils/classList';
import m from 'mithril';

/**
 * The file resources, keyed by their id.
 *
 * A post carries the identifiers of its files in its `files`
 * relationship. The resource itself is not included in the post payload,
 * so the attributes are fetched lazily — once per file, and cached.
 */
const fileCache = new Map();

/**
 * Load a file resource by id.
 *
 * The failure is not fatal: the post still renders, just without the
 * file's details. The promise is cleared on failure so a later redraw
 * retries.
 */
export function loadFile(id) {
  if (!fileCache.has(id)) {
    fileCache.set(
      id,
      app
        .request({ url: app.forum.attribute('apiUrl') + '/files/' + id })
        .then((payload) => payload.data)
        .catch((error) => {
          fileCache.delete(id);
          throw error;
        })
    );
  }

  return fileCache.get(id);
}

/**
 * The identifiers of a post's files.
 */
function fileIdentifiers(post) {
  const identifiers = post?.data?.relationships?.files?.data;

  if (!Array.isArray(identifiers)) return [];

  return identifiers.filter((identifier) => identifier && identifier.id);
}

/**
 * One attachment row.
 *
 * The file's URL comes from the resource, and it points at the forum's
 * own file route — the bytes are never linked at PocketBase's native
 * file URL, which lives at the root of the listener the forum does not
 * own.
 */
function attachmentRow(file) {
  if (!file) return null;

  const attributes = file.attributes || {};

  return (
    <li className="UploadsList-item">
      <a href={attributes.url} target="_blank" rel="noopener noreferrer" className="UploadsList-link">
        <Icon name="fas fa-paperclip" />
        <span className="UploadsList-name">{attributes.baseName}</span>
      </a>
      {attributes.humanSize ? <span className="UploadsList-size">{attributes.humanSize}</span> : null}
    </li>
  );
}

/**
 * The attachments of a post.
 *
 * The list is rendered from the cache, and a file that has not loaded
 * yet is rendered as a placeholder rather than being skipped: the
 * attachment is part of the post, and a post whose file silently
 * vanished is worse than one that shows a placeholder.
 */
function attachmentsOf(post) {
  const identifiers = fileIdentifiers(post);

  if (identifiers.length === 0) return null;

  const items = identifiers.map((identifier) => {
    const cached = fileCache.get(identifier.id);

    if (!cached) {
      // Not loaded yet: ask for it.
      loadFile(identifier.id)
        .then(() => m.redraw())
        .catch(() => {});
      return <li className="UploadsList-item UploadsList-item--loading" />;
    }

    return attachmentRow(cached);
  });

  return <ul className={classList('UploadsList', 'CommentPost-uploads')}>{items}</ul>;
}

/**
 * Whether the caller may upload at all.
 *
 * The forum document carries the answer (`fof-upload.canUpload`), computed
 * with the same permission the upload endpoint checks — so the button is
 * offered exactly when the request would be accepted. A hardcoded `true`
 * here would be a button the server refuses; upstream reads the same
 * attribute.
 *
 * @returns {boolean}
 */
function canUpload() {
  return Boolean(app.forum && app.forum.attribute('fof-upload.canUpload'));
}

/**
 * Upload files and hand back their embeds.
 *
 * The request is the extension's own: a POST to `{apiUrl}/fof/upload` with
 * the files under a repeated `files` field, answered by a JSON:API
 * collection whose resources carry a ready-made `bbcode`.
 *
 * @param {File[]} files
 * @returns {Promise<string[]>}
 */
function uploadFiles(files) {
  const body = new FormData();

  for (const file of files) {
    body.append('files[]', file, file.name);
  }

  return app
    .request({
      method: 'POST',
      url: app.forum.attribute('apiUrl') + '/fof/upload',
      body,
      // A multipart body sets its own Content-Type, boundary included;
      // overriding it would strip the boundary and the server could not
      // parse the parts.
      serialize: (data) => data,
    })
    .then((payload) => {
      const resources = Array.isArray(payload.data) ? payload.data : [payload.data];

      return resources
        .map((resource) => resource && resource.attributes && resource.attributes.bbcode)
        .filter((bbcode) => typeof bbcode === 'string' && bbcode !== '');
    });
}

/**
 * The attach button.
 *
 * It is a plain `<input type="file">` behind a button, which is what the
 * browser offers a reader; the picker's `change` event is the upload.
 */
class UploadButton extends Component {
  view() {
    return (
      <label className={classList('Button', 'Button--icon', 'UploadsButton', this.uploading && 'disabled')} title="Attach a file">
        <Icon name={this.uploading ? 'fas fa-spinner fa-spin' : 'fas fa-paperclip'} />
        <input
          type="file"
          multiple
          className="UploadsButton-input"
          disabled={this.uploading}
          onchange={(event) => this.onchange(event)}
        />
      </label>
    );
  }

  onchange(event) {
    const input = event.target;
    const files = Array.from(input.files || []);

    if (files.length === 0) return;

    const editor = this.attrs.editor;

    this.uploading = true;
    m.redraw();

    uploadFiles(files)
      .then((embeds) => {
        if (!editor) return;

        // The insert is one edit per file, so an undo takes back one
        // attachment rather than all of them.
        for (const embed of embeds) {
          editor.insertAtCursor(embed + '\n', false);
        }
      })
      .catch((error) => {
        // The server's own wording is the useful one ("That file is too
        // large.", "You are not allowed to upload files."), and it is the
        // same sentence the endpoint would show anywhere else. The core's
        // generic message is the fallback for a failure with no JSON:API
        // body at all — a dropped connection.
        const errors = (error && error.response && error.response.errors) || [];
        const detail = errors[0] && errors[0].detail;

        app.alerts.show({ type: 'error' }, detail || app.translator.trans('core.lib.error.generic_message'));
      })
      .then(() => {
        this.uploading = false;
        // The input keeps the chosen name, so selecting the same file
        // twice would not fire `change` again.
        input.value = '';
        m.redraw();
      });
  }
}

export const extend = [
  {
    extend() {
      // The attachments ride below the post's body.
      extendMethod(CommentPost.prototype, 'content', function (content) {
        const attachments = attachmentsOf(this.attrs.post);

        if (!attachments) return;

        content.push(attachments);
      });

      // The composer's attach button. `controlItems` is where the submit
      // and preview buttons live, and upstream `fof/upload` adds its
      // button to the same list.
      extendMethod(TextEditor.prototype, 'controlItems', function (items) {
        if (!canUpload()) return;

        items.add(
          'fof-upload',
          m(UploadButton, { editor: this.attrs.composer && this.attrs.composer.editor }),
          5
        );
      });
    },
  },
];

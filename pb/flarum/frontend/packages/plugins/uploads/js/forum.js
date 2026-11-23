// The Uploads extension's forum initialiser.
//
// It renders the files attached to a post.
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
// `app.boot()` after the forum and the session exist.
import app from 'flarum/forum/app';
import { extend as extendMethod } from 'flarum/common/extend';
import CommentPost from 'flarum/forum/components/CommentPost';
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

export const extend = [
  {
    extend() {
      // The attachments ride below the post's body.
      extendMethod(CommentPost.prototype, 'content', function (content) {
        const attachments = attachmentsOf(this.attrs.post);

        if (!attachments) return;

        content.push(attachments);
      });
    },
  },
];

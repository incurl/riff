# The forum's JSON:API contract

The reused Flarum frontend speaks JSON:API, so the forum reproduces
Flarum's wire format rather than inventing one. Everything below is
pinned by tests in `pb/flarum/`.

## The envelope

A successful response is a JSON:API document:

```json
{
  "data": { "type": "discussions", "id": "…", "attributes": {…}, "relationships": {…} },
  "included": [ … ],
  "links": { "first": "…", "next": "…", "prev": "…" },
  "meta": { "page": { "total": 42 } }
}
```

- The content type is `application/vnd.api+json`.
- `meta.page.total` is the number of records matching the query, ignoring
  pagination. The frontend's pagination control reads it.
- Pagination is `page[offset]` and `page[limit]`. The default limit is 20
  and the maximum is 100.

A failure is a JSON:API error document:

```json
{ "errors": [ { "status": "404", "code": "not_found", "detail": "That discussion does not exist." } ] }
```

`status` is the numeric HTTP status as a string. `code` is one of
`validation_failed`, `not_authenticated`, `permission_denied`,
`not_found`, `conflict`, `internal_error`.

## Resource types

| Type | Collection | Notes |
| --- | --- | --- |
| `forums` | — | The singleton boot document, id `1`. |
| `users` | `users` (shared) | Read-only; the forum owns no account store. |
| `groups` | `forum_groups` | |
| `discussions` | `forum_discussions` | |
| `posts` | `forum_posts` | |
| `notifications` | `forum_notifications` | |
| `tags` | `forum_tags` | Owned by the Tags plugin. |

## Endpoints

All of them live under the module's base path, `/forum/api` by default.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api` | The forum document (the frontend's boot payload). |
| GET | `/api/discussions` | List. Filters: `filter[user]`, `filter[tag]`, `filter[state]`, `filter[q]`. |
| POST | `/api/discussions` | Create a discussion with its first post. |
| GET | `/api/discussions/{id}` | Show. |
| PATCH | `/api/discussions/{id}` | Rename. |
| DELETE | `/api/discussions/{id}` | Delete. |
| GET | `/api/posts` | List. Requires `filter[discussion]`. |
| POST | `/api/posts` | Reply. |
| GET | `/api/posts/{id}` | Show. |
| PATCH | `/api/posts/{id}` | Edit. |
| DELETE | `/api/posts/{id}` | Delete. |
| GET | `/api/users` | List. |
| GET | `/api/users/{id}` | Show. |
| PATCH | `/api/users/{id}` | Update the forum-side profile only. |
| GET | `/api/groups` | List. |
| GET | `/api/groups/{id}` | Show. |
| GET | `/api/notifications` | List the caller's notifications. |
| PATCH | `/api/notifications/{id}` | Mark one read. |
| POST | `/api/notifications/read` | Mark all read. |
| GET | `/api/settings` | Read the settings. |
| POST | `/api/settings` | Write the settings (administrators only). |
| GET | `/api/extensions` | The enabled plugins' frontend bundles. |
| GET | `/api/tags` | The Tags plugin's tag list. |
| GET | `/api/tags/{id}` | A tag by id or slug. |
| POST | `/api/discussions/{id}/tags` | Attach a tag. |
| DELETE | `/api/discussions/{id}/tags/{tagId}` | Detach a tag. |
| POST | `/api/posts/{id}/like` | Like a post. |
| DELETE | `/api/posts/{id}/like` | Unlike a post. |

## Resource schemas

### `discussions`

Attributes: `title`, `slug`, `createdAt`, `lastPostedAt`,
`lastPostNumber`, `commentCount`, `participantCount`, `viewCount`,
`isHidden`, `hiddenAt`, `canReply`, `canRename`, `canHide`, `canDelete`.

Relationships: `user`, `lastPostedUser`, `firstPost`, `lastPost`,
`hiddenUser`, `posts`, `tags`.

### `posts`

Attributes: `content`, `contentHtml`, `contentType`, `createdAt`,
`editedAt`, `hiddenAt`, `isHidden`, `number`, `type`, `canEdit`,
`canDelete`, `canHide`, `renderFailed`.

`content` is the Markdown source and `contentHtml` is its rendered
body. The frontend inserts `contentHtml` as trusted HTML, so the forum
renders it server-side with riff's markdown engine; raw HTML in the
source is escaped. `contentType` is `text/html`.

The Likes plugin contributes `likesCount`, `isLiked` and `canLike`.

Relationships: `user`, `discussion`, `editedUser`, `hiddenUser`, and —
from the Likes plugin — `likes`.

### `users`

Attributes: `username`, `displayName`, `slug`, `avatarUrl`, `isAdmin`,
`joinTime`, `lastSeenAt`, `discussionCount`, `commentCount`,
`unreadNotificationCount`, `newNotificationCount`, `preferences`,
`canEdit`, `canEditAvatar`, `canEditGroups`, `canDelete`.

Relationships: `groups`.

### `groups`

Attributes: `nameSingular`, `namePlural`, `color`, `icon`, `isHidden`,
`position`, `memberCount`.

### `tags`

Attributes: `name`, `slug`, `description`, `color`, `icon`, `position`,
`isHidden`, `isPrimary`, `isRestricted`, `discussionCount`.

Relationships: `parent`.

### `notifications`

Attributes: `type`, `subject`, `excerpt`, `readAt`, `createdAt`,
`isRead`.

Relationships: `user`, `fromUser`, `discussion`, `post`.

## Sessions

The forum's session is a PocketBase auth token in an HttpOnly cookie
(`forum_access_token`) plus a double-submit CSRF token in a readable
cookie (`forum_csrf`).

- `POST /forum/login` accepts `{"identification", "password",
  "remember"}` — the shape the frontend's `Session.login` sends — and
  answers with the user and the CSRF token.
- `POST /forum/logout` clears both cookies.
- Every mutating request must send the CSRF token in the `X-CSRF-Token`
  header, with the same value as the cookie. A mismatch is a `403`.

A bearer token (`Authorization: Bearer <pocketbase token>`) is accepted
too, for API clients that do not use cookies.

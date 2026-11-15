# The shared `users` boundary

The forum has **no account store**. The host's PocketBase built-in
`users` auth collection is the forum's single account source of truth,
and the forum reuses it directly.

## What the forum reads

Only the permitted public fields: the record id, `username`, and the
`avatar` file. The `avatar` URL is served by the host.

## What the forum writes

Nothing to the shared collection. The forum's own extras live in
`forum_user_profiles`:

| Field | Purpose |
| --- | --- |
| `user` | The shared record's id (relation, unique). |
| `display_name` | A forum-side display-name override. |
| `preferences` | The account's forum preferences (JSON). |
| `is_active` | Whether the account is active in the forum. |
| `is_blocked` | Whether the account is blocked from the forum. |
| `joined_at` | When the account joined the forum. |
| `last_seen_at` | When the account was last seen. |

`PATCH /api/users/{id}` writes only these fields.

## What the forum never touches

The reserved auth fields — `email`, `password`, `passwordHash`,
`tokenKey`, `verified` — are never read into a response, never written,
and never altered by a forum migration. The forum does not create the
collection, does not change its schema, and does not change its access
rules.

## Account lifecycle

Registration, email verification, password reset and credential rotation
stay with the host's auth layer. The forum has no `/register`,
`/forgot` or `/confirm` endpoint.

## The one forum-side exception

The forum's group membership is stored in `forum_group_users`, which
references the shared `users` collection through a relation. That is a
reference, not a copy: deleting the account cascades the membership
rows away.

## Pinned by tests

- `TestCore_DoesNotCreateAUsersCollection` — there is no `forum_users`.
- `TestForum_SharedUsersIsNotRecreated` — the forum's own collection
  list never contains one.
- `TestCheckCollisions_SharedUsersIsAllowed` — the shared collection is
  not treated as a collision.

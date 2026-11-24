# Mentions

Status: Implemented

Date: 2026-11-24 (design), 2026-11-25 (implementation)

> **The design below is the handoff as it was written, and it is kept
> as written.** It records what was verified in the code on 2026-11-24,
> and the reasoning behind each step. Where the implementation had to
> settle something the design left open, the answer is recorded in
> "What the implementation settled" at the end of this document, and in
> `DECISIONS.md`.

Related: [JSON:API contract](jsonapi.md), [Extension points](extension-points.md), [Plugin authoring](plugin-authoring.md), [Known deviations](deviations.md)

## Summary

The forum has no mention support. Upstream `flarum/mentions` lets a post
reference a user with `@username`, stores the reference, notifies the
mentioned account, and shows on a post who else has mentioned it.

riff is closer to this than it looks, and the gap is not the parser: a
`@username` parser, an AST node and an `ExtractedData.Mentions` field
already exist in `markdown/`, and the memos layer enables them. What is
missing is everything downstream of the parse — a renderer, the storage,
the notification, and the post resource fields — plus two defects in the
surrounding code that have to be fixed first, one of which silently
deletes the text.

This document records what was verified in the code, what has to be
built, in what order, and which decisions are still open. It is written
so that the work can be picked up without re-deriving any of it.

## Scope

Core only:

- `@username` user mentions in **forum posts**;
- persisted to `forum_post_mentions_user`;
- the mentioned account is notified;
- `mentionedBy` / `mentionedByCount` on the post resource;
- the mention renders as a link to the account's profile.

Explicitly out of scope:

- the composer autocomplete;
- the quoted `@"Display Name"#id` syntax (needed only for a display name
  with a space, and riff usernames are `[a-z0-9_-]{3,30}`, so the bare
  form always works);
- post mentions (`#p123`), group mentions (`@"…"#g123`) and tag mentions
  (`#slug`);
- the "mentioned by" modal, the reply action and the quote button;
- the `flarum-mentions.allow_username_format` setting (upstream uses it
  to turn the bare `@username` form off; with the quoted form out of
  scope, turning the bare form off would leave no way to mention at all).

## Goals

- A post that says `@alice` renders as a link to `/{base}/u/alice`, and
  the text is never dropped.
- The reference is stored, so it survives a restart and can be queried.
- The mentioned account receives one notification per mentioning post,
  and is not notified about a mention of itself in its own post.
- An edited post's mentions follow the edit: a removed mention is
  removed, an added one is added.
- A mention of a username that does not exist is inert — it does not
  fail the save and does not notify anybody.

## Non-goals

- Autocomplete. The poster types the username.
- Mentioning by display name.
- Cross-content mention parity with memos. The memos layer has its own
  (working) mention *extraction*; this design must not change it.

## Current state

Facts verified in the code on 2026-11-24.

### What already exists

| Piece | Where |
| --- | --- |
| `@username` parser | `markdown/parser/mention.go` (`FindMentionMatches`) |
| goldmark AST transformer | `markdown/extensions/mention.go` (`MentionExtension`) |
| AST node | `markdown/ast/mention.go` (`MentionNode{Username, Source}`, `KindMention`) |
| extraction | `markdown/markdown.go:475-477` fills `ExtractedData.Mentions` |
| opt-in | `markdown/markdown.go:110` (`WithMentionExtension`) |
| notification kind | `pb/flarum/migrations/core.go:610` allows `mentionedInPost` |

The parser recognises the **bare form only** (`\B@[a-z0-9_-]+`, not
followed by `#`), which is the subset this design needs.

### What is missing

1. **There is no HTML renderer for `MentionNode`.** `markdown/html`
   registers no node renderer, so goldmark emits nothing for the node —
   and because the node carries no source segment, nothing can be
   recovered from it. Verified:

   ```
   NewService(WithMentionExtension()).RenderHTML("hey @alice, look")
     => "<p>hey , look</p>"      // the mention is GONE
   NewService().RenderHTML("hey @alice, look")
     => "<p>hey @alice, look</p>" // plain text
   ```

   This is the reason the forum disables the extension, and the comment
   at `pb/flarum/register.go` (the block ending at line 226) says so.

2. **The forum's markdown service enables nothing.** `register.go:226`
   is `markdown.NewService()` with no options, so in a forum post
   `@alice` is literal text. `RenderHTML` has exactly one caller —
   `pb/flarum/api/service.go:160` — so any renderer change is
   forum-scoped and cannot affect the memos SPA. (The memos layer calls
   `ExtractAll`, not `RenderHTML`.)

3. **No storage.** There is no mentions collection.

4. **No post resource fields.** `mentionedBy` and `mentionedByCount` do
   not exist.

5. **`EventPostEdited` is declared and never dispatched.**
   `pb/flarum/core/events.go:28` defines it; the only dispatch sites are
   `pb/flarum/api/service.go:105`, `:106` and `:140`, none of which is an
   edit. The post update handler dispatches nothing. An edited post's
   mentions would therefore never update — and would keep the ones the
   edit deleted.

6. **No plugin subscribes to the event bus yet.** No first-party plugin
   calls `SubscribeAfter`, so there is no in-repo precedent to copy. The
   mechanism exists (`core/events.go:133`, `Unsubscribe` at `:153`) and
   the plugin already receives the bus as `ext.Bus`.

### Two defects that block the notification

These are pre-existing and are **not** mention-specific, but a mention
notification cannot work without fixing them.

7. **The notification wire format does not match the SPA.** The
   serializer emits the kind as `type` (`pb/flarum/api/serialize.go:271`)
   and the body as `subject` / `excerpt`; upstream emits `contentType`
   and `content` (`NotificationResource`), and the vendored SPA reads
   `contentType` (`common/models/Notification.ts:5`) to pick a component
   (`forum/components/NotificationType.tsx:12`). So `contentType()` is
   `undefined` for **every** notification, the component lookup misses,
   and the list renders an empty row. The unread badge still counts,
   because it reads `isRead` — so the symptom is a badge with a blank
   list. This affects the `postLiked` notification shipped with the
   likes plugin as much as a future mention one.

8. **No component is registered for the mention kind.** The SPA's
   `notificationComponents` map holds only `discussionRenamed`
   (`forum/ForumApplication.tsx:42`), and `Extend.Notification` is
   available (`common/extenders/Notification.ts`) for a plugin to add
   one.

## The upstream contract

For reference while implementing; do not copy the parts that are out of
scope.

- **Table** `post_mentions_user(post_id, user_id)`, primary key on the
  pair. (Upstream also has `post_mentions_post` and
  `post_mentions_group`; both are out of scope.)
- **Post resource fields** (`Api/PostResourceFields.php`):
  `mentionedByCount` (integer, counted over posts the actor may see),
  `mentionedBy` (to-many posts, `oldest('id')`, limit 4),
  `mentionsPosts`, `mentionsUsers`, `mentionsGroups` (the last three are
  the post's own outgoing mentions — `mentionsUsers` is the one this
  design stores).
- **Listener** `UpdateMentionsMetadataWhenVisible` syncs on
  Posted/Revised/Restored, i.e. the mention set is **replaced**, not
  merged, and notifications are sent from a queued job.
- **Blueprint** `UserMentionedBlueprint`, type `userMentioned`. riff's
  notifications collection allows `mentionedInPost` instead; see the
  open question below.
- **Renderer** emits `<a class="UserMention" href="{profile}@slug">@name</a>`,
  and a `<span class="UserMention UserMention--deleted">` when the
  account does not exist.

## Design

### Step 1 — a `MentionNode` HTML renderer (prerequisite)

Nothing else can be tested until this exists: enabling the extension
without it deletes the text.

Add a goldmark `NodeRenderer` for `KindMention` in `markdown/html`,
rendering

```html
<a class="UserMention" href="{href}">@{username}</a>
```

with the href supplied by the caller as a `func(username string) string`.
Register it through a goldmark renderer option on the service's own
instance (`markdown/markdown.go:146-152`), not on a second instance, so
the HTML keeps the same extensions as the AST.

The layering rule for `markdown/html` holds: it may import goldmark and
nothing PocketBase-specific, and a function value keeps it that way — the
forum decides the URL, the package decides the markup.

**The default must not drop text.** When no href builder is configured,
render `<span class="UserMention">@{username}</span>`. A caller that
enables the extension and forgets the renderer then gets degraded links
instead of missing words.

### Step 2 — enable it for the forum, with its base path

Extend `markdown`'s options so one option does both halves, and use it at
`pb/flarum/register.go:226`:

```go
markdownService := markdown.NewService(
    markdown.WithMentionLinks(func(username string) string {
        return opts.BasePath + "/u/" + url.PathEscape(username)
    }),
)
```

The forum's user route is `/u/:username`
(`frontend/packages/core/src/forum/routes.ts:31`), so the link resolves
inside the mount path. `BasePath` is already in scope at that point.

**Memos must not change.** The memos layer keeps calling
`WithMentionExtension()` (in `pb/hooks/memos.go`,
`pb/hooks/memo_links.go`, `pb/hooks/notifications_helpers_content.go`).
It gains a span instead of a dropped mention if it ever renders HTML,
which is a fix, not a regression.

### Step 3 — the collection

A plugin-owned collection `forum_post_mentions_user`:

- `post` — relation to `forum_posts`, required, `CascadeDelete`;
- `user` — relation to the shared `users`, required, `CascadeDelete`;
- `created_at` — autodate.

Unique index on `(post, user)`: a post that mentions the same account
twice mentions it once. Upstream's primary key does the same.

Rules: read open, write closed. Every write goes through the plugin, as
with the other plugin collections.

### Step 4 — parse on save

The plugin needs to run after a post is written. Two pieces:

1. **Dispatch `EventPostEdited`.** Add `s.dispatch(ctx, …)` to the post
   update handler in `pb/flarum/api/handlers_mutations.go`, next to the
   existing `forum.post.created` dispatches. Without this an edit never
   reaches a listener.
2. **Subscribe in the plugin.** `Register` (or `Boot`) calls
   `ext.Bus.SubscribeAfter` for `EventPostCreated` and `EventPostEdited`
   with the plugin ID as the owner, and `Shutdown` calls `Unsubscribe`.
   The owner is what makes a disabled plugin leave nothing behind.

The handler then:

- re-extracts the mentions from the post's **stored content**
  (`markdown.Service.ExtractAll`), not from the request, so an edit that
  changed nothing keeps the same set and a post created by another path
  is still covered;
- resolves each username to a user id through the shared `users`
  collection, dropping the ones that do not resolve (a mention of a
  missing account is inert, and must not fail the save);
- **replaces** the post's rows with the resolved set — upstream syncs,
  and a merge would keep a mention the edit removed;
- notifies, per Step 5.

The notification work must not fail the save: the post is already
committed by the time the event fires, and a notification that could not
be written is not worth reporting to the author.

### Step 5 — the notification

Fix the wire format first (defect 7), then emit:

- `contentType` — the SPA's lookup key. Also add `content` while there;
  upstream carries it and the SPA's model already declares it.
- the notification writing helper in the plugin, following the likes
  plugin's `pb/flarum/plugins/likes/events.go` (`repo.CreateNotification`,
  best-effort, author excluded).

Rules:

- **the mentioned account is notified, not the author's own mention of
  itself** — same shape as upstream, which builds the recipient list from
  the post's mentions minus its author;
- one notification per (post, mentioned account) — the unique index makes
  a re-save idempotent only if the writer checks, so either delete the
  rows the set dropped and leave the rest, or rewrite the whole set in one
  go;
- a mention of an account that cannot see the post still gets a
  notification upstream (the notification is filtered at read time by the
  discussion's visibility). Matching that is easiest: write it, and let
  the existing notification list apply the access rules.

Then register a component for the kind:

```js
new Extend.Notification().add('mentionedInPost', UserMentionedNotification)
```

in the plugin's frontend bundle, or in the core SPA's
`notificationComponents` if the theme is "core owns the kinds it seeds".

### Step 6 — the post resource fields

- `mentionedByCount` — how many posts mention this one, counted over the
  posts the caller may see (upstream applies `whereVisibleTo`), so the
  number cannot reveal a post the caller cannot read.
- `mentionedBy` — the mentioning posts, oldest first, limit 4. The limit
  is upstream's `PostResourceFields::$maxMentionedBy = 4`; the count is
  the full number, so the UI can say "and 3 more".

Both are attribute/relationship contributions on `api.TypePost`, the
existing `ext.AddAttribute` / `ext.AddRelationship` vocabulary.

## Order of work

Steps 1 and 2 are a unit: neither is useful alone, and Step 1 without
Step 2 changes nothing observable.

1. Renderer + `WithMentionLinks` + forum wiring. Test:
   `RenderHTML("hey @alice")` yields the anchor, and `RenderHTML` with the
   extension enabled but no builder yields the span — never a dropped
   word.
2. Seed the collection. Test: the collection exists, the unique index is
   there, and two rows for the same pair are refused.
3. Dispatch `EventPostEdited`. Test: an edit dispatches it (a listener
   records the call).
4. The plugin: subscribe, extract, resolve, sync. Tests: a created post
   stores its mentions; an edit replaces them; an unknown username is
   ignored; a self-mention is stored (it is a real reference) but does
   not notify.
5. Fix the notification wire format, then notify. Tests: the mentioned
   account has one notification with `contentType` set; the author has
   none for its own self-mention; a re-save does not duplicate.
6. The post resource fields. Test: `mentionedByCount` and the
   `mentionedBy` relationship agree, and a post the caller cannot see is
   not counted.

## Verification

```bash
go test -count=1 ./markdown/...                      # the renderer
go test -count=1 -run 'TestMentions_' ./pb/flarum/   # the plugin
go test -count=1 ./pb/flarum/                        # no regressions
PATH="$HOME/go/bin:$PATH" golangci-lint run ./pb/flarum/... ./markdown/...
cd pb/flarum/frontend/packages/plugins/mentions/js && \
  NODE_PATH=…/frontend/node_modules node …/webpack-cli/bin/cli.js \
  --config webpack.config.cjs --mode production      # if a bundle is added
```

Pin the deletion bug explicitly: a test that asserts
`RenderHTML("hey @alice")` **contains `@alice`** fails on today's code and
on any regression that drops the node renderer.

## What the implementation settled

The three questions this document left open, answered. The reasoning
is in `DECISIONS.md` under "A MENTION IS A REFERENCE, NOT A
DECORATION".

1. **The notification kind is upstream's `userMentioned`.** The
   collection allowed `mentionedInPost`, and the collection is the
   authority on what a notification may be: a `SelectField` refuses a
   value it does not allow, so a kind the collection does not carry
   cannot be written at all. The plugin's migration widens the
   collection's allowed values — idempotently, so an instance that
   already has rows keeps them — and the old spelling stays valid
   beside the new one.

2. **The wire format was fixed here, and the fix is ADDITIVE.** The
   serializer publishes `contentType` and `content` ALONGSIDE the
   existing `type` / `subject` / `excerpt` rather than in place of
   them: a rename would be a breaking change on a shared surface, and
   the addition is what makes the component lookup work. Nothing that
   consumed the old names breaks.

3. **A mention of an account that does not exist renders the link
   anyway.** The renderer cannot consult the database — the markdown
   package imports nothing but goldmark — so the link is rendered
   regardless and the READ side decides whether the account exists.
   The mention is not stored either way, and the account is not
   notified.

Two things the design did not anticipate, both found while
implementing:

- **The event bus was never wired to the API server.** `api.NewServer`
  read `Options.Bus` into nothing, so `Server.dispatch` returned early
  on a nil bus and NO forum event reached a listener. The design's
  finding that "`EventPostEdited` is declared and never dispatched" was
  true, and understated: the `post.created` dispatch was equally dead,
  and no plugin had ever subscribed, so nothing looked wrong.
- **A re-save duplicated the notification.** The notification is one
  per (post, mentioned account), so the sync reports which mentions are
  NEW and only those are notified. An edit that changed nothing must
  not fill the reader's inbox.

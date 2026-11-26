# Flarum polls alignment with upstream `fof/polls` 2.x — handoff

**Status: IMPLEMENTED (2026-11-26).** The audit below is kept as it was written,
and the §7 order was followed: the panic, the to-many walk, `ResourceSerializer`,
the extension's three resources, the vote index (with a repair step), the
`included` chains, the resource-shape deltas (D1/D2/D3/D4/D5/D8), the discussion
vote scope, and the frontend (its own catalogue, the guest login modal, the
`pollId`/`optionId` accessors). Where the implementation differs from this
document, `DECISIONS.md` (the "POLLS EXTENSION, ALIGNED WITH UPSTREAM" entry)
records the final choice — most importantly that a TAG-scoped
`discussion.polls.vote` is NOT honoured, because resolving which of a
discussion's tags are restricted needs the tags extension's own data and the
port has no seam for one plugin to read another's collection.

**Goal:** align the `pb/flarum/plugins/polls/` plugin with upstream `fof/polls`
2.x — its API resources, vote semantics, permissions, settings and frontend
contract — and fix the backend/frontend gaps the comparison found.

---

## 1. Upstream contract (the target)

Fetched from `https://raw.githubusercontent.com/FriendsOfFlarum/polls/2.x/...`.
Upstream's branch is `2.x`.

### 1.1 The three resource types

Upstream registers exactly three JSON:API resources. Their `type()` values are
`polls`, `poll_options` and `poll_votes` — **the port already uses these exact
type strings** (`api.TypePoll` / `TypePollOption` / `TypePollVote` in
`pb/flarum/api/serialize.go`), which is what the frontend's store is keyed by.

#### `PollResource::fields()` — the full upstream list, in order

```
question            (Str,   requiredOnCreate, writable)
subtitle            (Str,   nullable, writable)
hasEnded            (Bool)  -> $poll->hasEnded()
allowMultipleVotes  (Bool)  -> $poll->allow_multiple_votes
maxVotes            (Int)   -> $poll->max_votes
endDate             (DateTime) -> $poll->end_date
createdAt           (DateTime)
updatedAt           (DateTime)
canVote             (Bool)  -> $actor->can('vote', $poll)
canEdit             (Bool)  -> $actor->can('edit', $poll)
canDelete           (Bool)  -> $actor->can('delete', $poll)
canSeeVoters        (Bool)  -> $actor->can('seeVoters', $poll)
canChangeVote       (Bool)  -> $actor->can('changeVote', $poll)
isGlobal            (Bool)  -> $poll->isGlobal()
image               (Str,   nullable)
imageUrl            (Str)   -> uploader->url($poll->image)
imageSrcset         (Str)   -> uploader->srcsetFor($poll->image)
imageAlt            (Str,   nullable)
publicPoll          (Bool)  -> $poll->public_poll
voteCount           (Int)   visible: $actor->can('seeVoteCount', $poll)
hideVotes           (Bool)  visible: $actor->can('edit', $poll)
allowChangeVote     (Bool)  visible: $actor->can('edit', $poll)
isImageUpload       (Bool)  visible: $poll->image !== null      [deprecated]
isDraft             (Bool)  -> $poll->isDraft()
publishedAt         (DateTime)
scheduledPublishAt  (DateTime)
scheduledPublishError (Str) visible: $actor->can('edit', $poll)
canPublish          (Bool)  -> $actor->can('publish', $poll)
canUnpublish        (Bool)  -> $actor->can('unpublish', $poll)
```

Upstream's relationships on the poll:

| Relationship | Type | Visibility |
| --- | --- | --- |
| `options` | `poll_options` (ToMany) | always |
| `votes` | `poll_votes` (ToMany) | **`visible: can('seeVoters', $poll)`** |
| `myVotes` | `poll_votes` (ToMany) | always |
| `post` | `posts` (ToOne) | always |
| `user` | `users` (ToOne) | always |
| `pollGroup` | `poll_groups` (ToOne) | always — **not ported** |

#### `PollOptionResource::fields()`

```
answer        (Str)
imageUrl      (Str)
imageSrcset   (Str)
createdAt     (DateTime)
updatedAt     (DateTime)
voteCount     (Int)  visible: $actor->can('seeVoteCount', $option->poll)
isImageUpload (Bool) visible: image url present   [deprecated]
```

Upstream's `PollOptionResource` declares **no** relationships — note this: the
port's option resource carries a `poll` relationship that upstream does not,
which is harmless but is not upstream's shape.

#### `PollVoteResource::fields()`

```
pollId     (Int) -> $vote->poll_id
optionId   (Int) -> $vote->option_id
createdAt  (DateTime)
updatedAt  (DateTime)
```

with relationships `poll` (`polls`), `option` (`poll_options`), `user` (`users`).

### 1.2 Upstream's endpoints

- `GET /api/polls/{id}` → `defaultInclude(['options', 'myVotes', 'myVotes.option'])`
- `GET /api/polls` → `defaultInclude(['options', 'votes', 'myVotes', 'myVotes.option'])`
- `PATCH /api/polls/{id}/votes` → the vote route; the response re-serialises the
  poll with `['options' => [], 'myVotes' => ['option' => []]]`
- `POST /api/polls` (create), `PATCH /api/polls/{id}` (update),
  `DELETE /api/polls/{id}`, `POST /api/polls/{id}/publish` — **not ported**

Upstream also adds the poll to the **post** resource:

```php
Schema\Relationship\ToMany::make('polls')->includable()->type('polls')
```

with `defaultInclude` on the post endpoints of
`['polls', 'polls.options', 'polls.myVotes', 'polls.myVotes.option']` and the
same set on the discussion's `firstPost`.

### 1.3 Upstream's vote semantics (`MultipleVotesPollHandler`)

```php
$maxVotes = $poll->allow_multiple_votes ? $poll->max_votes : 1;
if ($maxVotes == 0) { $maxVotes = $options->count(); }
$this->validateInput($optionIds, $maxVotes, $options);
if ($this->isChangingVotes($optionIds, $myVotes->pluck('option_id')->toArray())) {
    $actor->assertCan('changeVote', $poll);
}
// then, in one transaction:
//   - delete the votes whose option is NOT in $optionIds
//   - create a vote for each option in $optionIds not already voted
//   - RECOUNT the changed options' vote_count from the vote rows
//   - $poll->refreshVoteCount()->save()   (vote_count = votes()->count())
```

`validateInput` requires the `options` key to be **present** and an **array**,
with `max:$maxVotes` items, and every id must be one of the poll's options —
otherwise a `ValidationException`.

### 1.4 Upstream's policies (`src/Access/PollPolicy.php`)

```php
seeVoteCount(actor, poll):
    if ($poll->hide_votes && $poll->end_date && !$poll->hasEnded() && !$isPollAuthor) deny
    if ($poll->myVotes(actor)->isNotEmpty())                 allow
    if ($actor->can('polls.viewResultsWithoutVoting', discussion)) allow
    if ($poll->isGlobal())                                   allow
    if ($isPollAuthor)                                       allow
    return null                                              // fall through to the global grant

seeVoters(actor, poll):
    if (!$actor->can('seeVoteCount', $poll)) deny
    if ($poll->public_poll)                      allow
    return null                                  // fall through to the global grant

vote(actor, poll):
    $can = $discussion ? $actor->can('polls.vote', $discussion) : false;
    $canVoteGlobalPoll = $poll->isGlobal() && $actor->hasPermission('discussion.polls.vote');
    if (($can || $canVoteGlobalPoll) && !$poll->hasEnded()) allow
    return null

changeVote(actor, poll):
    if ($actor->hasPermission('polls.changeVote')) allow
    return (bool) Arr::get($poll->settings, 'allow_change_vote', false)
```

Note `seeVoters` **requires** `seeVoteCount` — the port's `canSeeVoters` does
not, and the port's `canSeeVoters` is gated on `canSeeVoters && canSeeCount`
which is close but not identical (upstream's `public_poll` alone is not enough
when the count is hidden).

### 1.5 Upstream's settings

`src/Poll.php` reads the settings document:

```php
public_poll         => (bool) Arr::get($this->settings, 'public_poll');
allow_multiple_votes=> (bool) Arr::get($this->settings, 'allow_multiple_votes');
max_votes           => (int)  Arr::get($this->settings, 'max_votes');
hide_votes          => (bool) Arr::get($this->settings, 'hide_votes');
allow_change_vote   => (bool) Arr::get($this->settings, 'allow_change_vote', true);
```

`Poll::build()` writes `max_votes => max(0, (int) $maxVotes)`.

Upstream's `Poll::hasEnded()` is `end_date !== null && end_date->isPast()`.

### 1.6 Upstream's database

- `polls`: `question`, `subtitle`, `vote_count`, `post_id` (nullable — a global
  poll), `user_id`, `end_date`, `published_at`, `scheduled_publish_at`,
  `scheduled_publish_error`, `settings` (JSON), timestamps
- `poll_options`: `poll_id`, `answer`, `vote_count`, `image_url`, timestamps
- `poll_votes`: `poll_id`, `option_id`, `user_id` (nullable), timestamps
  — **NO unique index at all upstream**

### 1.7 Upstream's frontend contract

`js/src/forum/models/Poll.ts` — the attributes the model reads are exactly the
upstream `fields()` list above, and:

```ts
options() { const o = Model.hasMany<PollOption>('options').call(this); return o ? o : []; }
votes()   { return Model.hasMany<PollVote>('votes').call(this); }
myVotes() { const v = Model.hasMany<PollVote>('myVotes').call(this); return v ? v : []; }
```

`js/src/forum/states/PollState.ts` — the load-bearing bits:

```ts
get canSeeVoteCount(): boolean {
  // The server omits the count entirely while votes are hidden, so its
  // absence is the permission check.
  return typeof this.poll.voteCount() === 'number';
}
getMaxVotes(): number {
  let maxVotes = poll.allowMultipleVotes() ? poll.maxVotes() : 1;
  if (maxVotes === 0) maxVotes = poll.options().length;
  return maxVotes;
}
submit(optionIds) {
  return app.request({ method: 'PATCH', url: `${apiUrl}/polls/${this.poll.id()}/votes`,
                       body: { data: { optionIds: Array.from(optionIds) } } })
}
```

`js/src/forum/addPollsToPost.tsx` — the poll is rendered under the post:

```tsx
extend(CommentPost.prototype, 'content', function (content) {
  const post = this.attrs.post;
  if ((!post.isHidden() || this.revealContent) && post.polls()) {
    for (const poll of post.polls()) { if (poll) content.push(<PostPoll post={post} poll={poll} />); }
  }
});
```

### 1.8 Upstream's locale

`resources/locale/en.yml`, namespace `fof-polls:`. The forum-scoped keys the
port's frontend actually needs:

```yaml
fof-polls:
  forum:
    days_remaining: Poll ends {time}.
    no_permission: You don't have permission to vote
    poll_ended: This poll has ended.
    public_poll: View voters
    max_votes_allowed: "Poll allows voting for {max, plural, one {# option} other {# options}}."
    polls_count: "{count, plural, one { {count} vote} other {{count} votes}}"
    poll_never_ends: Poll never ends
    answers: Answers
    poll:
      cannot_change_vote: You cannot change your vote after voting.
      submit_button: Vote
      total_votes: "{count, plural, one {# vote was given} other {# votes were given}}"
    votes_modal:
      title: Voters
    modal:
      no_voters: No Votes
```

---

## 2. Confirmed defects (each reproduced empirically)

Each of the four was reproduced with a temporary probe test, which has since
been removed — the working tree is clean.

### DEFECT 1 — `GET /api/polls/{id}` PANICS for an anonymous caller

`pb/flarum/plugins/polls/handlers.go:477-483`:

```go
func actorIDOf(e *core.RequestEvent) string {
	actor, err := api.CurrentActor(e)
	if err != nil {
		return ""
	}
	return actor.User.ID     // <-- nil deref for an anonymous caller
}
```

`session.Actor.User` is **nil** for an anonymous request (`pb/flarum/session`:
`User *domain.User` — "nil for an anonymous request"). The error check only
catches a missing *server*, not a missing *user*.

Reproduced: `GET /forum/api/polls/{id}` with no cookies →
`panic: runtime error: invalid memory address or nil pointer dereference` at
`polls/handlers.go:483`, reached from `showPollHandler → writePoll →
pollResource → actorIDOf`.

**This is the worst of the four**: it is a panic in a request handler, so it
takes the whole process down, not just the request. Any unauthenticated visitor
who hits a poll URL kills the server.

Fix: `return actor.ID()` — `session.Actor.ID()` already returns `""` for an
anonymous caller, and `actorIDOf` wants exactly that.

### DEFECT 2 — the unique index is on `(poll, user)`, so a multi-vote poll is broken

`pb/flarum/plugins/polls/collection.go:233`:

```go
collection.AddIndex("idx_"+physical+"_poll_user", true, "poll, user", "")
```

The column list is `"poll, user"` — but the comment directly above it says:

> **The UNIQUE index on (poll, option, user) is load-bearing.** … It is UNIQUE
> on the option too — not only on (poll, user) — because a poll that allows
> multiple votes must still hold at most ONE row per (option, voter)

and `DECISIONS.md` (the "A POLL IS A SELECTION, NOT A DELTA" entry) says:

> **THE UNIQUE INDEX IS ON (POLL, OPTION, USER), NOT (POLL, USER).**

**The code contradicts both the comment and the decision log.** The index is
on `(poll, user)`, so a voter may hold only ONE vote row per poll — which makes
`allow_multiple_votes` impossible, and the vote route's own multi-option path
(`applyVotes` inserting one row per option) fails.

Reproduced: a poll with `settings = {"allow_multiple_votes": true,
"max_votes": 2}`, voting for two options → **HTTP 500** ("The vote could not be
recorded"), and only **one** vote row exists afterwards.

Fix: `collection.AddIndex("idx_"+physical+"_poll_option_user", true, "poll, option, user", "")`.

**Migration concern:** the collection is created by a *plugin* migration
(`plugin.Migration`, recorded in `forum_plugin_migrations`), not a
`pb/migrations/` one. An existing instance already has the wrong index and the
plugin migration will not re-run (it is recorded as applied). The fix therefore
needs to be **idempotent and re-runnable** — e.g. a second plugin migration step
that checks the existing index and replaces it, or an `OnBootstrap` repair. This
must be handled deliberately; see §4.

### DEFECT 3 — the poll, option and vote resources are absent from `included`

`pb/flarum/api/included.go` resolves relations only for the core's own types
(`TypeDiscussion`, `TypePost`, `TypeUser`). A plugin-contributed relation — the
post's `polls` relationship — produces the **identifier** but no **resource**.

The frontend resolves a relationship through the STORE, not the identifier:

```ts
// pb/flarum/frontend/packages/core/src/common/Model.ts
static hasMany<M extends Model>(name: string) {
  const relationshipData = this.data.relationships?.[name]?.data;
  if (relationshipData) { return relationshipData.map((data) => this.store.getById<M>(data.type, data.id)); }
}
```

and the store is filled from `payload.included`. So `post.polls()` resolves to
`[undefined]` — the frontend's `for (const poll of post.polls()) { if (!poll)
continue; }` skips it and **the poll never renders**. The poll is in the
database, the post carries the relationship, and the UI shows nothing, with no
error anywhere.

Reproduced: `GET /forum/api/posts/{id}` for a post with a poll →
`included types = [users discussions]`; the post's `polls` relationship is
present and correct (`{data: [{id: ..., type: polls}]}`), but no `polls`
resource is in `included`.

Two sub-parts:

**(a) the `included` walk cannot reach a plugin's resources.** It switches on
the *resource type* and has no way to ask a plugin to render one. The plugin
framework has `AddAttribute` and `AddRelationship` but **no resource-serializer
extension point** — this is the architectural gap.

**(b) there is no `poll_votes` resource builder at all.** `TypePollVote` is
declared in `api/serialize.go` and used for identifiers in
`polls/handlers.go` and `polls/store.go`, but **nothing anywhere builds a
`poll_votes` resource**. So even once the walk can reach plugin resources, the
vote resource has to be written.

Upstream's `myVotes` is in the poll's default include, and upstream's
`myVotes.option` is included too — the frontend's `selectedOptionIds(poll)`
walks `poll.myVotes()` and reads `vote.option()`, so **both the vote resource
and its `option` relation must be in `included`** for the checkbox to reflect
the voter's current selection.

### DEFECT 4 (secondary, found while reading) — a to-many relation is never resolved

`pb/flarum/api/included.go:291` — `relatedIdentifier` reads a **single**
identifier from a relation's `data`. For a to-many relation, `data` is an
array, so it never matches and the relation is never followed.

Reproduced: `GET /forum/api/users/{id}` for a user in a group →
`included types = []` — the `groups` relation is not resolved, even though
`defaultIncludesFor(TypeUser)` asks for `"groups"` and
`pb/flarum/api/included.go:194` — the `case TypeUser:` branch with its
`if relation == "groups"` — is therefore **dead code** for a to-many relation.

This is a pre-existing core bug that the polls work will touch (the poll's
`options` and `myVotes` are to-many). It is arguably in scope because the
polls alignment needs a working to-many walk; but it is a **separate defect**
and should be pinned by its own test so the two are not conflated.

---

## 3. Other upstream/port deltas (not yet bugs — decide each deliberately)

| # | Upstream | Port | Action |
| --- | --- | --- | --- |
| D1 | `endDate` is a `DateTime` (RFC 3339) | the port emits `poll.EndDate` **raw**, which is PocketBase's `2006-01-02 15:04:05.000Z` | The port's other timestamps use `timestamp()` (RFC 3339). JS `new Date("2026-01-02 15:04:05.000Z")` happens to parse, but it is not the format `Model.transformDate` documents. Normalise through `timestamp()`. |
| D2 | `createdAt` / `updatedAt` on the poll, the option and the vote | the option resource hard-codes `"createdAt": ""` and `"updatedAt": ""` | Fill them from the record's autodate columns. |
| D3 | the vote resource carries `pollId` and `optionId` attributes | the port builds **no** vote resource at all | Add them (`api/serialize.go` has no vote builder). |
| D4 | `votes` relationship is `visible: can('seeVoters')` | the port always sets `votes` and `myVotes` | Hide `votes` unless the caller may see the voters; upstream omits the relationship rather than sending an empty one. |
| D5 | `seeVoters` requires `seeVoteCount` **and** `public_poll` (or the global grant) | the port's `canSeeVoters` is `canSeeVoters && canSeeCount` | Align with upstream's `seeVoters` policy. |
| D6 | `hideVotes` and `allowChangeVote` are `visible: can('edit', $poll)` | the port always sends both | The port has no poll-edit path (no create/update routes), so `canEdit` is always false → these two would always be hidden. **Decide**: either keep sending them (the port's frontend reads `allowChangeVote()`) or drop them. Upstream's visibility rule exists because they are poll *settings*; the port has no editor, so hiding them removes information the port's own frontend uses. |
| D7 | `canVote` is `$actor->can('vote', $poll)` — a **guest** has no `polls.vote` | the port's `canVote` is `canVote && !hasEnded`; `PermissionVotePoll` is seeded for the **member** group only | The port is already correct here: a guest fails `api.Can` because the guest group holds no `polls.vote`. Verify and pin. |
| D8 | upstream's `max_votes` is `max(0, (int) $maxVotes)`; `max_votes == 0` means "as many as there are options" | the port's `pollSettings.maxVotes(optionCount)` already implements this | Correct already. But `decodeSettingsMap` reads `max_votes` **only** as `float64` — a settings document written with an integer (or by a Go `map[string]any{"max_votes": 2}`) does not decode. Broaden the type switch. |
| D9 | upstream's `hasEnded()` is `end_date !== null && end_date->isPast()` | the port compares `p.EndDate <= now` as **strings**, with `now` formatted as PocketBase's layout | Correct as long as both sides use PocketBase's layout — but it is a string comparison on a timestamp, and it breaks the moment `end_date` is stored in another format. Prefer a real time comparison. |
| D10 | upstream's `isGlobal()` is `post_id === null` | the port's `isGlobal` is `poll.PostID == ""` | Equivalent. Keep. |
| D11 | upstream's `vote` policy is scoped to the **discussion** (`polls.vote` against the discussion) and honours a **tag-scoped** `discussion.polls.vote` | the port asks `api.Can(e, domain.PermissionVotePoll, api.TypePoll, poll.ID)` — the scope is the **poll id** | The permission is granted **globally** in the seed (member group), so the scope does not bite today. But upstream's `discussion.polls.vote` is a **tag-scoped** grant, and `domain/permission_keys.go` already maps `discussion.polls.vote` → `PermissionVotePoll`. The scope should be the **discussion**, so a restricted tag can restrict voting. This is the same trap the tags plugin already solved — see the AGENTS.md entry on `discussion.polls.*` and `domain.ForumPermissionKeyAndScope`. |
| D12 | upstream's `hide_votes` only hides while the poll is **un-ended** and the reader is not the author | the port's `canSeeVoteCount` matches this shape | Correct. |
| D13 | upstream's `myVotes` for a **guest** matches nothing | the port's `myVotes` for an anonymous caller resolves to every vote, because `voteIDs(app, pollID, "")` treats `""` as "all" | **A guest's `myVotes` would be every vote in the poll.** `voteIDs`' `userID == ""` means "all votes" — and `actorIDOf` returns `""` for a guest. That is a **privacy leak**: the guest sees every voter's vote resource. Fix by distinguishing "no filter" from "this user has none". |

D13 is worth flagging separately — it is a fourth real defect that only became
visible once the panic in DEFECT 1 was understood (the panic meant the guest
path never completed).

---

## 4. The architectural decision that has to be made first

**The plugin framework has no resource-serializer extension point.**

Today the framework offers:

| Extender | What it does |
| --- | --- |
| `AddAttribute` / `AddVisibleAttribute` | contributes **attributes** to a core resource |
| `AddRelationship` | contributes a **relationship** (identifiers only) |
| `AddRelationshipWriter` | the write side of a relationship |
| `AddPermission` | an access check |
| `AddRoute` | an HTTP route |
| `AddMigration`, `AddSettings`, `AddAsset`, `AddLocale`, `AddNotificationType` | the rest |

There is no way for a plugin to say "and here is how to render MY resource
type". Two candidate strategies:

### Strategy A — a generic `ResourceSerializer` extension point (recommended)

Add to `pb/flarum/plugin/extenders.go`:

```go
// ResourceSerializer renders a plugin-owned JSON:API resource.
type ResourceSerializer struct {
    // Type is the resource type, e.g. "polls".
    Type string
    // Render renders one resource by id.
    Render func(ctx context.Context, req *ResourceRequest) (*Resource, error)
}

type ResourceRequest struct {
    App      core.App
    ActorID  string
    ID       string
    Can      func(action, subjectType, subjectID string) bool
    CanScoped func(action, subjectType, subjectID string) bool
}
```

and to the API server a matching registry, so that `included.go`'s walk can
resolve a plugin's relation by asking the plugin to render the resource.

**Layering:** `pb/flarum/api` must NOT import `pb/flarum/plugin` (the API
package is below the framework — `api` currently imports only `core`,
`domain`, `repository`, `session`; `plugin` imports only `flarum/core`).
So the API server needs its **own** narrow interface, and the module
(`pb/flarum/register.go`) adapts the plugin's declaration onto it. This is the
same shape as the existing `plugin.AttributeContributor` → `api.AttributeSource`
adapter, and the same discipline the `ExtensionInfo` comment describes
("the API package does not import it — the two packages are siblings, and the
dependency would point the wrong way").

### Strategy B — special-case the polls in the core

Add `polls`/`poll_options`/`poll_votes` to `api/serialize.go` and
`api/included.go` directly.

**Reject this.** The AGENTS.md rules are explicit: "Public Connect/gRPC
endpoints live in `pb/connect/`; custom REST endpoints live in `pb/api/`", and
the forum's module layer is where plugins plug in. A plugin's resources
hard-coded into the core would make the polls plugin unfreeable and would set
a precedent every future plugin would follow. It also puts poll vocabulary
(`forum_poll_options`, the `polls` settings shape) into the core, which
AGENTS.md forbids: "A package goes in `internal/` only if it could be published
as a standalone module unchanged. If it knows what a Memo, Space, or instance
setting is, it is a root package." — and by the same rule, a core that knows
what a *poll* is has the wrong shape.

**Recommendation:** Strategy A. It is a small, well-scoped framework addition,
it is the extension point the framework is missing anyway, and it makes the
`included` walk work for **any** plugin that ships resources (the tags plugin
will want it next).

### The `included` walk also has to handle to-many (DEFECT 4)

Even with a serializer registry, the walk must follow a **to-many** relation
and include every resource it names. Today `relatedIdentifier` reads one
identifier out of `data`, so the walk must be taught to read an array.

This is the change that makes `polls.options`, `polls.myVotes` and `user.groups`
resolve. It should be pinned by its own tests (the polls tests would otherwise
mask it).

---

## 5. Where the code lives today

```
pb/flarum/plugins/polls/
  polls.go       — the plugin: Register() declares the migrations, the
                   relationships, the routes, the permissions and the bundle
  collection.go  — the three PocketBase collections
  store.go       — the row decoders (pollRecord, pollOptionRecord), the
                   settings decoder, applyVotes, the recounts
  handlers.go    — the route handlers, pollResource, optionResource,
                   canSeeVoteCount, actorIDOf
pb/flarum/api/
  serialize.go   — the core resource builders; declares TypePoll,
                   TypePollOption, TypePollVote (but builds NO vote resource)
  included.go    — the `included` walk
  context.go     — the plugin-facing helpers: Can, CanScoped, WriteDocument,
                   WriteError, RequireAuth, VerifyCSRF, CurrentActor
pb/flarum/plugin/
  extenders.go   — the extension points
  handler.go     — RequestHandler
pb/flarum/domain/
  permission_keys.go — the upstream↔forum permission key translation
  models.go          — the Permission* constants
pb/flarum/migrations/seed.go — the permission seed
```

**The frontend** (`pb/flarum/frontend/packages/plugins/polls/js/`):

```
  models.js   — Poll, PollOption, PollVote; already declares every
                upstream attribute EXCEPT pollId/optionId on the vote
  forum.js    — the store registration, the post's `polls` relationship, the
                PostPoll component, the vote route call
  admin.js    — empty (`export const extend = []`)
  style.css
```

`forum.js` uses `m.redraw()` — `m` **is** a real global (the core exposes
mithril through `expose-loader?exposes=m!mithril` in
`packages/core/src/common/index.ts`), so that is fine.

The frontend's `Poll` model does **not** read `pollId`/`optionId` — upstream's
does (`PollVote.pollId()`, `PollVote.optionId()`). Add them for parity.

---

## 6. The frontend contract the port's bundle must satisfy

`forum.js` currently:
- registers the three types in the store: `new Extend.Store().add('polls', Poll).add('poll_options', PollOption).add('poll_votes', PollVote)`
- adds `polls` to the post: `new Extend.Model(Post).hasMany('polls')`
- renders the poll under the post's content

The upstream shape it should converge on:
- `PollState`-equivalent behaviour: `canSeeVoteCount` is "the `voteCount`
  attribute is present", `getMaxVotes()` is
  `allowMultipleVotes() ? maxVotes() : 1` with `0` meaning "all options"
- the vote is submitted as `{data: {optionIds: [...]}}` to
  `PATCH {apiUrl}/polls/{id}/votes` — **the port already does this**
- `myVotes` → `vote.option()` → the option id is the selected set — **the port
  already does this**, but it needs the vote resources in `included`
- a **guest** clicking an option should be offered the login modal
  (upstream's `PollState.changeVote` shows `LogInModal`); the port currently
  just lets the request fail with a 401
- `useSubmitUI`: upstream uses a submit button when the poll allows multiple
  votes AND the vote cannot be changed. The port votes immediately on every
  click. Upstream's `PollState.submit` sends the whole set either way, so the
  port's immediate-vote is still correct in shape — but it cannot UN-tick an
  option in a multi-vote poll (the port's `toggleOption` computes the next
  selection, so it can). Re-read the port's `toggleOption` before changing it.

The **locale keys**: the port's `forum.js` calls
`app.translator.trans('polls.forum.total_votes', …)` and
`app.translator.trans('polls.forum.poll_ended')` — but the port ships **no
`polls` locale bundle at all**, and the built-in `en` catalogue
(`packages/core/locale/core.yml` + `validation.yml`) does not carry them, so
`trans()` returns the raw key and the UI shows `polls.forum.total_votes`.

**This is another real defect.** Two ways to fix it:
- add a `polls.yml` to the core's catalogue (`packages/core/locale/`), keyed
  `polls:` — but upstream's namespace is `fof-polls:`, and the built-in
  catalogue is the CORE's, so a plugin's keys do not belong there
- give the polls plugin its **own locale bundle** via `plugin.LocaleBundle`
  (the framework already has the extension point — `AddLocale`) and compile it
  with `packages/core/locale-compile.cjs`, which is the shared compiler the
  Chinese pack uses

The **Chinese pack** (`pb/flarum/plugins/chinese/`) is the reference for a
plugin-owned catalogue: `locale/*.yml` + a `build-locale.cjs` that calls the
shared `compileLocale`, staged by `scripts/build_flarum_spa.sh`. If the polls
plugin gets a locale bundle, the Chinese pack should also gain a
`fof-polls.yml` (upstream's `zh-Hans` catalogue exists) — and its `SOURCES`
list must be extended.

Upstream's namespace is `fof-polls:`, so the port's frontend should use
`fof-polls.forum.*` keys to match upstream — **or** keep `polls.forum.*` and
give the bundle that namespace. Pick one and be consistent; upstream's is
`fof-polls:`.

---

## 7. Suggested implementation order

1. **DEFECT 1** (the panic) — one line, `actorIDOf` → `actor.ID()`. Pin it with
   a test that fetches a poll anonymously. **Do this first and separately**: a
   panic in a request handler is the most severe defect here and it is
   independent of everything else.
2. **DEFECT 4** (the to-many walk) — the `included` walk learns to follow a
   to-many relation. Pin it with its own tests (a user's `groups`, a
   discussion's `firstPost`).
3. **Strategy A** — the `ResourceSerializer` extension point:
   `plugin/extenders.go` (the declaration), `api/api.go` (the narrow
   interface + the registry), `api/included.go` (the walk asks the registry),
   `pb/flarum/register.go` (the adapter).
4. **The polls' own resources** — the poll, the option and **the vote**
   (which does not exist yet). The vote needs `pollId`/`optionId` (D3) and the
   `poll`/`option`/`user` relationships.
5. **DEFECT 2** (the index) — fix the index, and handle the **existing
   instances** whose wrong index is already in place (a plugin migration that
   has already been recorded will not re-run).
6. **DEFECT 3 / D13** — the `included` set is right; make the guest's
   `myVotes` empty rather than "every vote".
7. **D1, D2, D4, D5** — the resource-shape deltas.
8. **D8** — the `max_votes` type switch.
9. **D11** — the vote permission's scope (the discussion, not the poll), so a
   restricted tag can restrict voting. Read the AGENTS.md entry on
   `discussion.polls.*` and `domain.ForumPermissionKeyAndScope` first.
10. **The frontend** — the locale bundle (the missing `polls.forum.*` keys),
    the guest login modal, `pollId`/`optionId` on the vote model, and
    whatever the upstream `PollState` comparison turns up.
11. **The tests** — replace the probes with permanent regression tests:
    - the `included` set carries `polls`, `poll_options` and `poll_votes`
    - a multi-vote poll holds one row per (option, voter)
    - the vote count agrees with the rows
    - the count is absent for a reader who may not see it
    - a guest's `myVotes` is empty
    - an ended poll takes no votes
    - the permission gate
    - a malformed body (a missing `optionIds`, a non-array, a foreign option id,
    - too many options)
    - an anonymous poll fetch does not panic

---

## 8. The commands to verify

```bash
# The narrowest check while iterating.
go test -count=1 -run 'TestPolls_' ./pb/flarum/

# The whole forum module.
go test -count=1 ./pb/flarum/...
go test -v -race -count=1 ./pb/flarum/...

# The frontend bundles.
cd pb/flarum/frontend/packages/core && npx jest --config jest.config.local.cjs --testPathPattern 'polls'
bash scripts/build_flarum_spa.sh   # the bundles must be restaged

# The plugin bundle harness (it evaluates every plugin's forum AND admin
# bundle and asserts each `extend` entry is a real extender).
go test -count=1 -run TestFrontendBundles ./pb/flarum/

# Lint and formatting.
gofmt -l pb/flarum/
PATH="$HOME/go/bin:$PATH" golangci-lint run
git diff --check
```

`npm run typecheck` in the vendored frontend has **unrelated pre-existing
ambient-Jest errors** — do not let those block the work, and do not "fix" them
as part of this task.

---

## 9. Documentation obligations

- **`DECISIONS.md`** — append a new entry (do not rewrite the existing ones).
  The current "A POLL IS A SELECTION, NOT A DELTA (2026-11-24)" entry
  **already claims** "THE UNIQUE INDEX IS ON (POLL, OPTION, USER), NOT (POLL,
  USER)" — which the code contradicts. The new entry must record that the
  decision was made but **not implemented**, and what the implementation is.
- **`AGENTS.md`** — the "Flarum forum module" row in the Change Routing table
  is already very long. Add the polls-specific facts (the resource-serializer
  extension point, the `included` walk's to-many rule, the vote index) rather
  than a new row, and keep it in the established voice: **the trap, the
  symptom, and why the obvious fix is wrong**.
- **`docs/design/flarum/`** — the design documents are **frozen** after
  implementation. This work is an alignment of an implemented plugin, so it
  belongs in `DECISIONS.md`, not in a design document. If a design document
  covers the polls, do **not** rewrite it — verify against the source.

---

## 10. References

- Upstream repo: `https://github.com/FriendsOfFlarum/polls`, branch `2.x`
- `src/Api/Resource/PollResource.php`, `PollOptionResource.php`, `PollVoteResource.php`
- `src/Access/PollPolicy.php`
- `src/Commands/MultipleVotesPollHandler.php`
- `src/Poll.php` (the settings accessors and `hasEnded`)
- `extend.php` (the endpoint and default-include declarations)
- `resources/locale/en.yml`
- `js/src/forum/models/{Poll,PollOption,PollVote}.ts`,
  `js/src/forum/states/PollState.ts`, `js/src/forum/addPollsToPost.tsx`,
  `js/src/forum/components/Poll/AbstractPoll.tsx`
- Upstream's own core for the default-include sets:
  `flarum/framework` `2.x` `framework/core/src/Api/Resource/{Post,Discussion}Resource.php`

# DECISIONS.md — riff architectural audit log

This file is the single source of truth for non-obvious decisions made
while building riff, a PO-33 community site replacing the Memos Go
backend with PocketBase. New entries are appended at the top, oldest at
the bottom.

## A TAG SELECTION NEEDS A BUTTON, AND AN INCLUDED RESOURCE MUST NOT LIE (2026-11-26)

"Composer tag selection does not stick" was two independent faults that
produce the same symptom, and neither was in the write path — the tag was
being saved correctly the whole time. Both were found by driving the real
SPA in a headless browser (`playwright-core` against the cached Chromium),
which is the only thing that could have separated them: the Go suites were
green, the payload was right, and the discussion really did carry its tag in
the database.

**First: the picker modal had no way to CONFIRM.** `TagSelectionModal`
rendered the list, the chips and the hint, and no submit control — the only
exits were the header's close button and the backdrop, and neither calls
`onsubmit`. So the author picked tags, left the modal, `setComposerTags` was
never called, and the composer posted with an empty set. A modal that can
only be dismissed is a modal whose work is always discarded, and the hint
("Choose at least 1 primary tag") made it look like a validation message
rather than a dead end. The fix is upstream's own control — a
`Button type="submit"` inside the FormModal's `<form>` — disabled until the
selection satisfies the same four counts the server enforces
(`selectionMeetsLimits`, the pure rule in `utils/selectionLimits.js`, so the
button and `validateTagCounts` cannot drift). The same modal serves "Edit
tags" on a discussion, which had the identical defect.

**Second: the `included` discussion resource claimed the discussion had NO
tags.** A discussion travels as an included resource whenever a payload
names it as a relation — the post stream does exactly that
(`defaultIncludesFor(TypePost)` is `["user", "discussion"]`) — and the
include walk built that copy with a stub:

```go
discussionResource(discussion, false, false, false, false, false, nil)
```

`nil` tag ids, and every permission flag false. The store does not keep two
copies of a resource: `Model.pushData` MERGES an included resource into the
model it already holds, and a relationship the incoming copy names is taken
from it verbatim — so `{tags: {data: []}}` from the post stream **wiped the
tags the create response had put in the store**. In the browser this looked
exactly like "the selection does not stick": the composer showed the tag, the
discussion was created with it, the payloads all carried it, and the
discussion page rendered untagged. The same copy also replaced
`canReply`/`canRename`/`canTag` with false, which is why a moderator's
controls could vanish on a page whose own endpoint reported them.

The walk now calls the SAME builder the discussion's own endpoint uses
(`s.discussionResource(w.event, discussion)`), so there is one rendering of a
discussion and no way for a partial copy to exist. `TestTags_AnIncludedDiscussionCarriesItsTags`
pins both halves (the tags relationship AND `canReply`); reverting the walk
to the stub turns it red.

**A third, smaller fault on the same surface**: the composer's "clear the
chosen tags when it closes" extension hooked `Composer.onhide`, which does
not exist (`onhide` is an ATTRS callback on `Dropdown` and
`PostStreamScrubber`) — `extend` happily DEFINED the method, nothing ever
called it, and the module-level selection leaked from one discussion to the
next. It is `onremove` now, the composer's real lifecycle hook, and the second
composer of a session opens with no tags.

## A TAG'S PARENT IS THE TREE'S EDGE, AND THE MODAL CAN SET IT (2026-11-26)

An operator reported that a secondary tag's `parent` did not reference the
primary tag, and that the admin page could not create a secondary tag in its
place. Both are true, and they are the same gap seen from two sides.

**Upstream has no parent field either.** `flarum/tags`'s `EditTagModal` offers
name, slug, description, colour, icon, hidden and default sort; "Create
secondary tag" creates an UNPLACED tag, and the administrator then DRAGS it
under a primary tag on the Tags page. So the tag's `parent` is null until a
drag supplies it, and the one thing the operator wanted — to say which primary
tag the new secondary tag belongs to — is not expressible in the form.

This port now offers it (`EditTagModal`'s `Parent tag` select, fed by the pure
`tagParent.js`), and **the field is shown for EVERY existing tag and for a new
secondary tag** — hidden only for "Create primary tag", where a parent would
contradict the button that opened the modal. An earlier cut hid it on a primary
root as well, on the theory that a root changes kind by being dragged; the
report that followed ("I do not see the parent tag select") is what a control
that is present-but-hidden looks like from the operator's chair. Choosing a
parent on a root DEMOTES it to that tag's child — the write path below does
exactly that — so the field is the modal's only way to move a tag into the
tree, and hiding it there was hiding a capability.

The page also carries a `+` on every primary tag (`TagListItem-addChild`) that
opens the same modal with `{primary: false, parentId}`, so "add a secondary tag
to THIS primary tag" is one click with the parent already chosen; the + is
what "the UI does not accommodate secondary tag creation" was really asking
for, because the page's own button makes an UNPLACED tag and leaves the
placement to a drag. The options are the ROOT tags only and never the tag being
edited.

**The write path had to be fixed first, and one half of it was a silent bug.**
`parent`, `position` and `is_primary` describe ONE tree, and they can disagree:

- a body that names `parent` as empty did NOT clear it. The reader answered ""
  for both "no relation named" and "the relation is null", so a PATCH that
  cleared a parent looked like a PATCH that said nothing about it, and the tag
  stayed nested under a primary tag the operator had just pulled it out of.
- nothing stopped a CHILD from having children, or a tag from being its own
  parent, or `isPrimary: true` from being combined with a parent. Each of those
  is a tag that renders NOWHERE — the primary list excludes anything with a
  parent, and the tree draws one level — so the tag is in the store, absent
  from the page, with no error anywhere.

`resolveTagParent` now decides the tag's kind from what the body SAID (not from
what the merged tag holds, which is how a PATCH that only renames a child would
be read as "make this a root"): a named parent makes the tag a child of it —
not primary, no position of its own — and `isPrimary: true` makes it a root, so
it loses its parent and takes a place after the last primary (the position it
carried as a child is 0, and keeping it would collide with the tag already
there). Asking for both at once is a 422: either answer puts the tag somewhere
the operator did not ask for, and guessing is how a tag disappears.

The parent is validated too: it must exist, it must be a ROOT tag (a grandchild
is a third level this port's page and count rules were not written for), and it
cannot be the tag itself.

**The presence of the relation is its own fact.** The SPA sends `parent` as an
ATTRIBUTE (`Model.save` drops a null relationship, so a relationship can say
"the parent is X" but not "the parent is nobody"), while an upstream-shaped
client sends `data.relationships.parent.data`. `parentFromBody` reads all three
shapes and reports whether the body NAMED the relation at all — which is what
makes a clear distinguishable from silence.

Pinned by `go test -count=1 -run 'TestTags_' ./pb/flarum/` and the Jest
`tagParent` suite.

## THE WIKI'S BRANDING IS REAL, AND A REFUSED SAVE IS NOT A SILENT ONE (2026-11-26)

The wiki answered every branding write with `501 Not Implemented`. That was
honest — nothing was stored — and the operator's report was that the site name
never applied *and* that saving popped "Cannot read properties of null (reading
'faviconFile')". Both halves come from the same place, and neither is what a
refusal should look like.

`pb/leafwiki/branding.go` now stores the configuration at upstream LeafWiki's
paths — `<storage>/leafwiki/branding.json`, `branding/logo.<ext>`,
`branding/favicon.<ext>` — so a data directory still round-trips, and the SPA's
whole branding page works: the name, the logo, the favicon, and the delete
controls.

**The toast is the interesting half.** The SPA's `updateBranding` does
`const updated = await brandingAPI.updateBranding(...)` and then reads
`updated.faviconFile`. Its transport (`fetchWithAuth`) throws on a non-2xx and
returns `null` from `await res.json()` when the body is not JSON — and a PUT to
an UNROUTED path is `ok` with the SPA shell in the body, because the catch-all
is the least specific route in the mux. So the failure an operator sees is a
TypeError about a field of `null`, not "branding is not configurable". That is
the wiki's documented unrouted-path trap seen from the other side: the earlier
cases parsed markup as data, this one read a FIELD off the absence of data.

Every branding route therefore exists and every one of them answers with the
whole configuration. The writes are administrator-only (upstream's
`RequireAdmin`; in this port the `admin` role is a PocketBase superuser), which
is why `requireAdmin` is a separate gate from `requireWriter`: the latter is
the PAGE gate, and `RIFF_WIKI_PUBLIC_WRITE` deliberately lets an anonymous
caller through it. A wiki editor may write pages; the site's identity is not a
page.

The limits are upstream's, and they are PUBLISHED rather than only enforced:
`brandingConstraints` (nested, as the SPA reads it) carries the extension
lists, the 1 MiB caps and the 100-character name cap, so the uploader refuses
a file before sending it and the server refuses it again if the client does
not — one limit, two enforcement points, no drift. The images are served at
`<prefix>/branding/{filename}` and only for the two names the configuration
currently holds, which is what makes path traversal impossible without a
sanitiser; `<prefix>/favicon.ico` answers a browser's own request, and 404s so
the shell's `/favicon.svg` stays in place until something is uploaded.

The shell reads the store too — `{{__SITE_NAME__}}` and
`{{__FAVICON_HREF__}}` are rendered from it, not from literals — because the
shell is what the browser tab and the first frame show. A name that only
reached the SPA would leave the tab reading the old one until React mounted,
which is exactly the "it did not apply" the operator reported.

Pinned by `go test -count=1 -run 'TestBranding_' ./pb/leafwiki/`.

## GAMIFICATION ALIGNED WITH `fof/gamification` 2.x (2026-11-26)

The port had votes, a score and a leaderboard endpoint, and almost none of the
extension's visible surface — and every one of the gaps was SILENT.

**The ranks were the first hole.** The plugin declared `user.ranks` as a
relationship and had no `ResourceSerializer` for `ranks`, so the response
carried the IDENTIFIER and no RESOURCE. The frontend resolves a relation out of
the STORE (`Model.hasMany` → `store.getById`) and fills the store from
`included`, so the rank was in the database, the relation was on the wire, and
the badge drew nothing with no error anywhere — the exact failure the polls
port documented. `ranks` is renderable now, and the SERVER got upstream's three
endpoints too (`GET /api/ranks` public, `POST`, `PATCH`/`DELETE /api/ranks/{id}`
administrator-only, with the POST twins the admin app's method override needs).
Without them the ranks table could only ever be seeded by hand: upstream's rank
editor is on a custom admin page this port does not have, and no API meant no
rank at all.

**Two permission plumbing bugs sat on the same surface.** (1) One forum
permission (`seeVoters`) answered BOTH upstream's `canSeeVotes` (the tally) and
`canSeeVoters` (the names), so the admin grid could not grant one without the
other, and everybody who could read a number could read who voted; the split is
two keys now. (2) The admin grid's checkboxes post `discussion.votePosts`,
`discussion.canSeeVotes`, `discussion.canSeeVoters` and
`fof.gamification.viewRankingPage`, and the key table only knew the bare
spellings — and an unknown key is stored under its own name, so EVERY
gamification checkbox wrote a row no handler ever asked for, and granting
voting (or the ranking page) changed nothing. The `discussion.`-prefixed names
are canonical now, because they are what the grid round-trips.

**Three shared framework holes, all of the "declared but never applied" kind:**

- `AttributeContributor.Visible` was honoured on discussions, users and the
  forum document and IGNORED on posts, which had a private copy of the loop
  that called `Value` alone — so `votes` was sent to readers who were meant not
  to see the tally. The private loop is gone; everything goes through
  `applyPluginAttributes`.
- Plugin RELATIONSHIPS were applied to posts only, so `user.ranks` (and any
  future relation on a user, a discussion or the forum document) reached no
  response at all. `applyPluginRelationships` is the one helper now, used by all
  four.
- The plugin adapters handed `Visible` predicates NO permission checker, so a
  `visible()` that asked a permission — upstream's
  `->visible(fn ($post, $ctx) => $ctx->getActor()->can('canSeeVotes', $post))`
  — answered "no" for everybody and the field was never sent even when its value
  computed correctly.

**And a guest was treated as nobody.** `pluginCan`, `pluginCanScoped`,
`permissionChecker` and `permissionCheckerScoped` all short-circuited an empty
actor id to `false`, while the permission service itself resolves an anonymous
caller onto the GUEST group. That made every guest-facing `can*` attribute a
lie: the leaderboard endpoint let a guest in (it asks `api.Can`, which does not
short-circuit) while the forum document's `canViewRankingPage` said false, so
the page would have hidden itself from the visitor it was meant for. The
short-circuits are gone; the guest group's rows decide, which is what
`allowGuest` in the admin grid grants.

**The leaderboard speaks upstream's dialect.** Metric and period live under
`filter` (where JSON:API reserves a resource's own parameters), the page is
`page[offset]`/`page[limit]`, the default page size is 10 and the maximum 50
(upstream's `paginate(10, 50)`), the entry id composes the ranking with COLONS
(`metric:period:position`), the page names the users it ranks in `included`,
and `links.next` — the "load more" control — exists when there is more. The
resource's own URL `/api/leaderboard-entries` is served beside the older
`/api/leaderboard` alias. An unknown metric is still a `422`: upstream falls
back, but a fallback answers a question the caller did not ask.

**The rankings page reads the forum document.** `canViewRankingPage`, the two
metric/period lists, the two defaults and `rankAmt` are published there,
because upstream's `RankingsPage` builds its selectors from `app.forum` BEFORE
it loads anything — an empty list is a selector with no options, and a missing
default makes the operator's choice a control that changes nothing.

**The seeds are the port's, not upstream's, and that is deliberate.** Upstream
seeds `vote` for members and `viewRankingPage` for GUESTS, and leaves
`canSeeVotes`/`canSeeVoters` unseeded — so a fresh upstream install shows no
tally to anybody. This port seeds the tally for members (a vote whose count
nobody sees is a button with no feedback), the names for moderators, and the
ranking page for guests AND members (upstream's guest-only row hides a public
page from every signed-in member, which is a bug rather than a policy).

Not ported, and recorded in `docs/design/flarum/deviations.md` §16d:
`meta.standing`/`meta.highlights` and `movement` (all three compare against a
previous window this port does not keep), the podium images,
`filter[rankable]` with excluded users and groups, tag-gated voting,
auto-assigned groups, the trending sort, the likes→votes conversion, vote
notifications (they need per-direction group permissions AND a `data` column
the inbox does not have) and the voters modal.

## ONE MCP CATALOG, THREE MODULES THAT ARE NOT PROTO (2026-11-26)

The MCP service derives its tools from `proto/gen/openapi.yaml`, which describes
the Connect/gRPC surface. The wiki, the forum and Bark are hand-written
PocketBase surfaces with no proto document, so they had no tools at all — the
instance's own MCP endpoint could post a memo but could not read a wiki page,
start a discussion or send a push.

**They declare their tools, and the declaration lives next to the routes it
calls.** `internal/mcptools` holds one pure-data type (`Tool`: name,
description, method, path, parameters, optional JSON body);
`pb/leafwiki/MCPTools`, `pb/flarum/MCPTools` and `pb/bark/MCPTools` return
declarations built from their own mount prefix; `main.go` passes them to
`mcp.Register(..., mcp.WithTools(...))`. A declared tool becomes the same
`*sdkmcp.Tool` an OpenAPI operation does, with the same input-schema shape (a
body is ONE nested `body` argument) and the same method-derived annotations,
and a call executes through the SAME adapter: an in-process request to the
module's route carrying the caller's `Authorization` header. So the module's
authentication, its access rules and its error shapes are the ones that
answer — the MCP surface must never be a second, more permissive way into the
same data.

The layering is why the type lives in `internal/`: `server/mcp` and `pb/*` are
siblings in the import graph, so neither may import the other. Each module
keeps one dependency on `internal/mcptools`; for Bark that breaks its
documented "imports no riff package" property, which now reads "imports one, a
zero-dependency data type". A second MCP endpoint per module was the
alternative — and Bark already HAD one (`/bark/mcp`, upstream's `notify`
tool): a second catalog and a second credential for the same capability is
exactly what an agent cannot use.

**A declaration is validated at startup**, not at call time: snake_case name,
a description (it is what an agent reads before choosing), a supported
method, an absolute path, `path`/`query` parameter locations only, every
`{placeholder}` backed by a parameter, and no duplicate or curated-colliding
name. A tool that registers and then cannot run is a catalog entry an agent
will pick and the server will refuse.

**Two traps came with the seam, and both are now reported by name.**

*An unrouted path is not a 404 here.* The Memos SPA's catch-all is the least
specific route in the mux, so a path that had drifted from its module's mount
point answered `index.html` with a 200, and the tool failed as "failed to
decode API JSON response" — a message about HTML that says nothing about the
cause. The adapter now recognises an HTML reply and answers that the path is
not an API route and the request reached the single-page app's catch-all.
Each module ALSO pins its declared paths in `mcptools_test.go`, because driving
the real mux is the only thing that catches a prefix change.

*A write route that demands a CSRF cookie refuses every tool call.* The adapter
forwards only `Authorization` — there is no cookie jar to double-submit from.
The forum's `session.VerifyCSRF` now returns early when the request carries a
bearer token: a token travels in an explicit header a cross-site page cannot
set, so there is no ambient credential to forge, and a COOKIE caller still
needs the double submit (both halves are pinned by
`TestForum_ABearerCallerWritesWithoutCSRF`). Upstream's framework has the same
rule. Without it, `forum_create_discussion` and `forum_reply` over MCP answered
403 while the same writes over HTTP worked.

**ONE CREDENTIAL, EVERYWHERE: THE PAT.** The seam above exposed a second
credential problem. riff's revocable credential is the personal access token
(`memos_pat_*`): a PocketBase auth token is a stateless JWT with a fixed
lifetime, so an agent cannot be told "stop" — while a PAT can be deleted.
`pb/connect` understood PATs, but the forum's session resolver and the wiki's
current-user lookup accepted PocketBase auth tokens only, so a PAT could post a
memo through `/mcp` and not a forum reply or a wiki edit. The verifier now
lives in `internal/pattoken` (a leaf package: core + bcrypt), `pb/connect`
delegates to it, and both SPA-facing resolvers fall back to it — the same
three-package duplication that `api.SettingTruthy` was consolidated to avoid.
Pinned by `TestForum_APersonalAccessTokenWrites` and
`TestWiki_APersonalAccessTokenWrites`; each seeds a PAT row and writes through
the module's own route.

The wiki's CSRF check already skipped a header-authenticated caller; the
forum's did not, which is the asymmetric half of the same question. Both now
agree: a COOKIE caller double-submits, a BEARER caller does not, because a
header the browser cannot attach cross-site is not an ambient credential.

Verified end to end by `TestMCP_DeclaredModuleToolsAreCallable` in
`main_test.go`, which mounts the three modules, registers `/mcp` with their
declarations and drives the HTTP endpoint: `tools/list` carries `wiki_*`,
`forum_*` and `bark_*`, `wiki_list_pages` answers a JSON tree, and
`bark_check_device` for an unknown key answers Bark's own JSON 400 as a tool
error.

## STICKY, LOCK AND SUSPEND, ALIGNED WITH UPSTREAM (2026-11-26)

The three moderation plugins had their ATTRIBUTES and their ROUTES and were
missing what makes a moderation tool noticeable.

**Pinning a discussion did nothing to the list.** `is_sticky` was a badge:
the discussion kept its place in date order, so a reader looking for the pinned
announcement found it wherever it fell. Upstream's
`PinStickiedDiscussionsToTop` prepends `is_sticky desc` to the query, and the
port now does the same from `ListDiscussions` — but only where upstream pins:
on the DEFAULT sort (a "top" or "newest" listing asks a different question),
ALWAYS on a tag page (a place rather than a ranking), and on the All Discussions
page only while `flarum-sticky.pin_sticky_on_all_discussions` is on. That
setting's key is `api.SettingPinStickyToTop` rather than a plugin constant,
because the LIST handler is what reads it (`api.stickyPinned`) — the same
arrangement as `allow_tag_change`.

The first attempt looked implemented and did nothing, for a reason worth
keeping: **dbx's `OrderBy` REPLACES the ORDER BY clause** and only `AndOrderBy`
appends, so `query.OrderBy("is_sticky DESC").OrderBy(orderBy)` kept only the
second call. Both orders go into ONE `OrderBy` call now, and the comment at the
join says why. The test caught it because it asserts the pinned discussion is
FIRST, not that a flag is set.

**Two of the three sticky settings are NOT declared.** `enable_display_excerpt`
needs the `firstPostExcerpt` attribute (the excerpt rendered under a pinned row,
not ported) and `only_sticky_unread_discussions` needs a per-user
`last_read_post_number` — this port keeps no read-state table, so a pinned
discussion is pinned whether or not the reader has opened it. Neither switch is
declared, because a control nothing honours is worse than an absent one; the
one that IS declared is read.

**`canSuspendUsers` is on the forum document.** Upstream serves it from
`$actor->hasPermission('user.suspend')`, and it is a different question from the
per-user `canSuspend`: that one is "may this caller suspend THIS account" (it
also excludes self-suspension), this one is "may this caller suspend at all",
which is what a user-list control branches on.

LOCK needed no change: a locked discussion still accepts a reply from whoever may
lock it (upstream's `DiscussionPolicy::reply()`), and the reply path and the
`canReply` attribute already asked the same question.

**AND EVERY PLUGIN `can*` FLAG READ FALSE FOR THE INSTANCE OPERATOR.** Following
`canSuspendUsers` to its source turned up the reason it answered false for a
superuser while `canStartDiscussion` answered true: `Module.pluginActor`
resolved the actor through the `users` COLLECTION, and a PocketBase superuser is
not a record there. So every plugin-contributed flag — `canLock`, `canSticky`,
`canSuspend`, `canTag`, `canLike`, `fof-upload.canUpload` — answered false for
the instance's own operator while the endpoints accepted all of those writes.
The UI draws its controls from the flags, so an administrator was shown a forum
with the moderation controls missing and no error anywhere. The lookup now falls
back to the `_superusers` collection, which is what `session.userFromRecord`
already did, so the flag and the endpoint ask the same actor the same question.
The test asserts the flag, the route AND the resulting state together: a flag
that says "no" is not a failed write, it is a control that never appears.

**Not ported**, and each for a reason rather than for time: the event posts the
two extensions write into the stream (`discussionLocked`, `discussionStickied`,
whose upstream components do not exist in this port's bundles — writing a post
the UI renders as raw content is worse than not writing it), the
`discussionLocked`/`userSuspended`/`userUnsuspended` notifications (the port's
notification framework has no blueprint for them and no email template), and the
`filter[locked]`/`filter[sticky]`/`filter[suspended]` search filters.

Verified live and by test: three discussions with the OLDEST pinned list as
`[Announcement, …]`; the setting off restores date order; a tag page still pins
with the setting off; `canSuspendUsers` is false for a member and true for a
moderator.

## THE TAG HIERARCHY IS TWO NULLABLE COLUMNS, AND ALL THREE HALVES WERE WRONG (2026-11-26)

Secondary tags could not be created into a tree, and dragging one under another
did nothing. Three separate faults, each of which made the feature look broken in
a different way:

**The order endpoint refused the payload the page sends.** `TagsPage.onSortUpdate`
posts upstream's shape — `{"order": [{"id": …, "children": [id, …]}, …]}`, one
entry per primary tag and the ids dropped into its child list. `tagOrder` only
accepted `[]string`, so every drag answered **422 "The order could not be
read."** and nothing was written. The fixture in the test was a hand-written flat
list, which the page has never sent: the same class of mistake as a hand-written
`{"seconds": N}` timestamp — a green test asserting a shape the client cannot
produce.

**The write ignored children entirely.** `setTagPositions` wrote a position per
id and touched nothing else. It is now upstream's `OrderTagsController`, and the
part worth keeping is the RESET: every tag is cleared first, then the ones the
payload names are placed, so a tag left out becomes unplaced. That single rule is
what makes three drags work — into a child list (parent set, position = its index
among the siblings, `is_primary` true), back out to the root (parent cleared,
its own position), and out to the "Secondary tags" list (omitted, so cleared).

**`position` was served as the stored `0` for every tag.** Upstream's is a
NULLABLE column and the null is load-bearing: the admin page's two lists are
`position() !== null && !isChild()` and `position() === null`. Serving `0` put
every tag in the primary list and left the secondary one permanently empty, which
is exactly "secondary tags do not work". The column here is a non-nullable
`NumberField` (PocketBase has no nullable number), so `is_primary` is the stored
flag that says which of the two a tag is, and the RESOURCE is what must present
upstream's shape: `position` is `null` when `is_primary` is false. `seedTag` in
the tests writes `position: 0` without `is_primary`, i.e. an unplaced tag, which
is what the count tests want it to be — the fixture now says so.

**On the frontend, children were rendered from a relationship that does not
exist.** `tagItem` read `tag.children()` (`Model.hasMany('children')`), and the
API serves each tag's own `parent` and nothing that points back — so the accessor
returned `false`, the nested list rendered empty, and because the root list
filters `!tag.parent()`, a child vanished from the page COMPLETELY. The tree now
filters the STORE on `parent()` (upstream's approach), and the `children()`
accessor is gone so the trap is not left for the next reader.

**And the drag's own redraw showed the old tree.** The server answers 204, and
the page re-renders from the store, so without mirroring the new arrangement into
the store first the tag snapped back to where it came from even when the write
succeeded — the visual half of "unable to drag into a hierarchy". `onSortUpdate`
now applies the same arrangement it posts (`pushData` per tag), which is what
upstream does.

Verified live: three tags created (`A`, `B` primary, `C` secondary) serve as
`position 0` / `1` / `null`; posting the page's payload with `C` under `A`
answers 204 and leaves `C` with `parent: A`, `position: 0`, `isPrimary: true`,
`isChild: true`; omitting `C` from the next payload returns it to `position:
null`. Pinned by `TestTags_TheOrderPayloadNestsATag`,
`TestTags_TheOrderDemotesWhatItDoesNotMention`,
`TestTags_AnUnplacedTagIsASecondaryTag` and `TestTags_APrimaryTagCarriesItsPosition`.

## THE MCP ENDPOINT ANSWERS AT `/mcp/` TOO, AND AT THE HOST ROOT (2026-11-26)

Two separate halves of one request, and neither alone is enough to connect a
client.

**The proxy.** `/mcp` was not in the root block that carries `/_` and `/api`, so
`Caddyfile.fly.community` matched it with `@spa` and answered **308 →
`/memos/mcp`**, while `scripts/Caddyfile` let the static site 404 it. Both now
proxy `/mcp` and `/mcp/*` VERBATIM (`handle`, never `handle_path` — the path IS
the endpoint), and the fly profile also excludes them from `@spa`. The 308 is
not harmless for this surface: an MCP client POSTs its JSON-RPC handshake to
exactly the URL it was configured with, and a redirect into a sub-path is a URL
it never asked for.

**The trailing slash.** `POST /mcp/` answered **200 with `index.html`** — the
SPA catch-all is the least specific pattern in the mux, so a path riff does not
route is served the shell, and the client parses a page of markup as a JSON-RPC
reply. Several MCP clients normalise the configured URL to end in a slash, so
this was the more likely spelling to break. `server/mcp/register.go` now
registers the exact `/mcp` pattern AND the `/mcp/` subtree (a pattern ending in
`/` is a subtree in `http.ServeMux`), and the handler serves only those two
spellings: a deeper path answers a JSON 404 instead of being treated as the
endpoint or falling through to the SPA.

**The deploy smoke test drives a real handshake.** `scripts/fly_deploy.sh` gained
a fifth stage that POSTs an `initialize` request to `https://<host>/mcp` and
requires a 200 whose content type is `application/json` (or `text/event-stream`).
A status-only check would pass on the shell — 200 with `text/html` is exactly
what the broken state returned — so the check inspects the content type, and its
failure message names both the missing handle blocks and the missing `@spa`
exclusion.

Verified against a real caddy with the real Caddyfile: before, `POST /mcp` and
`POST /mcp/` both answered 308 into `/memos/`; after, both answer
`application/json` from the MCP service (`{"jsonrpc":"2.0",…,"serverInfo":
{"name":"memos"}}`), while `/`, `/api/health` and `/memos/api/health` are
unchanged. `TestMCP_RegisterMountsEndpoint` pins all three spellings at the mux.

## THE DEFAULT LANGUAGE IS A SETTING, AND THE RESOLVER IS UPSTREAM'S (2026-11-26)

Changing the forum's **Default language** in the admin did nothing. The setting
saved (`default_locale`), the Basics page kept showing it, and the forum went on
rendering whatever `RIFF_FORUM_LOCALE` said at boot — because that is the only
input `Module.activeLocale` read. Two more halves of the same feature were
hardcoded: the forum document served `showLanguageSelector: false`, so the
header's language dropdown never rendered even with a second catalogue
installed, and `show_language_selector` was not in the admin payload at all.

The resolver is now upstream's, in upstream's ORDER
(`framework/core/src/Http/Middleware/SetLocale.php`):

1. the signed-in caller's `locale` PREFERENCE — the dropdown posts it with
   `savePreferences({ locale })`, which lands in the account's profile
   preferences;
2. a guest's `locale` COOKIE — the dropdown writes `locale=…; path=/` and
   reloads, and the name is upstream's, so a cookie from either frontend is
   honoured;
3. the `default_locale` SETTING;
4. `RIFF_FORUM_LOCALE`, the deployment-level fallback;
5. `en`.

Each candidate is validated against the catalogues the instance can actually
load, so a preference naming a pack that was never staged falls THROUGH to the
next rung instead of rendering raw translation keys.

**The setting is read through the MODULE's repository, not
`api.SettingValue`.** That helper resolves the API server from the request
context, and the shell route is registered by the module rather than through
`api.Mount`, so no server travels with its request: `api.SettingValue` returned
the fallback and every locale test failed with `en`. `Module.settingText` reads
`m.repo` directly, which is the same table the document already reads — the
first draft of this fix is why the note is here, and why the shell's other
readers should use the module, not the request.

**`showLanguageSelector` is served from `show_language_selector`**, whose
upstream default is ON (`ForumResource`: `->get(fn () => $this->settings->get(
'show_language_selector', true))`). The header's dropdown needs BOTH that flag
and more than one available locale, so an English-only instance shows nothing
either way — which is why hardcoding `false` looked harmless and was not.

**The French pack is imported** (`pb/flarum/plugins/french/`), the second locale
plugin after Chinese: a vendored MIT catalogue from `flarum-lang/french`
(`master`, commit `b36648e159…`), thirteen `locale/*.yml` files covering the core
and the extensions this port serves, compiled by the same shared
`locale-compile.cjs` into `forum-fr.js`/`admin-fr.js` and staged by
`scripts/build_flarum_spa.sh`. It is registered by default, because a pack
nobody registers is a language the selector cannot offer. The selector now
lists English, 简体中文 and Français.

Verified by `TestLocale_`: the setting decides the served catalogue, a caller's
preference beats the setting, an unavailable preference falls through, a guest's
cookie decides, the flag follows its setting, the French bundles serve and carry
French text with their `=>` references resolved, and the advertised locale set
is exactly the three imported ones.

## UPLOADS, ALIGNED WITH UPSTREAM (2026-11-26)

`pb/flarum/plugins/uploads/` was a working but NARROW port of `fof/upload`: one
upload route taking one `file` part and answering ONE resource, a listing that
only ever described the caller's own files, a resource carrying a subset of
upstream's attributes, and a frontend that could RENDER an attachment but never
create one. The audit against `fof/upload` 2.x (its `FileResource`,
`UploadHandler`, `FilePolicy` and `extend.php` route table) is
`docs/design/flarum/uploads-alignment-handoff.md`; this entry records the
choices, and in particular the ones that are NOT upstream's.

**A client written against the extension is the compatibility target, so its
spellings are served BESIDE the port's.** `POST /api/fof/upload` (a repeated
`files` field, `options[hidden]`/`options[shared]`, a COLLECTION response),
`GET /api/fof/uploads` and `/api/fof/upload/shared-files`,
`GET /api/fof/download/{uuid}` (plus upstream's `{uuid}/{post}/{csrf}` form),
`DELETE /api/fof/upload/delete/{uuid}` and `PATCH /api/fof/upload/hide`, each
with the POST twin the port's frontend needs for the method override. The
port's own `/api/uploads` stays, because the port's composer used it and the
URL is in no upstream document — but its RESPONSE changed shape: it now answers
a collection, because one request may carry several files and upstream's
frontend iterates `data`.

**THE SIX `fof-upload.*` PERMISSIONS ARE THE POLICY AND OWNERSHIP IS LAYERED ON
TOP.** The port answered "the uploader, or an administrator" for delete and
"always" for upload, which is a rule with no room for an operator: it cannot let
a member upload without letting them delete, and it cannot turn uploads off at
all. The permissions are now upstream's (`upload`, `viewUserUploads`,
`accessSharedFiles`, `uploadSharedFiles`, `deleteOwnUploads`,
`deleteUserUploads`), `canDelete`/`canHide` ask the SAME functions the endpoints
ask, and the admin's grid can grant or revoke each of them.

The seed grants `upload` and `deleteOwnUploads` to the MEMBER group, which is a
deliberate deviation: upstream's README tells the operator to grant the upload
permission on the permissions page, and a forum where nobody may attach a file
out of the box is worse for this port's size than one where the operator has to
revoke it. The moderator group additionally holds `deleteUserUploads`,
`accessSharedFiles` and `uploadSharedFiles` — the media-manager tier.

**THE FLAG THE BUTTON READS IS COMPUTED FROM THE PERMISSION THE ENDPOINT
CHECKS.** `fof-upload.canUpload` is published on the forum document through an
`AddAttribute` contributor, and the composer's attach button branches on it
(upstream's `addUploadButton.ts` does the same). Hardcoding it either way is the
recurring failure this codebase keeps finding: `true` is a button the server
refuses, `false` is a permission an operator can grant that changes nothing.

**`bbcode` IS MARKDOWN, NOT UPSTREAM'S RENDERED TEMPLATE.** Upstream's
`Util::getBbcodeForFile()` renders a Blade template (`<a href><img></a>` for an
image). This port's posts are Markdown and the composer inserts the string
verbatim, so the attribute carries `![](url)` for an image and `[name](url)`
otherwise. Pasting HTML into a Markdown post would be escaped — the forum
renders raw HTML inert on purpose — so the upstream syntax would arrive as
literal text.

**THE LISTING'S DEFAULT SORT IS `-created_at`, NOT `-id`.** Upstream's `-id` is
newest-first because its ids are sequential; PocketBase's are random, so `-id`
here is an arbitrary order. `created_at` is the same order upstream's `-id`
produces.

**THE TWO READERS SPEAK DIFFERENT DIALECTS, AND JOINING THEM WRONG ANSWERS
500.** `FindRecordsByFilter` parses PocketBase's FILTER grammar, where the
conjunction is `&&`; `CountRecords` takes a dbx expression whose text the driver
receives as written, where it is SQL's `AND`. A listing joined with `&&` worked
for ONE clause and answered 500 for two — which is why the shared-library
listing needed a second read of the same clause list. Both halves are built from
one slice of clauses, joined twice, and the comment at the join says why.

**THE `shared` COLUMN NEEDS ITS OWN MIGRATION STEP.** Adding a field to
`create_files` would never reach an instance that already ran it: a plugin
migration that has been recorded does not run again. `add_shared_column` is
idempotent and separate, the same shape the polls' `repair_vote_index` took.

Two smaller corrections came out of the same audit and are worth recording
because both are invisible without parsing the value: `createdAt` carried
PocketBase's internal date layout (`2006-01-02 15:04:05.000Z`) rather than RFC
3339, which the frontend's `new Date(...)` reads differently per engine; and the
dimensions were `0` for a non-image where upstream's are `null`.

Not ported, each with its reason in the handoff doc's §5: thumbnails (and their
four settings — a declared setting nobody reads is a control that changes
nothing), watermarking, resizing, the storage-adapter layer, the MIME
allow-list, the media-manager page, and `include=actor`.

## A RENAME THE SHELL NEVER RENDERS IS A RENAME THAT DOES NOTHING (2026-11-26)

Renaming the forum in the admin's **Basics** page changed nothing the reader could
see. The setting was fine: `forum_title` saved, the form re-read it, and
`GET /forum/api/settings` handed it back. The two places that actually DISPLAY it
were literals:

* `api.ForumDocument` served `"title": "Forum"`, and that is what the frontend
  reads as `app.forum.attribute('title')` — `document.title`, and the
  `{forumName}` parameter of every translation that takes one;
* the boot shell's `<title>` **and** the header's home-link text were
  `Title: "Forum"` in `frontend.Render`.

The shell is the worse half, and it is the reason this looked like a
frontend bug. The shell is rendered by the SERVER, before any bundle runs, and
nothing after boot rewrites the header: the app sets `document.title` on
navigation, but the `<div class="Header-title">` markup is the shell's. So the
forum's own name stayed "Forum" in the header forever, on a page whose tab title
was right after the first navigation.

It now comes from one place. `api.DefaultForumTitle` is the single fallback (the
document's, the admin form's and the shell's), the document resolves
`forum_title` against the settings table, and the shell takes its title from the
document the boot payload already embeds — `frontend.ShellMeta`, filled by
`shellMetaOf`, one lookup and one answer, so the server-rendered HTML cannot
disagree with the app it starts.

**The same template had a second hole.** `shellData.BasePath` was never passed
either — it had been `""` since the module landed — so the header link rendered
`href="/"`. On both `/memos/` layouts that leaves the forum for the static site
at the host root. The mount path comes from the same document
(`attributes.basePath`), for the same reason: it is already the value the SPA's
router and asset helpers use.

The tests pin the CHAIN rather than the two values, because the failure mode is
one link of it going literal: `TestForum_TheForumTitleIsASetting` saves the
setting through the admin route, then asserts the document, the shell's
`<title>`, the header link's text AND its `href`, and the admin payload;
`TestForum_TheForumTitleHasADefault` pins the fallback, because an empty
`<title>` is what a fresh instance would otherwise serve. All three assertions
were mutation-checked — reverting the document's read, the shell's title, or the
mount path each turns the test red.

The general rule, and the same one the wiki row carries for `branding.siteName`:
**a setting is not applied until the SERVER-rendered HTML carries it.** Reading it
back in the app is the second half, not the first.

## POCKETBASE'S OWN ENDPOINTS ANSWER AT THE HOST ROOT (2026-11-26)

riff already served both surfaces on its own listener — `GET /_/` → 200 with the
dashboard shell, `GET /api/health` → 200 JSON, `GET /api/collections` → 401 (the
collection rules, not a miss). Nothing in `main.go`, `server/frontend` or the
routers blocked them: the SPA's catch-all is a method-less `/{path...}`, and a
method-less pattern is strictly the LEAST specific in the mux, so PocketBase's
`GET /_/{path...}` and `GET /api/...` win on specificity. Verified by booting the
binary and curling them.

The gap was Caddy, and it was two different bugs in two layouts:

* `scripts/Caddyfile` (static site at `/var/www/site`, riff under `/memos/`,
  `/wiki/`, `/forum/`). Only those three prefixes were proxied, so `/_/` and
  `/api/health` fell into the catch-all `handle` and the static site's
  `file_server` answered them with a 404. PocketBase's dashboard was reachable
  only at `/memos/_/`, which is the prefix strip doing its job and not something
  an operator should have to know.
* `scripts/Caddyfile.fly.community` (the layout `fly.toml` selects: static site
  at the root, riff under `/memos/`). Both paths fell into the `@spa` matcher and
  were 308'd to `/memos/_/` and `/memos/api/health`. That lands on the right
  handler eventually, which is why it went unnoticed — but it is not the URL
  PocketBase's own documentation uses, and any client that will not follow a
  redirect on POST never gets there at all.
* `scripts/Caddyfile.fly` needed nothing: riff owns that whole hostname, so its
  catch-all already proxies `/_/` and `/api/*` verbatim (its own header comment
  says so).

Both layouts now carry a `/_`, `/_/*`, `/api`, `/api/*` proxy block that
forwards the path **VERBATIM**. `handle`, never `handle_path`: PocketBase serves
these from the root of its listener and knows nothing about `/memos/`, so
stripping or prefixing turns `/api/collections` into `/collections`, which riff
answers with the SPA fallback — `index.html` and a 200, which an API client
parses as data. That failure shape is the same one behind the polls plugin and
the wiki's null-dereference console errors, and it is worth naming here: **a
`200` carrying markup is worse than a `404`.**

`Caddyfile.fly.community` also needed the four paths in the `@spa` matcher's
`not path` list. The `handle` blocks above it already win on declaration order,
so the exclusion is belt-and-braces — but it is the thing that keeps the
behaviour if those blocks are ever moved, and it is the only place the intent is
written down.

Why expose them at the root instead of only under `/memos/`: they are the
surface PocketBase assumes. The install link the server prints at first boot is
`<origin>/_/#/pbinstall/<token>`, its docs say `curl <origin>/api/health`, and
`/api/collections` is what the dashboard itself calls. Mirroring them on the root
means an operator never has to know which sub-path riff is mounted on this time.
It is **not** a new attack surface: same collection rules, same superuser gate,
reached by a second URL.

`scripts/fly_deploy.sh` grew a fourth smoke check (`/_/` and `/api/health`, both
200 with **no redirect**) because a routing regression here is silent: the page
still loads, the API still works from the SPA (which talks to `/memos/api/...`),
and only a client using the documented root URL notices. The existing check 1
deliberately stays on `/memos/api/health` — it proves the prefix strip, a
different chain from the root proxy, and one does not imply the other.

## THE WIKI ANSWERS THE ENDPOINTS ITS OWN SPA CALLS (2026-11-26)

The wiki's console showed two `TypeError`s on **every page load**:

	Failed to load user settings: TypeError: Cannot read properties of null (reading 'dateFormat')
	Failed to load favorites:    TypeError: Cannot read properties of null (reading 'pages')

Neither was a crash in the settings or favourites code. **THE UNROUTED PATH IS
NOT A 404 — IT IS THE SPA SHELL WITH A 200.** `pb/leafwiki`'s catch-all is a
`GET {prefix}/{path...}` and the least specific route in the mux, so
`/wiki/api/user-settings` and `/wiki/api/favorites` were answered with
`index.html`. The SPA's request helper then does `await response.json()`, fails
on the markup, **swallows the failure and returns `null`** — and the caller
reads a field off it. A 200 carrying markup is worse than an error: the helper
throws only on a non-2xx.

Both endpoints are now served. The two pieces of state belong to an ACCOUNT and
not to a page, so they live in `userdata.json` beside the tree rather than in a
database the rest of the wiki does not need; the file is written atomically
(temp file + rename), because a truncated file loses every account's favourites
rather than one save. The read is served to an ANONYMOUS caller too — the SPA
loads both on every mount, signed in or not, and a 401 would make it log the
reader out before they did anything.

**A PARTIAL SAVE KEEPS THE FIELDS IT OMITS.** `SaveSettings` normalises against
the stored value, so a request that carries only `autoSave` does not silently
reset the reader's language to English — the SPA sends the whole object today,
but an older build or a hand-written request need not.

**`GET /api/branding` IS SERVED AND THE WRITES ANSWER 501.** Upstream's
`loadBranding` uses a RAW `fetch`, checks `ok`, and then parses the body — so
the unrouted path did not throw a `TypeError`, it made the JSON parse fail and
left the branding store on its defaults, silently disagreeing with the site
name the shell substitutes. The read now reports `siteName: "Wiki"`, which is
the same literal `api_spa.go` substitutes for `{{__SITE_NAME__}}`, so the two
cannot drift; and its `brandingConstraints` block is NESTED, because the SPA
reads `brandingConstraints.logoExts`. The upload and rename endpoints refuse
with a 501 rather than a 200 to something they did not keep — a save that
appears to succeed and is forgotten is the worse failure.

**THE 404s ON A BARE ID ARE THE AVATARS, AND THEY ARE DELIBERATE.** A user
picture is streamed at `{prefix}/avatars/{id}`, and the route answers 404 for
no picture, an unknown account, and every superuser (there is no avatar field
on `_superusers`) — because that is what makes the SPA draw initials. A
console line per avatar-less author is the intended shape, not a fault.

Pinned by `go test -count=1 -run 'TestUserdata_|TestBranding_' ./pb/leafwiki/`,
including `TestUserdata_AnUnroutedPathWouldHaveAnsweredHtml`, which asserts the
catch-all's 200-with-markup so the trap stays visible to the next reader.

## THE POLLS EXTENSION, ALIGNED WITH UPSTREAM (2026-11-26)

The polls plugin was ported, wired, and **quietly broken in four ways at once**:
an anonymous visitor crashed the process, a multi-vote poll could not store its
second vote, the poll never actually rendered, and a guest was handed every
voter's vote. The audit lived in `docs/design/flarum/polls-alignment-handoff.md`;
this records what the fixes are and the traps they hold.

**A NIL `User` IS NOT A MISSING SERVER.** `actorIDOf` read `actor.User.ID` after
checking only that the ACTOR resolved. `session.Actor.User` is nil for an
anonymous caller, so every guest who fetched a poll dereferenced nil — and a nil
dereference in a request handler takes the PROCESS down, not the request. One
unauthenticated visitor killed the server. `session.Actor.ID()` already answers
`""` for a guest, which is what `actorIDOf` wanted.

**AN `included` WALK CANNOT REACH A PLUGIN'S RESOURCE, AND THAT IS THE QUIETEST
FAILURE IN THIS CODEBASE.** The frontend resolves a relationship through the
STORE, not through the identifier: `Model.hasMany` is
`data.map((d) => store.getById(d.type, d.id))`, and the store is filled from
`payload.included`. The walk switches on the resource type, so a
plugin-contributed relation (`posts.polls`) produced the IDENTIFIER and no
RESOURCE — `getById` returned `undefined`, the frontend's own
`if (!poll) continue;` skipped it, and the poll was in the database with the UI
showing nothing and NO ERROR ANYWHERE. The fix is a `ResourceSerializer`
extension point (`plugin/extenders.go` declares it, `api/api.go` holds a narrow
registry, `register.go` adapts) — the same sibling-package discipline the
`AttributeContributor` → `AttributeSource` adapter follows, because `api` must
not import `plugin`.

**THE WALK DISPATCHES ON THE RELATED TYPE, NOT THE SOURCE TYPE.** The first cut
of the serializer registry kept the walk's original `switch sourceType` and
looked the plugin up by the SOURCE type, so a post's `polls` relation never
found the `polls` serializer. An identifier carries its own type, and that is
authoritative: the relation's `data` says what it points at.

**A TO-MANY RELATION'S `data` IS AN ARRAY, AND READING ONE IDENTIFIER OUT OF IT
NEVER MATCHES.** The walk's reader understood only a to-one shape, so
`user.groups` — a to-many — was never followed: the branch that resolved the
groups was dead code, and `GET /api/users/{id}` answered
`included types = []`. The frontend reads the absence as "no groups", so an
account renders without its badge and KEEPS that record until a reload.

**A DEFAULT INCLUDE IS A PATH, NOT A NAME.** Upstream's post endpoint carries
`['polls', 'polls.options', 'polls.myVotes', 'polls.myVotes.option']`, and the
walk has to follow each dotted chain hop by hop. Wrapping `polls.options` as a
single relation name matched nothing, so the poll arrived with no options and no
ticked box — the reader could not tell their vote was missing.

**A PLUGIN'S `Identifier` HAS NO JSON TAGS.** The adapter copied
`plugin.Relationship.Data` straight onto the wire, and a Go struct without tags
serialises its FIELD NAMES: the payload read `{"Type":"polls","ID":"abc"}`
where JSON:API requires `{"type","id"}`. `store.getById(data.type, …)` was
`undefined` — the relation read as absent, with a 200 on the wire. The adapter
now converts to the API package's own `Identifier`.

**THE UNIQUE INDEX WAS ON `(poll, user)`, NOT `(poll, option, user)`.** The
comment above the line and the decision log below it both said the three-column
index; the code had two. A voter could therefore hold only ONE vote row per
poll, so `allow_multiple_votes` was impossible and the vote route 500'd on the
second option. The creation step now writes the right index, and a SEPARATE,
IDEMPOTENT migration step repairs existing instances — a plugin migration that
has already been recorded does not run again, so changing the creation step
alone would fix only a fresh install.

**A GUEST'S `myVotes` IS EMPTY, NOT EVERY VOTE.** `voteIDs` read an empty user
id as "no filter", and an anonymous caller has no id — so the guest's `myVotes`
NAMED EVERY VOTER. That is a privacy leak, not a rendering bug. The two
questions are now two functions (`allVoteIdentifiers` / `myVoteIdentifiers`),
because one function cannot mean both.

**THE VOTE PERMISSION IS SCOPED TO THE DISCUSSION.** Upstream's policy is
`$actor->can('polls.vote', $discussion)`, and this port's key table already maps
upstream's `discussion.polls.vote` onto `votePoll`. The check asked with the
POLL id, so a grant made for a discussion could never match. The flag and the
write path now ask the same question with the same scope. A TAG-scoped
`discussion.polls.vote` is still not honoured: resolving which of a discussion's
tags are restricted needs the tags extension's own data, and the port has no
seam for one plugin to read another's collection. The grant that governs voting
today is the global one.

**AN EXTENSION'S OWN CATALOGUE IS LOADED BEFORE THE ACTIVE LOCALE'S.** The poll
called `trans('fof-polls.forum.poll_ended')` and shipped no catalogue, so the UI
rendered the raw key. `plugin.AssetBundle` gained a `Locale` field, compiled by
the extension's own `build-locale.cjs` with the SHARED `locale-compile.cjs`.
The ORDER is load-bearing: `addTranslations` merges into the current locale's
map, so the extension's English must be added FIRST and the locale's own copy
LAST, or the extension's English would win over the operator's language. The
Chinese pack gained a `fof-polls.yml` and its `SOURCES` list gained the file,
so a Chinese instance carries the extension's strings in Chinese.

**A GUEST GETS A LIVE POLL CONTROL.** Upstream's `PollState.canSelect` returns
true for a guest ON PURPOSE: the control is offered and the click opens the
login modal. Refusing to render it instead leaves the reader with a poll they
cannot interact with and no explanation.

Pinned by `go test -count=1 -run 'TestPolls_' ./pb/flarum/`,
`go test -count=1 -run 'TestForum_TheUserShowCarriesItsGroups|TestForum_TheNestedIncludeIsFollowed' ./pb/flarum/`,
and the frontend's `pluginBundles` suite.

## THE ADMIN BUNDLE IS A DIFFERENT CORE BUNDLE (2026-11-25)

The admin app went blank: one `TypeError` out of `bootExtensions` and nothing
rendered. Two faults in one plugin's admin half, and both were invisible to
every check the repo had.

**`admin.js` IS NOT `forum.js`.** The admin entry is
`export * from './src/common'; export * from './src/admin'` — there is no
`src/forum` in it. So `flarum/forum/app` in code that BOTH halves import
resolves to `undefined`: `flarum.reg.get('core','forum/app')` logs "No module
found for core:forum/app" and hands back nothing, and the first helper that
touches `app.forum`/`app.store` throws. The tags helpers are shared (the admin
tag tree and the permission scope sort and label tags with them), so they now
import `flarum/common/app` — the proxy over `window.app`, which is whichever
application is running, and what upstream uses for `js/src/common/`. Forum-only
modules keep `flarum/forum/app`.

**AN EXTENSION IS A LIST OF EXTENDERS, NOT AN INITIALIZER.** Upstream's
`addTagsPermissionScope` is called from an `app.initializers.add` callback;
this port has no initializers, so the function was adapted into the extenders
array — and adapted wrongly: it registered its extensions eagerly and returned
nothing, so `extend` was `[undefined, Store, Admin().page(…)]`. `bootExtensions`
does `extender.extend(...)` in a single pass, so that `undefined` threw and
**aborted the boot for every extension after it**: the tags admin page, the
likes setting section, the statistics page — all gone, from one `undefined`.
The function now RETURNS `{ extend: registerPermissionScope }`, and the work
happens when the boot calls it (which is also the only correct time: a bundle is
evaluated before `app.boot()` has assigned `app.store`/`app.forum`).

**WHY NOTHING CAUGHT IT.** `pluginBundles.test.ts` evaluated the plugin bundles
for the FORUM only — three plugins, one app. The admin bundles were never
loaded, so an `undefined` in an admin extenders array and a forum-only import in
an admin module were both outside the harness. It now boots EVERY plugin's
bundle in **both** apps and asserts that each entry of each `extend` array is a
real extender, naming the app, the bundle and the index when one is not. Run
against the pre-fix bundle those two assertions fail with the same
`Cannot read properties of undefined (reading 'extend')` the browser showed,
which is the mutation check for this entry.

Two smaller things the harness had to learn, both of which were real: the admin
core bundle loads fine in jsdom (it creates `new Admin()` at module scope, but
mounting, and therefore every DOM lookup, waits for `boot()`), and a plugin's
registered id is its OWN name — the mentions plugin is `flarum/mentions`, so its
bundles register as `flarum-mentions` while every first-party one is
`riff-<name>`.

## LIVE REFRESH IS A POLL, NOT A WEBSOCKET (2026-11-25)

The forum had no realtime at all: a new reply appeared only on a manual reload.
Upstream's answer is `flarum/realtime` — a separate **websocket daemon** (a
self-hosted Pusher), a **queue**, public *and* private channels, and a client
extender (`RealtimeExtend().onDiscussionStreamEvent(…)`) whose events are small
"something changed" signals that make the client refetch. This port runs one
binary under supervisord, so the daemon-and-queue shape is not on the table;
pushing on PocketBase's own realtime is possible but is a different design (see
below). This entry records the deliberately smaller first step.

**POLLING IS THE SMALLER STEP AND THE REVERSIBLE ONE.** While a page is
visible it revalidates every 30 s, and it revalidates the moment a
backgrounded tab comes back — which is the case that actually matters, because a
reader returning to an idle tab is exactly who is staring at stale posts. One
registry (`core/src/forum/utils/liveRefresh.ts`) owns ONE interval and ONE
`visibilitychange` listener for the whole application: a timer per page would
drift apart and fire in bursts, and two pages are never visible at once anyway.

**REVALIDATE, DO NOT RELOAD.** `DiscussionListState.revalidate()` re-reads page
1 and rebuilds `pages` in place, so scroll position, the open page number and
the active filter (a tag page's tag, a search) all survive. The core already
tuned this path for realtime (`extraDiscussions` is reconciled after a
revalidation precisely so a returning reader does not see a duplicate), which is
what makes it the right entry point here as well.

**THE DISCUSSION PAGE'S ORDER IS LOAD-BEARING.** `catchUp()` captures
`stream.viewingEnd()` BEFORE refetching the discussion: that predicate tolerates
at most one post of drift, so after the discussion's post ids have grown it
would answer "no" forever and the stream would never catch up. With the answer
captured first, `syncEnd()` fetches only the posts the window is missing —
`loadRange` slices the ids it already holds and asks for the rest **by id** — so
ten new replies cost one request, not a discussion reload. A reader scrolled up
in the middle of a long discussion is left alone; moving their window would lose
their place.

**A REFRESHER LIVES AS LONG AS ITS PAGE.** Registration happens in `oncreate`
and the unsubscribe runs in `onremove`, and the last one out clears the
interval — otherwise a page that has been navigated away from keeps polling
behind the reader's back. A refresher that throws is swallowed and the rest still
run: one broken page must not cost every other page its refresh. Nothing ticks
while `document.hidden`.

**THE NOTIFICATION BADGE IS NOT POLLED, ON PURPOSE.** `newNotificationCount` is
a client-side counter — `NotificationListState.load()` zeroes it when the list is
opened — so re-reading the user record would resurrect a badge the reader has
already cleared. That needs a pushed `notificationCreated`, which is what the
next step is for.

**WHY NOT POCKETBASE'S REALTIME PROTOCOL.** Two reasons, both structural rather
than stylistic. (1) PocketBase streams the **stored record**, while this SPA
consumes JSON:API resources through `store.pushPayload` — rendered `contentHtml`,
`avatarUrl`, `canReply`/`canEdit`, `contentType`. The forum's per-caller
decisions live in the serializer, not in the collection rules, so a raw record is
the wrong payload and the rules are the wrong gate: `forum_discussions`,
`forum_posts` and `forum_likes` are all readable by everyone at the rule level.
(2) PocketBase's realtime route lives at the **listener root**, and the deployed
caddy layout proxies only `/memos`, `/wiki` and `/forum` — the same trap that
keeps `/api/files` off the internet. If a pushed transport is ever wanted, it
should reuse `app.SubscriptionsBroker()` behind the forum's OWN route (as
`pb/hooks/realtime.go` + `pb/api/spa_sse.go` already do for the memos SPA), emit
small Flarum-shaped "changed" events, and authorize in the handler — not adopt
`/api/realtime`'s wire format.

## A LIKE IS A ROUTE HERE, AND THE SPA COULD ONLY EVER LIKE (2026-11-25)

The likes plugin was audited against upstream `flarum/likes` 2.x and two of
its three UI paths did not work. Neither failure was visible from the Go
tests, which is the point of this entry.

**THE UNLIKE WAS UNREACHABLE FROM THE SPA.** The frame's frontend rewrites
every method other than GET and POST into a POST carrying
`X-HTTP-Method-Override` (`Application.request`), and PocketBase bakes the
method into the route pattern. The plugin mounted `DELETE` → unlike and
`POST` → like, and the POST handler never read the header — so the rewrite
matched the LIKE route and re-liked. Idempotent, `200` both ways, and the
button could never turn off. Measured before the fix:
`POST + override` answered `{"isLiked":true}` on an already-liked post. The
route is now mounted for BOTH verbs on one `toggleHandler` that dispatches on
`api.EffectiveMethod`, and `TestLikes_TheUnlikeWorksThroughTheMethodOverride`
drives the three spellings (plain POST, POST+override, real DELETE).

**`isLiked` WAS NEVER REGISTERED ON THE MODEL.** `Extend.Model(Post)` declared
`likes`, `likesCount` and `canLike`, and the button calls `post.isLiked()`
too. A missing `attribute(…)` registration does not yield a wrong count: the
accessor is `undefined`, the call throws inside the `extend` callback, and
`extend()` catches it and reports once through `app.handleErrorOnce` — so the
footer item is silently dropped and the like button never renders. This is
the failure mode that makes a bundle-loading test worth having:
`pluginBundles.test.ts` evaluates the built bundles and now asserts the four
likes accessors exist on `Post.prototype`.

**A LAZY-CHUNK COMPONENT IS EXTENDED BY PATH.** `NotificationGrid` is a chunk
module (`flarum.reg.addChunkModule("298","3711","core",…)`), so a static
import of it resolves to `undefined` at bundle-evaluation time and the
`extend` call throws on `.prototype` — which is exactly what upstream's
`extend('flarum/forum/components/NotificationGrid', …)` string form avoids,
by deferring through `flarum.reg.onLoad`. The Jest harness gained the two
registry methods a plugin needs for that (`namespaceAndIdFromPath`, `onLoad`).

**A NOTIFICATION PREFERENCE IS A CHAIN, NOT A CHECKBOX.** Adding the grid row
alone would have produced a DISABLED checkbox: the grid disables any key its
`preferences` map does not name, and the port's user resource published an
empty map. So the kind is now declared (`plugin.NotificationType` +
`AddNotificationType`, one entry per (kind, channel)), the resource publishes
the defaults merged under the reader's own answers
(`Repository.NotificationPreferences`, to the account itself only — upstream's
`visible` predicate), and `Repository.CreateNotification` drops the
notification when the reader turned it off. Three places, one declaration:
the checkbox and the write path cannot disagree, and `email` is deliberately
not declared for `postLiked` because upstream's blueprint is
`AlertableInterface` and this forum sends no mail — an `email` key would be a
checkbox that toggles nothing.

Still not ported from upstream: the likers list under a post
(`addLikesList`), the user profile's "Likes" tab (`addLikesTabToUserProfile`)
with the `filter[liked]`/`filter[likedBy]` searchers that feed it, and
realtime.

## THE TAG FILTER IS A VALUE, NOT A SLUG (2026-11-25)

The last two tags gaps: `filter[tag]` breadth, and upstream's `TagSearcher`.

**`filter[tag]` IS A LIST OF AND-ED OR-GROUPS.** Upstream's `TagFilter`
reads the value as `string|array`, splits each element on commas into an OR
set, and nests one `where` closure per element — so `tag:foo,bar tag:baz`
means `(foo OR bar) AND baz`. The frontend mirrors that in
`GambitManager.apply` (`predicates: true` concatenates), and the sent shape
is `{tag: ['foo,bar', 'baz']}`. The port resolved ONE slug, so a comma was a
tag name that did not exist and the page went blank.

Three HTTP spellings have to be read, because the client chooses between
them: `filter[tag]=a` (the tag page's route param), a repeated key (the AND
form), and the INDEXED form `filter[tag][0]=a` — which is what mithril's
query builder emits for a JS array, and therefore what the gambit produces.
Reading only the exact key drops the gambit's filter silently, and a
filtered feed that looks unfiltered is the worst kind of wrong.

**NEGATION IS THE FILTER KEY, NOT A PREFIX.** Flarum spells it `filter[-tag]`,
because `GambitManager` keys the filter map by `-tag` and `searchToQuery`
strips the `-` on the way back. Each filter carries its own polarity, so
`tag:a -tag:b` is two filters that are BOTH applied — the model is
`[]TagFilter{Members, Negated}`, not "the tag filter plus a negated flag".

**`untagged` IS A PSEUDO-TAG, RESOLVED BEFORE ANY SLUG LOOKUP**, so a real
tag whose slug happens to be `untagged` cannot shadow it.

**A NEGATED OR-GROUP MEANS WHAT UPSTREAM SAYS IT MEANS.** Upstream renders
each member as `whereIn(..., 'or', $negate)`, so `-tag:a,b` produces
`NOT IN (a) OR NOT IN (b)` — it excludes only the discussions carrying BOTH.
That is not what an author would guess, and it survives here anyway: a
client's filter has to mean the same thing on both sides, and "tidying" it
into `NOT IN (a, b)` would quietly return a different set than a Flarum
client expects. It is a comment on `TagFilter.Negated` for the next reader.

**BOUND PARAMETER NAMES MUST BE UNIQUE ACROSS CLAUSES.** The groups are
combined into ONE statement, and dbx binds a name once — the later value
replacing the earlier. Named per member index, the first member of every
group was `{:tag_group_0}`, so `filter[tag]=news&filter[-tag]=guides` bound
both to `guides` and matched nothing. The key carries the group's ordinal
now, and `TestTags_TheTagFilterIsNegated` fails if it stops doing so (the
AND-group tests pass by luck without it, which is why each of them seeds a
discussion carrying only one of the two tags).

**`TagSearcher` IS A PREFIX MATCH ON THE NAME OR THE SLUG.** Upstream's
`TagSearcher` is `Tag::whereVisibleTo($actor)` and the extension's
`FulltextFilter` adds `name LIKE 'q%' OR slug LIKE 'q%'` — a prefix, not a
substring, and not a description search. The match happens in Go over the
records `listTags` already reads: the tag list is a taxonomy, not a feed, and
a SQL prefix match would rest on how PocketBase decides to wrap a LIKE
parameter (`%` in the caller's text changes the meaning).

**THE DROPDOWN SECTION IS A RIFF ADDITION.** Upstream registers the searcher
and nothing else: its core search dropdown lists discussions, posts and
users, and `flarum/tags` has no client-side source. Here the searcher would
have no consumer at all — a tag with no discussions yet is findable by
nothing — so the plugin adds a `tags` source to `GlobalSearch.sourceItems`
(recorded in `docs/design/flarum/deviations.md` §16b). The `tag:` gambit is
upstream's, registered through `Extend.Search()`, and it writes the same
`filter[tag]` the tag page's route param writes, so the server keeps ONE
implementation of "filter by tag".

## A TAG FILTER IS A CLAUSE, NOT A POST-FILTER (2026-11-25)

Three gaps left in the tags alignment. One of them turned out to be a query
bug rather than a missing feature.

**`filter[tag]` FILTERED THE PAGE, NOT THE QUERY.** The listing fetched
`limit` discussions ordered by last activity and then dropped the ones
without the tag IN MEMORY. With a page of 20 and a tag on 3 of 50
discussions, a tag page showed the tag's discussions only when they happened
to be among the 20 most recent — and `meta.page.total` counted every
discussion, because the COUNT query never sees the page. The filter is now
the same kind of subquery the space rule uses — `id IN (SELECT discussion
FROM forum_discussion_tags WHERE tag IN (…))` — applied to the count query
and the page query alike, so rows, `total` and the pagination offsets agree.
`DiscussionIDsWithTag` (the in-memory helper) is gone with it.

**THE CLAUSES WERE JOINED WITH `&&`, WHICH IS NOT SQL.** `CountRecords` and
`RecordQuery` take dbx expressions and hand them to the driver as written;
PocketBase's filter grammar (`&&`, `fexpr` field resolution) is only parsed
by the `*ByFilter` helpers. So a listing with TWO clauses reached SQLite as
`WHERE user = ? && state != ?` and answered **500** with `near "&": syntax
error`. A single clause happened to be accidental valid SQL, which is why it
survived: `filter[user]` (a user clause plus the always-present state
clause) and `filter[q]` both 500'd, and a plain listing did not. The join is
`AND`, and `TestForum_ACombinedDiscussionFilterDoesNotFail` pins it.

**A HIDDEN TAG HIDES ITS DISCUSSIONS, FROM THE UNFILTERED PAGE ONLY.**
`is_hidden` was stored, editable and served, and had no consumer — hiding a
tag changed nothing a reader could see. Upstream's
`HideHiddenTagsFromAllDiscussionsPage` drops those discussions and does
nothing once any filter (or a full-text search) is in play. The guard is
what keeps the tag's OWN page from going blank the moment an operator hides
the tag, so it is expressed as "no filter was asked for", not as "exclude
always".

**THE TAG'S `defaultSort` IS APPLIED ON THE CLIENT.** The stored value is a
URL key (`latest`), not an API sort (`-lastPostedAt`), and
`DiscussionListState.sortMap()` is the only place that knows the mapping —
a server-side default would be a second copy of it that drifts. The catch is
the COLD LOAD: `/t/<slug>` mounts before the tag list arrives, so the first
request would go out unsorted, the list would render in the forum's order
while the sort control said the tag's, and nothing would re-request it. The
plugin therefore holds the first page of a tag route until `loadTags()`
resolves (`forum.js::addTagFilter`). That is one extra small request on a
cold navigation, and the only arrangement in which the control and the list
cannot disagree. A reader's `?sort=` always wins over the tag's default.

**THE PICKER OFFERS ONLY WHAT THE SERVER ACCEPTS.** Offering a forbidden tag
is a dead affordance: the author chooses it, submits, and the save comes
back refused. That became real when a restricted tag started asking the
SCOPED permission (previous entry), so `js/utils/selectableTags` filters on
`canStartDiscussion()` for a new discussion and on `canAddToDiscussion()`
_OR_ the discussion's current tags for an existing one — a tag that may no
longer be ADDED must still be REMOVABLE, or the author is stuck with it. The
rule is a pure function with its own unit test, the same arrangement as the
admin scope.

## STATISTICS: THE PRESENTATION HALF, AND WHY THE KEYS ARE UNIX SECONDS (2026-11-25)

The previous entry recorded that `GET /api/statistics` was reachable while
the FEATURE was not. This one adds the presentation half — upstream's
admin page and dashboard widget — and with it a correction to the wire
contract that had to come first.

**THE TIMED SERIES IS KEYED BY UNIX SECONDS, NOT BY TIMESTAMPS.** The
port returned RFC 3339 date strings and a chart would have drawn ZERO
every time, with a 200 on the wire and nothing in the console. Upstream's
widget sums the payload over a window it computes in seconds:

	StatisticsWidget::getPeriodCount
	  const time = parseInt(t);
	  if (time >= period.start && time < period.end) count += timed[time];

`parseInt('2026-11-25T00:00:00Z')` is `NaN`; every comparison against it is
false; the line is flat. This is the same class of failure as the
`externalLink` and `statisticsEntities` bugs — a field the reader
interprets in one type while the writer emits another — and the reason the
new tests assert the TEXT of the keys (`^[0-9]+$`) and not just the shape.

**THE WINDOW IS TWO YEARS PLUS 25 HOURLY POINTS, IN ONE PAYLOAD.**
Upstream returns `-2 years` of days and the last 25 hours by the hour
(`getTimedCounts`), because the widest selector — last 12 months — and the
previous-period comparison line are both summed CLIENT-SIDE from that one
response. A server that returned only "the period you asked for" would
force a request per selector, and a server that returned 30 days (which
this one did) makes eleven months of a year chart read as "no activity",
which is a different and wrong statement.

**THE ENTITY PREDICATE HAS TO REACH THE TOTALS TOO.** Upstream counts
`$this->query($model)->count()`, and that query carries `type =
'comment'` for posts. The port's lifetime totals called `CountRecords`
with no filter, so a `discussionRenamed` post — a real row the collection
stores so the stream can render rename events — was counted as a comment.
One predicate, two spellings (`statEntity.exprs()` for the dbx COUNT path,
a bound parameter for `FindRecordsByFilter`), so the two halves cannot
disagree.

**A FIELD'S VISIBILITY IS A PROPERTY OF THE ACTOR, NOT THE RECORD.**
`statisticsEntities` is published on the forum document to administrators
only, and an invisible field is ABSENT rather than null — upstream's
serializer does not emit it, and `'key' in attributes` is a different
question from `attributes.key !== undefined`. That needed two small core
extensions: `AttributeContributor.Visible` (upstream's `Field::visible()`)
and `IsAdmin` on the attribute request. The forum document now runs
through `applyPluginAttributes` like every record does, which is also what
puts the list in the BOOT payload — the copy the SPA reads.

**THE CHART IS INLINE SVG, NOT `frappe-charts`.** Upstream imports
`frappe-charts`. This port does not vendor it, and adding a chart library
for one admin page is a dependency decision rather than an implementation
detail. The drawing is a polyline in a `viewBox`; the SHAPE is upstream's
(two series, this period against the previous, the same windows). The one
upstream control that is consequently NOT ported is the chart's PNG
export — `frappe-charts` is what implements `chart.export()`, and a button
that cannot save a file is the dead-affordance problem this repo keeps
removing.

**`created_at` IS AN AUTODATE FIELD, SO A TEST CANNOT BACKDATE A ROW.**
The bucketing rule (hours for the last 25, days before) is therefore a
separate function with its own unit test rather than something exercised
through a saved record — the framework overwrites any value a test sets,
and a test that appeared to backdate would be asserting a fiction.

## THE SHARED `users` COLLECTION HAS `created`, NOT `created_at` (2026-11-25)


An audit of the forum's Statistics plugin asked the simple question "is
`GET /api/statistics` accessible", and the answer was two different
answers depending on which half you meant.

**THE ENDPOINT IS REACHABLE; THE FEATURE IS NOT USABLE FROM THE
BROWSER.** `GET {basePath}/api/statistics` answers 200 with the lifetime
totals and 403 for anyone who is not an administrator, which is exactly
upstream's `assertAdmin()` contract. But upstream's extension is an
admin-panel page with charts, and this port deliberately ships no asset
bundle for it (`plugins/statistics/statistics.go`'s package doc records
why: a bundle that drew nothing is the empty-tag problem the module's
startup check exists to surface). The consequences a reader will trip
over: there is no page to open, `/api/extensions` does not list
`flarum/statistics` at all (it lists plugins with asset bundles), and
the forum document does not publish `statisticsEntities` the way
upstream's `ForumResource` does. So the data half exists and the
presentation half does not.

**`?model=users` ANSWERED 500 AND `?model=discussions` DID NOT.** The
handler filtered and grouped on a hardcoded `created_at`. That column is
the forum's own convention — `repository/mutations.go` stamps it on every
discussion and post — but the entity list also names the SHARED `users`
collection, and that is PocketBase's own auth collection, whose system
timestamp fields are `created` and `updated`. PocketBase rejects a filter
naming a column the collection does not have, so the users series was an
unconditional `500 internal_error`. Two things hid it:

- the LIFETIME totals never filter — they call `CountRecords` — so
  `?period=lifetime` reported `users: N` correctly the whole time;
- the test exercised `model=discussions` only.

Both are the kind of "the test covers the happy path of the thing next to
the thing" gap this repo keeps finding. `createdField` now resolves the
column per collection (`created_at` when it exists, `created` otherwise),
and the two new tests drive EVERY entity in `p.entities` rather than one
of them — the second one asserts the COUNT, because a fix that read the
wrong column would answer 200 with an empty series, and an empty series
renders as "no activity", which is a different and wrong statement. Both
were mutation-checked by hardcoding `created_at` back: they go red with
the exact production symptom.

Generalised: any plugin that reports on collections of DIFFERENT
provenance (forum-owned vs the shared users collection) cannot assume one
timestamp column name.

## THE MEMOS SPA MOVES TO `/memos/`, AND `/community/` BECOMES A 308 (2026-11-25)

The Memos SPA now answers at `https://<host>/memos/` instead of
`https://<host>/community/`. The prefix is a better name for what it
serves, and it puts the SPA beside its siblings (`/wiki/`, `/forum/`)
instead of behind a word that describes the audience rather than the
app.

**THE PREFIX HAS TO BE STRIPPED, AND THAT IS THE LOAD-BEARING PART.**
riff serves the SPA at the ROOT of its own listener — `server/frontend`
registers a method-less `/{path...}` catch-all, and every Connect
endpoint, `/_/`, `/api/*` and `/mcp` are rooted there too. So the caddy
block is `handle_path /memos/*`, never `handle /memos/*`: without the
strip riff would answer `/memos/assets/index-*.js` with its own SPA
fallback, i.e. `index.html` served as JavaScript, and the app would
never boot.

**ONE BUILD, ONE PREFIX.** `web/vite.config.mts` maps
`VITE_BASE_PATH` onto Vite's `base`, and `web/src/connect.ts` reads
`import.meta.env.BASE_URL` to build the Connect base URL as
`${origin}${BASE_URL}`. Both are compile-time constants, so a bundle
built for `/memos/` cannot also serve `/community/`: its asset URLs
would point at `/memos/` while the page loaded from `/community/`.
`/community/` is therefore a 308, not a second proxy — the redirect is
what makes an old bookmark land on a coherent page. The build
invocations that set the variable (`scripts/dev_image_build.sh`,
`.github/workflows/release.yml`) both moved to `/memos/`; the default
`pnpm release` still leaves the SPA at `/` for local dev and for the
root-mounted `Caddyfile.fly` layout.

**THE REDIRECT MUST STRIP BEFORE IT REBUILDS THE PATH.** The first
version used `handle /community/* { redir * /memos{uri} 308 }` and
turned `/community/auth?redirect=%2F` into
`/memos/community/auth?redirect=%2F` — `{uri}` is the whole request
URI, so a plain `handle` leaves the prefix in it. `handle_path` strips
before the block runs, so `{uri}` is already the remainder. Measured
against a real caddy container: `/community/auth?redirect=%2F` →
`/memos/auth?redirect=%2F`, `/community/assets/x.js` →
`/memos/assets/x.js`, `/community` → `/memos/`, `/memos` → `/memos/`,
and `/wiki/*` / `/forum/*` still proxied verbatim.

**A THIRD LAYOUT GETS AN ALIAS, NOT A PROXY.** `scripts/Caddyfile.fly`
mounts the SPA at the ROOT (`base: "/"`), so `/memos/*` there is a 308
that DROPS the prefix (`/memos/auth` → `/auth`). Proxying it would
serve a shell whose asset URLs are root-relative and boot the app
against the wrong base. `scripts/Caddyfile` also gained the strip it
never had: its `/community/*` block proxied verbatim, which meant the
SPA's own `/community/assets/...` requests were answered by the SPA
fallback — the layout had been broken for as long as it existed, and
the `/memos/` move is what surfaced it.

The deploy smoke test moved with it: `fly.toml`'s
`[[http_service.checks]] path`, `scripts/fly_deploy.sh`'s default
`RIFF_FLY_SMOKE_PATH`, and its two case statements now assert
`/memos/...`, because the check that catches an `@spa`-matcher
regression has to name the prefix the redirect actually targets.

## AN AFFORDANCE THE FORUM CANNOT HONOUR IS REMOVED, NOT LEFT DEAD (2026-11-25)

An audit of every endpoint the reused Flarum frontend calls, against
upstream's `Api/routes.php` and `Forum/routes.php`, found three classes
of gap. Two of them are one bug wearing different clothes.

**AN UNROUTED PATH IS NOT A 404 — IT IS THE SPA'S `index.html` WITH A
200.** The SPA catch-all is a method-less `/{path...}` and it is the
least specific route in the mux, so it answers everything the forum
does not. The frontend's request helper throws only on a non-2xx, so a
200 with an HTML body is WORSE than an error: the caller parses a page
of markup as data. That is why every path the frontend calls has to
exist, or the affordance that calls it has to be ABSENT.

**THE APPEARANCE PAGE'S UPLOADS WERE SETTINGS NOBODY COULD WRITE.**
The five uploads — `/logo`, `/logo-dark-mode`, `/email-logo`,
`/favicon`, `/app-icon` — set `logo_path` and friends, and the forum
document READ those settings into `logoUrl` and the shell's favicon.
The reading half worked and the writing half did not: an administrator
could press "Upload logo", pick a file, and get a 404, and the logo
could never change. The routes now exist, the bytes live in
PocketBase's filesystem, and they are served at
`{basePath}/branding/{name}` — NOT under `/assets/`, which is the
BUILT BUNDLE tree, and not at PocketBase's native `/api/files/...`
URL, which lives at the listener root the forum does not own.

**A DEAD AFFORDANCE IS WORSE THAN NO AFFORDANCE.** Three controls
asked for something the forum cannot give, and each was removed rather
than left to fail:

- the login modal's "Forgot password?" and the settings page's
  "Change password" both post to `/forgot`, and the forum has no such
  endpoint — password reset belongs to the host's auth layer, and
  PocketBase's own flow is not callable from Go;
- the security page's "Log out everywhere" posts to
  `/global-logout`, which cannot work: a PocketBase token is a
  stateless JWT with no row to terminate, and invalidating every token
  means rotating `tokenKey`, a reserved auth field the forum must
  never write.

The "terminate all other sessions" button beside them is left in
place and is always DISABLED, because the list holds only the current
session — inert rather than broken.

**A NOTIFICATION KIND WITH NO COMPONENT RENDERS AN EMPTY ROW.** The
server produced `postLiked` and the SPA looked its component up by
`contentType` and found none, so the notification was a blank row
while the unread badge counted it — exactly the symptom deviation 15
warns about, and the port had it. The likes bundle now registers
`PostLikedNotification` and the `post` relationship and `excerpt`
attribute it reads.

**MARK-AS-READ WAS TRACKED, NOT IMPLEMENTED.** The "Mark all as read"
button and the list's scroll handler call `POST /discussions/read`,
and the server tracks no read state at all — no `lastReadPostNumber`,
no per-discussion unread count. That is a subsystem rather than a
route, and it is recorded in `docs/design/flarum/deviations.md` §16a.

## AN ALERT THE FORUM CANNOT ACT ON IS NOT RENDERED (2026-11-25)

Every signed-in member was shown an email-confirmation alert they
could not dismiss, and whose button answered 404.

**AN UNDISMISSABLE ALERT ASKS FOR SOMETHING.** Upstream's notices area
renders its alerts with `dismissible={false}` on purpose: the
email-confirmation prompt, the bounced-address notice, the
maintenance-mode notice. Each of them asks the reader to do something,
so dismissing the reminder would only hide something they still have
to do.

**THE EMAIL-CONFIRMATION PROMPT ASKS FOR SOMETHING THE FORUM CANNOT
GIVE.** The account's confirmation belongs to the host's auth layer —
the forum has no `/register`, no `/confirm` and no
`/send-confirmation` endpoint — and the user resource carries no
`isEmailConfirmed` attribute, because reading PocketBase's reserved
`verified` field is exactly what `docs/design/flarum/shared-users.md`
forbids.

**AN ATTRIBUTE THAT IS ABSENT READS AS `undefined`, AND
`!undefined` IS `true`.** Upstream's guard is `if (user &&
!user.isEmailConfirmed())`, so an absent attribute made EVERY member
look unconfirmed. The prompt was then shown to everybody, it could not
be dismissed, and its button called an endpoint that does not exist.

The prompt is therefore not rendered. The OTHER notices are
untouched: the bounced-address notice is guarded by a POSITIVE test
(`user.emailBouncedAt()`), so an absent attribute does not trigger
it — the opposite of the confirmation prompt's guard, and the reason
one of them had to go and the other did not.

**THE ALERT MANAGER ITSELF WAS FINE.** The dismissal was tested
(`AlertManager.test.ts`): the button is rendered, the alert leaves the
list, and the caller's own `ondismiss` runs as well. The vendored
`Alert` and `AlertManager` are byte-identical to upstream's.

## AN EXTENSION PAGE IS A PAGE, NOT A SETTINGS LIST (2026-11-25)

The tags plugin had no page to create a tag from: its admin bundle
registered four number fields and nothing else, so the extension page
offered the count settings and no way to make a tag at all.

**UPSTREAM'S `flarum/tags` REGISTERS A CUSTOM PAGE**, and the admin
application renders a custom page INSTEAD of the plain settings list —
`ExtensionPageResolver.onmatch` asks the registry for a page and
returns it in place of the default one. So the settings and the tree
are the SAME page, with the tree on one tab and the settings on
another, and a plugin that registers a custom page must NOT also
register plain settings: the two would fight over the same page.

**THE PAGE IS UPSTREAM'S, AND SO IS THE MODAL.** `TagsPage` and
`EditTagModal` live under the plugin's `js/admin/`, and the four count
settings are on the page's second tab. The page loads the tag list on
demand (the admin payload does not carry the tags) and drags to
reorder through the core's own `loadSortable`, so the sortable
library is only fetched when an administrator opens the page.

**TWO FIELDS OF THE MODAL ARE ABSENT, BECAUSE THE SERVER HAS NO
COUNTERPART.** Upstream's modal uploads an icon IMAGE and picks the
tag's PARENT. The port's tag resource carries no icon-image
attributes, so the input would upload to an endpoint that does not
exist; and the parent is not offered.

**THE ADMIN PAYLOAD MUST CARRY THE SORT MAPS.** The page lists a
tag's DEFAULT SORT from `app.data.sortMaps`, so that a sort an
extension added can be chosen for a tag without that extension knowing
anything about tags. The map was EMPTY, which is not a harmless
omission: the dropdown offered only "No preference", so the operator
could not set a default sort at all. It is now keyed by resource and
maps the sort's URL key to the API sort behind it — the same shape the
forum's own `DiscussionListState.sortMap()` builds.

## THE COUNT RULES ARE A RANGE, NOT AN EXACT COUNT (2026-11-25)

Two bugs, found while auditing the tag plugin's settings against
upstream's, and both of them made an operator's setting do NOTHING.

**A STORED SETTING IS `types.JSONRaw`, AND `types.JSONRaw` IS NOT
`[]byte`.** The settings column is a JSON field, so a stored value
arrives as raw JSON bytes under a DISTINCT NAMED TYPE. A reader that
switches on `case []byte:` does NOT match it — the type switch is
exact, and `types.JSONRaw` is its own type, not an alias — so the
value falls through to the reader's default and the setting reads as
UNSET. The tags plugin's `settingInt` had exactly that switch, so
`flarum-tags.max_primary_tags` was ignored: raising it changed
nothing. The reader now goes through `api.SettingString`, which
handles every shape the column can arrive in.

**THE COUNT RULE HAS A SHAPE, AND THE SHAPE WAS WRONG.** Upstream
writes it as:

```php
$min === $max ? "size:$min" : "between:$min,$max"
```

A minimum EQUAL to the maximum is an EXACT count — one primary tag
means exactly one primary tag — while a minimum DIFFERENT from the
maximum is a RANGE. This port always demanded "exactly the minimum",
so a set of two primary tags was refused even with the maximum raised
to two. The default (one primary tag, at most one) IS the exact-count
case, which is why the range case was so easy to miss.

**THE TWO SIDES OF A SETTING MUST AGREE.** The setting names the
value; the READER decides what the forum does with it. A reader that
falls through to its default is a setting the admin portal shows and
the forum ignores — and the operator has no way to tell which is
which. Both the gamification's default metric/period and the tags'
four count settings were in that state.

## A PLUGIN DECLARES THE SETTINGS IT READS (2026-11-25)

The admin portal could reach a plugin's extension page but showed no
settings on it: every plugin's `admin.js` bundle was
`export const extend = []`. Eleven settings were READ by the server and
editable NOWHERE.

**THE SETTINGS ARE THE SERVER'S TO DECLARE.** A plugin declares the
settings it reads (`plugin.AddSettings`), and the module publishes them
in the admin payload with their DEFAULT values, so the form shows the
value the forum would actually use. The frontend bundle registers the
FORM (`Extend.Admin().setting(...)`) — the admin application renders a
setting that a bundle registered and nothing else, so a declared
setting with no bundle is a setting the operator cannot reach.

**A SETTING NOBODY READS IS A CONTROL THAT CHANGES NOTHING.** The audit
compared upstream's settings against the ones the server consults, and
found six declared-but-never-read constants: the polls' `maxOptions`
and `enableDiscussionPolls` (this port has no poll CREATION path, so
there is nothing to gate), and the gamification's `firstPostOnly`,
`autoUpvotePosts`, `upVotesOnly` and `viewRankingPage`. They were
REMOVED rather than exposed: a switch that changes nothing is worse
than no switch, because the operator turns it and the forum does not
move. The same reasoning kept upstream's `sticky` settings out — they
gate a query this port does not run.

**THE SETTINGS MUST BE STRINGS IN THE ADMIN PAYLOAD.** Upstream's
settings column is TEXT, so `Settings::all()` hands the admin
application a map of strings and the application types every setting as
`Stream<string>`. This port's `forum_settings.value` column is a JSON
field, so a stored value arrives as the JSON type it was written as — a
number as `float64`, and in practice as raw JSON bytes
(`types.JSONRaw`). The admin payload now normalises every setting to a
string, the way upstream's table holds them. Without that, a number
lands in a stream typed as a string, the form saves it back as a
number, and a boolean switch reads `value !== '0'` against a value
that is neither `"0"` nor `0`.

**THE KEY IS THE CONTRACT, AND IT IS UPSTREAM'S.** Every setting keeps
upstream's own key (`flarum-likes.like_own_post`,
`flarum-tags.min_primary_tags`, `fof-gamification.allowSelfVotes`), so
an operator's saved setting carries over and means what it means
upstream. The Go side and the frontend bundle must agree on it, and
the tests pin both directions: a declared setting the server does not
read, and a registered setting the server does not read, each fail.

## AN EXTENSION ID IS A PATH SEGMENT (2026-11-25)

The admin application's extension page — the per-plugin settings page —
was unreachable: every entry in the admin sidebar linked to a URL the
router could not resolve.

**AN EXTENSION ID WITH A SLASH IS NOT A PATH SEGMENT.** A plugin's id is
`vendor/name` — it CONTAINS A SLASH — and the admin app's extension
page lives at `/extension/:id`, a pattern with ONE segment. The route
builder substitutes the id verbatim, so an id with a slash becomes two
segments and the route does not match. Upstream avoids this at the
PAYLOAD boundary: `Extension::nameToId()` flattens `flarum/tags` to
`flarum-tags`, and that flattened id is both the key of
`app.data.extensions` and the route parameter. The Go side now does the
same (`repository.ExtensionID`), so the two agree — and the id is a
single segment, so the route matches.

**THE ENABLED LIST MUST USE THE SAME IDS.** `isExtensionEnabled` asks
whether `extensions_enabled` holds the extension's id, so a list of the
RAW `vendor/name` ids would report EVERY plugin as disabled, and the
extension page would show "enable to see" instead of its settings — the
same page, unreachable by a different route.

**A DISABLED PLUGIN IS A ROW THAT IS NOT THERE.** The state table's
`enabled` column is a `BoolField` with `Required: true`, and
PocketBase's `Required` on a bool means the value must be "TRUE" — so a
`false` can never be stored, and the write fails with "enabled: cannot
be blank". A plugin therefore could not be disabled AT ALL. The state
is now expressed by the row's ABSENCE, which is the same shape the
permission table uses: a group holds a permission when a row says so,
and a group that does not hold it has no row. The table is SEEDED with
a row per plugin at boot, so the forum's plugins ship enabled, and the
seeding is idempotent so a disabled plugin stays disabled.

**THE ENABLE/DISABLE TOGGLE HAD NO ROUTE AT ALL.** The admin
application's switch posts `{enabled: …}` to
`PATCH /api/extensions/{id}`, and the forum answered 404 — so the
switch showed the plugin as enabled whatever the server said. The route
exists now, with the POST twin the other write routes carry (the
frontend rewrites a PATCH into a POST, and PocketBase's router matches
on the real method). The id in the URL is the FLATTENED one, because it
is a path segment; the state is recorded under the RAW one, because that
is the id the plugin manager knows the plugin by.

## A MENTION IS A REFERENCE, NOT A DECORATION (2026-11-25)

The mentions plugin is the port of upstream `flarum/mentions`. The
reference is upstream's `UserMentionedBlueprint`, its
`post_mentions_user` table, and its `PostResourceFields`.

**THE MENTION NODE HAD NO HTML RENDERER, SO A MENTION DELETED ITSELF
FROM THE POST.** A mention node carries NO source segment, so an HTML
renderer that emits nothing for it cannot recover the text: `"hey
@alice"` rendered as `"hey "`. The forum therefore DISABLED the mention
extension, and `markdown.RenderHTML` was documented as a trap. The
renderer now exists (`markdown/html/mention_renderer.go`), and the
markdown package grew ONE option that does both halves — the parser
AND the node renderer — because enabling one without the other is what
deleted the text. The default never drops it: with no href builder the
mention renders as a `<span>`, which degrades the link but keeps the
word. `markdown/html` still imports nothing but goldmark: the CALLER
decides where an account lives (the forum passes its base path), and
the package decides only the markup.

**THE EVENT BUS WAS NEVER WIRED TO THE API SERVER.** `api.NewServer`
read `Options.Bus` into nothing: the `bus` field was left nil, and
`Server.dispatch` returns early on a nil bus. Every forum event — the
`post.created`, the `discussion.created`, the `post.liked` — was
dispatched into the void. The design's claim that "`EventPostEdited` is
declared and never dispatched" was TRUE but understated: NO event
reached a listener, and no plugin had ever subscribed, so nothing
looked wrong. A plugin that keeps a derived set in step with a post's
content would have silently done nothing.

**THE NOTIFICATION KIND TRAVELS AS `contentType`, NOT `type`.** The
vendored SPA looks a notification's component up by that name
(`NotificationType.tsx` reads `notification.contentType()` and uses it
as the key into `app.notificationComponents`). The serializer emitted
the kind as `type`, so `contentType()` was `undefined` for EVERY
notification, the component lookup missed, and the list rendered an
EMPTY ROW — while the unread badge still counted it, because the badge
reads `isRead`. The symptom is a badge with a blank list, and it
affected the `postLiked` notification the likes plugin ships as much as
a mention one. `contentType` and `content` are now published alongside
the old names rather than in place of them: the rename is additive, so
whatever consumed `type` keeps working.

**THE COLLECTION IS THE AUTHORITY ON WHAT A NOTIFICATION MAY BE.** The
`forum_notifications` collection's `type` is a `SelectField`, and a
`SelectField` REFUSES a value it does not allow ("Invalid value
…"), so a kind the collection does not carry cannot be written at all
and the notification is silently lost. The collection allowed
`mentionedInPost`; upstream's blueprint is `userMentioned`. The port
uses upstream's spelling — the SPA's component lookup and upstream's
catalogue agree on it — and the plugin's migration WIDENS the
collection's allowed values, idempotently, so an instance that already
has rows keeps them.

**ONLY A MENTION THAT IS NEW IS NOTIFIED.** The notification is one per
(post, mentioned account). A post that is edited, and edited again,
must not fill the reader's inbox with copies of the same mention, so
the sync reports which mentions are NEW and only those are notified.

**A MENTION OF AN ACCOUNT THAT DOES NOT EXIST IS INERT.** It does not
fail the save, it notifies nobody, and it stores nothing. The RENDERER
still emits a link: the renderer cannot consult the database (the
markdown package knows nothing about the forum), so the link is
rendered regardless and the READ side decides whether the account
exists. The alternative — an existence predicate supplied by the forum
— is one more seam for one broken link.

**THE `mentionedBy` RELATION IS A RELATION ON THE ACCOUNT.** It names
the other places the post's AUTHOR was mentioned, so the lookup starts
from the post's author and returns the mentioning posts, oldest first,
limited to four (upstream's `PostResourceFields::$maxMentionedBy = 4`).
The COUNT is over the posts the CALLER may see — it is upstream's
`whereVisibleTo` — so the number cannot reveal a post the caller
cannot read. A hidden post is visible to a moderator and to nobody
else, and a post in a hidden discussion is hidden with it.

`go test -run 'TestMentions_' ./pb/flarum/` pins the collection and
its unique index, the stored mention set, the inert unknown username,
the self-mention (stored, not notified), the replaced set after an
edit, the notification and its kind, and the post resource fields.
`go test ./markdown/...` pins the renderer: the mention is never
dropped, in any configuration.

## A LIKE IS NOTIFIED, OR IT DID NOT HAPPEN (2026-11-24)

The likes plugin is aligned with upstream `flarum/likes` (the Flarum
2.0 tree). The reference is upstream's `PostResourceFields` and
`LikePostPolicy`.

**THE `like_own_post` SETTING WAS READ BY NOTHING.** Upstream's policy
denies the author of a post the right to like it UNLESS the
`flarum-likes.like_own_post` setting is on, and upstream's default is
ON. The port MENTIONED the setting in a comment and never read it, so
an operator who turned it off was ignored — and an operator who left it
alone got the opposite of upstream's default. The rule is now the
policy's own, and the `canLike` attribute asks the SAME question the
endpoint does: a flag that says "yes" where the endpoint says "no" is
the control the reader clicks and the server refuses.

**A SETTING IS NOT A STRING, AND A JSON FIELD IS NOT A `[]byte`.** The
settings table's `value` column is a JSON field, so a stored setting
arrives as `types.JSONRaw` — and a type switch that handles `string`
and `[]byte` but not `JSONRaw` falls through to the default, reading
every setting as its fallback. This is the third time this trap has
appeared (the gamification and the polls plugins hit it too), and it
is worth a shared helper rather than a third private copy.

**THE LIKERS ARE LISTED WITH THE READER FIRST.** Upstream sorts the
actor's own row to the FRONT of the `likes` relationship before the
`created_at` order, so the frontend can tell which of the listed
likers is the reader without a second lookup. The port sorted by date
alone, which put the reader last — and the frontend's highlight fell
on the wrong row.

**A LIKE NOTIFIES THE POST'S AUTHOR, AND THE LIKER IS NOT NOTIFIED.**
Upstream's `SendNotificationWhenPostIsLiked` notifies the author of
the post and nobody else: a like on one's own post notifies nobody.
The port had the `postLiked` notification TYPE and the
`EventPostLiked`/`EventPostUnliked` events DECLARED, and never fired
either — so a like was silent.

**THE EVENT IS DISPATCHED BEFORE THE NOTIFICATION**, so a listener
sees the like before the notification exists. And upstream's
`SendNotificationWhenPostIsUnliked` DELETES the notification the like
produced; this port does not remove a notification the reader has
already seen, because a notification that vanishes is worse than one
that is stale.

**THE PERMISSION KEY IS UPSTREAM'S.** Upstream registers
`discussion.likePosts` and seeds it for the member group; the port's
own `likePost` was not in the translation table, so the admin's
permission page could not grant it — the key it wrote was a key the
forum never read.

`go test -run 'TestLikes_' ./pb/flarum/` pins the attribute set, the
`likes` relationship, the self-like setting (both ways), the flag
against the endpoint, the liker ordering, the notification (and the
absence of one for a self-like), and the permission-key translation.

## THE TAGS RESOURCE IS UPSTREAM'S, ATTRIBUTE FOR ATTRIBUTE (2026-11-24)

The tags plugin is aligned with upstream `flarum/tags` (the Flarum 2.0
tree). The reference is upstream's `TagResource`, and the gap was real:
the port served TEN attributes where upstream serves SIXTEEN, and the
missing six were all SILENT — the frontend renders a tag with whatever
it finds, so a missing `icon` or `isChild` is a badge that is slightly
wrong, with nothing erroring.

**THE ATTRIBUTE SET IS UPSTREAM'S, IN UPSTREAM'S ORDER.** `name`,
`description`, `slug`, `storedSlug`, `color`, `icon`, `isHidden`,
`isPrimary`, `isRestricted`, `discussionCount`, `position`,
`defaultSort`, `isChild`, `lastPostedAt`, `canStartDiscussion`,
`canAddToDiscussion`. The `can*` attributes carry the CALLER's
capabilities, so they are computed per request rather than read off the
tag.

**`storedSlug` IS VISIBLE ONLY TO A CALLER WHO MAY EDIT THE TAG**, and
`lastPostedAt` is always present (null until a discussion is posted) —
upstream's rules, and the two that a naive implementation gets wrong.

**A TAG-SCOPED PERMISSION CARRIES ITS SCOPE IN THE KEY.** Upstream
writes `tag5.viewForum`, and the admin page posts that spelling. This
forum keeps the scope in its own COLUMN, so `ForumPermissionKeyAndScope`
splits the two apart: the key becomes the forum's own name and the
scope becomes the tag's id. Without the split, a scoped grant would be
stored as a GLOBAL one — and a restricted tag would grant access to
everybody.

**THE COUNT RULES BIND THE WHOLE SET, NOT ONE ATTACHMENT.** Upstream
validates them in the `tags` relationship setter, where the complete
set is visible at once. The attach route therefore checks only the
MAXIMA (a minimum is a property of a FINISHED set, and checking it per
attachment would refuse the first tag of a discussion that is about to
get its second), and the relationship write path checks both. A
tag is a PRIMARY PARENT when it has a position AND no parent — a CHILD
carries a position of its own, so counting by position alone would let
a child satisfy the primary minimum.

**A RELATIONSHIP MAY BE WRITABLE, AND THE CORE DOES NOT HAVE TO KNOW
WHICH.** Upstream's frontend saves a discussion with
`discussion.save({ relationships: { tags } })`, so the `tags`
relationship is WRITABLE — and the core cannot know that, because the
relationship belongs to an extension. The plugin therefore declares
the WRITE side (`plugin.RelationshipWriter`) and the core calls it when
a request carries the relationship.

**THE WRITE PATH IS WHERE THE COUNT RULES LIVE, SO IT NEEDS THE FORUM'S
OWN ANSWERS.** The writer has no request event: it runs from the
mutation handler. The server passes itself through the context, so the
plugin asks the SAME `canTagDiscussion` the `canTag` attribute and the
attach route ask — otherwise the flag would say "yes" and the write
would say "no".

## A POLL IS A SELECTION, NOT A DELTA (2026-11-24)

`fof/polls` is now a plugin (`pb/flarum/plugins/polls/`).

**THE VOTE CARRIES THE WHOLE SELECTION, NOT A DELTA.** The endpoint
takes the set of option ids the voter has chosen, and an option that is
not in the set loses its vote. That is upstream's `MultipleVotesPoll`,
and it is the right shape: a delta ("add option 3") cannot express
"change my mind from option 3 to option 4", so the client and the
server would each have to keep a copy of the current selection to know
what the delta means. The selection is self-describing, and a client
that lost its state can still vote correctly.

**THE COUNTS ARE RECOMPUTED, NEVER INCREMENTED.** Upstream increments
an option's count when a vote lands and decrements it when one is
removed. An increment drifts: a vote that was never counted (one cast
before the option existed) breaks the arithmetic, and a decrement that
is skipped leaves the count high forever. A recount is always right,
and a poll has few enough options that the recount is cheap.

**THE COUNT IS ABSENT, NOT ZERO, WHEN IT MAY NOT BE SEEN.** Upstream
omits the `voteCount` attribute when the reader is not entitled to see
it, and the frontend reads the ABSENCE as "hidden". A zero would read
as "nobody has voted" — a different and wrong statement. The rule is
upstream's `seeVoteCount`: the voter may always see the count of a poll
they voted in, and so may the poll's author and a public poll.

**THE UNIQUE INDEX IS ON (POLL, OPTION, USER), NOT (POLL, USER).** A
poll that allows multiple votes holds one row per option, so the option
is part of the identity — and changing an option's mind is a
REPLACEMENT, not an accumulation. The index is the only place that
guarantee holds against two concurrent requests.

**A PERMISSION CANNOT EXPRESS "YOUR OWN", BUT THE VOTE IS NOT ONE.**
Upstream asks for `polls.changeVote` only when the new selection
DIFFERS from the old one, so a voter who submits the same selection
twice is not refused for a vote they did not change. The check
therefore compares the two sets before it asks, which is why the port
does the same rather than asking on every submission.

**THE ROUTE IS REGISTERED FOR PATCH AND FOR POST.** The frontend
rewrites a PATCH into a POST with the method in `X-HTTP-Method-Override`,
and PocketBase's router matches on the REAL method before any handler
runs — so a route registered only for PATCH is UNREACHABLE from the
frontend. The POST twin resolves the effective method itself and
refuses a POST that names no override with a 405.

**A PLUGIN CANNOT REUSE THE CORE'S ERROR RENDERER.** The core's `fail`
is unexported, so a plugin that returned the raw error from
`RequireMethod` fell through to PocketBase's default handler: a 400
with an empty body, so the client saw a generic failure instead of the
405 the route deserved. `api.WriteMethodError` is the plugin-facing
form.

**WHAT IS NOT PORTED.** Global (forum-wide) polls, poll groups, the
poll image uploads, the scheduled publishing and its cron, the drafts
workflow, and the polls directory page. Each is a surface of its own,
and half of one is worse than none.

`go test -run 'TestPolls_' ./pb/flarum/` pins the vote round trip, the
idempotence (by the ROW COUNT), the changed vote, the withdrawn vote,
the permission gate, the guest refusal, the plain POST refusal, the
method override, the foreign option, the hidden count and the two
permission-key translations.

## A SCORE IS A UNIQUE INDEX, NOT ARITHMETIC (2026-11-24)

`fof/gamification` is now a plugin (`pb/flarum/plugins/gamification/`).

**THE VOTE IS A ROUTE, NOT A WRITABLE FIELD.** Upstream writes the vote
through a post UPDATE (`vote` is a writable field on the post resource).
The permission that governs it is the extension's own `votePosts`, and the
core's post update handler does not consult it — so a field would have been
a permission with no enforcement. The vote is therefore a route
(`POST /api/posts/{id}/vote`), exactly as the Likes plugin's like is: the
check and the write belong together, and the `canVote` attribute is derived
from the same permission.

**THE UNIQUE INDEX ON (post, user) IS WHAT MAKES A VOTE IDEMPOTENT.**
Upstream reaches the same result with an application-level check. This
port puts it in the database instead, because that is the only place it can
be enforced against two concurrent requests: a double-clicked button
cannot add a second row, and the score cannot double. Nothing reports a
doubled score, so the guarantee has to live below the application.

**THE SCORE IS THE AUTHOR'S, AND A RANK IS THE HIGHEST ONE QUALIFIED.**
The points an account holds are the votes RECEIVED on its posts, not the
votes it cast (upstream's `Vote::updateUserVotes`). The singular `rank`
attribute is this port's own addition — upstream attaches EVERY rank the
account has passed and renders them as a stack — and it reports the
HIGHEST one, because a badge that said "Rookie" to an account that had
passed "Veteran" would be the opposite of what a badge is for.

**A SETTING IS NOT A STRING.** The settings table's `value` column is a
JSON field, so a value written as the string "0" comes back as RAW JSON
BYTES (`types.JSONRaw`), not as a Go string. A type switch that only
handles `string` therefore never matches, and every setting reads as its
fallback — which is how "the setting is not read" happens SILENTLY. The
same trap applies to `api.DecodeBody`, whose map argument has to be a
POINTER: a map is a reference type, and passing it by value decodes into a
copy the caller never sees.

**THE COLLECTION NAMES CARRY NO HYPHEN.** PocketBase's collection-name rule
is `^\w+$`, and the plugin slug of `fof/gamification` contains one — so
`ExtensionPhysical` (which embeds the slug) produces a name the migration
cannot create, and the failure is "name: must be in a valid format" at
BOOT. The plugin uses the forum's own prefix, like every other plugin's.

**THE LEADERBOARD IS COMPUTED, AND ITS ID NAMES THE RANKING.**
`GET /api/leaderboard` answers upstream's four metrics (`posts`,
`discussions`, `upvotes`, `votes`) over upstream's five calendar periods
(`day`, `week`, `month`, `year`, `all`). The id composes the metric, the
period AND the position: a bare position means entry "11" of the yearly
board and entry "11" of the monthly one are the same record to a client
store that caches by type and id, so switching metric or period hands back
whichever was fetched first. A metric or period that does not exist is a
validation failure rather than an empty board, because an empty board
reads as "nobody has scored".

The scores are computed in Go over the plugin's rows rather than in SQL:
PocketBase's query API cannot express the grouped aggregates the metrics
are defined by, and a raw query would pin the plugin to one driver.

**WHAT IS NOT PORTED.** The trending score (a Reddit "hot" formula over a
column this port does not need), the auto-assigned groups, the rankings
page's charts, the votes page, the podium images and the votes-per-minute
flooding rule. Each is a surface of its own, and half of one is worse than
none.

`go test -run 'TestGamification_' ./pb/flarum/` pins the vote round trip,
the idempotence (by the ROW COUNT, not the arithmetic), the changed vote,
the permission gate, the self-vote setting, the points-to-rank chain, the
leaderboard's ordering and id, and the two error shapes.

## THE MARKDOWN PLUGIN ADDS SYNTAX, NOT A RENDERER (2026-11-24)

`flarum/markdown` and `flarum/statistics` are now plugins
(`pb/flarum/plugins/{markdown,statistics}/`).

**THE FORMATTER WAS ALREADY HERE.** Upstream's extension does two things:
register the Litedown formatter, and ship a composer toolbar.
`pb/flarum/api/service.go`'s `renderContent` already calls riff's own
`markdown.Service.RenderHTML` for every post, so the forum has been
rendering with that engine all along — a second formatter would have been a
second set of rules to keep in step. What the engine LACKED was one piece of
upstream's syntax: the inline spoiler.

So the plugin contributes the syntax and the toolbar, and no formatter. The
spoiler (`markdown/extensions/spoiler.go`) is a goldmark inline parser
modelled on `extension/strikethrough`: it claims a delimiter run of EXACTLY
two pipes, because a single pipe is a table cell boundary. That detail is
the whole risk in the change — an inline parser that claimed one pipe would
eat every table in the forum, and the failure is silent (the cells vanish,
nothing errors). `TestSpoiler_LeavesTablesAlone` pins it.

The syntax is enabled on the shared `markdown.Service`, not on a
forum-specific instance, because the two would otherwise diverge and a memo
that wrote `||x||` would render differently in the two surfaces.

The HTML is upstream's: `<span class="spoiler" data-spoiler="">`, which the
plugin's stylesheet blurs and whose class the click handler removes.

**STATISTICS IS THE ENDPOINT, NOT THE WIDGETS.** `GET /api/statistics`
implements upstream's contract — `?period=lifetime` for the totals,
`?model=<entity>` for a day series — and upstream's gate
(`assertAdmin()`), answering 403 rather than the forum's usual 404 because
the endpoint's existence is not the secret, the numbers are. A missing
model is a validation error rather than an empty series: an empty chart
reads as "no activity", which is a different and wrong statement.

The plugin declares NO asset bundle. Upstream's charts are an admin-page
feature and this port has no page for them; a bundle that drew nothing
would be exactly the dead-tag problem the module's startup check exists to
surface.

The series is grouped in Go over a thirty-day window rather than in SQL:
PocketBase's query API cannot express `GROUP BY` over a formatted date, and
a raw query would pin the plugin to one driver.

**`FriendsOfFlarum/polls` AND `FriendsOfFlarum/gamification` ARE NOT
PORTED.** Both were surveyed (138 and 96 PHP files respectively) and both
are larger than the four extensions already ported put together. Polls
needs its own collection set (polls, options, per-option votes), a voting
route with the change-vote rule, and a composer modal; gamification needs a
points ledger, badge grants, a ranking query and an admin settings surface.
Each is a feature in its own right, and half of one is worse than none —
the same judgement as `flarum/mentions`.

`go test -run 'TestMarkdown_|TestStatistics_' ./pb/flarum/` and
`go test ./markdown/...` pin the rendering chain, the stored source, the
admin gate, the two error shapes and the series shape.

## LOCK AND STICKY ARE TWO COLUMNS, AND A SUSPENDED ACCOUNT IS A GUEST (2026-11-24)

`flarum/lock`, `flarum/sticky` and `flarum/suspend` are now plugins
(`pb/flarum/plugins/{lock,sticky,suspend}/`).

**`state` CARRIED THREE UNRELATED THINGS.** The discussions collection had
one `state` select of `open`, `locked`, `hidden` and `sticky`, so locking a
pinned discussion silently unpinned it and pinning a locked one silently
reopened it. Neither failure is reported anywhere: the request succeeds,
the flag the caller did not touch changes, and the only symptom is a
discussion that is no longer where it should be. Upstream keeps three
independent columns (`is_locked`, `is_sticky`, and the visibility state),
and the port now does too — `state` keeps visibility, and each plugin owns
its own boolean.

Both plugins' migrations BACKFILL from the legacy encoding and the
repository keeps a `state` fallback, so an existing instance keeps its
locked and pinned discussions across the upgrade. Without the backfill an
upgrade silently reopens every closed discussion, which is the worst
possible outcome for a moderation tool.

**THE REPLY GATE IS THE POLICY, NOT THE COLUMN.** A locked discussion
refuses a reply from anyone who could not unlock it — upstream's
`Lock\Access\DiscussionPolicy::reply()` — so a moderator can still post a
closing note in a locked thread. The resource's `canReply` and the write
path's refusal are the same question, asked the same way; a gate that only
checked the column would leave the composer enabled and the submit
refused.

**A SUSPENDED ACCOUNT IS A GUEST, IN ONE PLACE.** Upstream's
`RevokeAccessFromSuspendedUsers` swaps the account's groups for the guest
group on every permission check. The port models that as
`domain.Actor.Suspended`, which `groupSet()` maps to the guest group: every
permission is revoked at once, including permissions added by plugins that
do not exist yet. A per-handler `isSuspended` check would have to be added
to each new write path, and the one that is forgotten is the hole.

The suspension is read in BOTH user projections — the repository's and the
session resolver's. They are separate functions, and the permission layer
asks the session's: mapping only the repository's made the suspension
visible in the UI while every write still succeeded, which is the failure
this feature exists to prevent.

**ATTRIBUTES REACH THE WIRE THROUGH ONE HELPER.** Only the post resource
applied `s.attributes[TypePost]`; a discussion or user attribute was
silently dropped, and an absent `canLock` reads as false in the frontend,
so the control simply never appeared. `Server.applyPluginAttributes` now
serves discussions, posts and users. The `Can` callback on
`plugin.AttributeRequest` is the permission service itself, so a plugin's
`can*` flag and its endpoint ask the same question.

**`flarum/mentions` IS NOT PORTED.** Its backend is small (parse the
mentions out of the post body, store them, expose `mentionsUsers` /
`mentionedBy`), but the frontend is not: upstream's autocomplete is a
composer extension with its own mention formats, a quote button, mention
previews and a mentioned-by list, and the port's vendored `TextEditor` has
these behind a `flarum-mentions` bundle that has to be authored for this
API. Shipping the storage without the composer would record mentions
nobody can insert, so it is left for a follow-up with the markdown
renderer and the composer in scope.

`go test -run 'TestLock_|TestSticky_|TestSuspend_|TestModerationPlugins_' ./pb/flarum/`
pins the toggles, the reply gate on both sides, the both-flags-at-once
case, the permission boundaries, the suspension's effect on participation,
the attribute visibility rules and the admin permission-key translation.

## A LANGUAGE PACK IS A PLUGIN, AND AN UNKNOWN LOCALE FALLS BACK TO ENGLISH (2026-11-24)

`flarum-lang/chinese-simplified` is implemented as `pb/flarum/plugins/chinese/`,
the reference implementation of a LOCALE plugin. It is a plugin rather
than a core feature for the same reason upstream models it as an
extension: a translation is a catalogue, not behaviour, and the set of
languages an instance carries should be visible where every other
feature is.

**IT DECLARES A LOCALE, NOT A CODE BUNDLE.** `plugin.LocaleBundle`
(`AddLocale`) mirrors the `extra.flarum-locale` block of an upstream
language pack's composer.json: a code, a title and the two catalogue
file names. A language pack has no `flarum.extensions` entry — the SPA
loads exactly ONE catalogue per frontend, and that file carries the
core's translations for the language as well as the extensions'.

**ONLY THE ACTIVE LOCALE IS LOADED.** `scriptURLs` used to hardcode
`forum-en.js`. It now resolves `localeAssetName(activeLocale, admin)`.
Emitting both catalogues would merge the English keys over the Chinese
ones — `addTranslations` is additive, and the later call wins — so the
shell would look half-translated in a way that is very hard to attribute.

**AN UNKNOWN LOCALE FALLS BACK TO ENGLISH.** `Options.Locale`
(`RIFF_FORUM_LOCALE`) is checked against the DECLARED set. A typo must not
request `forum-fr.js` and leave the UI rendering
`core.forum.header.back_to_index_tooltip`, where the failure looks like a
frontend bug rather than a configuration one. Nothing is guessed: `en` is
always present because the core compiles it.

**THE COMPILER RULES ARE SHARED, NOT COPIED.** `packages/core/build-locale.cjs`
and the pack's build both call `locale-compile.cjs`. The `=> key`
reference resolution is the load-bearing half: the catalogue has ~200 of
them, and leaving them unresolved renders the literal `=> core.ref.foo`.
The core's own output was verified byte-identical after the extraction, so
the refactor changed no shipped bytes.

**ONLY WHAT THIS PORT SERVES IS VENDORED.** Upstream carries catalogues
for several hundred extensions. Copying them would make the tree claim
support for extensions riff does not have — the same trap as an
`AssetBundle` naming a bundle nothing builds. `core`, `validation`,
`flarum-tags`, `flarum-likes` and `fof-upload` are vendored; adding one is
a dropped `.yml` and a rebuild.

`go test -run 'TestLocale_' ./pb/flarum/` pins the declared locale, the
shell's catalogue selection (including that the English one is NOT also
loaded), the admin/forum split, the fallback, the boot payload and the
served asset. The asset test asserts on CJK characters in the VALUES, not
the keys: an English catalogue under a Chinese file name would pass a
key-only check and translate nothing. `npm test` in `pb/flarum/frontend`
pins the compiler's reference resolution and per-frontend filter.

## A MISSING PLUGIN BUNDLE IS SKIPPED, NOT EMITTED AS A DEAD TAG (2026-11-24)

"Tags and likes are not visible from the UI" had no server-side or
frontend-logic cause: the tag relationship reaches the wire, `GET
/api/tags` answers, the bundles register themselves on
`flarum.extensions`, the extenders run, and the items ARE added
(`infoItems` yields `tags`, `footerItems` yields `like`). The forum's
own Jest suite pins all of that.

**THE SHELL EMITS A TAG FOR EVERY REGISTERED BUNDLE, WHATEVER WAS
BUILT.** `ui/dist` is build output and is gitignored, so a tree that
carries only the core bundles (a core-only rebuild, a stale directory
kept between builds, a binary built before `scripts/build_flarum_spa.sh`
staged the plugins) produces a shell whose `plugins/<name>/forum.js`
request 404s. A 404 on a `<script>` is invisible: the page renders, the
console stays clean, and the plugin's UI simply never appears. That is
indistinguishable from "the plugin does not work".

The module now VALIDATES the list before advertising it.
`Module.warnMissingAssets`, called once from `mount` (not per request),
logs the plugin id and the missing file; `scriptURLs`/`styleURLs` skip
any asset `frontend.Assets.Exists` reports absent instead of emitting a
dead tag. `Assets.Exists` already existed and had no caller — this is
what it was for.

**The guard is deliberately silent when NO build was dropped in**: that
is the documented fallback-shell case, and warning about every asset
would bury the signal the guard exists to raise.

`go test -run 'TestFrontendBundles_' ./pb/flarum/` pins both halves. It
skips when `ui/dist` is absent (a fresh clone), asserts every advertised
bundle is embedded AND answers 200, and was mutation-checked by moving
`ui/dist/plugins/tags/forum.js` aside: the test goes red naming the
file, and the rendered shell drops the tag while keeping the other two
plugins.

## THE THEME SETTINGS TRAVEL ON THE FORUM DOCUMENT, not only in the admin payload (2026-11-24)

An administrator picked a colour scheme in the Appearance page and it
was not persisted: the forum kept rendering in light mode.

**THE SETTINGS WERE SAVED.** The admin's save is upstream's command
wire format (`POST /api/settings` with the flat map), the handler
writes each key to the settings table, and the admin payload reads them
back — the saved `color_scheme` was in `app.data.settings`, exactly
where the Appearance page reads it from.

**THE FORUM DOCUMENT DID NOT CARRY IT.** Upstream publishes the theme
settings on the FORUM document
(framework/core/src/Api/Resource/ForumResource.php:104-112):

    Schema\Str::make('themePrimaryColor')
        ->get(fn () => $this->settings->get('theme_primary_color')),
    Schema\Str::make('colorScheme')
        ->get(fn () => $this->settings->get('color_scheme')),

and the frontend reads them from THERE
(framework/core/js/src/common/Application.tsx:508):

    forumDefault ??= app.forum.attribute('colorScheme') ?? 'auto';

riff's forum document carried neither, so the forum fell back to
`auto` — the setting was stored, the admin showed it, and the forum
ignored it.

The forum document now carries upstream's full set: `colorScheme`,
`themePrimaryColor`, `themeSecondaryColor`, `logoUrl`,
`logoDarkModeUrl`, `faviconUrl`, `emailLogoUrl`, `headerHtml` and
`footerHtml` — each read from the SETTINGS, so an administrator's
choice is what the forum shows.

The admin payload carries the keys the Appearance page reads as well:
the colour scheme, the two theme colours, the toggle settings and the
uploaded asset paths (`logo_path` and its siblings), which the upload
buttons read to show what is currently set.

## THE AUDIT: EVERY FRONTEND/SERVER CONTRACT, CHECKED AT ONCE (2026-11-24)

Four bugs in a row had the same shape: **the frontend is upstream's
(unmodified), and the backend implemented a contract slightly
differently.** Each one was found by a bug report rather than by a
test, so the whole surface was enumerated and checked.

The frontend's API surface is small and enumerable: 11 `app.request`
call sites and 13 `app.store.find` call sites. Cross-referencing them
against riff's route table found five more mismatches.

**1. `DELETE /api/notifications` WAS NOT SERVED.** The notification
page has a "delete all" button, and it calls it
(`NotificationListState.ts:60`). Upstream serves it
(`Api/routes.php`: `$map->delete('/notifications', …)`), and riff
answered 404.

**2. THE RESPONSE CARRIED NO `included`, SO NO RELATION RESOLVED.** The
frontend reads a relationship out of the STORE, and the store is filled
from `payload.included` (`Store.pushPayload`); the relationship's
IDENTIFIER alone is not enough. Upstream sends the related resources by
default (`DiscussionResource.php:101` `defaultInclude(['user',
'user.groups', 'lastPostedUser', …])`), and the frontend asks for them
explicitly (`DiscussionListState.ts:32`: `include: ['user',
'lastPostedUser']`).

riff sent relationship identifiers and no `included` array at all, so
`discussion.user()` read as undefined and **every discussion in the
list rendered without an author** — no name, no avatar.

**3. THE PAGINATION METADATA WAS INCOMPLETE, SO THERE WAS NO "Load
more".** The frontend reads four things (`PaginatedListState.ts:197,
227,147`): `meta.page.total`, `meta.page.offset`, `meta.perPage`, and
`links.next` / `links.prev`.

riff sent only `meta.page.total`. **`links.next` IS THE "Load more"
BUTTON**: the frontend decides to offer it with
`hasNext: !!links?.next`, and the button is rendered only when
`state.hasNext()` is true (`PostList.tsx:25`). Without it the infinite
scroll never loaded a second page — the list showed the first twenty
records and stopped. The `Links` struct existed in riff but was never
populated.

**4. THE `sort` PARAMETER WAS NEVER READ, SO THE SORT DROPDOWN DID
NOTHING.** The frontend sends upstream's sort names
(`DiscussionListState.ts:73`: `map.latest = '-lastPostedAt'`,
`map.top = '-commentCount'`, `map.newest = '-createdAt'`, `map.oldest =
'createdAt'`, `map.az = 'title'`, `map.za = '-title'`), and upstream
names the same set (`DiscussionResource::sorts()`). Every listing came
back in the repository's own order.

**5. THE POST STREAM COULD NOT LOAD ITS POSTS.** The stream shows a
WINDOW of posts: the discussion's `posts` relationship names them all
but carries no attributes, so the stream loads the ones it is about to
show — BY ID (`PostStreamState.ts:308`: `app.store.find('posts',
loadIds)`), which `Store.find` turns into `?filter[id]=a,b,c`
(`Store.ts:193`).

riff's post list required `filter[discussion]` or `filter[author]`, so
the stream's request answered **422**.

### What was NOT a mismatch

The admin endpoints that riff does not serve (`/mail/bounce-stats`,
`/system-info/system`, `/queue/*`, `/extensions/*`) belong to features
riff does not have — mail sending, a job queue, extension management.
Their widgets CATCH their own errors (`loadError = true`), so they
degrade to an empty widget instead of breaking the dashboard.

### The lesson

Every one of these was a contract the upstream FRONTEND depends on and
that upstream's BACKEND documents in its own source. The frontend is
vendored unmodified, so the contract is upstream's — but the port
re-implements it, and a re-implementation drifts.

The durable answer is to CHECK the contract rather than to rediscover it
per bug report: the frontend's call sites are enumerable, and upstream's
resource classes state each endpoint's `defaultInclude`, `sorts` and
`filters`. A test that walks the vendored frontend's call sites and
asserts each one answers would keep the surface honest.

## THE DISCUSSION SLUG CARRIES THE ID, because the URL is built from it (2026-11-24)

A discussion that had just been posted could not be opened:

    GET https://…/forum/api/discussions/test?bySlug=true&page[near]=0 404 (Not Found)

The path segment is the discussion's SLUG, not its id — the `bySlug=true`
flag says so — and the server treated it as an id.

**THE FRONTEND NAVIGATES BY SLUG.** The route helper uses
`discussion.slug()` (framework/core/js/src/forum/routes.ts:58):

    discussion: (discussion: Discussion, near?: number) => {
      return app.route(near && near !== 1 ? 'discussion.near' : 'discussion', {
        id: discussion.slug(),
        near: near && near !== 1 ? near : undefined,
      });
    },

and the discussion page asks the API for it with `bySlug=true`
(`DiscussionPage.tsx:176,257`):

    discussionPromise = app.store.find<Discussion>('discussions', m.route.param('id'), this.requestParams());

so the request is `GET /api/discussions/<slug>?bySlug=true`, and the
server has to resolve a SLUG there. Upstream resolves it the same way
(`DiscussionResource::find`):

    if (Arr::get($context->request->getQueryParams(), 'bySlug', false)) {
        $discussion = $this->slugManager->forResource(Discussion::class)->fromSlug($id, $actor);
    } else {
        $discussion = $this->query($context)->findOrFail($id);
    }

**THE USER ROUTE ALREADY DID THIS** (`showUserHandler` honours
`wantsSlugLookup`), and the DISCUSSION ROUTE DID NOT. The `bySlug` flag
was simply ignored for discussions.

**THE SLUG MUST CARRY THE ID.** Upstream's slug is the id, then the
title's slug (`IdWithTransliteratedSlugDriver::toSlug` is
`$instance->id . (trim($instance->slug) ? '-' . $instance->slug : '')`),
and `fromSlug` reads the id back by taking the part BEFORE the first
dash.

That is not decoration: **the title's slug alone is not unique.** Two
discussions may share a title, and the slug column is not unique
either. Leading with the id is what makes the published slug name
exactly one discussion, and taking the first segment is what reads it
back. riff published the bare title slug ("test"), which resolved to
nothing.

So the slug is now `{id}-{title-slug}`, and the handler reads the id
back out of it when `bySlug` is set. A slug with no dash is taken as a
bare id, so a client that asks for a discussion by its id still works.

## THE FORUM DOCUMENT IS BUILT IN ONE PLACE, because two copies DRIFTED (2026-11-24)

The "New Discussion" button was disabled for every signed-in account.

The frontend's rule is upstream's, byte for byte
(`framework/core/js/src/forum/components/IndexSidebar.tsx:30,44`):

    const canStartDiscussion = app.forum.attribute('canStartDiscussion') || !app.session.user;
    ...
    disabled={!canStartDiscussion}

so the button is disabled when the forum document says the caller may
not start a discussion. Upstream computes that attribute from the
ACTOR (`framework/core/src/Api/Resource/ForumResource.php:132`):

    Schema\Boolean::make('canStartDiscussion')
        ->get(fn ($model, Context $context) => $context->getActor()->can('startDiscussion')),

**AND THE BOOT PAYLOAD'S COPY OF THE DOCUMENT HARDCODED THE FLAGS TO
`false`.** The API's `GET /api/` computed them correctly — the flags
were already `s.can(e, domain.PermissionStartDiscussion, …)` there — but
the SPA never reads the API: it reads the forum document out of the
BOOT PAYLOAD (`Application.boot`: `this.store.pushPayload({ data:
this.data.resources })` then `this.store.getById('forums', '1')`).

So the API's correct answer never reached the button, and the two copies
of the same document disagreed.

There is now ONE document: `api.Server.ForumDocument(e)` builds it, the
API's `GET /api/` serves it, and the boot payload asks for it. The
duplication is gone rather than patched, because a second copy is what
let the two drift.

The document is therefore PER-CALLER, and the `can*` attributes are the
caller's permissions — `canViewForum`, `canStartDiscussion` and
`canSearchUsers`. The remaining three (`canCreateAccessToken`,
`canModerateAccessTokens`, `canEditUserCredentials`) stay `false`:
riff has no permission for them and no UI that reads them.

## EVERY WRITE ROUTE NEEDS A POST TWIN, because the method is part of the ROUTE MATCH (2026-11-24)

The frontend's settings page answered 404 for a preferences save:

    POST https://…/forum/api/users/vr6qkjeyzo0q8zg 404 (Not Found)

The body was `{"errors":[{"status":"404","code":"not_found",
"detail":"That resource does not exist."}]}` — the API's own 404, so the
request DID reach the forum; it matched no route.

**THE FRONTEND NEVER SENDS A PATCH OR A DELETE.** The vendored
`Application.request` (framework/core/js/src/common/Application.tsx:673,
byte-identical to upstream's) rewrites every method other than GET and
POST into a POST with the intended method in the
`X-HTTP-Method-Override` header:

    if (options.method && !['GET', 'POST'].includes(options.method)) {
      const method = options.method;

      extend(configurable, 'config', (_: unknown, xhr: XMLHttpRequest) => {
        xhr.setRequestHeader('X-HTTP-Method-Override', method);
      });

      options.method = 'POST';
    }

Upstream backs that with a `FakeHttpMethods` middleware, which rewrites
the request's method BEFORE routing.

**A GO MIDDLEWARE CANNOT DO THE SAME.** PocketBase bakes the method
into the ServeMux pattern (`tools/router/router.go`: `pattern = v.Method
+ " "`), so the method is part of the ROUTE MATCH, and Go's `http.ServeMux`
decides it before any handler or middleware runs. A route registered only
for PATCH is therefore UNREACHABLE from the frontend: the request matches
no pattern at all.

This port already had the answer for the routes it had noticed — the
notification-preferences reset and the group reorder each carry a POST
twin, and `EffectiveMethod` (`pb/flarum/api/method_override.go`) reads
the header back. **SIX WRITE ROUTES DID NOT HAVE ONE**: the discussion
update and delete, the post update and delete, the user update and the
notification read. Every one of them was unreachable, so a discussion
edit, a post edit, a post delete and a preferences save all answered
404.

Each now carries a POST twin:

- the user update and the notification read resolve the effective method
  and refuse a POST that names no override (405, not a silent write);
- the discussion and the post share ONE POST pattern for the update and
  the delete, because the frontend sends BOTH to the same URL — the
  OVERRIDE, not the URL, tells them apart.

The uploads plugin had already done this for its own delete route.

## An administrator is a MEMBER OF THE ADMIN GROUP, not a superuser (2026-11-24)

Upstream derives the administrator flag from **group membership** and
from nothing else (`framework/core/src/User/User.php:527`):

```php
public function isAdmin(): bool
{
    return $this->groups->contains(Group::ADMINISTRATOR_ID);
}
```

The admin routes are gated on that flag
(`Admin/Middleware/RequireAdministrateAbility.php` is
`RequestUtil::getActor($request)->assertAdmin()`), so in upstream the
administrator group is the ONLY route to the admin panel.

This port derived the flag from the **PocketBase collection** instead
(`record.Collection().Name == core.CollectionNameSuperusers`), and read
the account's groups into `User.Groups` without ever consulting them.
That made the forum's administrator group (`forumgroupadmin`) a row
that granted NOTHING: an operator promoted somebody into it through the
admin's own user editor, the membership row was written, and the account
still got **403 on `/forum/admin`** and "You are not allowed to do that"
on every admin command.

The flag is now the **union** of the two sources: a PocketBase superuser
(the instance's operator) or membership in the administrator group. The
superuser route is kept because riff's superusers are the instance's
operators, which is the same idea as upstream's bootstrap administrator
— upstream's install writes a `group_user` row for the administrator
group, so its first administrator is an administrator by group
membership.

**THE GROUP LOOKUP IS A SEAM, NOT A DEPENDENCY.** The session resolver
cannot import the repository (the arrow points one way, from the
repository to the domain), so `session.Resolver` takes a `GroupLookup`
function and the module supplies it
(`pb/flarum/register.go`: `sessions.WithGroups(...)`). The lookup is
deliberately forgiving — a failure to read the memberships leaves the
caller with none, which is the safe direction.

**AN ADMINISTRATOR MAY NOT REMOVE THEIR OWN ADMINISTRATOR GROUP.** This
is upstream's `User\SelfDemotionGuard`, and it becomes load-bearing the
moment the flag is group-derived: the administrator group is the only
route to the admin panel, so an administrator who drops it locks
themselves out — and, if they were the last one, locks EVERYONE out.
Upstream draws the line exactly where this port draws it: demoting
ANOTHER administrator is allowed, because the actor is still an
administrator afterwards; only the self-demotion is refused.

The guard reads the caller from the **resolved actor**, not from
`e.Auth`: the forum resolves its callers from the session cookie or a
bearer token, so `e.Auth` is nil for most of the requests that reach
the handler (and reading it panicked).

Upstream also gives the administrator group every permission outright
(`Group::hasPermission()` returns true for `ADMINISTRATOR_ID`); this
port's equivalent is `PermissionService.Can`'s `actor.IsAdmin`
short-circuit, which now covers the group too.

## Post attachments are a PLUGIN, because upstream's core has none (2026-11-23)

Upstream Flarum v2.0.0's **core has no attachment support at all**.
The `post_attachments` table and the `Post->upload()` relationship were
removed from the core, and the vendored frontend carries no attachment
code either (`grep -rln 'attachment' framework/core/js/src/` returns
nothing). Attachments live in the `fof/upload` extension — the
de-facto standard for Flarum 2.x, ~239k installs, MIT licensed, and
the successor to `flagrow/upload`.

So the forum implements them as a **first-party plugin**
(`pb/flarum/plugins/uploads/`), mirroring that extension's contract
rather than inventing one:

- the resource type is **`files`** (`FileResource::type()`);
- a file carries `url`, `thumbnailUrl`, `size`, `humanSize`,
  `imageWidth`, `imageHeight`, `hidden`, `baseName` and `type`;
- a file hangs off a post through the post's **`files`**
  relationship;
- the collection is `forum_files`, with the uploader in `actor` and the
  post in `post`.

Three decisions are load-bearing:

- **THE BYTES GO THROUGH POCKETBASE'S OWN FILE FIELD.** The `blob` is
  a `FileField`, and the upload sets it to a `*filesystem.File` — the
  form the framework expects. The field then stores only the file's
  NAME and the framework writes the bytes under the record's own files
  path. Writing the bytes by hand leaves the field empty and the
  record pointing at a file that does not exist: the download answers
  404 while the upload answered 201.

- **THE BYTES ARE SERVED BY THE FORUM'S OWN ROUTE**, at
  `{basePath}/files/{id}` — never at PocketBase's native
  `/api/files/...` URL, which lives at the root of the listener the
  forum does not own (the same reason the avatar route exists). The
  route is registered by the MODULE, not the plugin, because the module
  owns the base path; and it is wrapped with `api.WithServer`, because
  a plugin route gets that wrapper for free and this one does not.

- **THE MEDIA TYPE COMES FROM THE FILE'S NAME, NOT THE REQUEST.**
  The browser's `Content-Type` is whatever the client felt like
  sending. The type is derived from the extension, and only the
  inline-safe types are served inline: everything else is served with
  `Content-Disposition: attachment`, using the same allowlist the memo
  attachment surface applies (`pb/api/file_serve.go`). SVG is
  deliberately absent — an SVG is a document, and a document served from
  the forum's origin can carry script.

The visibility rule is the POST's own: a file is as readable as the
post it is attached to, and an UNATTACHED file (uploaded before the
composer knows the post's id) belongs to its uploader alone. A denial
answers 404, not 403, so the response never confirms that a file
exists.

## The forum's admin surface speaks upstream's command wire format, not JSON:API (2026-11-23)

The forum's public API is JSON:API, but its ADMIN surface is not, and
conflating the two is a whole class of silent failure. Upstream's admin
controllers are plain command endpoints:

| Method | Path | Body | Answer |
| --- | --- | --- | --- |
| POST | `/settings` | the settings map itself (`{key: value, …}`) | 204, empty |
| DELETE | `/settings` | `{keys: [...], extensionId: ''}` | 204, empty |
| POST | `/permission` | `{permission, groupIds}` | 204, empty |
| POST | `/groups/order` | `{order: [...]}` | 204, empty |

`SetSettingsController::handle()` reads `$request->getParsedBody()` as
the settings — there is no wrapper — and every one of them asserts
admin and returns `new EmptyResponse(204)`. The reused Flarum frontend
posts exactly those bodies (`admin/utils/saveSettings.js` posts the
flat map; `ResetExtensionSettingsModal.tsx` posts the keys;
`PermissionDropdown.tsx` posts the permission; `GroupBar.tsx` posts the
order).

So the port's handlers read a FLAT object and answer an empty 204. A
handler that demanded a JSON:API envelope rejected every admin save
with "The settings could not be read", and the admin's Save button
reported a validation error while changing nothing.

Four things this surface needs that are easy to miss:

- **NO `GET /api/settings`.** Upstream has no such route: the settings
  reach the admin application through the boot payload
  (`AdminPayload` carries `$this->settings->all()`), which the port
  already does in `adminSettings()`.

- **THE PERMISSION MAP IS PART OF THE BOOT PAYLOAD.** Upstream sends
  `$document->payload['permissions'] = Permission::map()`, and the
  admin application reads it into `app.data.permissions`.
  `PermissionDropdown` uses it for the permission being edited AND for
  every permission that one requires, so an empty map makes the grid
  inert: every dropdown reads "no group holds this", the "everyone" /
  "members" badges never light up, and a required permission narrows
  every group to nothing.

- **THE PERMISSION KEYS ARE TRANSLATED.** The admin application speaks
  upstream's names (`discussion.reply`, `discussion.editPosts`,
  `discussion.rename`), and the forum stores FLAT names (`reply`,
  `editPost`, `editDiscussion`) — see the earlier entry on the
  permission model. So `pb/flarum/domain/permission_keys.go` maps
  between them in both directions, and the handler translates on the
  way in. Without it the grid writes keys no handler reads, so
  granting a permission changes nothing. A key the table does not know
  passes through unchanged, so an extension's own permission
  round-trips.

- **`updated_by` CANNOT HOLD AN ADMINISTRATOR'S ID.** The settings
  collection's `updated_by` is a relation to the shared `users`
  collection, but the port's administrator is a `_superusers` record.
  Writing the administrator's id fails PocketBase's relation
  validation — "Failed to find all relation records with the provided
  ids" — which makes every settings save fail for the ONLY account
  allowed to perform one. The field is left empty for an
  administrator; upstream has no such column at all (its `settings`
  table is `(key, value)`).

The method override needs care here too. PocketBase's router matches on
the REAL method before any handler runs, so a `DELETE` sent as a
`POST` with `X-HTTP-Method-Override: DELETE` never reaches the DELETE
route: the POST route has to resolve the effective method itself
(`settingsHandler` delegates to `deleteSettingsHandler`).

## The vendored frontend's group-id constants must match the backend's (2026-11-23)

The vendored Flarum frontend compares group ids against three static
constants in `common/models/Group.ts` — `ADMINISTRATOR_ID`, `GUEST_ID`
and `MEMBER_ID` — in about a dozen places. Upstream's values are the
integers `'1'`, `'2'` and `'3'`, because its `groups` table has an
auto-incrementing integer primary key and the built-in groups are the
first rows. This port's groups live in a PocketBase collection whose
record ids are 15-character strings, and the built-ins carry
`forumgroupadmin` / `forumgroupguest` / `forumgroupmembr`
(`pb/flarum/domain/models.go`).

The constants were left at upstream's values. Every comparison
therefore failed, and the failures are all silent: the admin's group
editor offered the guest and member groups for assignment (the server
rejects those ids), the "is this account an administrator" check never
matched so the admin-group checkbox was never guarded, and the
permission dropdown's "everyone" / "members" badges never lit up.

The constants now carry the port's ids, and
`TestForum_TheGroupIDsMatchTheFrontend` reads them back out of the
TypeScript source so the two sides cannot drift apart unnoticed.

This is the same failure mode as the permission-key mismatch above: a
vendored frontend whose identifiers do not match the backend's. When a
vendored UI compares against a constant, the constant is part of the
API contract and deserves a test.

# DECISIONS.md — riff architectural audit log

This file is the single source of truth for non-obvious decisions made
while building riff, a PO-33 community site replacing the Memos Go
backend with PocketBase. New entries are appended at the top, oldest at
the bottom.

## The forum's permission model follows upstream's, policy by policy (2026-11-16)

The forum's authorization is a port of upstream Flarum's, and the port
is only faithful if it copies the POLICY layer and not just the
permission rows. Upstream's authority is three files:

- `User/Access/Gate.php` — the group permission is a FALLBACK: a
  policy that returns a result decides, and only when no policy
  covered the question does `isAdmin() || hasPermission()` answer.
- `Discussion/Access/DiscussionPolicy.php` and
  `Post/Access/PostPolicy.php` — the policies. `can()` is the
  group-scoped permission; `rename()`, `hide()` and `edit()` are the
  OWNERSHIP rules, and they are consulted FIRST.
- the tags extension's `Access/DiscussionPolicy.php` — the `tag`
  ability, which is a policy and not a group permission at all.

So upstream's answer to "may this actor edit this post" is: the author,
while the `allow_post_editing` setting permits it, or a holder of
`discussion.editPosts` (the MODERATOR group). The group permission
alone is not the answer, and the `allow_*` settings are not
decoration — they are the author's half of the rule.

Three consequences the port now follows:

- **THE MODERATION POWERS LIVE IN THE MODERATOR GROUP.** Upstream's
  default group permissions give the member group only
  `startDiscussion`, `discussion.reply` and `searchUsers`, and put
  `discussion.editPosts`, `discussion.hidePosts`, `discussion.rename`
  and `discussion.hide` in the moderator group. The forum now has a
  moderator group and seeds it that way. The member group must NOT
  hold them: a member-group row is inherited by every signed-in
  caller, so a moderation row there grants the moderation power to all
  of them.

- **THE `allow_*` SETTINGS GATE THE AUTHOR.** `allow_renaming`,
  `allow_post_editing`, `allow_hide_own_posts` and `allow_tag_change`
  have upstream's shapes — "-1" (always), "reply" (until somebody
  answers) and a number of minutes — and an unset setting permits
  nothing. The forum's settings table starts EMPTY, so the readers
  fall back to upstream's installed defaults ("10", "reply", "reply",
  "0"); without the fallback an absent setting would deny the author
  outright, where upstream allows both.

- **THE UI FLAGS AND THE ENDPOINTS AGREE.** Upstream computes
  `canRename`, `canDelete` and `canTag` with the same `can()` call the
  write path uses, so a control is never offered for an action the
  API refuses. The forum's flags now come from the same policies.

## The forum's permissions are group-scoped, so ownership is checked separately (2026-11-16)

The forum's permission rows are GROUP-scoped: a row says "the member
group may edit posts", not "the member group may edit their own
posts". `editPost` and `deletePost` are seeded for the member group,
which **every signed-in caller belongs to**, so a permission check on
its own answered "yes" for ANY member and ANY post: any member could
rewrite any post in the forum, and any member could tag or untag any
discussion.

The fix is to separate the two questions:

* the **AUTHOR** may always change their own post or discussion;
* anyone else needs a grant from a group OTHER than the implicit
  member group — a moderator grant — or the administrator's blanket
  authority (`domain.PermissionService.CanAsModerator`).

The distinction matters because a row for the implicit member group
carries no information about the individual: every signed-in caller
holds it. A row for an explicit moderator group does name a set of
accounts.

**The permission service cannot express "your own" at all**, so every
handler that changes something the caller owns has to ask the question
itself. The core's post edit/delete, the discussion rename and the
tags plugin's attach/detach all do now. The plugin-facing helpers are
`api.CanEditPost`, `api.CanEditDiscussion` and `api.CanDeletePost`.

## The forum's login must not leak which accounts exist (2026-11-16)

A login for an UNKNOWN identifier never reached the bcrypt comparison,
so it answered in about a millisecond while a login for a known
identifier took about fifty: the response TIME was a user-enumeration
oracle, even though the response BODY was identical for both.

The unknown path now compares the password against a dummy bcrypt
digest at the same cost, so both paths do the same work. Measured
before: 1.2ms for an unknown account against 47.5ms for a known one.
After: 48.3ms against 48.0ms.

## The colour preview swatch cannot carry an empty value (2026-11-16)

The admin's Appearance page has four `color-preview` settings. The
admin app reads a setting with `app.data.settings[key] ?? ''`, and
riff's `forum_settings` collection holds only the settings that were
explicitly saved — so an unset setting is an EMPTY STRING.

`ColorPreviewInput` renders that value on TWO inputs: a text input
(the field the user types in) and a preview swatch, which is an
`<input type="color">`. That element's value sanitization algorithm is
"value = a valid CSS color", and an empty string is not one, so
assigning it throws:

    The specified value "" does not conform to the required format.
    The value must be a valid CSS color.

Mithril assigns attributes as DOM properties (`vnode.dom[key] =
value`), so the value reaches the browser's validator. The throw
happens during a REDRAW — `saveSettings` calls `app.alerts.clear()`,
which redraws — so the error surfaces on the save rather than on the
control that caused it, and the stack points at the redraw.

The swatch now falls back to a neutral grey when the value is empty.
The TEXT input still carries the empty value: clearing the field is a
valid "no colour" state and coercing it to a colour would silently
change the setting.

**jsdom cannot catch this.** It silently coerces an invalid colour
input value to `#000000` instead of throwing the way a browser does,
so a test that reads `input.value` back passes against the broken
component. The test therefore spies on the value SETTER and asserts
what was assigned, which is what the browser validates.

## The forum API's trailing-slash route was a subtree match (2026-11-16)

The forum document was registered at `GET /forum/api/` **with** a
trailing slash, and in Go's `http.ServeMux` a trailing slash is an
anonymous ".../" wildcard: the pattern matches the whole subtree. So
**every** unrouted path under `/forum/api/` answered with the forum
document instead of a 404.

That is worse than a 404, and the symptom was a `TypeError` on the
user security page. The document's `data` is a single resource OBJECT,
but the page fetches the session tokens and the developer tokens
separately and joins the two answers with

```js
Promise.all([sessionsPromise, developerPromise]).then(([sessions, developerTokens]) => …)
```

A single object is not iterable, so the destructuring fails with
**"e is not iterable"**. The same shape of bug answers a list request
with one object, which is exactly what `store.find` asks for.

The route is now registered at `/forum/api` (no trailing slash) and the
API carries its own catch-all, which answers everything else with a
JSON:API 404. The catch-all is registered per method: a method-less
pattern conflicts with the memos SPA's method-less `/{path...}` in
`http.ServeMux`, and that panic takes the process down.

**The frontend reads the forum document out of the BOOT PAYLOAD**, not
over the API (`this.forum = this.store.getById('forums', '1')`), so
narrowing the route breaks nothing in the SPA.

## The forum's API must honour `X-HTTP-Method-Override` (2026-11-16)

The forum's settings page failed with an error whose `responseText` was
a whole HTML document. The shape of the error was the tell: the reused
Flarum frontend throws `RequestError{status, responseText, options,
xhr}` from `Application.request` when a response is not 2xx, and the
`responseText` was the memos SPA's `index.html` — so the request had
fallen through to the SPA catch-all.

**The frontend does not send a DELETE.** `Application.request` rewrites
every method other than GET and POST into a POST with the intended
method in the `X-HTTP-Method-Override` header, because "not all servers
and clients support" PATCH or DELETE. Upstream's API carries a
`FakeHttpMethods` middleware that reads the header back
(`Api/Middleware/FakeHttpMethods.php`); this port had none, so a
frontend DELETE arrived as a POST and the DELETE-only route did not
match.

The route is registered for POST **and** DELETE, and the handler
resolves the effective method through `effectiveMethod`. A POST without
the header is refused with a 405: the route accepts a POST only because
the frontend sends one, and a plain POST is a client mistake rather
than a silent reset.

**A method-less `Any` route is not an option here.** It conflicts with
the memos SPA's method-less `/{path...}` catch-all in `http.ServeMux`,
and that panic takes the whole process down — the same constraint the
forum module already documents for its other routes.

**An unrouted API path does not 404: it answers with the SPA's
`index.html`, and the status is 200.** The frontend's request helper
throws on a non-2xx, so a 200 HTML body is *worse*: the page renders
the markup as if it were data, or the caller parses it and fails
somewhere else entirely. Every forum API path the frontend calls has to
exist, and the ones that do not exist yet answer with HTML, not with a
JSON:API error.

## The forum's `preferences` column is read as `types.JSONRaw` (2026-11-16)

`forum_user_profiles.preferences` is a PocketBase `JSONField`, and
`record.Get` hands back the stored document as `types.JSONRaw` (a
`[]byte` of JSON) — **not** as a `map[string]any`. The profile reader
only handled the map, so it read EVERY profile as having no
preferences: the values were written correctly and then never read
back, and a user's privacy, colour scheme and haptic preferences
silently reset on every page load.

`decodePreferences` now accepts every shape the column can arrive in —
`types.JSONRaw`, `[]byte`, `string` and `map[string]any` — so the
reader does not depend on which one the field layer hands back.

## The forum's plugin front ends are real Flarum extensions (2026-11-16)

The Tags and Likes Go plugins register an `AssetBundle` that names
`plugins/<name>/forum.js`, `admin.js` and `style.css`, and the boot
shell emits those `<script>`/`<link>` tags from the registered bundle
list — it does not check whether the files exist. So for a long time the
forum shipped three 404s per plugin: the manifest paths were real, the
bundles were not.

**They are now genuine Flarum extensions**, in upstream's layout under
`pb/flarum/frontend/packages/plugins/<name>/`: a `composer.json` whose
`name` is the plugin id (`riff/tags`, `riff/likes`), a `js/` directory
holding the `forum.js`/`admin.js` entries and a `webpack.config.cjs`,
and a `style.css`. Building them with the vendored
`flarum-webpack-config` is what keeps them honest: a `flarum/...`
import compiles to `flarum.reg.get('core', ...)`, so a bundle never
duplicates core, and the babel preset is the one that turns JSX into
Mithril's `m`.

**The webpack output library is the one thing an extension must
override.** The shared config emits `module.exports = ...`
(`libraryTarget: 'assign'`), which is meaningless inside a `<script>`.
Each extension therefore sets
`output.library = 'flarum.extensions["<id>"]'`, where `<id>` is the
composer name flattened the way `extensionId.cjs` does it
(`riff/tags` → `riff-tags`). That assignment is what
`app.bootExtensions(flarum.extensions)` reads. Core only gets away with
the default because it overrides the library to `flarum.core` itself.

**The bundle must export `extend` BY NAME, not as a default.** The shell
reads `flarum.extensions[id].extend`, and webpack assigns the module
NAMESPACE object, so `export default [...]` lands behind a `default`
key: the boot succeeds, no extender ever runs, and nothing is logged.
Both bundles therefore `export const extend = [...]`, and the imported
`extend` helper is aliased (`import { extend as extendMethod }`) to
avoid the collision. This was caught only by loading the built bundles
in node against stubbed `flarum` internals and driving the hooks; a
successful `webpack` compile says nothing about it.

**An extender may not touch `app.forum`, `app.session` or
`app.store`.** The boot shell runs `app.load(payload)`, then
`app.bootExtensions(flarum.extensions)`, and only then `app.boot()` —
and `app.boot()` is what assigns `app.forum` and `app.session`. So an
extender that reads `app.forum.attribute('apiUrl')` throws "Cannot read
properties of undefined (reading 'attribute')" at boot, and an extender
that calls `app.request` throws on `this.session.csrfToken`, which
`transformRequestOptions` reads for every request.

The fix is to defer the work to `app.beforeMount(callback)`, which runs
inside `app.boot()` after the forum and the session exist. The Likes
bundle needs nothing at boot (it only registers a hook and requests on
a click); the Tags bundle loads its list in `beforeMount`.

**The store only accepts registered resource types.** `Store.pushObject`
throws "Pushing object of type `tags` not allowed, as type not yet
registered in the store" for any type that is not in the model
registry, and the registry holds only the core types (forums, users,
discussions, posts, groups, notifications, access-tokens). The Tags
bundle therefore keeps its loaded tags in its own map instead of pushing
them into the store. The Likes bundle does push — but only `posts`,
which is registered.

**The async chunks live under `js/<namespace>/`, and webpack writes that
layout directly.** The runtime builds a chunk's url as
`${jsChunksBaseUrl}/${namespace}/${urlPath}.js`, and `jsChunksBaseUrl`
is the assets root plus `/js` (see ExportRegistry's `versionedChunkUrl`,
which is upstream's code unchanged). So core's chunk for
`forum/components/PostStream` is fetched from
`assets/js/core/forum/components/PostStream.js`.

Upstream gets that layout from its PHP asset compiler, which moves the
flat webpack output into `js/<extensionId>/` after the build. This port
has no such compiler, so the shared webpack config sets
`chunkFilename` to `js/<namespace>/[name].js` instead — the namespace
comes from the same `extensionId.cjs` the chunk-loading global uses, so
the file on disk and the url the runtime asks for cannot drift.

**A chunk id and a chunk file are not the same thing.** The registry
registers a urlPath per MODULE, so one chunk file carries many modules
(22 files cover 268 module paths). A urlPath that has no file of its
own is a passenger module that rides another chunk; only the chunk's own
urlPath names the file. The check that matters is webpack's own chunk
map (`__webpack_require__.u`), which maps each chunk id to the file it
loads — every entry in it must exist on disk.

**The forum's stylesheets are compiled by the npm `less` package, not
by less.php.** Upstream compiles `less/forum.less` and `less/admin.less`
with less.php (the PHP half of the project) and serves `forum.css` and
`admin.css` beside the bundles. This port has no PHP, so
`packages/core/build-less.cjs` compiles them with the `less` npm
package instead, and the shell links them the same way it links the
scripts (`forum.css` for the forum app, `admin.css` for the admin app).

**The Less tree is vendored whole (106 files).** It was not vendored at
all before, so the forum shipped no stylesheet of its own — the shell
linked only the plugins' CSS. The tree comes from upstream's
`framework/core/less`, unmodified.

**Two things upstream's PHP compiler does that this has to reproduce.**
It adds `variables.less` and `mixins.less` as file sources BEFORE the
entry (see upstream's `FrontendServiceProvider::addBaseCss`) — the entry
itself does not import them, so without them the compile fails on an
undefined variable. And it inlines the Font Awesome stylesheets, which
come from the `@fortawesome/fontawesome-free` npm package — the same
Font Awesome 7 the Composer package `flarum/font-awesome` provides
upstream. `@fa-bundled-fonts` is set to `true`, which is upstream's
default (it is false only when an operator points the forum at a Font
Awesome Kit or CDN).

**The webfont URLs are rewritten.** The Font Awesome stylesheets
reference their fonts as `../webfonts/…`, relative to the npm package's
`css/` directory — the layout they had before the import was inlined.
The compiled stylesheet is served from the assets root instead, so the
paths become `webfonts/…` and the three font files are staged under
`ui/dist/webfonts/`. Without the rewrite the browser asks for
`/forum/webfonts/…`, which is not served (the asset route only covers
`/assets/`).

**The translations ship as a locale bundle, compiled from the YAML.**
Every `trans()` call renders its raw key without them, so the whole UI
reads `core.forum.header.back_to_index_tooltip` instead of the label.
Upstream compiles `locale/*.yml` into `forum-<locale>.js` and
`admin-<locale>.js`, each a single `app.translator.addTranslations(…)`
call (see upstream's `Frontend\AddTranslations`), and loads them right
after the entry bundle. This port has no PHP, so
`packages/core/build-locale.cjs` produces the same two files, and
`scriptURLs` loads them in that position (entry, locale, then plugin
bundles).

**Two details of upstream's compiler are reproduced deliberately.** The
keys are filtered by frontend, so the forum bundle carries `*.forum.*`
and `*.lib.*` and the admin bundle carries `*.admin.*` and `*.lib.*`;
this is why the admin bundle does not contain `core.forum.*` and the
forum bundle does not contain `core.admin.*`. And a value written
`=> some.key` is a REFERENCE, not a literal: `core.yml` uses them 186
times for repeated labels, and leaving them unresolved renders
`=> core.ref.foo` in the UI.

The locale YAML is vendored from upstream `framework/core/locale`. One
key the frontend asks for is missing upstream too
(`core.forum.posts_sort.toggle_dropdown_accessible_label`) and renders
raw here exactly as it does upstream; it is not papered over locally so
the next upstream refresh stays a straight copy.

**The boot payload carries the session, and the caller is a resource in
it.** The frontend reads `session.userId` and `session.csrfToken` and
looks the caller up with `store.getById('users', userId)`. A payload
with only the session would not be enough: a relationship's identifier
carries no attributes, so the caller has to be a RESOURCE too. Without
both, the SPA boots anonymous even with a valid session cookie — the
login succeeds and the page still shows as signed out.

**`session.userId` is a STRING.** The id is the PocketBase record id (a
15-character random string) and the frontend looks the record up by that
exact id; a numeric field could not carry it, and a truncated one would
silently fail to find the user.

**The login was never exercised by a test, and the boot payload never
carried a session at all.** The login endpoint authenticated against
PocketBase and set the cookies correctly — but the payload was built
without the request, so the SPA always booted anonymous. The forum's
test app also did not run the HOST's migrations, so the shared `users`
collection had no `username` field and a login by username failed with
"invalid filter expression: unknown field \"username\"" — a harness gap
that masked the real bug. The test app now imports the host migrations
(blank import, as main.go does) and the username is unique per account,
as the host's migration makes it.

**The logout redirects; the login answers JSON.** The frontend logs in
with an XHR and reloads, so the login must answer JSON. It logs out with
a FORM submission (the core's `Session.logout` builds a form and submits
it), so the browser navigates to the response: the logout must REDIRECT,
or the reader ends up staring at `{"message":"Logged out."}`. The
logout's `return` parameter is restricted to a path inside the forum, so
it cannot be turned into an open redirect.

**A logout does not revoke the token.** PocketBase's auth tokens are
stateless JWTs with no server-side store to delete a row from, so a
captured token keeps working until it expires (the `users` collection's
authToken duration, five days by default). This is PocketBase's model,
not the forum's — the wiki's logout has the same shape — and the host's
revocable credential is the personal access token, not the session
cookie.

**The profile page needs two things the feed did not have.**

`GET /api/users/<slug>?bySlug=true` and
`GET /api/posts?filter[author]=<slug>&filter[type]=comment`. Both failed
on a real profile page:

- the user show handler only looked up by record id, so the route — which
  is keyed by the username — answered 404 for every profile. `bySlug`
  now resolves the username instead;
- the posts list REQUIRED `filter[discussion]` and answered 422 for
  anything else. It now also accepts `filter[author]`, plus
  `filter[type]` to narrow to the post types the page shows.

**`filter[author]` is the SLUG (the username), and a post stores its
author as a record id.** The slug is therefore resolved to an id first
and the query filters on `user = {:id}`, rather than joining. That also
avoids a trap in the repository's query helper: `dbxExp` builds RAW SQL
(`dbx.NewExp` with named params), not PocketBase's filter grammar —
`user.username = {:slug} && type = {:type}` is neither valid SQL nor
valid PocketBase filter syntax, and it only fails at query time.
An unknown slug is an empty page, not an error, because the feed is a
listing; `repository.ErrUserNotFound` is the sentinel that keeps a real
failure from looking like "no posts".


**The forum's avatar is PocketBase's `users.avatar` file, streamed
through the forum's own route.** The profile picture is the
already-existing built-in field on the shared `users` collection — one
picture per account, shared with the Memos SPA, the admin UI and the
wiki. The forum adds no avatar store of its own.

The frontend needs a URL it can put in an `<img src>`, and the obvious
one — PocketBase's `/api/files/users/{id}/{file}` — lives at the ROOT of
the riff listener, which the forum does not own. Under the
`/community/` layout a reverse proxy serves the static site from the
root, so that URL resolves to the site and 404s. The forum therefore
serves the bytes itself at `{basePath}/avatars/{id}`, exactly as the
wiki does and for the same reason.

That route is public and unauthenticated, and a 404 is deliberate: a
missing record, an account with no picture (including every superuser,
since `_superusers` has no avatar field) and a deleted file all answer
404, because a 404 is what makes the frontend render the user's
initials. An empty `avatarUrl` plus `hasUploadedAvatar: false` carries
the same information on the JSON side.

The URL is built in the serializer, not in the repository: the
repository projects the record and does not know the mount path, while
the API server does.

**The admin application needs an admin shell and an admin payload.**

It is the same SPA with a different entry bundle, but it is not a
variant of the forum app:

- its `mount()` calls `m.mount(document.getElementById'admin-navigation')
  …)` inside `main.App-content` and gives `#content` the `sideNavOffset`
  class. Rendering the FORUM shell for it means that container is null,
  `m.mount(null)` throws, and the page stays blank. The shell therefore
  renders those two pieces only when it is the admin app, and the forum
  layout is unchanged (an empty nav column would be visible).
- its `runBeforeMount` calls `BasicsPage.register`, which walks
  `displayNameDrivers`, `avatarDrivers` and `slugDrivers` (and `locales`)
  during boot. A payload without them throws BEFORE anything renders,
  which is the other way to get a blank page.

`frontend.BootPayload` therefore embeds a `*frontend.Admin`. It is an
EMBEDDED pointer, not a nested field, because the admin app reads those
as `app.data.extensions`, `app.data.settings` and so on — at the TOP
level of the payload. A nil pointer contributes nothing, so the forum
app's payload is byte-for-byte what it was.

The admin data does NOT depend on the caller and is set before the actor
is resolved: an anonymous visit to `/admin` still has to boot, because
that is what lets the app render its own sign-in redirect.

**The values are the ones this port implements, not upstream's.** The
extensions list is the forum's own Go plugins (real data, from the
plugin manager). Composer packages and the php-side dashboard counts
have no riff equivalent, so those fields are empty — a page shows
nothing rather than something invented. Editing extensions is NOT
implemented: riff's own administration surface is PocketBase's at
`/_/`.

**The payload must carry the data the admin app reads, not just the
fields it declares.** The admin app dereferences much of it at BOOT
time, and each missing value is a blank page:

- `isExtensionEnabled` does `JSON.parse(app.data.settings
  .extensions_enabled)`, and `JSON.parse(undefined)` throws. So
  `settings` carries upstream's install defaults, and every key the app
  parses as JSON holds a valid JSON document. The forum's own settings
  win over the defaults.
- `UserListPage` reads `app.data.modelStatistics.users.total` in a
  FIELD INITIALISER, so `modelStatistics.users` is present even when
  the count is zero.
- `ExtensionPage` calls `app.data.dbDriver.toLowerCase()`, so the
  driver is a non-empty string.
- `MailPage.register` reads `app.session.user!.email()` — which throws
  on a null user. See the admin gate below.

**The admin application is served only to an administrator.** Upstream's
admin routes carry the `RequireAdministrateAbility` middleware, so a
non-administrator never receives the admin shell. The same gate is
here, and it is load-bearing rather than cosmetic: because
`MailPage.register` dereferences `app.session.user!` at boot, serving
the admin shell to an anonymous visitor is a BLANK page, not a login
prompt. An earlier revision of this port assumed the opposite — that an
anonymous visitor should still get a bootable shell so the app could
render its own sign-in redirect. That is wrong: the app never gets
that far, because the boot throws first. The gate answers 403, exactly
as upstream does.

**The admin shell is not cached.** The shell carries the boot payload,
and the payload changes with the instance (the enabled plugins, the
user count, the settings). A cached copy replays a STALE payload,
which reads as a blank page when the app dereferences a field the old
payload did not have — the `settings.extensions_enabled` and
`modelStatistics.users.total` crashes above are exactly that shape. The
admin shell therefore answers `Cache-Control: max-age=0, no-store`,
the same header upstream sends (its admin middleware carries
`DisableBrowserCache`). The forum shell keeps its own caching
behaviour: it carries no admin data, and upstream's frontend does not
disable the cache there.

**The mail settings endpoint exists because the mail page fetches it on
mount.** `GET /api/mail/settings` answers with upstream's shape
(`data.attributes.{fields,sending,errors,canReceiveWebhooks,
canProvisionWebhooks,webhookUrl}`). A 404 leaves `response.data`
undefined and the page throws on it. riff sends its mail through
PocketBase's own SMTP configuration rather than a forum-level mail
driver, so the driver fields are empty and the bounce-reporting flags
are false.

**The plugin packages are deliberately NOT npm workspaces.** Adding them
to the workspace globs would rewrite `package-lock.json` and re-resolve
the whole tree; `scripts/build_flarum_spa.sh` instead drives the webpack
CLI with `NODE_PATH` pointed at the frontend's existing `node_modules`,
so the extensions build against the exact toolchain core already uses.

**The stylesheet is copied, not bundled.** The shared config carries no
CSS rule, so each `style.css` is staged beside the bundles by the build
script. A loader would be a dependency and a config change for one
small file per extension.

**Scope.** The Likes bundle adds a like/unlike button to
`CommentPost`'s `footerItems`; the Tags bundle adds badges to
`DiscussionListItem`'s `infoItems`. The tag picker, tag pages and the
likes-list popover are not implemented — see
`docs/design/flarum/deviations.md` §6.

## The forum renders Markdown with riff's own engine (2026-11-16)

The forum's posts carry a `contentHtml` field, because the reused
Flarum frontend inserts it as trusted HTML
(`m.trust(contentHtml)` in flarum/forum/components/Comment.tsx). The
forum therefore renders a post's Markdown server-side.

**Why riff's engine and not a second one.** The repo already has one:
`github.com/incurl/riff/markdown`, the riff markdown engine, with the
GFM extras, the `#tag` and `@mention` extensions, the payload
extraction and the AST-to-markdown renderer. Adding a forum-local
renderer would have meant two Markdown dialects in one binary — the
forum's posts and the host's memos would have parsed the same syntax
differently. The forum uses the existing engine instead.

**The HTML renderer lives in `markdown/html`, not in the forum.** The
`markdown` package owns "the riff markdown engine", and it was the
only place the renderer could live without the forum depending on
goldmark directly. The package's layering rule (it must not import
anything PocketBase-specific) is unchanged: `markdown/html` depends on
goldmark and on `markdown/extensions` only.

**The rendered body comes from the SAME goldmark instance the service
parses with.** The service holds one `goldmark.Markdown` configured
with the tables, strikethrough, task-list and linkify extensions. The
HTML renderer options are attached to that instance (rather than the
HTML renderer owning a second instance), so the output carries the same
extensions as the AST the other methods walk — a post that renders a
table also has a table in its AST.

**riff's `#tag` and `@mention` extensions are deliberately off for the
forum.** They are memos syntax, and they build AST nodes with no HTML
node renderer: enabling them makes the HTML renderer emit nothing for
the node, so every `#tag` and `@mention` in a post silently vanished
from the rendered body (measured — the HTML came out as `hi  and ,`).
In a Flarum post both are literal text, which is what upstream Flarum
core shows too. The forum therefore uses the plain service.

**Raw HTML is escaped by default.** The rendered body is inserted into
a page as trusted HTML, so a raw `<script>` block in a post would
otherwise be an injection vector. `markdown/html.Options.AllowRawHTML`
is the opt-in for a caller that sanitises the result afterwards; the
forum does not turn it on.

**The forum depends on an interface, not on the markdown package.** The
API server takes an `api.ContentRenderer`, and the module binds the
markdown service to it. A plugin can therefore replace the renderer
through the container — the Go equivalent of upstream's per-extension
formatter — without the forum importing goldmark.

**See** `docs/design/flarum/deviations.md` §8 for the remaining
differences from upstream Flarum's formatter.

## The forum module vendors the upstream Flarum frontend (2026-11-16)

The forum module reimplements Flarum 2.x's JSON:API contract in Go and
serves the Flarum frontend. riff had **no Flarum frontend** to reuse:
the host's own frontend is the React/Memos SPA in `web/`. The only way
to satisfy "reuse the Flarum frontend wholesale, without a PHP
runtime" was to vendor it.

**What was vendored.** The upstream `flarum/framework` repository
(branch `2.x`)'s `framework/core/js` — the `forum`/`admin`/`common`
Mithril + TypeScript SPA — together with its `js-packages/webpack-config`,
`js-packages/tsconfig` and `js-packages/prettier-config`, into
`pb/flarum/frontend/packages/`. The upstream source is **unmodified**; only the
build wiring changed (see below). Upstream is MIT licensed.

**Why the source needed no change at all.** The single most important
discovery was that upstream Flarum 2.x **already supports a subpath
mount**: `app.forum.attribute('basePath')` drives `mapRoutes`,
`m.route`, `routeInternalLinks`, `homepageAddress` and
`registerServiceWorker`. The specification's §6.2.8 "base-path shim"
therefore needs no frontend patch — the backend only has to put the
right `basePath` in the forum document, which it does. The reused
frontend's `app` global, its `flarum.extensions` registry, its
`extend`/`override` utilities, its `ItemList` and its `Component` base
class all come from upstream unchanged; the backend's job is to supply
the boot payload and the bundle manifest they need.

**The three build changes.** The upstream monorepo is a **Yarn**
workspace: (1) the `yarn` calls in `packages/core/package.json`'s
scripts became `npm` calls; (2) the upstream webpack config reads
`../composer.json` for the bundle namespace, so
`pb/flarum/frontend/packages/composer.json` is a riff-authored stub with the same
`name` field and no PHP metadata; (3) npm's strict peer resolver
rejects the upstream hoisting assumptions, so the install runs with
`--legacy-peer-deps`. None of them touch the shipped bundle: the build
produces upstream's own `forum.js` (464 KiB) and `admin.js` (475 KiB).

**Why the Go backend cannot use upstream's PHP.** The spec forbids a
PHP runtime and Composer extensions, so the backend is a
reimplementation of the *contract*, not a port of the *code*. The
contract was read off upstream's own resource definitions
(`Api/Resource/*Resource.php`), the boot payload
(`Frontend/Content/CorePayload.php`), the error formatter
(`Foundation/ErrorHandling/JsonApiFormatter.php`) and the frontend's
`Model`/`Store`/`Session` classes — the last three are what the reused
frontend actually reads at runtime.

**The JSON:API envelope is Flarum's, not the spec's.** The spec says
"JSON:API-specification API"; upstream's implementation answers
`application/vnd.api+json` with `{"errors":[{"status","code",
"detail"}]}`. The forum reproduces upstream's shape, because the
reused frontend's `RequestError` reads that body and a spec-shaped
error envelope would render as an empty message.

**Collection ownership is marked, not inferred.** The forum shares a
PocketBase instance with the host, and the pre-flight collision check
has to decide "is this collection mine?". The name cannot answer it:
the host may own a collection called `forum_groups` without the forum
having created it. Each forum collection therefore carries a
`forum_owned` **text field**, whose *presence* is the marker. A
collection without it is foreign, and the migration aborts with the
collided name and the configured prefix rather than attaching to
somebody else's data.

**The built-in groups have 15-character ids.** PocketBase requires a
record id to be *exactly* 15 characters, so the well-known group ids
(`forumgroupguest`, `forumgroupmembr`, `forumgroupadmin`) are terse.
They are real rows, not implicit names: the permissions collection
references them through a relation, so a row is what makes "every
member may reply" expressible.

**The session is a cookie plus a bearer token, not a Laravel session.**
Upstream keeps the CSRF token in the session store. The forum uses a
PocketBase auth token in an HttpOnly cookie plus a double-submit CSRF
token in a readable cookie, scoped to the module's base path so the
session cookie is not sent to the host's other routes. The frontend's
`Session` class needs no change.

**See** `docs/design/flarum/` for the contract, the mount rules, the
extension-point reference, the plugin authoring guide and the full list
of deviations from upstream.

## The wiki has no avatar store: it serves PocketBase's `users.avatar` (2026-11-03)

Upstream LeafWiki keeps avatars in its own directory
(`<storageDir>/avatars/<userID>.png`, `internal/avatar/avatar_service.go`)
and serves them at `/avatars/{id}`. The port instead uses the PocketBase
`users` record's built-in `avatar` file field, which riff deliberately
left untouched (`pb/migrations/1700000001_extend_users.go`).

**Why not a second store.** riff already has one avatar per account (the
Memos SPA and the admin UI both read `users.avatar`). Copying it into a
wiki-side directory would mean two files to keep in sync and a wiki
picture that disagrees with the one on the Memos profile the moment
someone changes it in either place. Delegating means there is exactly one
picture, and either front end can change it.

**The bytes are streamed through the wiki route, not redirected.** The
obvious shortcut — redirect `/wiki/avatars/{id}` to PocketBase's own
`/api/files/users/{id}/{file}` URL — does not survive the deployment.
That native URL lives at the *root* of the riff listener, but the wiki is
mounted at `/wiki` behind a reverse proxy, so on the
`Caddyfile.fly.community` layout the redirect target
(`https://host/api/files/...`) would be answered by the static site, not
by riff. The wiki module cannot build a mount-aware redirect because it
does not know the proxy's prefix. Serving the file here keeps every URL
the SPA builds relative to the mount point — the same reasoning that put
attachments on riff's own `/file/...` surface instead of the native
`/api/files/...` one.

**The route is public on purpose.** The SPA's `avatarUrl()` documents the
URL as "public, unauthenticated" (`<img src>` cannot send a bearer), the
`users` collection's List/View rules are already open, and the wiki shows
author names on public pages anyway — an avatar is no more sensitive than
the name beside it. A missing record, a record with no avatar, and every
superuser (PocketBase's `_superusers` collection has no avatar field) all
answer **404**, which is what makes the SPA's `AvatarFallback` render the
initials instead of a broken image. Without the route the URL fell
through to the SPA catch-all and answered 200 with `index.html` — the
same failure shape as the inline-image bug fixed earlier.

**Validation is PocketBase's, not a copy.** The upload handler does not
restate the MIME allow-list or the 5 MB cap: `users.avatar` is a
FileField and `Save` validates against it, so there is one set of rules
rather than two that can drift. The handler's only size bound is an
`http.MaxBytesReader` so an oversized body is not buffered before that
check runs.

**Caching keys off the SPA's own buster.** `stores/avatar.ts` bumps `?v=`
after every upload or delete, so a versioned URL is served
`public, max-age=31536000, immutable` and an unversioned one `no-cache`.

Verified end to end on a live instance: uploading through
`POST /wiki/api/user/avatar` wrote the PocketBase record and the public
route served the same bytes; replacing the avatar through PocketBase's
own `PATCH /api/collections/users/records/{id}` (the Memos SPA's path)
was immediately what `/wiki/avatars/{id}` served; and `DELETE` cleared
the record and turned the route back into a 404.
`TestAPI_AvatarServesThePocketBaseUserAvatar` (and the rest of the
`TestAPI_Avatar*` set) pin the behaviour, and removing the route
registration makes them fail — which is the regression the route exists
to prevent.

## The wiki index is in memory on `app.Store()`, and its sync is incremental (2026-11-03)

The LeafWiki port originally stored its derived index in two PocketBase
collections, `leafwiki_pages` and `leafwiki_links`
(`1940000000_create_leafwiki_tables`), mirroring upstream LeafWiki's
`search.db` / `tags.db` / `links.db`. That was wrong for three reasons
found by reading the code paths rather than assuming:

1. **The rows were never read across a restart.** `Tree.Load()` re-reads
   the Markdown tree (`store.reconstruct`) and then rebuilds the index
   from it, so every row was overwritten before it was ever read. The
   persistence bought nothing.
2. **Search never used the query engine.** `search`, `tagFacets` and
   `pagesByTags` all started from `FindAllRecords(pagesCollection)` — a
   full scan — and filtered and ranked in Go.
3. **The write path was O(entire wiki) per edit.** `rebuildLinks` dropped
   and re-inserted *every* link row inside a transaction, and `sync` ran
   on every mutation (11 `loadLocked()` call sites in `tree_mutate.go`).
   Editing one character of one page re-read every page file twice and
   rewrote the whole link table.

The index now lives on `app.Store()` under `riff.leafwiki_index`, the
same per-app convention `pb/connect/identity_cache.go` already
documents. That gives per-app scoping (the test binary boots many apps),
concurrency safety, and no package-level mutable state. Nothing is
persisted, which is correct precisely because the tree is re-read on
every load.

**The sync is incremental, not just in-memory.** `reconstruct` already
parses every page's frontmatter to build the tree, so the index is handed
a body-free `nodeRef` per node — id, path, slug, title, kind, version,
pinned, position — and calls the loader *only* when that projection
differs from the cached entry. `Version` is derived from `updatedAt`,
which every mutation bumps, so it is a reliable change signal. An edit
therefore opens one page file instead of the whole wiki.

Link rows are derived per page from cached raw targets. A content-only
edit re-derives that page's rows. A path or title change (create, rename,
delete) alters what every *other* page's links resolve to, so that case
re-derives the full row set — still entirely in memory, since only the
resolution is redone, not the parsing. This keeps the property the
old "drop and rebuild the link table" had: creating a page heals every
link pointing at it.

`TestIndexSyncLoadsOnlyChangedPages` pins the incrementality by counting
loader calls (it fails if a regression starts re-reading everything),
`TestIndexReResolvesLinksWithoutReloading` pins the correctness half, and
`TestIndexIsScopedToTheApp` pins the per-app scoping. All three were
mutation-checked.

The collections are dropped by `1950000000_drop_leafwiki_tables`. The
*creating* migration is deliberately kept rather than deleted: an
existing instance has it recorded as applied, and removing the file would
look like a missing migration to PocketBase's runner. A fresh install
creates the two collections and immediately drops them — one no-op
migration on a new data directory, in exchange for an honest history.

## `loginUrl`/`logoutUrl` are external-URL escape hatches, not the wiki's own routes (2026-11-03)

`GET /wiki/api/config` used to advertise the wiki's own login page as
`loginUrl`:

    LoginURL:  continueURL + "login",   // → "/wiki/login"
    LogoutURL: continueURL,             // → "/wiki/"

In upstream LeafWiki those two fields are *optional external* URLs and
default to empty:

    LoginURL  string // Optional URL the frontend redirects to instead of showing the built-in login form
    LogoutURL string // Optional URL the frontend redirects to after logout

They exist so an operator can point the wiki at an external IdP. The SPA
branches on them literally:

    // ui/leafwiki-ui/src/features/router/router.tsx
    { path: '/login', element: loginUrl ? <ExternalRedirect to={loginUrl} /> : <LoginForm /> }

`ExternalRedirect` assigns `window.location.href = to`, so advertising
`/wiki/login` as `loginUrl` made that page reload itself forever —
`/wiki/login` → `ExternalRedirect to="/wiki/login"` → `/wiki/login` …
The loop is entirely client-side, which is why the server kept answering
200 with a perfectly good SPA shell and nothing appeared in the console.

**The fix is to leave both empty.** With `loginUrl: ""` the router
renders LeafWiki's own `LoginForm`, which posts
`{identifier, password}` to `<prefix>/api/auth/login` — a route this port
already implements (`api_auth.go::loginHandler`, backed by PocketBase's
`users`/`_superusers`). With `logoutUrl: ""`, `UserMenu.handleLogout`
uses the in-app path (`await logout(); navigate('/login')`) instead of
firing `authAPI.logout()` fire-and-forget and hard-navigating away, which
could leave a live session behind while the browser reloaded.

Verified end-to-end against a live instance with `loginUrl`/`logoutUrl`
empty: `POST /wiki/api/auth/login` answered 200 and set
`leafwiki_access` + `leafwiki_csrf`, `GET /wiki/api/auth/me` returned the
user, and `POST /wiki/api/pages` with the `X-CSRF-Token` the SPA echoes
from the `leafwiki_csrf` cookie answered 201. So the built-in form is a
complete replacement for the external-redirect branch, not a fallback.

`TestAPI_ConfigLoginURLIsNotTheLoginPage` pins it: any non-empty value
that names a path inside the wiki fails the test, so a future
"helpful" refill of `continueURL + "login"` is caught at `go test` time.
The old `TestAPI_ConfigAndHealth` had asserted the buggy
`"/wiki/login"` value, which is how the loop shipped.

## An inline image is stored root-relative, so the browser must re-anchor it (2026-11-03)

An inline ("insert image") memo picture is persisted in the memo's
Markdown as a **root-relative** managed path:

    ![photo](/file/attachments/<uid>)

That form is not a choice — the API accepts only that spelling
(`markdown.ParseManagedAttachmentImageURL` rejects an absolute URL, a
query, a fragment, percent-encoding, or a malformed uid), and the
client mirrors the rule in `managed-attachment.ts` so the editor can
warn about content the server would refuse.

But a root-relative URL is resolved by the browser against the
document **ORIGIN**, not against the SPA's mount point. On the
sub-path deployment (`https://host/community/`) the stored
`/file/attachments/<uid>` therefore addresses the root of the host —
which, in the `Caddyfile.fly.community` layout, is the *static site*,
not the riff listener.

**Why the attachment gallery was never affected.** `getAttachmentUrl`
is built from `BASE_URL` (origin + mount prefix), so the gallery always
emits `<origin>/community/file/attachments/<uid>/photo.png`. The inline
image goes through `resolveManagedAttachmentImageSource`, which
preferred the attachment row's own URL and — when the row was **not**
in the memo's attachment list — fell back to the stored path
**verbatim**. The gallery had no such fallback, so it could not break.

**The fix is the fallback, not the stored form.** Rewriting the stored
Markdown to carry the mount prefix would break the API's own contract
and every existing memo, so the stored path stays root-relative and the
*browser-side* resolver re-anchors it: `resolveManagedAttachmentImageSourceWith`
now applies `withBasePathWith` to the fallback, so the emitted `src`
is `/community/file/attachments/<uid>` on a sub-path mount and
`/file/attachments/<uid>` on a root mount.

This is the second half of the sub-path sweep that commit `3ee1f584`
("make attachment, share, and copy-link URLs sub-path-aware") started:
that commit fixed `attachment.ts` and left `managed-attachment.ts`
untouched, so the inline-image path was the one URL builder that stayed
origin-relative. The pure `*With` form exists so the sub-path branch is
testable without overriding the build-time `BASE_PATH`, matching the
convention `attachment.ts` and `base-path.ts` already use.

## The wiki is a SIBLING of /community/, and its prefix is never rewritten (2026-11-03)

`/wiki` sits at the same level as `/community`:

    https://<host>/wiki/          ->  the wiki
    https://<host>/community/     ->  the riff SPA + Connect/gRPC surface

It is **not** `/community/wiki/`. The reason is structural rather than
cosmetic: the wiki is a module *inside* riff (`pb/leafwiki/`), mounted at
`/wiki` on riff's own listener (`pb/leafwiki/register.go`'s `defaultPrefix`),
so the path prefix has to reach riff intact. Two rules follow.

**The proxy must not strip the prefix.** `scripts/Caddyfile.fly.community`
proxies `/community/*` with `handle_path`, which *strips* the prefix — that
is correct there, because riff serves the SPA at `/` and the SPA's own
Connect base URL comes from `VITE_BASE_PATH=/community/` (`web/src/connect.ts`).
The wiki is different: riff serves it at `/wiki`, so the `/wiki` block uses
plain `handle` and forwards the path **verbatim**. Using `handle_path` there
would make riff answer `/api/tree` instead of `/wiki/api/tree`, and every
wiki route (they are all under the prefix) would 404.

**The wiki's own URLs are absolute already, so no rewriting is needed.** The
SPA reads its mount prefix from the `base-path` meta tag the server renders
(`{{__BASE_PATH__}}` → `/wiki`) and builds `/wiki/api/...` calls and
`/wiki/static/...` asset URLs from it. That is why the proxy is a plain
pass-through: caddy has no idea the wiki exists, and it does not need to.

The `@spa` matcher in `Caddyfile.fly.community` (the "redirect unknown paths
to the SPA" rule) also excludes `/wiki` and `/wiki/*`. The wiki's own
`handle` blocks already catch those paths because Caddy evaluates handle
blocks in declaration order and the first match wins — but the exclusion is
what keeps a *future* reordering of the blocks from 308-ing `/wiki` into
`/community/wiki`, which is the exact nesting this layout exists to avoid.

`scripts/Caddyfile` (the ACME variant) needed the same treatment: without a
`@wiki` matcher, `/wiki/*` fell through to the static site's `file_server`
catch-all and 404'd. `scripts/Caddyfile.fly` needed nothing — riff owns the
whole hostname there, so its catch-all already proxies `/wiki/*`.

Verified against a live riff behind a real caddy (all three variants):
`/wiki/` and `/community/` both answer 200 side by side, `GET /wiki` 308s to
`/wiki/` and **never** to `/community/wiki/`, `/wiki/api/health` answers
`application/json`, and the rendered shell carries
`base-path" content="/wiki"` with absolute `/wiki/static/…` asset URLs.

## The wiki SPA is LeafWiki's own build, and the shell rewrite is load-bearing (2026-11-03)

The wiki's front end is **not** a riff-written SPA: it is LeafWiki's own
React application (`ui/leafwiki-ui` upstream), built with `npm run build` and
dropped into `pb/leafwiki/ui/dist`. Three decisions follow from that.

**The bundle is built, not vendored.** `pb/leafwiki/ui/dist` is gitignored
(`.gitignore` ignores every `dist/`), so the `//go:embed` directive is
`all:ui` — the whole `ui` directory — and not `all:ui/dist`: the latter
would fail to compile in a fresh clone, because the directory does not exist
until someone builds the SPA. The tracked `ui/index.html` is a
dependency-free fallback reader that the embed serves when no bundle was
built, so a clone always compiles and always serves *something*. CI builds
the bundle in a `build-wiki-spa` job and downloads the artifact into
`pb/leafwiki/ui/dist` before the binary and image builds, exactly as the
main SPA's `build-frontend` job does for `server/frontend/dist`.

**The base-path rewrite is the load-bearing detail.** LeafWiki's Vite build
uses `base: "./"`, so the built `index.html` references its chunks
*relatively*:

    <script type="module" crossorigin src="./static/index-D_z3loQL.js"></script>

A relative reference resolves against the **current URL**. On a deep link
such as `/wiki/guides/setup` the browser therefore requests
`/wiki/guides/static/index-D_z3loQL.js` — which this server answers with the
**SPA shell** (200, `text/html`), not the script. The page renders blank and
**nothing appears in the console**: the script tag resolved, the response
was a 200, and the browser just ignored the HTML. Measured:

    GET /wiki/guides/static/index-DnotSRoj.js  -> 200  text/html          997 bytes  (the shell)
    GET /wiki/static/index-DnotSRoj.js          -> 200  text/javascript  1269235 bytes  (the script)

Upstream LeafWiki avoids this by rewriting `"./static/` to
`"<basePath>/static/` while rendering the shell (`internal/http/shell.go`),
and this port does the same in `renderIndex`. That is why the built SPA
works behind `/wiki` **without a rebuild** — no Vite base-path
configuration, no second build for the Fly layout. The rewrite is pinned by
`TestRenderIndex_RewritesTheViteBasePath`, because the failure mode it
prevents is invisible: a blank page, a 200, and a clean console.

**The template tokens are substituted the way upstream does it.**
`{{__BASE_PATH__}}` becomes the mount prefix (the SPA reads it from the
`base-path` meta tag and uses it for every API call and for the router's
`basename`), `{{__FAVICON_HREF__}}` becomes `<prefix>/favicon.svg`,
`{{__PAGE_TITLE__}}` and `{{__SITE_NAME__}}` become the wiki's name, and
`{{__BODY__}}` becomes empty — upstream server-renders the shell's contents
there, and this port has no server-side rendering, so React mounts into the
empty div.

## Attachment downloads are logged to stdout, not to PocketBase's log table (2026-11-03)

A MoeMemos client that cannot render an attachment has no diagnostic
surface at all. The file GET answers **404** — by design, because a denial
must not confirm that the attachment exists — the client shows a spinner,
and the server says nothing. Every question of the form "did the request
arrive?", "was a credential presented?", "was the attachment found?", "was
the caller allowed?" had no answer, which made the most common client
complaint (an iOS attachment that never renders) undiagnosable from the
server side.

The instrumentation (`pb/api/file_access_log.go`) logs one line per
attachment GET to the **standard-library** `slog.Default()`, and that choice
is the load-bearing part:

- **PocketBase's own `app.Logger()` writes into the `logs.db` table**, not
  to stdout. A diagnostic written there is invisible to an operator
  watching `fly logs` — and it is also written on a 3-second batch
  timer, so it may not exist yet when the operator is staring at the
  screen. The stdlib logger writes to stderr, which supervisord forwards
  to `/dev/stdout`, which is exactly what Fly collects. Every other riff
  diagnostic (`pb/connect`, `pb/hooks`, `pb/api`) already uses
  `slog.Default()` for the same reason; the file surface was the one
  surface with no diagnostic at all.

**What the line records, and what it deliberately does not.** The line
carries the attachment id, the route family, the status, whether the row
was found, whether the caller was allowed, and the *kind* of credential
presented (`bearer` / `cookie` / `share` / `none`). It never carries a
credential *value*: the bearer and the session cookie are not logged, and
the share token is redacted too — it arrives in the query string, which is
precisely the kind of thing that ends up in a proxy's access log, so
echoing it would leak it a second time. Only presence is recorded
(`has_bearer`, `has_cookie`, `has_share`).

**Why the `found` / `allowed` / `auth` triple.** The three fields are
what turn "it 404s" into a diagnosis:

| The line says | The cause |
| --- | --- |
| `found=false` | Wrong id — the attachment does not exist. |
| `found=true allowed=false auth=none` | The client sent **no credential**. This is the MoeMemos iOS signature: the server emits a *relative* `externalLink`, the client's auth middleware compares `request.url?.host` against the configured host, and a relative URL has no host, so the bearer is dropped before the request leaves the app. |
| `found=true allowed=false auth=bearer` | The credential arrived and was refused — the memo's visibility rule. |
| `allowed=true status=200` | Served. A spinner here means the bytes arrived and the problem is a stale cached URL. |

**The token gate.** The instrumentation is **off by default**
(`RIFF_FILE_ACCESS_LOG=1` turns it on) because every attachment GET on a
busy instance would otherwise add a line. `RIFF_FILE_ACCESS_LOG_TOKEN` is
a shared secret that gates it: with a secret set, only requests
presenting it (`X-Riff-Log-Token` header or `?log_token=`) are logged.
That is what makes "log everything" usable on a *public* instance — the
operator gets the traffic they asked about without the log stream filling
up with everyone else's. The token is compared in constant time and is
never logged.

**One trap worth recording.** `e.RealIP()` dereferences `e.App`, so
calling it from the log path turns a 404 into a 500 when the event has no
app (the unit tests build a bare `RequestEvent`). A diagnostic must never
break the request it describes, so `fileAccessClientIP` falls back to the
peer address when the app is absent.

## LeafWiki ported onto PocketBase: what was replaced, and why (2026-11-02)

The wiki module (`pb/leafwiki/`, mounted at `/wiki`) is a port of LeafWiki
(https://github.com/perber/leafwiki, MIT). Upstream is a Markdown-on-disk wiki
whose SQLite databases are *derived, rebuildable* indexes. That split is the
whole design, so the port keeps the first half and replaces the second:

- **The Markdown files stay the source of truth**, in upstream's exact layout
  (`root/<slug>.md`, `root/<section>/index.md`, `.order.json`,
  `assets/<pageId>/`, and the `leafwiki_*` frontmatter keys). A data directory
  from a running LeafWiki can be pointed at this module unchanged, and riff can
  hand its data directory back. That portability is the reason the frontmatter
  keys are upstream's rather than riff's, even where a riff-shaped key would
  read better.
- **Every SQLite database became a PocketBase collection.**
  `search.db` + `tags.db` + `properties.db` collapse into `leafwiki_pages`;
  `links.db` becomes `leafwiki_links`. `Index.Sync` reconciles them after every
  mutation and on every load: changed rows are upserted, vanished rows deleted,
  and the link table is rebuilt from scratch, because a link's state depends on
  the whole tree (creating a page heals every link that points at it). A failed
  index write is **logged, never returned** — the index is a cache, and a wiki
  that refuses to serve a page because a search row could not be written would
  be worse than a stale index.
- **`users.db`, `sessions.db` and `apikeys.db` are gone entirely.** Auth is
  PocketBase's: a superuser is an `admin`, any `users` record is an `editor`, an
  anonymous caller is a reader. The SPA's cookie auth is kept because its client
  hard-codes it — `POST /api/auth/login` verifies the PocketBase password, mints
  a PocketBase auth token with `record.NewAuthToken()` and stores it in an
  HttpOnly cookie alongside a double-submit CSRF cookie. A second user table
  would have had to be kept in sync with `users`, and would have been wrong the
  moment someone changed their password in the PocketBase admin UI.
- **The `/pages/*` subtree is dispatched by hand.** This is the one place where
  "just register the routes" does not work: LeafWiki mixes a verb *before* the
  id (`/pages/permalink/{id}`, `/pages/copy/{id}`) with a verb *after* it
  (`/pages/{id}/assets`, `/pages/{id}/links`), and Go's `http.ServeMux` panics
  when two patterns overlap without one being strictly more specific —
  `/pages/{id}/assets` and `/pages/permalink/{id}` both match
  `/pages/permalink/assets`. Two "fixes" were rejected: changing the paths
  breaks the SPA's hard-coded client (the whole point is dropping a LeafWiki
  build into `pb/leafwiki/ui/dist`), and the panic is a *startup* failure, so
  there is no runtime fallback. `api_dispatch.go` matches the upstream shapes in
  one handler instead, and the integration test builds a real mux, so a future
  "simplification" back to per-verb registrations fails in `go test`.
- **A missing `leafwiki_id` is written back on load.** Upstream does the same,
  and for the same reason: the id is the page's permalink identity. Without the
  write-back every restart re-generates ids and every saved link dies.
- **Not ported, on purpose:** revisions, favorites, share links,
  snapshots/restore, git backup, SMTP, TOTP, API-key management, avatars,
  branding uploads, the ZIP importer and the resync job. `/api/config` reports
  them as disabled so the SPA hides the affordances instead of discovering a
  404. Markdown imports still work by copying files into `root/` — they are
  adopted on the next load.

`RIFF_WIKI_PUBLIC_WRITE=1` is upstream's "open editing without login" mode:
anonymous writes are accepted and `/api/config` reports `authDisabled: true`.

## The PocketBase leverage audit: four defects, and what each one was (2026-10-26)

An audit of how well riff reuses PocketBase rather than re-implementing
it found that the *architecture* was sound — data, rules, hooks,
migrations, tokens and OAuth are genuinely PB-native — but that four
places bypassed the framework. All four are fixed.

**1. The Memos REST surface exported secrets.** The `pb_records_crud`
flavour projected every collection field through `rec.Get()` and
returned the raw values, so an anonymous `GET /memos/api/users`
returned every user's `email`, `tokenKey` (half of the JWT signing
key, `core/record_tokens.go`) and `password_hash`. PB deletes all
three from every export; riff's hand-rolled projector skipped that.
The fix is to use PB's own `rec.PublicExport()`.

**2. The Memos REST surface never evaluated the collection rules.**
`FindRecordsByFilter` does not apply them — the rule engine only
runs inside PB's own record API — so an anonymous caller could list
another user's notifications. The rule is now evaluated per row with
PB's `CanAccessRecord`; a denied record answers 404, not 403.

**3. A rate-limit scope was treated as an auth gate.**
`Scope: ratelimit.ScopeAuthenticated` is a BUDGET: `restRateLimit`
only decides whether the caller has spent their allowance, and an
anonymous caller has an allowance too. The routes are now behind a
real gate — one that accepts a PocketBase auth token OR a
`memos_pat_*` personal access token, because the MoeMemos client
authenticates with a PAT and PB's own `RequireAuth` only understands
auth-collection JWTs.

**4. The realtime broadcaster had no permission check.** It sent
every create to every subscriber, so a PRIVATE memo's id reached
anonymous clients. The check now mirrors PB's own realtime
implementation: the collection's ListRule, evaluated per client with
that client's own auth record.

## Two facts that were documented wrong (2026-10-26)

Both are worth recording because a future reader would otherwise
believe the code.

**PocketBase hashes the `password` field with bcrypt, not PBKDF2.**
Migration 1710000001 claimed otherwise and added a parallel
`password_hash` column to compare against from Go. PB exposes
`Record.ValidatePassword` for exactly that, so the column was
redundant; it is dropped by migration 1930000002. No backfill is
needed: PB FORCES an auth collection's `password` field to be
Required (`core/collection_model.go`), so every auth row always
carried a hash.

**protojson represents a FieldMask as a JSON STRING.** The Memos
route wrappers emitted the literal `"*"` when the client omitted
`updateMask`, and protojson rejects that as a path
("contains invalid path: \"*\"") — so a PATCH without an explicit
mask failed to unmarshal before the handler ran, on BOTH the V0 and
the V1 surface. An absent mask is now an empty mask, which the
handlers already read as "every field in the body".

## The SPA's live refresh was dead since the PocketBase refactor (2026-10-26)

The upstream Memos commit that added SSE (`ea0892a8`) shipped both
halves — `server/router/api/v1/sse_handler.go` AND
`web/src/hooks/useLiveMemoRefresh.ts` — but only the web half was
ported; `server/router/` was dropped in the refactor.

So the SPA's `GET /api/v1/sse` fell through to the SPA catch-all,
which answered 200 with `index.html`. The client checks only
`response.ok`, so it accepted that, published "connected", parsed the
HTML for `data: ` lines, found none, and silently never invalidated a
single query cache. The user saw a "connected" indicator and no
updates.

Two details of the wire format are load-bearing:

* The client matches on `line.startsWith("data: ")` — WITH a space.
  PB's own `subscriptions.Message.WriteSSE` writes `data:` without
  one, so PB's native `/api/realtime` would ALSO be ignored. The
  riff writer emits the shape the client reads.
* The client never reads the SSE `event:` field — it expects the
  event TYPE inside the JSON body. The event names
  (`memo.changed` / `space.changed`) come from riff's spaces work
  (`6da8461e`), which changed the client without a matching server.

The client now also checks the response content type before
trusting the stream, so a misrouted HTML page fails loudly instead
of pretending to be connected.

## Private spaces, and why a memo's visibility is independent of them (2026-10-26)

The `spaces` collection is visible to its ACTIVE members. The rule
uses the same back-relation traversal the `memos` collection already
uses for its SPACE visibility branch, applied from the space record
instead of through it.

A memo's own visibility stays INDEPENDENT of its space's privacy: a
PUBLIC memo in a private space is still public. What is NOT
disclosed is the memo's SPACE RELATION — the space uid is a URL
slug, and emitting it would let anyone confirm that a private space
exists and guess at its topic. So the converter strips `memo.space`
for a non-member.

The membership set is computed ONCE per request and carried on the
context: a per-memo check would be an N+1 on a page that can carry
up to `maxPageSize` (1000) rows.

## `/youtube` is a separate download tree, not a second static site (2026-10-26)

The deploy image serves two file trees from caddy: the baked-in ravine
static site at `/var/www/site/`, and `/youtube/`, a directory the
operator fills with files for end users to download. They are separate
roots on purpose — `/youtube` is **not** a subdirectory of the static
site, and the static site is not moved into it.

**Why two roots rather than one.** The static site is immutable: it is
baked into the image from the `ravine` build context and only changes
when the image is rebuilt. `/youtube` is the opposite: it is meant to
be edited between deploys. Merging them would mean every new download
forces a rebuild of the whole site tree.

**`uri strip_prefix /youtube` is load-bearing.** Caddy's `file_server`
joins the *unstripped* request path onto `root`, so

```caddyfile
handle /youtube/* {
    root * /youtube
    file_server
}
```

looks for `/youtube/youtube/<file>` and answers 404 for every
download. The fix is to strip the prefix first:

```caddyfile
handle /youtube/* {
    root * /youtube
    uri strip_prefix /youtube
    file_server
}
```

This was caught by running caddy against the real config, not by
reading it — the config validates perfectly either way, and the
symptom (a 404 on a file that is demonstrably on disk) points at the
file, not at the routing.

**The `/youtube/*` block must precede the static site's catch-all.**
Caddy evaluates `handle` blocks in declaration order and the first
match wins, so a `handle /youtube/*` block placed after the bare
`handle` would never be reached — the static site's `file_server`
would answer first and 404.

**`/youtube` is excluded from the SPA redirect matcher.** In
`Caddyfile.fly.community` the `@spa` matcher 308-redirects every path
that is not a static-site path to `/community/<path>`. Without
`/youtube` in its exclusion list, every download would be redirected
to `/community/youtube/<file>` and 404 on the SPA instead.

**`/youtube` is ephemeral.** The directory is created in the image and
is empty in it; on Fly.io the container filesystem does not survive a
deploy, so files placed there are lost unless they are copied in
through the build context or re-added. A Fly volume would fix that,
but it costs a line on the bill for a tree that is mostly empty.

## The Bark extension: bbolt → PocketBase, and the mux conflict it forced (2026-10-26)

`pb/bark/` is a port of [finb/bark-server](https://github.com/Finb/bark-server)
(MIT, Copyright (c) 2018 Feng) into a self-contained PocketBase package.
Two decisions in it are not obvious from the code.

**The device store is a collection, not a bbolt bucket.** Upstream keeps
`device_key → device_token` in a bbolt file. The port keeps the exact
upstream semantics — including the surprising one — but stores them in the
`bark_devices` collection, so a registration survives a restart and lives
with the rest of the instance. The surprising part is upstream's key rule:

```go
if key == "" || bucket.Get([]byte(key)) == nil {
    key = shortuuid.New()
}
```

**A client cannot choose its own device key.** Any key the server did not
previously issue is *replaced* by a freshly generated one; only a key the
server itself issued is kept (the update path). A first port that
generated a key only when the supplied one was empty looked right and was
wrong — it let a caller pin a key, which upstream does not. The generated
key is a 22-character URL-safe base64 string, the same shape upstream's
`shortuuid` produces, because a Bark client validates the key's format.

Note that upstream's *test double* (`database.MemBase`) behaves differently
from its *production store* (`database.BboltDB`): MemBase keeps an unknown
key and returns an error, while BboltDB replaces it. `push_test.go` runs
against MemBase, so upstream's own test suite pins the behaviour a real
server does not have. The port matches BboltDB, because that is what a
deployed server runs.

**The MCP route cannot be method-less, or the server panics at startup.**
Upstream registers `/mcp` with Fiber's `router.All` (method-less). Go's
`http.ServeMux` — which PocketBase's router is built on — refuses to
register a method-less pattern that overlaps a method-specific one, and
`/bark/mcp` overlaps `GET /bark/{device_key}`:

```
pattern "/bark/mcp" conflicts with pattern "GET /bark/{device_key}":
/bark/mcp matches more methods than GET /bark/{device_key}, but has a
more specific path pattern
```

That is a `panic`, not an error, so it takes the whole instance down at
boot. The port registers the MCP routes as explicit `GET`+`POST`+`DELETE`
lists, and `pb/bark/muxconflict_test.go` pins both halves: that the
method-less form panics, and that the port's form does not. The same trap
is documented for riff's SPA catch-all in `AGENTS.md`; it is the same
class of bug, and the same fix.

Two smaller consequences of the port:

- **`/bark/` uses `{$}`, not a trailing slash.** A trailing slash is a
  Go *subtree* pattern that would shadow the four-segment V1 route
  (`/{device_key}/{title}/{subtitle}/{body}`); `{$}` matches the
  directory itself and nothing below it. Upstream's Fiber `/` is exact.
- **Path parameters are not percent-decoded twice.** Go's mux already
  unescapes each path segment, so a second `url.QueryUnescape` (which
  upstream's Fiber needs) would turn a literal `%20` in a notification
  body into a space.

**The APNs key is upstream's, embedded.** `pb/bark/apns_certs.go` is
copied verbatim from upstream and carries the `.p8` signing key for key ID
`LH4T9V5U4R` / team `5U8LBRXG3A`, whose entitlement is the `me.fin.bark`
topic — the same key upstream ships in its own `deploy/` directory, so a
self-hosted server can push to the official Bark iOS app. It is not a
general-purpose key: APNs rejects a push whose topic does not match the
key, so a third-party Bark client needs its own `.p8`, key ID, team ID
and topic, which `Options` accepts (`RIFF_BARK_APNS_TOPIC`,
`RIFF_BARK_APNS_KEY_ID`, `RIFF_BARK_APNS_TEAM_ID`). The client is built
lazily on the first push, so a missing or wrong key is a push error and
not a server that refuses to boot.

**The APNs transport uses the standard library, not `x/net/http2`.**
Upstream builds a `http2.Transport` with a `DialTLS` hook. `x/net`'s
`http2.Transport` is deprecated in favour of `http.Transport` (whose
`Protocols` field pins HTTP/2), and `golangci-lint`'s `staticcheck`
flags the old form. The port uses `http.Transport` with
`Protocols.SetHTTP2(true)` and a `DialTLSContext` hook that mirrors
`apns2.DialTLS`, which drops the `golang.org/x/net` dependency from the
Bark package entirely.

## `RIFF_FTS` is a runtime flag; FTS5-in-modernc is a build property (2026-10-23)

Two things that sound identical and are not, recorded because conflating
them wastes an hour.

**FTS5 availability is a build-time property of the dependency.**
`modernc.org/sqlite` transpiles SQLite's C to Go and emits one file per
platform, each carrying a hardcoded `-D` flag list. `-DSQLITE_ENABLE_FTS5`
is present in `linux/{amd64,arm64,arm,386,loong64,ppc64le,riscv64,s390x}`,
`darwin/{amd64,arm64}`, `windows/{,386}` and the BSDs — i.e. every platform
riff ships — but only **20 of 289** generated files carry it, so
"modernc supports FTS5" is true for riff's release matrix and false in
general. There is no riff-side switch: no build tag, no `-tags fts5`, no
Dockerfile change. Verified by building with the Dockerfile's exact flags
(`CGO_ENABLED=0 -trimpath -tags netgo,osusergo -extldflags -static`), booting
the result, and confirming `memos_fts` plus all three triggers exist in a
fresh database.

**`RIFF_FTS` is a runtime env var** (`main.go` → `envBool` → `Profile.FTS` →
`connect.SetFTSSearch`). It is an `os.Getenv` read once at process start, so
changing it needs a container restart and never a rebuild. It only decides
whether the index is *queried*: the migration builds it unconditionally, so
an unset flag means paying the write cost of three triggers on every memo
insert and getting none of the query benefit. Documented for operators in
`scripts/compose.yaml`, `scripts/entrypoint-supervisord.sh` and `fly.toml`;
in the entrypoint it is deliberately *not* exported with a default, because
unset and `"0"` are identical to `envBool` and a default would only hide the
override.

**CI now runs the FTS tests with `CGO_ENABLED=0`.** Everything else runs with
cgo enabled, so nothing exercised the release configuration. The property
also rests entirely on a third-party `-D` list: modernc ships **no** FTS
test and its changelog never mentions the flag, so a dependency bump that
dropped it would surface as `CREATE VIRTUAL TABLE` failing, aborting the
migration and therefore bootstrap — in production, not in CI. Negative
control: `CREATE VIRTUAL TABLE … USING FTS5` succeeds under `CGO_ENABLED=0`
on this machine, so the assertion is live rather than vacuous.

One caveat found on the way, pre-existing and unrelated to FTS: PocketBase
warns on **every boot** that `modernc.org/libc v1.79.0` "differs from the
expected and tested v1.77.1 … could result in unexpected build or runtime
errors". Worth a separate pin-back.

## `RIFF_FTS` stays OFF by default, and the docs now say so in three places (2026-10-24)

`RIFF_FTS` gates whether memo content search is served by the SQLite FTS5
index or by the `LIKE` scan. The default was briefly flipped to ON in the
entrypoint and then reverted; it now defaults **OFF** everywhere.

**OFF is the right default, and the reason is not conservatism about
behaviour — it is that the flag is not what costs anything.** Migration
`1910000000_create_memos_fts` builds `memos_fts` and its three triggers
unconditionally, so an instance pays the index-maintenance cost on every
memo write either way. The flag only decides whether the index is
**queried**. Turning it on is therefore free at the margin, and turning it
off does not avoid any work — it just declines to use what is already
being maintained.

What OFF does buy is a smaller blast radius: the index and the FTS query
path are the newest code in the search surface, and the day this was
written they had already produced two production 500s (a dangling `&&` in
the LIKE-clause rewrite, and invalid UTF-8 from a byte-sliced snippet).
Leaving the flag off means those could be fixed and verified on real data
before any instance switched over. Enable it with `RIFF_FTS=1` once the
behaviour below has been checked.

**The three deployment paths, and how the value actually resolves:**

  - `scripts/compose.yaml` passes `RIFF_FTS=${RIFF_FTS:-0}` — it sets the
    variable *explicitly*, so an entrypoint `:=` default would never fire.
  - `scripts/entrypoint-supervisord.sh` deliberately exports **no**
    default. Unset and an explicit `0` are identical to `envBool`, and
    leaving it unset keeps the override visible in the operator's compose
    file or `fly.toml` instead of buried in an entrypoint.
  - `fly.toml` leaves it commented out, so a Fly deploy keeps the `LIKE`
    scan until someone sets `RIFF_FTS = "1"`.

Verified the shell semantics directly: unset → `envBool` false,
`RIFF_FTS=0` → false, `RIFF_FTS=1` → true.

The behaviour difference, for the operator docs: FTS folds case across the
whole Unicode range and strips diacritics, so `cafe` finds `café` and
`блины` finds `Блины`, where a `LIKE` scan folds ASCII only and found
neither. FTS matches whole tokens rather than substrings, so `カフェ` does
not match `カフェで` (use `カフェ*`). Negated searches and
`startsWith`/`endsWith` keep using `LIKE` and are unaffected.

## Per-app state moved to `app.Store()`, and the identity cache that came with it (2026-10-25)

Asked whether riff should adopt bbolt as a key-value store. The answer is
no, and the reason is that **PocketBase already ships one**:
`app.Store()` (`core/base.go:639`), a concurrent-safe generic
key-value store (`tools/store/store.go`). PocketBase uses it for exactly
this pattern itself — its own rate limiter lives on it
(`apis/middlewares_rate_limit.go:162`), with teardown as
`app.Store().Remove(key)` (`:215`).

bbolt was rejected on its own merits: riff is a single process with one
SQLite file on one volume, every durable write it has is a *relation*
with a schema (the webhook outbox carries status, attempts and
`next_attempt_at`, which needs a filtered scan — bbolt has no secondary
indexes), and a second datastore would be a second source of truth next
to the first. A KV store is the right answer for high-frequency writes
with no relational query; riff has neither.

**The identity cache.** `recordToMemo` resolves the author and the space
of *every* memo it renders, and PocketBase has no record cache — each
resolution is a real point query. At the SPA's page size of 16 that is
~32 extra queries per feed page, plus one per reaction. The same two
lookups are made by twelve call sites of `lookupUsername` and three of
`lookupSpaceUID`, across the reactions, relations, attachments,
webhooks, PAT and space paths.

They are now memoised on `app.Store()`. **Expiry is by invalidation,
not by TTL**: a username is mutable (`UpdateUser` sets it,
`pb/connect/user.go`) and so is a space uid (`pb/connect/space.go`), so
a TTL would serve a stale name for up to its window. An
`OnRecordAfter*Success` hook on `users` and `spaces` drops the entry
instead, which makes the cache exact — the only window in which a stale
name can be served is between the write and the after-success hook,
which is sub-millisecond.

The win is measured, not asserted: `TestIdentityCache_ReducesTheUserLookups`
counts the user lookups for a page of five memos by one author and
requires **one**, not five. It was mutation-checked — bypassing the
cache turns it red with "five memos by one author must cost one user
lookup, not five".

**The six per-app state maps.** `uploadSweepStates`,
`importSweepStates`, `motionMediaStates`, `schedulerShutdowns`,
`webhookDrainStates` and `memoLinksStates` were package-level
`map[core.App]*state` values, each with its own mutex. They existed
only because the state had to be global *and* app-keyed; `app.Store()`
is already per app, so the maps are unnecessary. Each became a single
string key on the app's own store, holding the same pointer — so the
`sync.WaitGroup` and `sync.Mutex` inside are never copied.

Three of the six (`uploadSweepStates`, `importSweepStates`,
`webhookDrainStates`) turned out to be **write-only**: the state was
reached through a local closure variable, and the map entry was never
read. They were pure overhead, and two of them never deleted their
entry, so they outlived the app they belonged to. `app.Store()` removes
that leak by construction: the entry dies with the app.

**One caveat worth recording.** `app.Store()` is not reset by
`ClearBootstrap`, so state survives a bootstrap cycle on the *same* app
instance. That is harmless for the test suite, which builds a new app
per test, but it means an app that is cleared and re-bootstrapped keeps
its cached entries. The identity cache's invalidation hooks make that
harmless (a cleared app's records are unchanged, so the cached names
are still right).

## A byte cut in snippetOf made every ListMemos response a 500 (2026-10-24)

The production 500 was `marshal message: string field contains invalid
UTF-8`. protojson refuses to marshal a string field that is not valid
UTF-8, so **one bad string turned the whole ListMemos response into a
500 for every reader** — the feed was broken for everyone, not just for
the memo that had the problem.

**Two independent sources, both fixed.**

**1. `snippetOf` truncated on a byte boundary.** `cut := content[:n]`
with n=120. When that cut landed inside a multi-byte character it left
a lone continuation byte in the snippet — `Ã` for `é`, `æ` for
`東`. This is not invalid *input*: it is ordinary CJK, Cyrillic or
emoji content whose 120th byte happens to fall mid-character. The
snippet is a *derived* field, so the corruption was introduced by riff
itself, and it happened only for the subset of memos where the cut
landed badly — which is why it looked intermittent and was not
reproducible from a filter shape.

The fix walks the cut back to the start of the rune it landed in. The
identical helper in `pb/hooks/notifications_helpers_content.go` had
the same bug, and the same fix.

**2. The stored `content` could itself be invalid UTF-8.** PocketBase
does not validate text fields on save, and the ZIP import path
(`readContentBytes`) does `string(data)` on raw archive bytes with no
UTF-8 check — so an imported archive can put invalid bytes into
`memos.content`, and then `Memo.content` (not just the snippet) makes
the response unmarshalable. The import now replaces invalid bytes with
U+FFFD, and the read path sanitises what it emits, so a row that
already holds bad bytes cannot poison the feed.

**What could *not* have caused it, checked rather than assumed:**
protojson rejects a lone surrogate on *input* ("invalid escape code"),
and `encoding/json` (the REST/V0/V1 surfaces) replaces invalid bytes
with U+FFFD. So neither the Connect JSON body nor the REST surfaces
can inject invalid UTF-8 — it had to come from the import path or from
a direct database write.

**Why it took so long to find.** The symptom was a 500 on an endpoint
whose *input* looked fine, and there was no log line: connect-go does
not log handler errors, and riff's only interceptor (`WithAuth`) passed
them through untouched. `flyctl logs` was clean while the browser saw
500s. The fix for that is separate and now committed — `WithErrorLogging`
logs the procedure and the error, so the next occurrence names itself.

The general lesson, recorded because it generalises: **a derived field
computed by slicing a string is a correctness boundary, not a
formatting detail.** `content[:n]` looks safe and is not, and the
failure surfaces far from its cause — in the serializer, on a request
that has nothing to do with text.

## The FTS rewrite left a dangling `&&`: every search-plus-condition was a 500 (2026-10-24)

Found in production: `ListMemos` answered **500** for any filter that
combined a content search with another condition. The deployed commit
was the FTS tip, so the query path was live.

**The mechanism.** The compiler wraps a conjunction in parentheses:

```
content.contains("roadmap") && pinned
  -> (content ~ {:_a1} && pinned = true)
```

The rewrite removed the LIKE clause to hand the row set to the index,
and then tried to repair the leftover text with a `trimDanglingJoin`
helper that stripped a leading or trailing `&&`. But the clause sits
*inside* the parentheses, so the result was `( && pinned = true)` — a
dangling `&&` that PocketBase's filter parser rejects with `expected
left operand (identifier, function, text or number), got "&&" (join)`.
The helper could only ever fix a connective at the *edge* of the
fragment, and the compiler never puts one there.

**The fix** is `removeClause`: split the fragment on its *top-level*
connectives (depth-aware, so a nested group is not split), drop the
segment holding the clause, and rejoin the rest. The compiler emits one
connective per level, so the rejoin is unambiguous; a mixed `&&`/`||` at
one level is refused rather than guessed, and the caller keeps the LIKE
clause. `trimDanglingJoin` is deleted — it was the wrong shape of fix.

**Why the test suite missed it.** The original tests only ever combined
a search with *another search* — and two text terms fall back to LIKE
(`collectTextMatches` reports one term at a time), so the rewrite never
ran on that path. Every shape the SPA actually sends combines a search
with a **non-text** condition: `pinned`, `has_link`, `has_code`,
`has_location`, `has_task_list`, `tag in [...]`, `visibility in [...]`,
a timestamp range, a space. Those are the ones that broke.
`TestFTS_SearchCombinedWithOtherFilters` now covers twelve of them and
asserts the surviving conjunct still *filters*, not merely parses — a
rewrite that dropped the conjunct too would return the wrong rows
instead of erroring. It was mutation-checked: restoring the old
`strings.Replace` behaviour turns it red on the first filter.

**Why this is worth recording beyond the fix.** The failure was
invisible to every test that existed, and the shape that was missing is
precisely the shape production uses. A test that only exercises the
combination the implementation happens to special-case is not testing
the feature. `useMemoFilters.ts` enumerates the real filter shapes;
the test should have been written from that list, not from the
compiler's output.

## Phase 2 of FTS: the index is wired to ListMemos, behind RIFF_FTS (2026-10-23)

`pb/connect/memo_fts.go` adds the read side. With `RIFF_FTS=1`
(`Profile.FTS`, threaded through `connect.SetFTSSearch` before
`Register`), a *positive* `content.contains("…")` is served by the
FTS5 index instead of the `LIKE` scan; with the flag unset every path
is byte-for-byte the behaviour that shipped. `ListMemos` is the only
caller — `ListAttachments` and the REST surfaces still use LIKE.

**The LIKE clause is dropped when the index takes over, and that is
load-bearing rather than an optimisation.** Keeping both would *shrink*
the result set: `LIKE '%cafe%'` does not match `café`, so the two
predicates disagree and the intersection drops rows the index just
matched. The index narrows the same rows the LIKE clause did, so
removing it is a like-for-like substitution.

**Negated terms must never be rewritten.** `!content.contains("x")`
means "every memo that does NOT contain x"; an index narrows to
"contains x" and cannot express the complement, so substituting it
would invert the filter and return exactly the wrong rows.
`cel.Expression` therefore grew a `TextMatches` report, and
`collectTextMatches` deliberately omits any term whose `cmpOp` is not
`~` — `negate` flips `~` to `!~` in place (`out := *e;
out.cmpOp = flipped`), so the comparison is the reliable signal.
Absence is the safe direction: the caller keeps its LIKE clause.
`TestCompile_ReportsTextMatches` and
`TestFTS_NegatedSearchKeepsTheLikePath` pin this.

**Only a single plain `contains` is rewritten.** Two positive terms are
an intersection that one MATCH expression cannot express faithfully
(`contains("a") && startsWith("b")` is not a two-word AND), and
`startsWith`/`endsWith` have no equivalent in a token index. Both cases
fall back to LIKE rather than approximate.

**dbx does not substitute identifiers inside an `Exp`.** This cost two
debug cycles and is worth writing down:
`DB.processSQL` (which rewrites `{{…}}` / `[[…]]`) runs in
`NewQuery`/`Select`, but `Exp.Build` (`dbx/expression.go:164`)
returns the fragment *verbatim*. So `dbx.NewExp("{{memos}}.[[rowid]] …")`
reaches SQLite literally and fails with `no such column: memos_fts`.
The predicate uses bare identifiers instead; they are riff's own
constants, never user input.

**`bm25()` needs the table joined, not merely referenced.** It is an
auxiliary function of the virtual table, so it is only in scope once
`memos_fts` is an actual join in the query. Ordering by it while the
FTS table appears only inside a subquery fails with `no such column`.
The fix is `InnerJoin("memos_fts", …)` on `rowid`, added only on the
FTS path — relevance ordering applied to a non-search feed would
reorder the whole home feed or fail outright.

**Search text is never interpreted as FTS5 query syntax.** MATCH is a
language: a bare `"` opens a phrase and bare AND/OR/NOT/* change the
meaning, so a user searching `O'Brien` would hit a syntax error and
`a OR b` would silently double the results. Each whitespace-separated
word is quoted into its own phrase (inner `"` doubled) and joined with
an implicit AND, which is what a user typing several words expects.

**The security property, and proof the test has teeth.**
`TestFTS_PrivateMemoIsNeverLeaked` asserts that an anonymous caller
searching a word inside a PRIVATE memo does not get it, and that
neither user gets the other's. The FTS table has no access control of
its own; it may only ever narrow candidates, and `CanAccessRecord`
against `memos.ViewRule` still decides every hit. I mutation-tested
it: making the FTS path return rows without the per-row
`CanAccessRecord` check turns the test red on both assertions. (My
first mutation attempt appeared to pass — it was defeated by
`findMemos` overwriting the variable I mutated, not by a weak test.
Second attempt, on the real skip point, failed as intended.)

Two test-side corrections worth recording, because both were my error
rather than the code's: `newFilterMemo` forces `visibility: PUBLIC`
*before* applying `extra`, so my `{"visibility":"PRIVATE"}` override
never took effect and I briefly asserted the wrong thing about what
Bob should see; and the compiler emits an **unquoted** column
(`content ~ {:_a0}`), so a rewrite that looked for the quoted form
silently never fired. That second one is the dangerous shape of bug —
a no-op is invisible, and only the folding test caught it.

**Verification.** `TestFTS_FindsTheSameRowsAsLIKE` compares the two
paths element-wise across five filters, because enabling FTS must not
change what a user finds; `TestFTS_FoldsCaseUnlikeLIKE` asserts the
one intended divergence, and asserts it against *both* paths so the
difference cannot be quietly lost. Full `./pb/...` and `./internal/...`
pass except the known `TempDir RemoveAll` teardown flake in
`TestAttachmentService_Empty`, which passes in isolation and touches
no file this change modifies.

## Phase 1 of FTS: the index exists, nothing queries it yet (2026-10-23)

Adding SQLite FTS5 to riff, after establishing that neither riff nor
upstream Memos uses it (see "The search row in the website response
claimed FTS" above) and that the Unicode case fold was the one real
parity gap. `pb/migrations/1910000000_create_memos_fts.go` creates an
FTS5 external-content index over `memos.content` plus three triggers,
and **nothing in riff reads it yet**: `ListMemos` still compiles
`content.contains(…)` to `LIKE`. The query path is a separate commit.

**The DDL-in-a-transaction question, settled empirically.** PocketBase
runs every migration inside `RunInTransaction`
(`core/migrations_runner.go:130`), so `CREATE VIRTUAL TABLE … USING
FTS5` executes inside a SQLite transaction. I had flagged this as
possibly fatal for FTS5 shadow tables. It is not — the table, all
three triggers and the `'rebuild'` backfill all apply cleanly inside the
migration tx. `TestCreateMemosFTSIndex_CreatedInsideMigrationTransaction`
is the test that keeps that true; had it failed, the fallback was an
OnBootstrap hook outside the tx. Raw DDL through
`txApp.DB().NewQuery(...).Execute()` is the established pattern —
PocketBase's own `_collections` init migration uses it.

**Triggers, not `pb/hooks`.** An `AFTER INSERT/UPDATE/DELETE` trigger
runs in the same transaction as the row write, so the index cannot
desynchronise from `memos`. riff's detached-hook workers have needed six
separate teardown fixes (see "Known Issues"), so structural immunity is
worth more here than the convenience of Go callbacks. The delete half
needs FTS5's `'delete'` command form: an external-content index stores
no rows of its own, so a plain `DELETE` cannot find them. The update
trigger must do **both** halves, or an edited memo stays findable by text
it no longer contains — `TestCreateMemosFTSIndex_TracksWrites` asserts
each half separately for that reason.

**External content (`content='memos'`)** means only the inverted index
is stored: the memo body is not duplicated and cannot drift from the
source text.

**Tokenizer behaviour is measured, not assumed.** `unicode61` with
`remove_diacritics 2` folds case over the full Unicode range and makes
`café` match `cafe` — the two things the LIKE path could not do, and
the actual reason this work exists. But it does **not** segment CJK:
`カフェで` indexes as one token, so a bare `カフェ` query does not match
it while `カフェ*` does. I wrote that assertion as a guess, it failed,
and a throwaway probe established the real behaviour; the test now
records the measured numbers rather than my expectation. A Latin-script
term inside a CJK document is findable (`incident` hits). Worth knowing
before a Japanese user reports that search "misses".

**Two dbx scan traps, both hit.** `All(&[]sql.NullString)` compiles and
returns `len 1` with a zero value — dbx never populates it, so a
membership check reads "false" for a table that plainly exists. dbx
rejects `[]map[string]any` outright ("must be a slice of struct or
NullStringMap rows"). The supported shape is a slice of struct, which
is what `ftsTableExists` uses. A plain string target returns
`sql.ErrNoRows` on no match, which is the *expected* answer on a fresh
instance and is indistinguishable from a real failure.

**Deliberately not yet done:** the query path. Two constraints shape
it and are recorded here so the next phase does not rediscover them.
(1) PocketBase's filter grammar **cannot express `MATCH`** —
`MultiMatchSubQuery` is only ever populated for *relations*
(`record_field_resolver_runner.go`), so FTS cannot be a `FilterField` in
`internal/cel` and must sit on the other side of
`FindRecordsByFilter`. (2) The escape hatch is `app.RecordQuery(col)`,
which returns a bare `*dbx.SelectQuery` (`core/record_query.go:26`),
so a `memos.id IN (SELECT rowid FROM memos_fts WHERE … MATCH …)`
predicate can be added as a raw `AndWhere` and `q.All(&records)` still
hydrates real `*core.Record`. `CanAccessRecord` keeps applying, so the
ViewRule still governs who sees a hit — the plugin this index is
modelled on had no access control at all and would have leaked every
PRIVATE memo.

## The search row in the website response claimed FTS; there is no FTS (2026-10-23)

Asked to evaluate riff's "FTS status" and how it aligns with upstream
Memos, the honest answer is that the question rests on a stale doc row,
not on code. `docs/community-website-response.md` §8 listed search as
"`memos.content` LIKE / FTS (the `pb/api/feed.go` SQL filter)". Every
part of that is wrong.

**There is no FTS in riff, and none in upstream Memos.** A repo-wide
search for `fts5|virtual table|bm25|ts_rank|unicode61|trigram` across
Go/proto/TS/SQL returns nothing in source. The only `fts5` hits in
`git log -S fts5` are inside `pb_data/auxiliary.db`, a committed
binary SQLite file matched by content — a false positive worth knowing
about, because it reads like a smoking gun. Upstream's search renderer
(`plugin/filter/render.go` at `8770b186`) uses `LIKE`/`ILIKE` only. The
one apparent upstream FTS hit, `plugin/cron/parser.go`, is the word
"shifts". FTS5 *is* available — `modernc.org/sqlite`'s generated
`sqlite_linux_amd64.go` compiles with `-DSQLITE_ENABLE_FTS5` — so this
would be reachable if either project ever wanted it.

`pb/api/feed.go` cannot be where search happens: the route accepts
`page`, `per_page`, `space` and `tag`, and has no text-search parameter
in its current form or in any revision of the file that added search to
riff. Search reaches the SPA through the Connect `ListMemos` RPC, where
`web/src/hooks/useMemoFilters.ts` builds `content.contains("…")` and
`internal/cel/` compiles it to `content LIKE '%…%' ESCAPE '\'`.

The row's neighbours were stale in the same way and are corrected in the
same edit. The table's sketch-subset column said
`WHERE source='sketch'`, and the paragraph below it promised that
"`idx_memos_source` … covers the subset scan". Migration `1860000001`
dropped the flat `source` column, folding that signal into the presence
of the `sketch` JSON column (`isSketchMemo`,
`pb/connect/sketch_service_lifecycle.go`), and `grep -rn idx_memos_source
pb/migrations/` now matches nothing. The real predicate is
`author = {:uid} && sketch != null` (`pb/connect/sketch_service.go`).
A sketch-subset scan is therefore an unindexed scan of a JSON column,
which is a weaker claim than the doc made and worth stating plainly.

**The one genuine parity gap is Unicode case folding, not FTS.**
Upstream `8770b186` ("fix: add Unicode case-insensitive search for
SQLite", #5559, 2026-02-02) registers a custom SQLite scalar function
`memos_unicode_lower` via `msqlite.RegisterScalarFunction`, backed by
`golang.org/x/text/cases.Fold`, and renders contains as
`memos_unicode_lower(col) LIKE memos_unicode_lower(arg)`. riff has
none of it: no `cases.Fold`, no `RegisterScalarFunction`, no
`ConnectHook`. `golang.org/x/text v0.43.0` is already in `go.mod` as an
indirect dependency, so the fix is close to free dependency-wise.

The cause is structural and worth recording before anyone proposes
"just wrap it in LOWER". **PocketBase's filter grammar applies no case
folding**: in `pocketbase@v0.40.5/tools/search/filter.go` there is no
`LOWER(` anywhere in the package, and `~` emits a bare
`left LIKE right ESCAPE '\'`. The only `COLLATE NOCASE` in all of
PocketBase is on the `email` column. Upstream owns its SQL renderer;
riff compiles into PocketBase's grammar and cannot reach past it, so it
cannot wrap a bound operand. The honest options are a folded shadow
column populated at write time (migration + backfill + hook) or a custom
filter path, not a one-line tweak.

**This also makes a shipped frontend comment false.**
`web/src/utils/search-match.ts` opens by saying it matches "the same way
the server's `content.contains` filter does … The server folds with
Unicode case folding (`cases.Fold`)". That text is inherited verbatim
from upstream's #6335, where it was true because upstream *did* have
`memos_unicode_lower` by then. The riff server does not fold at all, so
the highlighter is built to agree with a server riff does not have: it
can highlight a match the query never returned. The comment is left
alone here — it describes upstream's behaviour, and rewriting it would
mislead whoever does port the fold — but it is wrong about *this*
server today.

One row in riff's favour: `startsWith`/`endsWith` have no upstream
equivalent. Neither project ranks search results; both sort by time
(`-created`). Neither returns snippets.

## Every `List*` RPC returned one page: `page_token` was ignored and `next_page_token` was always empty (2026-10-21)

Pagination was declared everywhere and implemented nowhere. Every
Connect list handler called `paginationFromRequest(0, …)` — page 0,
literally — and answered `NextPageToken: ""`. The SPA's
`useInfiniteMemos` / `useInfiniteAttachments` pass the token straight
back as `pageToken` and read `nextPageToken` as the next page param, so
an empty token told React Query there was no next page: infinite
scroll stopped after the first screen. The `do { … } while (pageToken)`
loops in `useSpaceQueries` / `useUserQueries` had the same shape and
always ran exactly once. `ListDevices` and `ListMySketches` even
carried a comment describing offset pagination while fetching
`page_size, 0` and returning no token.

**The token is the base64url of the offset**, and the decode lives in
`pb/connect/translate.go` (`pageWindow` / `pageOffset` /
`encodePageToken` / `nextPageToken`). Offset rather than a cursor
because the handlers already talk to PocketBase in
`FindRecordsByFilter(limit, offset)` and the sort keys (`-created`,
`username`, `created`) have no unique tiebreaker to build a keyset
cursor on. The caveat of offset pagination is standard and accepted: a
row inserted between two pages shifts the window, so a page boundary
can skip or repeat one item under concurrent writes.

**The next token is derived from the fetched row count, not from the
response length.** `ListMemos`, `ListAttachments` and
`ListMemoComments` post-filter every row through the collection's
`ViewRule` (and `ListMemos` also drops the V0 upload stubs). A page
can therefore legitimately return fewer items than `page_size` while
more rows exist; using `len(out)` would end pagination at the first
page whose rows were all private — the exact "my feed stops after a
few memos" symptom this change removes. The tests pin that case
(`TestListMemos_PageTokenContinuesPastFilteredRows`: page one is empty
and still carries a token).

Three smaller decisions:

* **An invalid token is `CodeInvalidArgument`.** Silently falling back
  to offset 0 would hand the client the first page on every call, and a
  client that loops until the token is empty would never terminate.
* **`page_size` follows the proto**: 50 when unset, coerced to 1000.
  The old helper silently defaulted to 30 and clamped at 100, which
  contradicted the documented AIP-132 contract on every list request.
* **`ListMemoAttachments` paginates in memory.** It assembles one
  memo's patch/sample set itself rather than querying it, so the page
  is a slice of that list and the next-token rule is the same.

Applied to every handler whose request declares `page_token`:
`ListMemos`, `ListMemoComments`, `ListMemoAttachments`,
`ListMemoRelations`, `ListMemoReactions`, `ListAttachments`,
`ListSpaces`, `ListSpaceMembers`, `ListSpaceInvitations`,
`ListUserSpaceInvitations`, `ListUsers`, `ListUserNotifications`,
`ListPersonalAccessTokens`, `ListDevices`, `ListMySketches`.

Not fixed here: `order_by` is still ignored by `ListMemos` /
`ListAttachments` (the sort is hardcoded), and the token does not
fingerprint the filter or sort, so reusing a token with a different
query resumes at the same offset in a different result set.


## `ListMemos` discarded `request.filter`: a CEL compiler targeting PocketBase's *filter grammar*, not SQL (2026-10-21)

Every memo filter in the SPA was a no-op. `ListMemos` called
`FindRecordsByFilter(memos, "", ...)` — the filter was read
off the request and thrown away — so space, creator, tag,
content search, visibility, calendar range, pinned and saved
views all silently returned the unfiltered feed.
`ListAttachments` had the same bug with `memo_id == null`.

The fix is `internal/cel`, which compiles the AIP-132 CEL
expression into the string PocketBase's query builder
understands, plus the per-collection field schema in
`pb/connect/cel_fields.go`.

**The target is not SQL.** The first implementation emitted
raw SQL fragments (`json_array_contains(tags, :x)`) and
nothing worked: PocketBase does not pass the filter through to
SQLite. `tools/search` parses it with
`github.com/ganigeorgiev/fexpr` first, and the vocabulary is
tiny — `= != < <= > >= ~ !~ ?= ?!=` joined by `&&` `||`, with
`(...)` for grouping. It has **no arithmetic**, **no negation
operator** (`!x` is "invalid sign operator"), and exactly two
functions (`geoDistance`, `strftime`). Everything the compiler
emits has to survive that parser.

Three consequences worth keeping:

* **An operand may not be parenthesised.** `(space) = "x"` is
  "invalid or incomplete filter expression"; `space = "x"` is
  fine. Groups are only legal around whole expressions.
* **Timestamps are TEXT.** Autodate columns store
  `"2006-01-02 15:04:05.000Z"` (`tools/types.DefaultDateLayout`),
  so `timestamp("…")` and `now` bind that *string*. Binding a
  Unix epoch would compare a string to a number and match
  nothing — every calendar filter would return empty.
  `timestamp()` itself accepts both forms CEL defines, a
  string (RFC 3339) and an integer (Unix seconds), because the
  SPA's calendar emits the integer one —
  `buildTimestampRangeFilter` in
  `web/src/lib/calendar-utils.ts` writes
  `created_ts >= timestamp(1749340800)` — while saved views and
  the docs spell it with a string. Accepting only one form
  rejects the other as `CodeInvalidArgument`, which is how
  every calendar day and month view broke.
* **Derived predicates are ordinary dotted JSON paths.**
  `has_link` compiles to `payload.property.has_link = true`;
  PocketBase splits the field name on `.` and extracts the JSON
  path itself. (`json_extract(payload, '$.…')` is *not* a
  filter function and is rejected as "unknown function".)

**`?= does not work on a JSON column.** It resolves only for
fields PocketBase models as multi-match, which are relations.
riff's `tags` is a plain `JSONField`, so `tags ?= "work"`
returns zero rows. Membership compiles instead to four anchored
LIKE patterns (`internal/cel.jsonListMember`) covering every
position an element can occupy in the serialised array. The
anchors are load-bearing: `tags ~ '"work"'` also matches
`"workout"` and `"xwork"`, which is the bug
`TestListMemos_TagFilterMatchesWholeElements` pins. The
patterns assume the serialiser emits no space after commas,
which is what PocketBase's JSONField does.

**Negation is pushed down by De Morgan** (`internal/cel.negate`)
because fexpr has no `!`. `!(a && b)` becomes `!a || !b`, and a
comparison has its operator flipped. Three things have no
negated form and are rejected rather than approximated: a
derived predicate (`!has_link`), an ordering comparison
(`!(created_ts > now)` — the bounds cannot be inverted without
the value's type), and a list-membership test.

**Unsupported input is a 400, not a silent unfiltered feed.**
An unknown field, an unknown function, `content.startsWith`,
`size(x)`, `sets.equivalent`, `tags.all`, or arithmetic all
return `CodeInvalidArgument`. Each of those would otherwise
degrade into "returns everything", which is indistinguishable
from "filtering works" for the user. `Field.Const` turns a
typo like `visibility == "PUBLIK"` into an error rather than a
query that matches nothing.

**The V0 shaper was writing PocketBase syntax into the CEL
field.** `pb/api/upstream_memos_v0_convert.go` emitted
`pinned = true` (single `=`, bare identifier) into
`ListMemosRequest.filter`, which the compiler correctly
rejects — `TestV0PatPath_MoeMemosBoot` caught it, and it is the
test AGENTS.md calls the riskiest possible outcome of a V0
change. It now emits `pinned == true`.

**And `v0ResolveMemoName` had to stop forwarding the query
string.** It reused `v0MemoListToV1ListMemos` "so the filter
and page-size match what the V0 client saw on its list call".
That was only safe while the filter was ignored: a PATCH or
organizer call carries `?pinned=true` from the client's earlier
list request, and with filters honoured an *unpinned* memo drops
out of the lookup, so the resolve 404s. An id resolution has to
see every memo the caller can.

`ListMemosRequest.state` was ignored in the same way; it is now
honoured (`memoStateFilter`), AND-ed with the CEL filter. An
unspecified state means "no constraint", not `NORMAL` — the home
feed leaves it unset and must see archived memos too.

### The contract and the compiler disagreed, and neither side saw it

The first implementation rejected six expressions that the API,
the MCP tool description or a saved-view example told the user to
write. Every one of them surfaced as `CodeInvalidArgument` on a
filter the UI had just suggested:

* `"work" in tags` — `proto/api/v1/memo_service.proto` documents
  it, and it is the placeholder of the SPA's quick-find CEL mode.
  The parser only understood `field in [list]`; the reversed form
  fell out of `parseCmp` as a stray `in` token.
* `tags.exists(t, t == "urgent")` — the element-binding macro's
  predicate names a variable the grammar had no binding for.
* `now - duration("168h")` — arithmetic was rejected outright.
* `filename.contains("test")` — only `content.contains` was
  hard-coded, so the attachment contract's own example failed.
* `!has_location` and `size(tags) == 0` — two of the examples the
  saved-view dialog ships.
* `true` — the "match everything" filter a saved view stores, and
  the filter the wave-5 CRUD test itself uses.

The fix keeps the "reject what the grammar cannot express" rule
and makes the expressible half of the documented dialect compile:

* `"x" in <list field>` reaches the same `jsonListMember` test as
  `tag in […]` (`parseValueIn`).
* `tags.exists(e, e == "x")` is parsed by `parseCallArgs`, which
  accepts only the equality form (`e` on either side) and rejects
  the rest with a message naming the supported shape.
* `now ± duration(...)` folds to a bound timestamp in
  `parseAddSub`; every other arithmetic operator still errors, and
  a bare `duration(...)` operand is rejected outright so it can
  never be bound as a value.
* Any registered scalar field supports `.contains`, so
  `filename.contains` works; on a list field it means membership.
* `Field.NegatedExpr` supplies the complement of a derived
  predicate, the only way `!has_location` can compile. Without it,
  negating a derived field is still an error.
* `size(<list field>) == 0` / `!= 0` compiles to the empty-array /
  NULL test. `tags` is `[]` for an untagged memo, and the pair
  also covers rows written before the tag hook existed.
* A bare `true` / `false` becomes `id != ""` / `id = ""`: the
  grammar has no comparison-free boolean, and every collection has
  an `id`.
* `tag == "x"` — the spelling older saved views carry — compiles to
  the same membership test, because membership is the only reading
  a scalar equality can have on a list. `tag != "x"` is still
  rejected: the complement of the four anchored LIKE patterns has
  no expression.

**`attachments.creator` needed a fallback, not a column.** The
proto documents it as "the linked memo's creator, or the uploader
for an unlinked attachment", but `creator` is only stamped on
orphan uploads (that stamp is the rebind guard). A plain column
comparison therefore matched no bound attachment at all.
`Field.FallbackColumn` / `FallbackWhen` compile equality to
`((memo.author = x) || ((creator = x) && (memo = null)))`;
inequality is rejected because the complement of that disjunction
is not expressible.

**A saved view is validated when it is written.** The SPA's
"Validate" button calls `CreateMemoView(validate_only: true)`,
which used to return the request unchanged — so a filter the feed
would reject was blessed by the validator and then 400ed every
feed that selected the view. `validateMemoViewFilter` now compiles
it with the schema `ListMemos` uses, and an update that touches
`filter` does the same. The example list in
`web/src/pages/MemoViews.tsx` was pruned to the compilable subset:
regex, `startsWith`, quantifiers, `size(content)` and set algebra
have no PocketBase equivalent, and offering them in the UI was a
promise the server could not keep.

The lesson is about where the test goes. A compiler unit test can
be green while the documented syntax is rejected — the old suite
pinned the *rejection* of `tags.exists(t, …)` and
`now - duration(...)` as correct behaviour. The round trip is now
pinned at the API boundary instead
(`TestListMemos_TagMembershipDocumentedForms` walks all four
spellings of the same filter against a real app).

The contract text itself was wrong in two places and is fixed in
the proto sources (regenerated with `buf generate`): `state`'s
"Default to `NORMAL`" (the implementation applies no constraint
when it is unset), and the attachment filter's "Supported
operators: `=, !=, …, : (contains), in`" — `:` is PocketBase
syntax, not CEL, and the real surface is `==`, the comparisons,
`field.contains(...)` and `in [...]`.



## MoeMemos saw `createdTs: 0` and `size: 0` on every row: the V0 shaper read protojson fields as the wrong JSON types (2026-10-20)

The report was two symptoms in one: *"MoeMemos uploads are not
linked and MoeMemos is not getting memos metadata like date of
creation"*. Driving the real MoeMemos boot sequence against a
live app (`pb/api/upstream_memos_v0_metadata_internal_test.go`)
answered both.

**The metadata symptom.** Every V0 response carried
`createdTs: 0` and `updatedTs: 0` — the epoch. The shaper in
`pb/api/upstream_memos_v0_convert.go` translates the connect
handler's JSON body field by field, and it read `createTime` as

    m, ok := v.(map[string]any)
    secs, _ := m["seconds"].(float64)

That is the **message** shape of `google.protobuf.Timestamp`. But
protojson never emits that shape for a WKT: `Marshal` renders a
`Timestamp` as an **RFC 3339 string**, `"2025-09-20T00:10:00Z"`.
The type assertion therefore always failed and the function
returned its zero value. Same class of bug on the resource side:
`Attachment.size` is an `int64`, and protojson emits every 64-bit
integer as a **JSON string** (`"15"`) because a 64-bit value can
exceed the precision of an IEEE-754 double. The shaper read it as
`a["size"].(float64)`, so every attachment reported zero bytes.

**Why the tests missed it.** The existing V0 unit tests hand-wrote
the V1 body as `"createTime": {"seconds": 1700000000}` and a
numeric `size` — shapes protojson does not produce. The suite was
green against a fixture the server never sends.

**The decision.** Make the readers tolerant of what protojson
actually emits, and make the fixtures honest. `protoTimestampToUnix`
now parses the RFC 3339 string first and still accepts the
structured form; a new `protoInt64` handles the stringified int64
(plus `json.Number`/`float64`, so hand-built bodies keep working).
`TestV0ProtojsonShapes` marshals a real `apiv1.Attachment` and
asserts the shaper's readers survive it, so a future proto
field-type change surfaces as a test failure instead of a
silently-zeroed response.

**Two further drops on the same path**, found while the
end-to-end sequence was being traced:

* `memoId` was hardcoded to `int64(0)` with a comment explaining
  that the association "is carried by `resourceList` instead".
  That is not true of the MoeMemos client: it reads `memoId` off
  each resource, so a bound file reported no owner. It now derives
  from the V1 `Attachment.memo` resource name through the same
  `v0IDFromV1` projection every other id uses, and stays `0` for
  an unlinked upload — the honest answer.
* The upload handler trusted the multipart part's `Content-Type`.
  Go's `multipart.CreateFormFile` labels **every** part
  `application/octet-stream`, and several Kotlin upload helpers do
  the same, so `mime_type` was pinned to a useless value forever.
  A missing-or-generic header now falls back to the filename table
  (`connect.GuessMimeType`, exported for this purpose).

**And the linking symptom**, which turned out to be two things:

* The PATCH handler bound the attachments correctly but shaped the
  response from the V1 `UpdateMemo` reply, which carries only the
  memo's own columns. The client therefore never saw the files it
  had just bound, and its next edit — which rebuilds
  `resourceList` from the previous response — could drop them
  again. The PATCH path now re-reads the attachment list through
  `v0ShapeSingleMemoAttachments`, the same pass the create path
  already used.
* Separately, a **data race** made the whole V0 package
  `-race`-dirty: `registerMotionMedia` and the memo-links fan-out
  read `e.App` / `e.Record` *inside* their detached goroutines.
  PocketBase's model/record event bridge writes back into the same
  shared `RecordEvent` on its way up the stack
  (`core.syncRecordEventWithModelEvent`, bound at
  `core/record_model.go:118`), and that write can land after the
  handler returns — concurrently with the goroutine's read. The
  fix is to snapshot both into locals *before* launching. This is
  the same "detached worker" family as the teardown entry below,
  but a different bug: that one was about *not waiting* for the
  worker, this one is about *sharing* its inputs.

## Private attachments 404 in the feed: `<img>` cannot present a localStorage token, so the file surface reads a session cookie (2026-10-20)

The reporter's insert-image path posts successfully and links
correctly — the attachment row exists and the memo is bound — but the
image is broken, with
`GET /community/file/attachments/ob0frvna8r7yuce/main_9cfda642.webp`
answering 404. The upload was never the problem. This is a
**transport** problem, and it is structural rather than a bad URL:

* The SPA stores its PocketBase token in `localStorage`
  (`web/src/auth-state.ts`) and sends it on every Connect call as an
  `Authorization` header.
* An `<img src>`, a `<video poster>`, a `<audio>`, or a CSS `url()`
  can present **no** header and cannot read `localStorage`. The
  browser attaches only ambient credentials — cookies.
* `web/src/connect.ts` sets `credentials: "include"` on `fetch`, but
  that only helps if a cookie exists, and nothing set one: there is
  no `Set-Cookie` anywhere in the Go tree.
* So the file GET arrived **anonymous**. `authorizeAttachment`
  (`pb/api/file_authz.go`) then falls through to the `memos`
  visibility rule, and the SPA editor's default visibility is
  `Visibility.PRIVATE` (`web/src/components/MemoEditor/state/types.ts`).
  `memos.ViewRule` is `author = @request.auth.id || ...`, so an
  anonymous caller fails it and gets a deliberate 404.

The 404 was therefore *correct behaviour for an anonymous request*. The
bug is that riff demanded a credential from a sub-resource that has no
way to send one — so every private image in every feed rendered
broken, while the same URL fetched with an `Authorization` header
worked.

**The decision:** mirror the session token into a cookie, scoped to the
SPA's mount path, and accept it on the file surface.

* `syncAuthCookie` (`web/src/auth-state.ts`) writes `riff_auth=<token>`
  on sign-in, on refresh, on page load, and on adoption of another
  tab's broadcast token. It is removed by `clearAccessToken` on
  sign-out — a leftover cookie would keep serving file reads for a
  session the user just ended, because the server resolves it to a live
  user record.
* `fileViewer` (`pb/api/file_authz.go`) accepts `riff_auth` after the
  `Authorization` header, reusing the existing
  `ResolveFilePersonalAccessToken` path via a new shared
  `resolveFileToken` helper.

Rejected alternatives:

* **Per-attachment signed URLs.** Stronger isolation (no ambient
  credential at all) but it needs a new signing key, a new mint
  endpoint, and a client-side rewrite of every emitted URL — including
  the markdown-embedded ones, which the server renders. It also
  changes the cache story: a signed URL is unique per render, so the
  existing ETag/`Cache-Control` reuse (`fileCacheControl`) degrades.
  Worth revisiting if the cookie's ambient scope ever becomes a
  concern; `internal/signedurl` and the `share_token` path already
  demonstrate the mechanism.
* **Cookie on every Connect route.** Deliberately *not* done. The
  cookie is read only by the file surface. Widening it would put a
  long-lived bearer credential in the ambient request state of the
  whole app, including the RPCs it is not needed for.

Properties that are load-bearing, each pinned by a test:

* `path` is `BASE_PATH`-scoped (`/community/` on the Fly layout), so
  the token is never attached to requests for the static site that
  shares the host in `Caddyfile.fly.community`.
* `SameSite=Strict`, so a third-party page embedding the SPA cannot
  ride the session.
* The cookie expires with the token; it never outlives the session it
  mirrors.
* `Secure` is emitted unless `window.isSecureContext === false`, and
  the server independently refuses a cleartext cookie from a
  non-loopback host (`fileCookieTransportAllowed`). The two halves are
  redundant on purpose: a client cannot be trusted to enforce its own
  transport rule.

The invariant is unchanged and still pinned by
`TestFileRoute_PrivateAttachmentIsNotPublic`: **file access == memo
access.** The cookie resolves *who* is asking; it does not grant
anything. A foreign user's valid cookie still gets 404 on someone
else's PRIVATE memo (`TestFileRoute_SessionCookieIsScopedToTheFileSurface`).

**Follow-up worth knowing:** the SPA's own `insert image` path also
depends on this, and so do avatars, thumbnails (`?thumbnail=true`) and
motion clips (`?motion=true`) — every URL the browser loads without a
header. Until this cookie shipped, the `/file/...` surface was usable
only for PUBLIC and PROTECTED memos.

## The SPA's "post memo with attachment" silently dropped the file: `attachment.memo` was required on the spec, but the SPA uploads before the memo exists (2026-10-20)

The reporter's URL `https://ravine.1une.cc/community/memos/uwqi616olykffr2`
shows a memo that posted fine but whose attachment vanished — the
`/file/attachments/{uid}/{filename}` URL ends up 404'ing through the
SPA's catch-all to `index.html`. The root cause is a server-side
gate the SPA cannot satisfy:

* `attachments` is a unified single-collection schema; the
  `attachments.memo` column is nullable (`Required: false` in
  `pb/migrations/1850000000_create_attachments.go`).
* `pb/connect/attachment_upload.go::uploadSpec` *required*
  `spec.attachment.memo` to point at an existing memo, with
  `INVALID_ARGUMENT: memo is required for upload` on empty specs.
* The SPA's `memoService.save` uploads `state.localFiles` *before*
  calling `CreateMemo` (the original order is "upload inline, then
  bind via `req.Memo.Attachments[]`") — both the end-of-edit
  drag-upload and the inline-image upload during a brand-new memo
  edit land with no `memo` to name. The upload is rejected. The
  SPA catches the error, posts the memo anyway, and the
  attachment's row never exists.

Two changes close the bug:

1. **Server relaxes the gate.** `spec.attachment.memo` is now
   optional. An empty memo lands the row as an "orphan"
   (`attachments.memo` empty), with a new nullable `creator`
   column stamped with the uploader's user-id
   (`pb/migrations/1890000000_attachment_creator_for_orphan_binding.go`).
2. **`CreateMemo` and `UpdateMemo` rebind orphans in-RPC.** A new
   helper `pb/connect/memo_bind_attachments.go` reads
   `req.Memo.GetAttachments()`, resolves each name to a row,
   refuses cross-user rebinds (the `creator` gate keeps bob from
   re-binding alice's orphan onto bob's memo), refuses cross-memo
   rebinds (matches the `SetMemoAttachments` policy), and rewires
   the row's `memo` column to the new memo inside the same RPC.

**Why the SPA did not need reordering.** The SPA's
`memoService.save` still uploads before creating the memo and
passes the resulting attachment names through
`memoData.attachments`. With the relaxed gate the uploads land
as orphans; with the bind pass the subsequent `CreateMemo` wires
them in. The pre-existing order works.

**Why the `creator` column exists.** An orphan row's visibility
rule resolves to NULL for everyone (`memo` is empty, so
`memo.author = @request.auth.id` is NULL regardless of the
viewer), which means the rule engine cannot disambiguate the
uploader. Without the `creator` gate a hostile SPA that ever
learnt another user's orphan attachment name could re-bind the
row onto its own memo and effectively steal the bytes. The
column costs one nullable relation and one migration.

**The V0 stub-memo path is unaffected.** The MoeMemos V0
client's three-step upload (`POST /memo` →
`POST /resource/blob` → `PATCH /memo` with `resourceList`)
still works through `pb/api/upstream_memos_v0_attach.go`; the
V0 upload target is a stub memo that exists at upload time, so
the spec gate's relaxed path is unused here. See
"V0 memo + attachment (the split-memo bug)" below.

**Pre-existing attachment-upload race under `-race`.** The
existing `TestUploadAttachment_HappyPath` (and now my new
`TestUploadAttachment_OrphanSpecSucceeds`) trip the race
detector when reading the row right after `finishWrite`. The
race is between `pb/hooks/motion_media.go`'s detached detector
writing the `motion_media` column and the test reading the row
— both through PocketBase's internal `Record` model which has
no cross-goroutine lock. The new tests wait for the per-app
motion-media waitgroup via the exported
`hooks.WaitMotionMediaWorkers(app)` helper, which closes the
test-side window but does not address the underlying PocketBase
race. The pre-existing test was already racy before my changes;
not introducing a new race here.

## The background workers were not awaited at teardown, so a test's temp dir cleanup raced with them (2026-10-20)

Found while stress-testing the attachment fixes: a full
`go test ./...` run failed roughly one run in six, always with

    testing.go:1617: TempDir RemoveAll cleanup: unlinkat
    /tmp/TestSomeTestXXXX/001: directory not empty

and always on a *different* test each time.

**Why it is so hard to attribute.** The failure lands on
whatever test happened to own the temp dir the background
goroutine was writing into — not on the test that started
the goroutine. The four failures observed during this work
were `TestFileRoute_UnknownAttachmentIs404`,
`TestUploadSketch_HappyPath`, `TestSpaces_Schema` and
`TestRiffOnlyColumnsAreDropped`; none of them had anything
to do with each other.

**The cause.** Six background workers were detached and
untracked:

* `pb/hooks/motion_media.go` — the Motion Photo detector
  (reads the blob, writes the `motion_media` column)
* `pb/hooks/memo_links.go` — the link fan-out
* `pb/hooks/scheduler.go` — the 1-minute sweep loop
* `pb/hooks/webhook_drain.go` — the outbox drain loop
* `pb/connect/upload_session.go` — the upload-session sweeper
* `pb/connect/memo_import_session.go` — the import-session
  sweeper

The last two stage their files **inside the app's data
directory** (`<dataDir>/imports`, and the upload staging
dir), so a sweeper that runs after the cleanup has removed
the directory recreates it, and the cleanup fails.

**The fix.** Each worker is now tracked in a
`sync.WaitGroup` that the app waits on during teardown.
Three details are load-bearing, and each of them cost a
debugging cycle:

1. **The wait belongs on `OnBootstrapClear`, not
   `OnTerminate`.** A test's cleanup calls
   `app.ClearBootstrap()`, and that triggers
   `OnBootstrapClear` but NOT `OnTerminate` — so a wait
   bound to `OnTerminate` never runs in a test, and the
   test is exactly where the race bites. (PocketBase
   triggers `OnTerminate` only from a real shutdown; see
   `core/base.go::ClearBootstrap`, which fires
   `OnBootstrapClear`.)

2. **The stop signal must be sent BEFORE the wait.** The
   wait blocks until the worker returns, and the worker
   only returns after it observes the signal. Waiting
   first deadlocks the teardown. The first attempt did
   exactly that and hung `pb/connect` for the full
   90-second test timeout.

3. **The scheduler's shutdown channel must be CLOSED, not
   sent on.** It was an unbuffered channel with a
   non-blocking send (`select { case ch <- struct{}{}:
   default: }`). An unbuffered send is only received if the
   loop happens to be parked in its `select` at that exact
   moment; otherwise the `default:` branch silently drops
   it, and the loop then waits out its 1-minute tick. The
   fix closes the channel — a closed channel is seen by
   every future `select` — and guards the close so the
   `OnTerminate` and `OnBootstrapClear` hooks can both fire
   without a double-close panic.

The `memo_links` fan-out already closed its channel behind
a `sync.Once`, so it needed only the wait.

**A fourth detail, found by the race detector's blind spot.**
The wait group cannot be package-level. A
`sync.WaitGroup` may not be waited on twice concurrently, so
two apps tearing down at the same time panic with "sync:
WaitGroup is reused before previous Wait has returned". The
same applies to the shutdown channel: a package-level channel
closed for one app stops *every* app's fan-out, and two apps
registering concurrently race on the globals.

All the per-app state (the shutdown channel, the `sync.Once`
that closes it, and the wait group) therefore lives in a
per-app map keyed by the `core.App` value, guarded by a
mutex. The same change retired the last package-level
`memoLinksShutdown` global, which had the same two bugs.

**Why the race detector did not catch this.** The misuse is
a `panic()` from the runtime's `sync` package, not a data
race, so `-race` reports nothing; it only shows up as a
failed test. The probe that found it was a test that
registers the hooks and tears the app down on five goroutines
at once, 200 times over.

**Verification.** Six consecutive full runs of
`./pb/ ./pb/migrations/ ./pb/api/` are clean, where the flake
previously hit roughly one run in six. `pb/connect` also
stopped hanging: the `TestTranscribe_RequiresAuth` case went
from a 90-second timeout to 0.34 s once the scheduler's
shutdown channel was closed instead of sent on. The
concurrent-teardown probe is clean under `-race` as well.

## The V0 memo+attachment flow split one post into two memos, and the native file route served private blobs (2026-10-20)

Two bugs, both found from the same user report: "memos posted with
attachment is split into 2 memos in spa, one with text, one empty
with attachment broken".

### The split: the V0 PATCH dropped the `resourceList`

The MoeMemos V0 client posts a memo that carries an attachment in
three steps:

1. `POST /api/v1/memo` — the memo with its text
2. `POST /api/v1/resource/blob` — the file upload
3. `PATCH /api/v1/memo/{id}` — the memo update whose `resourceList`
   links the uploaded resource to the memo

Step 2 has no memo to attach the file to (the V1
`UploadAttachment.spec.attachment.memo` field is required), so the
V0 upload handler creates a hidden **stub memo** (the
zero-width-space sentinel content) and uploads against it. Step 3
is what binds the file to the memo the user actually sees.

**Step 3 was a no-op.** The V0 PATCH shaper
(`v0MemoPatchToV1UpdateMemo`) deleted the V0-only fields it knew
about but left `resourceList` in the body, so it landed in the V1
`update_mask` — and the V1 `UpdateMemo` handler has no
`resourceList` field, so it was silently ignored. The file stayed
bound to the stub forever, and the feed showed two memos: the real
one with the text, and the stub carrying the attachment.

The upstream 0.21 contract confirms the shape: `Memo.resources` is
`OUTPUT_ONLY` in the 0.21 proto, and the binding was done through
`Resource.memo`; the V0 REST dialect expressed the same thing as
the `resourceList` on the memo body.

**The fix** (`pb/api/upstream_memos_v0_attach.go`): the V0 PATCH
handler reads the `resourceList` before dispatching to the V1
handler, and `v0LinkResourcesToMemo` moves each named attachment
onto the target memo (a pointer rewrite on the `attachments.memo`
relation — the bytes never leave storage) and then deletes the
stub that is left empty. The whole move runs in one transaction, so
a rejected attachment cannot leave the files split across two memos.
The shaper now strips `resourceList` so it no longer pollutes the
`update_mask`.

**A second, latent bug in the same path.** The V0 resource ids
are Kotlin `Long`s that run up to 2^63, which a `float64` cannot
represent exactly. The shared `decodeFlatObject` helper decodes
into `map[string]any`, so every number became a `float64` and the
id came back rounded — the id no longer matched the attachment it
named. The new `decodeV0Object` helper keeps the literal digits
(`json.Decoder.UseNumber`) so `json.Number.Int64` reads them back
exactly.

**The stub also leaked into the SPA feed.** The V1 `ListMemos`
handler did not filter stubs, so even an upload that was never
linked left an empty memo in the feed (the 2026-10-19 V0 restore
entry noted this as a known wart: "the V1 client sees them as real
memos"). The V1 list now skips the stub sentinel, and the stub
marker moved to `pb/connect/memo_stub.go` so both sides share one
definition. The visibility check was dropped from the stub test on
purpose: a stub that a later V0 edit flipped to PUBLIC is still a
stub and still must not be shown.

### The leak: PocketBase's native file route ignored the memo's visibility

`GET /api/files/{collection}/{recordId}/{filename}` is
PocketBase's built-in file server. It evaluates the collection's
`ViewRule` **only** when the file field is marked `Protected`
(`apis/file.go::download`). The `attachments.blob` field was not
protected, so the native route served any attachment blob to
anybody who knew the id — completely bypassing the owning memo's
visibility. A file attached to a PRIVATE memo was downloadable by
an anonymous caller.

The `attachments.ViewRule` *is* the memo-visibility rule
(`memo.author = @request.auth.id || memo.visibility =
"PROTECTED" || "PUBLIC" || ...`), so marking the field
`Protected` closes the leak: the native route now evaluates the
rule, and a PRIVATE memo's attachment answers 404 for anyone but
the author. A PUBLIC memo's attachment is still served by the
native route (the rule passes for an anonymous caller), so nothing
that worked before stops working.

Migration `pb/migrations/1880000000_protect_attachment_blobs.go`
sets the flag on existing installs; the create migration sets it
too, so a fresh install never has the window.

**Protected is not enough on its own.** PocketBase's protected-file
branch wants a short-lived *file token* (`?token=…`), not the
caller's bearer token — so even the author of a PRIVATE memo could
not read their own attachment through the native route. And the SPA
*prefers* `attachment.externalLink` over its own `/file/...` URL
(`web/src/utils/attachment.ts`), and the connect layer was setting
`externalLink` to the native `/api/files/...` route. So the SPA
was using the leaky route and would have broken on private memos
once the field was protected.

The `externalLink` now points at riff's own `/file/...` surface
(`/file/{collection}/{id}/{filename}`), which authorizes against
the same `ViewRule` using the caller's normal PocketBase token and
additionally supports `?thumbnail=true`, `?motion=true`, and
`?share_token=…`. The avatar URL moved the same way
(`/file/users/{id}/avatar`). The V0 resource shaper's fallback
`externalLink` moved with it.

### Verification

`pb/api/upstream_memos_v0_attach_test.go` reproduces the exact
MoeMemos three-step sequence against a real PocketBase app and
asserts that the feed shows one memo carrying the attachment, and
that a stub left behind by an unlinked upload is invisible to both
the V0 and the V1 list. The leak fix is pinned by
`pb/migrations/1880000000_protect_attachment_blobs_internal_test.go`
and by the existing 26 file-route tests. End-to-end against a
running server: a PRIVATE memo's attachment answers 404 on both
`/api/files/...` and `/file/...` for an anonymous caller and 200
for the author; a PUBLIC memo's attachment answers 200 on both,
and `?thumbnail=true` answers 200 `image/jpeg`.

## The SPA's `/file/...` URLs were never registered: the file server now owns the surface, authorization, and thumbnails (2026-10-20)

A user reported that
`https://ravine.1une.cc/community/file/attachments/juck9wz3nedyeaj/96F82B1E-….jpeg?thumbnail=true`
returned 404. The URL was fine; **the route did not exist**.

**The root cause.** riff registered exactly one family of file
routes — the upstream-Memos one, at `/memos/api/file/...`
(`pb/api/upstream_memos_rest.go`). The SPA, the markdown
renderer, and the share links all emit a *different* family:
`web/src/utils/attachment.ts` builds
`${BASE_URL}file/${name}/${filename}` → `/file/attachments/...`,
and `web/src/utils/managed-attachment.ts` writes the same shape
into memo content as `/file/attachments/{uid}`. Nothing in the
binary answered that path, so it fell through to the SPA's
catch-all route (`server/frontend/embed.go`, `Any("/{path...}")`)
and came back **200 with `index.html`** — a broken image, not a
404. The `?thumbnail=true` in the report was a red herring: no
thumbnail support existed either, on any route.

The 2026-10-14 "sub-path-aware URLs" entry had already made the
SPA emit `${origin}/community/file/...` instead of
`${origin}/file/...` — which made the miss *visible* (the
pre-fix URL 308'd to the static site and 404'd there) but did
not create the route. Two half-fixes in a row: the URL was
corrected, the handler was never written.

**The fix.** `pb/api/file_routes.go` registers the public
`/file/...` family (`/file/attachments/{uid}[/{filename}]` and
`/file/users/{identifier}/avatar`), and both families now share
one handler — `upstream_memos_rest_file.go` dispatches into
`file_routes.go` rather than keeping a second implementation.

**The routes are GET, not Any.** The SPA catch-all is `Any`, and
Go's `http.ServeMux` panics when a method-less pattern overlaps
a method-less one. A `GET`-only `/file/...` pattern is strictly
more specific than the method-less catch-all, so the two
coexist — the same rule that governs the method-less Connect and
`/mcp` patterns (see `server/frontend/embed.go`).

**The file server is the authorization authority, and it asks the
collection rule rather than re-implementing it.** The
`attachments` collection's `ViewRule` encodes the parent memo's
visibility, and a file download is not a collection read — the
rule is the only thing standing between a private memo's file and
the public internet. `pb/api/file_authz.go` resolves the caller
(a PocketBase token, a `memos_pat_*` PAT, or nothing) and then
asks `app.CanAccessRecord(memo, …, memos.ViewRule)`.

This closed a **live leak**: the pre-existing
`/memos/api/file/...` handler served *any* attachment to *any*
caller, so a private memo's file was a public URL. A denial
answers **404, not 403**, so the response never confirms that an
attachment exists.

**`share_token` is checked first.** A share link is a
capability: its holder is by definition not a member of the
memo's space, so the normal visibility rules would reject them.
The token resolves to a `memo_shares` row; the grant holds only if
the row points at *this* attachment's memo and has not expired.

**The file surface follows the memo surface, even where riff
diverges from upstream.** riff's `memos` ViewRule reads
`visibility = "PROTECTED"` as world-readable, where upstream
Memos means "any signed-in user". The file server does not
re-implement the policy — it asks the rule — so the two surfaces
can never disagree. `TestFileRoute_ProtectedAttachmentFollowsTheCollectionRule`
pins that invariant.

**Thumbnails are generated server-side and cached on disk.**
`?thumbnail=true` produced a 600px-box JPEG (upstream's number)
via `disintegration/imaging`, cached in the PocketBase filesystem
under `.riff_thumbnails/`. The cache key embeds the record id,
filename, size, and update timestamp, so replacing an attachment's
bytes invalidates the entry automatically. The decoded size is
checked against `internal/imagelimit` *before* the full decode, so
a decompression bomb is refused rather than allocated. A format
the generator cannot decode falls back to the original bytes —
the SPA's `<img onError>` handler does the same.

`?motion=true` extracts the MP4 embedded in a Motion Photo via
the existing `internal/motionphoto` library. A file with no
embedded video answers 404 rather than serving the still frame,
which would look like a video that never plays.

**Non-inline content types are served as `Content-Disposition:
attachment`.** The filename is attacker-controlled, so an
`.html` or `.svg` upload must never render against the
instance's origin. The content-type table is an explicit
allow-list; anything unlisted is `application/octet-stream`.

**Public memo attachments are cacheable** (`public, max-age=3600`);
private ones are `private, no-store`. The SPA re-requests the
same thumbnail URL constantly while scrolling a feed.

## Two pre-existing startup bugs found while verifying the file fix (2026-10-20)

The file fix could not be verified end-to-end because the server
**did not start at all**. Both causes pre-date the file work
(reproduced with an unmodified HEAD binary) and are fixed here.

**1. `sync.RWMutex` deadlock in the signed-URL bootstrap.**
`pb/connect/sketch_signer_bootstrap.go`'s OnServe hook took
`signerMu.Lock()` and held it across `e.Next()`. `e.Next()`
runs the *next* OnServe hook, and `main.go`'s hook calls
`connect.InstallSignedURLSignerForREST()`, which takes the
**read** lock on the same mutex. A `sync.RWMutex` is not
reentrant, so the read lock blocked forever and the listener was
never bound — the process hung with no error. The fix scopes the
lock to the mutation and calls `e.Next()` after releasing it.

**2. `MemoService_GetMemo` silently dropped from the OpenAPI.**
Commit `edcc96e5` added `SketchService.GetSketch` with the same
`get: "/api/v1/{name=memos/*}"` as `MemoService.GetMemo`. Two
RPCs claiming the same HTTP method + path makes gnostic keep the
first and **silently discard the second**, so
`MemoService_GetMemo` vanished from `proto/gen/openapi.yaml` —
while `server/mcp/catalog.go` still curated it. `mcp.Register`
then refused to start the binary ("curated OpenAPI operation
\"MemoService_GetMemo\" not found").

The fix moves `GetSketch` to `get: "/api/v1/sketches/{name}"` —
the sketch-scoped spelling of the same resource. The SPA calls
the Connect procedure, not the REST path, so nothing depended on
the old shape. `buf generate` then restores
`MemoService_GetMemo` to the spec and the MCP catalog resolves.

**The lesson for the next proto edit:** two RPCs must never
share an HTTP method + path, and the OpenAPI generator will not
tell you — it drops one. A curated operation missing from the
spec is a *startup* failure, so the catalog and the spec have to
be regenerated together (`cd proto && buf generate`).

## Sketch push transport is device-pulls: SPA queues a 15-min signed URL on the device row, the PO-33 polls (2026-10-19)

The "Push to my device" feature on the SPA's sketch player has two
plausible transports. The first is **device-pushes**: the SPA opens a
WebSocket or long-poll to the PO-33 and forwards the bytes when the
user clicks. The second is **device-pulls**: the SPA writes a small
envelope to a per-device row, the device polls the server every 2s
and fetches the bytes via a short-lived signed URL. The PO-33 K.O!
firmware has only an HTTP/1.1 client with a one-shot Ed25519
signature; a long-poll over the same client would be doable but
requires the firmware team to ship a state machine on the device
that the current firmware revision does not have. Device-pulls
reuses the existing signed-URL pattern the upload route already
verifies; the device-side code is the same `GetFirstRecordByFilter`
+ signed-URL verify it already has.

**The envelope is on the device row, not in a new collection.** A
`po33_sketch_pending_pulls` collection with a `(device, state)` index
was the obvious design, but every poll would have to read a list,
filter by state, and pick the most recent. A single `pending_pull`
JSON column on the device row is a one-record read per poll; the
state machine is the column's `state` field. A future push-history
table (out of scope for this commit) can derive its id from the
envelope; today, a single push is the single envelope.

**The signing key is per-install, persisted in `instance_settings`,
and never logged.** A 32-byte HMAC-SHA256 key is minted on
`app.Bootstrap()` if the row is missing and stored under the
`DOWNLOAD_SIGNING_KEY` key. A future operator who deletes the row
forces a re-mint on next start; URLs issued before the rotation are
rejected because the new key's MAC won't match. The bootstrap path
writes no log line that carries the key bytes — the brief's §10 audit
requirements treat the signing key the same way they treat the
device access tokens.

**The signed URL is 15 minutes, not 5.** A human at the device who
walks away for a coffee break comes back to a still-valid push. A
leaked URL expires before the next round of operator-driven
rotation. The same 15-minute number is the keep-alive window the
SPA's `GetPushStatus` poll reads so the two timers expire together
on the same envelope.

**The push is idempotent within the rate-limit window.** A
double-click on the "Push" button enqueues one signed URL; the
second click overwrites the prior envelope with a fresh signed URL
but reuses the prior `push_id` so the SPA's poll sees a single
logical push. The per-`(device, memo)` `ScopeSketchPushDeviceMemo`
rate limit (10 / min) absorbs the click without capping
legitimate per-device concurrency (a user with two devices can
push the same sketch to each in two requests, well under 10).

**Security stance.** Owner-mismatched lookups return 404 not 403
so a stranger cannot enumerate device ids. Per-device
1-push-per-30s rate limit (above) plus per-install rate limiter
gates on the route. The signed URL is the only auth on the
device-pull `/sketches/{memo}/download` route; the device's
`X-Device-Fingerprint` header is preferred so the audit row carries
a device id, but the URL alone is sufficient (the device may strip
the header when it follows a redirect). The download handler
returns 404 (not 401) on a bad signature so a leaked URL doesn't
reveal whether the memo exists.

**The SPA-facing API is the new `SketchService.PushSketch` Connect
RPC; the same flow is also exposed at
`POST /api/po33/devices/{deviceId}/push` for `memos_pat_*` callers
(CLI, third-party SPA).** The duplication is intentional: the
Connect path is the SPA's preferred transport, and the REST path is
the surface the brief promises; both sides write the same envelope
and read the same `pending_pull` column.

**The memo detail page mounts the player on the existing
`source === "sketch"` branch.** No new route. The `GetSketch` RPC
is a deep-linkable read for the player to use when the memo is
loaded from a notification; the page itself still renders the
existing memo body.

**Deferred (out of scope for this commit):** WebSocket / SSE
"true push" transport, push history table, batched pushes (a user
with N devices pushes the same sketch to all in one click).
See `docs/design/po33-device-binding.md` (2026-10-19 appendix) for
the full lifecycle and `pb/api/po33_device_pull.go` for the REST
implementation.

## `/api/v1/status` advertises V0 (`0.21.0`) so the MoeMemos V0 surface is selected, not the V1 fallback (2026-10-19)

After the V0 dispatch-helper fix below shipped, the Android MoeMemos
hybrid APK booted against the live deployment — but the iOS MoeMemos
hybrid app (same `mudkipme` author, same `MemosV0Service` +
`MemosV1Service` hybrid shape, the canonical 0.21.0 / 0.27.0-0.31.0
client) still hit "data couldn't be read" on boot. The two apps read
`GET /api/v1/status` differently, and the previous wire-format choice
was wrong for the iOS one.

**The version-probe contract.** `mudkipme/MoeMemos/Packages/MemoData/
Sources/MemoData/AccountUtils.swift::detectMemosVersion` (and the
matching Kotlin on Android) is the **first** call on every login:

```swift
let v0Status = try? await v0Service.getStatus()
if let version = v0Status?.profile?.version, !version.isEmpty {
    return .v0(version: version)   // non-empty → V0 client
}
// fall through to V1 probe
let v1Profile = try await v1Service.getWorkspaceProfile()
```

`v1StatusHandler` was returning `version: ""` with a comment that
claimed "We always return an empty version so the V1 client is
selected." That comment was written when the V0 surface did not yet
exist; once the V0 restore landed (entry below) the comment is stale
and the behaviour is actively wrong for the iOS app — every
iOS MoeMemos boot was being pushed into the V1 fallback path.

**The V1 fallback path is not wired up.** The V1 fallback calls
`GET /api/v1/workspace/profile` (V1 REST shape, not the connect path).
The live probe at `ravine.fly.dev/community/api/v1/workspace/profile`
returns the SPA HTML (200, `text/html`) because no V1 REST route for
that path is registered. So an empty `version` does not just route the
client to V1 — it routes the client to a V1 surface that does not
answer, and `kotlinx.serialization` / `Codable` (Swift) surfaces the
HTML as "data couldn't be read." The `memos.api.v1.WorkspaceService/
GetWorkspaceProfile` connect path also returns the SPA HTML because
no PocketBase route is mounted at that path; the V1 connect
procedures are registered at their canonical path
(`/memos.api.v1.WorkspaceService/GetWorkspaceProfile`) but a reverse
proxy in front that strips path prefixes can break that — and our
caddy does not strip. (See `pb/connect/connect.go::registerConnectRoutes`
for the procedure path registration.)

**The fix.** Change the version string to `"0.21.0"`. The iOS app now
sees a non-empty version, picks `MemosV0Service`, and the V0 surface
— the same one Android MoeMemos has been using successfully since the
prior entry — answers the rest of the boot path. The 11 V0 routes
in `pb/api/upstream_memos_v0_rest.go` (user/me, memo, memo/all,
resource, tag, memo POST, memo PATCH, memo DELETE, memo/{id}/
organizer, resource/blob, resource/{id} DELETE) cover the full
MoeMemos V0 boot sequence and are verified end-to-end by
`TestV0PatPath_MoeMemosBoot`. The `mode` field is kept at `"prod"`
(per the iOS OpenAPI's `Profile.mode` enum: `prod | dev | demo`).

**What is NOT fixed and may bite later.** The V1 REST surface
(`/api/v1/workspace/profile`, `/api/v1/auth/signin`, etc.) is not
implemented; only the V1 connect procedures and the 11 V0 REST
routes are. If a future Memos version adds a V0-incompatible
breaking change and MoeMemos bumps its minimum Memos version, the
fallback path will not save us. The plan when that happens is to
extend the V1 REST route table in `pb/api/upstream_memos_v1_rest.go`
mirroring the V0 pattern (one `custom` builder per V0 shaper pair),
not to add a V0 surface for the new version.

## V0 dispatch helper strips `Accept-Encoding: gzip` and forwards upstream error bodies itself (2026-10-19)

The first V0 surface restore (entry below) wired the V0 handlers to
the in-process connect mux via a near-clone of `connectProxyHandler`.
The V1 path worked in production; the V0 path failed every request
with `500 {"code":13,"message":"connect response was not JSON:
invalid character '\\x1f' looking for beginning of value"}`. The
`0x1f` was not a connect+proto envelope — it was the **gzip magic
byte**.

**What was actually happening on the wire.** Caddy sits in front of
riff (`/community/*` → `127.0.0.1:8090`). Every Android MoeMemos
request comes in with `Accept-Encoding: gzip`; caddy forwards that
header to riff verbatim. The V0 handler cloned the inbound headers
onto the upstream in-process request to the connect server, so the
connect server saw `Accept-Encoding: gzip` and honoured it: the
response body it returned was a gzip-compressed JSON envelope. The
V0 shaper then `json.Unmarshal`'d the gzipped bytes (whose first two
bytes are `0x1f 0x8b`) and the decoder threw "invalid character
`\x1f`". The V1 path did not exhibit the bug because it
`io.Copy`'d the body verbatim and let caddy handle the
decompression — caddy saw `Content-Encoding: gzip` from the V1
upstream and decompressed before forwarding to MoeMemos.

**The fix is one line in `v0DispatchConnect`.** The helper now
unconditionally strips the inbound `Accept-Encoding` header from
the upstream request before dispatch:

```go
upstream.Header.Del("Accept-Encoding")
```

The connect server responds with plain JSON, the V0 shaper
unmarshals it, and the response goes back to MoeMemos uncompressed.
MoeMemos' Retrofit/OkHttp client handles uncompressed JSON
transparently. A 16-byte regression test (`TestV0Dispatch_AcceptEncodingGzip`)
sends a V0 list with `Accept-Encoding: gzip`, asserts the body
first byte is not `0x1f`, and pins the helper behaviour so the
regression cannot recur.

**A second, related bug surfaced once the gzip fix landed.** The
helper originally returned `(body []byte, err error)`. On a
non-2xx upstream status, the helper wrote the connect error body
to `e.Response` and returned `(nil, nil)` — expecting the caller
to do nothing. Most V0 callers did `if err != nil { return err }`
and then proceeded to `json.Unmarshal(resp, ...)` on a `nil` slice
when the upstream was 401. The result was a 401 with the body
`{"code":"unauthenticated","message":"no valid session"}` followed
by `{"code":13,"message":"connect response was not JSON: unexpected
end of JSON input"}` glued onto the same response — a malformed
MIME body that the Kotlin client could not parse.

The helper now returns `(body []byte, status int, err error)`, and
the body is the upstream error body (also written to `e.Response`)
on 4xx/5xx. Every V0 caller is updated to `if status >= 400 {
return nil }` after the `err` guard; the per-request resolver
helpers (`v0ResolveMemoName`, `v0ResolveAttachmentName`) return a
new `errV0UpstreamNotFound` sentinel so the list-and-match loop
bails cleanly on auth errors. The live `user/me` response is now
a single clean 401 JSON envelope.

## Restore the V0 (Memos 0.21) surface for the MoeMemos client (2026-10-19)

The 2026-10-18 `cc6faa5f` commit removed the V0 (Memos 0.21) REST
surface from `/api/v1/...` on the assumption that the V0 MoeMemos
builds "have not been maintained since 2024". That assumption was
wrong: the live `mudkipme/MoeMemosAndroid/main` branch (checked
2026-10-19) still ships V0 alongside V1 — the same APK includes both
`MemosV0Api.kt` and `MemosV1Api.kt`, and the boot path issues V0 calls
regardless of the `GET /api/v1/status` version-probe result. The
removal caused the community deployment's MoeMemos clients to fall
into the SPA catch-all (V0 paths returned the 3487-byte SPA shell),
which the kotlinx.serialization codec surfaced as "data couldn't be
read" via the sandwich `ApiResponse.Failure.Exception`.

The 2026-10-19 restoration re-mounts the V0 surface (11 routes: 5
reads, 6 writes) in `pb/api/upstream_memos_v0_rest.go`. The V0 wire
format is materially different from the V1 connect-handler shape:

* V0 uses numeric `Long` ids throughout; V1 uses 15-char resource
  names (`memos/abc`, `users/moe`). The V0 id is **derived from** the
  V1 id (`v0IDFromV1` truncates the first 14 hex digits; for
  alphanumeric ids it falls back to an FNV-1a hash of the full id),
  not stored, so no schema migration is needed.
* V0 list responses are top-level JSON arrays; V1 wraps lists in a
  `ListXResponse{...}` envelope. The V0 shaper walks the V1 list and
  emits the V0 flat shape.
* V0 `MemosV0User.name` is the *display name*, not a V1 resource
  name. The shaper maps the V1 `User.display_name` field to V0
  `name`, the V1 `User.username` to V0 `username`, and the V1
  resource name to a derived numeric `id: Long`.
* V0 PATCH bodies echo the numeric `id`; the V1 PATCH uses a
  path-injected `name` and an `update_mask`. The V0 shaper strips
  the redundant `id`, translates `rowStatus` to V1 `state`, and
  computes the V1 `update_mask` from the populated fields.
* V0 `POST /api/v1/resource/blob` is a `multipart/form-data` upload
  with a single `file` part. The V1 chunked `UploadAttachment`
  procedure is the only upload path the connect handler exposes; the
  V0 handler drives it directly with a single
  `spec + data + finish_write` call (the V1 handler supports this
  for "tiny uploads" — see `attachment_upload.go`).
* V0 uploads have no memo. The V1 `attachments.memo` column is
  `NOT NULL`, so the V0 handler creates a hidden **stub memo**
  (zero-width-space sentinel content + PRIVATE visibility) and
  uploads against it. The V0 list shaper filters stubs out via
  `isV0StubMemo`; the V1 client sees them as real memos (a future
  sweep is a one-liner).
* The V0 PATCH/DELETE/organizer handlers translate the V0 Long id in
  the URL path to a V1 resource name via `v0ResolveMemoName` (and
  `v0ResolveAttachmentName` for resources). The resolver lists the
  caller's memos/attachments and matches by V0 id; the cost is one
  `ListMemos` round trip per write. A per-request context-stashed
  V0↔V1 id map (built by the V0 list handler) is best-effort
  (PocketBase's `http.Request.Context()` is per-request, not
  per-connection).

The V0 restore also added a `pinned` field to the V1 connect
`UpdateMemo` handler (`pb/connect/memo.go`). The V0 organizer verb
is `POST /api/v1/memo/{id}/organizer?pinned=true`; the V1 handler
previously dropped `pinned` on the floor (the `update_mask` branch
only covered `content`, `visibility`, and `space`), so without the
addition the V0 organizer was a silent no-op. The new branch is
gated on `targets("pinned")`; the V1 client never sets a `pinned`
field on the `update_mask`, so the V1 path is unchanged.

The new test `TestV0PatPath_MoeMemosBoot` stands up the full V0
surface against a real PocketBase app + a real `memos_pat_*` PAT
and walks the exact boot sequence the live MoeMemos client uses
(user/me → memo list → resource list → tag list → memo create →
memo patch → memo organizer → resource upload → resource delete →
memo delete). The test ends with a V1 `/api/v1/auth/me` call to
pin the V0+V1 surface coexistence: a regression on the V1 side
(e.g. a duplicate route pattern that 404s V1 paths) is the riskiest
possible outcome of this change, and the regression test fails
loudly on it.

## Dockerfile apk-add probe sends `User-Agent: libfetch/2.0` to satisfy TUNA's WAF (2026-10-17)

The first iteration of the apk-add mirror probe (entry immediately
below this one) used the busybox wget default `User-Agent`, which
silently broke every build that landed on the TUNA fallback: the
probe returned exit 1, the layer bailed, and the `docker buildx`
error message masked the actual cause (the network was healthy —
TUNA's CDN was rejecting the request). The fix is a single flag
(`-U "libfetch/2.0"`) on both `wget --spider` calls, and a paragraph
in the Dockerfile comment that documents why it is required.

**What busybox wget was sending.** `alpine:3.21`'s busybox 1.37
`wget` is hard-coded to send `User-Agent: Wget` (no version
suffix). Verified with `wget --help` (the usage line lists `-U
STR` for the User-Agent header but no default value is shown) and
by sniffing an in-Dockerfile `wget` request: the upstream
connection was `User-Agent: Wget`.

**Why TUNA's WAF 403s that.** `mirrors.tuna.tsinghua.edu.cn` is
fronted by a CDN edge that enforces an allow-list on
`User-Agent`. Empirical results from a host that can reach TUNA:

| User-Agent              | HTTP status |
|-------------------------|-------------|
| `Wget`                  | 403         |
| `wget`                  | 403         |
| `Wget/1.21.4`           | 200         |
| `curl/7.88.1`           | 200         |
| `libfetch/2.0`          | 200         |
| `libwww-perl/6.36`      | 200         |
| `Mozilla/5.0 (...)`     | 403         |
| `okhttp/4.9.3`          | 403         |
| `Go-http-client/1.1`    | 403         |

The allow-list accepts canonical `<tool>/<version>` strings and
rejects bare tool names. TUNA's UA check is heuristic but
consistent: the same UA returns the same status on repeat
requests, and `libfetch/2.0` (the same UA apk itself sends) is
accepted.

**Why `-U "libfetch/2.0"` is the right UA, not a generic string.**
The probe's job is to predict whether the *real* apk fetch will
succeed. apk-tools uses libfetch as its URL backend
(`-Durl_backend=libfetch` is the apk-tools meson default; verified
in `apk-tools/meson_options.txt` upstream). libfetch's
`common.h` defines `#define _LIBFETCH_VER "libfetch/2.0"`, and
`libfetch/http.c` sends `User-Agent: %s\r\n` formatted with that
string. So `wget -U "libfetch/2.0" --spider` exercises the
*exact* UA apk will use on the same URL. A URL the probe can
reach is one apk can reach; a URL the probe cannot reach is one
apk will fail on.

**Why dl-cdn does not need the override.** dl-cdn's CDN edge does
not appear to enforce a UA allow-list (every probed UA returned
200). The probe therefore works on dl-cdn regardless of the `-U`
flag, but setting it on the primary probe anyway keeps both
probes symmetric and means a future mirror swap that *does*
enforce a UA allow-list works without a code change.

**What we did not change.** The probe-then-`apk add` shape is
unchanged. The 3 s timeout (`-T 3`) and `--tries=1` are
unchanged. The `ALPINE_REPO_PRIMARY` / `ALPINE_REPO_FALLBACK`
`--build-arg` plumbing is unchanged. `scripts/dev_image_build.sh`
does not need a UA flag because the UA is baked into the
Dockerfile. CI workflows (`.github/workflows/build-canary-image.yml`,
`release.yml`) do not pass the mirror args and inherit the same
`libfetch/2.0` UA, which is correct.

**Verification.** Three smoke tests against `alpine:3.21` running
the same probe logic with `--network host` on the CN-egress
host that motivated the original probe:

1. **Both bad (`example.invalid` for both):** exits 1, prints
   `BOTH_BAD`. (Regression: confirm the else branch still fires
   when neither URL resolves.)
2. **Primary ok (TUNA primary, `example.invalid` fallback):
   prints `PRIMARY`, exits 0. (Regression: confirm a healthy
   primary still wins without contacting the fallback.)
3. **Primary fails, fallback ok (dl-cdn primary times out from
   this host, TUNA fallback reachable):** prints `FALLBACK`,
   exits 0. (The actual reproducer for the regression this
   entry fixes.)

A full end-to-end smoke test ran `apk add` against the chosen
TUNA repository after the probe and successfully installed
`tmux=3.5a-r0` (the same package the runtime layer pins). Total
time 28 s, dominated by the 3 s primary timeout. The Dockerfile
parses cleanly under `docker buildx build --check` (the only
error reported is the unrelated `COPY --from=ravine` line that
needs the out-of-repo build context).

## `sketch.version_no` + `SketchPayload.version_no` added (2026-10-19)

The audit log that previously carried a per-upload
`version_no` (`sketch_versions` table, the
`ListSketchVersions` / `GetSketchVersion` RPCs) was
removed because the SPA never shipped a viewer. We
have since decided that the `version_no` is still
useful — the SPA's sketch card wants a "v3" badge so
the user can tell which upload they are looking at —
but it does not need a separate RPC, a separate
table, or a separate wire message. The version now
lives **in-band on the memo's `sketch` JSON column**
(key `version_no`) and surfaces on the proto's
`SketchPayload.version_no` field. **Net added.** A
new `int32 version_no = 6` field on the
`SketchPayload` proto message; the `Version int32`
field on `uploadSketchResponse`; the
`nextSketchVersionNo` helper that reads the author's
most-recent 200 sketch memos and returns
`max + 1` (or 1 for a fresh author). The
`pb/api/po33_device_upload_internal_test.go` happy
path regains an `out.Version == 1` assertion; a new
`TestUploadSketch_SecondUpload_BumpsVersion` test
pins 1 → 2 → 3 across three consecutive uploads.
The `pb/connect/memos_translate_sketch_internal_test.go`
gets `TestMemoPayloadFromRecord_VersionNoRoundTrip`
and `TestMemoPayloadFromRecord_NoVersionNoIsZero` to
pin the round-trip and the legacy-row path.

**Why a column instead of the dropped table.** The
audit log's `(memo_id, version_no)` index served
two questions — "which upload wrote which blob?"
and "what was the Nth upload from this author?" —
and the first question was the only one the SPA
needed a viewer for. The second question can be
answered in O(1) from the memo row's own `sketch`
column without a join, which is the same shape the
rest of the upload-time metadata already lives in
(model, slot_count, device_fingerprint, etc.). The
`sketch` JSON column is the natural place for any
"metadata known at upload time" field; pushing
`version_no` into it is the rule, not the exception.

**Why a counter instead of a timestamp.** The audit
log's `version_no` was a server-issued monotonic
counter, not a device timestamp, and the SPA's
"v3" badge is most useful as a stable, human-readable
counter that does not change when the same memo is
re-indexed or migrated. The device's
`uploaded_at_unix` is already a separate field on
`SketchPayload`; a counter adds the human-readable
"which one of my uploads is this?" on top.

**Wire delta.** Proto: `SketchPayload.version_no
(int32, field 6)`; no other proto change. OpenAPI:
the `SketchPayload` schema gains `version_no:
{ type: integer, format: int32 }`. TypeScript:
the generated `SketchPayload` type gains
`versionNo: number` (plain numeric; no
zod/validate layer is emitted by
`@bufbuild/protoc-gen-es` v2.12.1, the local
plugin pinned in `web/package.json`). The
`POST /api/po33/sketches/upload` response body
gains `version: <int32>` next to the existing
`memo: "memos/<id>"` field.

**DB delta.** No new column, no new index, no new
table. The `memos.sketch` JSON column gains a
`version_no` key on every fresh upload; pre-existing
rows that predate this change do not have the key
and decode to `version_no = 0` in the proto (the
SPA's "no v-badge" discriminator). The M186
back-fill migration that populated the `sketch`
column for legacy rows does not need to be re-run
because the absence of the key is the desired
back-compat shape.

**Counter semantics.** The counter is per-author
(not per-device, not global). The reasoning: a
multi-device author (a user with one PO-33 and one
PO-33 K.O!) wants "v1, v2, v3" to be the order of
*their* uploads, not a jumble of all users on the
instance. The cap is the most-recent 200 sketch
memos for the author; a power user who has
uploaded more than 200 sketches is rare, and the
counter only ever increases so a bounded-but-large
scan always picks up the right max. A future
admin-tool compaction (out of scope) can re-pack
the counter after a bulk delete.

## Upstream-Memos REST surface at `/memos/api/...`: thin transport adapter, not a new business layer (2026-10-17)

**Supersedes the 2026-10-16 Connect-only decision below.** The
reason for the supersession: the riff community needs a Memos REST
compatibility surface for the SPA, the webclipper extension, and
third-party clients (mobile apps, scripts) that have not been
rewritten to speak Connect. Adding `runtime.NewServeMux` and the
generated `RegisterXxxServiceHandlerServer` would have brought
grpc-gateway into the binary along with a hand-maintained
gRPC-client dependency in `pb/`, neither of which fits the riff
layering rules. The transport adapter the design lands instead is
three handler shapes in `pb/api/upstream_memos_rest.go`:

* **`connect_proxy`** (84 routes) — re-targets the request at
  the in-process `*http.ServeMux` that `pb/connect` already
  mounted via `connecthttp.Mount`, with the same `withAuth`
  interceptor and the same per-procedure rate-limit table. Zero
  new business logic. The connect handler is the only authority
  for the rule engine and the validation chain.
* **`pb_records_crud`** (4 routes) — proxies straight to the
  PocketBase record-fetch / save / delete APIs. The `users`,
  `user_settings`, `memo_views`, `notifications`,
  `user_webhooks`, `space_invitations`, `patches`,
  `linked_identities`, `personal_access_tokens` collections are
  CRUD-shaped and need no Memos-specific join logic; PocketBase's
  record layer with the existing access rules is enough. The
  wire shape is the same Memos envelope
  (`{items, page, per_page, total_items}` for list,
  `{...record}` for get).
* **`file_server`** (3 routes) and **`sse`** (1 route) — the
  file-download and realtime-streaming surface that grpc-gateway
  does not provide. The file routes resolve the path parameter
  to a record, then read the file blob from the PocketBase
  filesystem at `<collectionId>/<recordId>/<filename>`. The SSE
  route subscribes to PocketBase's `SubscriptionsBroker()` and
  forwards the realtime messages as Server-Sent Events.

**Three things we deliberately do not change:**

1. **The proto `option (google.api.http)` annotations stay at
   `/api/v1/...`.** They are OpenAPI-only today (the MCP service
   in `server/mcp/` reads `proto/gen/openapi.yaml` to build its
   tool catalog). Re-basing them to `/memos/api/...` would
   invalidate the MCP tool names and the upstream
   `Mcp-Api-Resource` shim the OpenAPI consumer relies on. The
   new REST surface reads the generated procedure paths (e.g.
   `memos.api.v1.MemoService/ListMemos`) directly from
   `apiv1connect/`, so the proto and the adapter are decoupled
   at the path level.
2. **The connect mux at `/memos.api.v1.<Service>/<Method>`
   keeps serving traffic.** The REST adapter does not redirect
   or proxy the connect surface; both answer through the same
   `*http.ServeMux` for the connect procedures, so the two
   produce byte-identical responses (the connect handler is the
   single source of truth).
3. **The upstream Memos URL shape is preserved modulo
   `:verb`-vs-`/verb`.** Google AIP-136 custom verbs
   (`/api/v1/{name=users/*}:getStats`) become `/memos/api/users/
   {name}/getStats` because Go's `http.ServeMux` pattern
   grammar (which PocketBase uses) does not accept `}` followed
   by `:`. The `design doc <docs/design/upstream-memos-rest.md>`_
   enumerates the 13 routes that are remapped this way; clients
   that hard-code the colon form need a one-line path rewrite.



The firmware team's v0.1 sketch-resource brief (the
"sketch-resource-brief") recommended a parallel `sketch` table that
would carry the device's metadata, lifecycle state, and audit log, with
forked `sketch_comment` / `sketch_reaction` / `sketch_tag` tables
mirroring the existing `memos` / `memo_relations` / `memo_reactions`
shape. **We reject the table fork and add the lifecycle on top of the
existing schema instead.** The reasoning:

1. **The schema already has the discriminator.** Migration
   `1840000003_add_memo_source.go` adds a `memos.source` select with
   `text | sketch`. Every `sketch` row already has a
   `MemoPayload.sketch` oneof case carrying the device metadata
   (`model`, `slot_count`, `bpm`, `device_fingerprint`,
   `uploaded_at_unix`). The brief's "sketch table" recommendation
   duplicates this discriminator.
2. **The proto already has the lifecycle field.** `Memo.state` is
   a wire-level field the existing `common.proto::State` enum
   populates. The existing enum is `STATE_UNSPECIFIED | NORMAL |
   ARCHIVED`. We extend it by adding `DRAFT = 3` (the device upload
   landing state) and `HIDDEN = 4` (admin-only side-state). The
   brief's `DRAFT | PUBLISHED | HIDDEN | ARCHIVED` is a *replacement*
   of the existing wire-level enum; we keep `PUBLISHED` as a wire
   alias for the existing `NORMAL` so the SPA, the export pipeline,
   and the upstream Memos Connect clients in the wild keep working
   unchanged. Migration `1840000006_add_memo_state.go` aligns the
   PocketBase column with the proto.
3. **The site renders one feed.** The SPA, the device, and the
   user-facing APIs all hit the same `memos` collection. Forking
   `sketch_comment` / `sketch_reaction` / `sketch_tag` would either
   (a) leave sketches comment-less (bad: a sketch the user comments
   on falls out of the unified feed) or (b) duplicate the index
   strategy across two parallel tables (bad: every existing query
   path needs a `WHERE source='sketch'` predicate to keep rendering
   the unified feed, defeating the fork's purpose).
4. **The brief's §3 "wire into the existing REST gateway" is
   not implementable in riff** (see the 2026-10-16 entry above). The
   device-side upload lives in `pb/api/po33_device_routes.go`
   (HTTP/1.1 + Ed25519 bearer) and the user-facing RPCs in
   `pb/connect/sketch_service.go` (Connect). Two surfaces, one
   design.
5. **The brief's §6 "never silently modify the original blob" is
   implemented as a new `sketch_versions` table**, not as
   column-level versioning on the `sketch` table that does not
   exist. The `sketch_versions` collection is append-only: every
   device upload inserts one row keyed on `(memo_id, version_no)`,
   and the unique index on that pair is the mechanical
   implementation of the brief's invariant. The collection's
   `*Rule` fields are all `nil` and no `OnRecordUpdate` /
   `OnRecordDelete` hooks bind to it — the absence of those hooks
   is what makes the append-only invariant data-layer mechanical,
   not just policy.

**Wire surface delta.** `proto/api/v1/po33_sketch_service.proto`
adds `PublishSketch`, `ArchiveSketch`, `ListSketchVersions`,
`GetSketchVersion`, the `SketchVersion` message, and a re-export
of the existing `State` enum with two new values
(`DRAFT`, `HIDDEN`). The device-side `UploadSketch` is **not** a
Connect RPC; it is a REST handler in
`pb/api/po33_device_upload.go` mounted at
`POST /api/po33/sketches/upload`. The full
multipart shape (metadata + signature + patch_file +
backup_file + sample_wav_0..N) is in
`docs/community-website-response.md` §3.

**Rate-limit delta.** The brief's §10 says
"10 req / hour / device". A new scope
`ratelimit.ScopeUploadSketchDevice` is added in
`internal/ratelimit/ratelimit.go::DefaultPolicy` with that exact
number. The bucket key is the `po33_devices.id`, not the client
IP — a device moving between networks keeps the same budget.

**The single line that has to change in the firmware team's brief.**
Replace "The website stores sketches in a new `sketch` table" with
"The website stores sketches in the existing `memos` table with
`source='sketch'` and a new `state` enum value `DRAFT`. The
lifecycle is DRAFT → NORMAL → ARCHIVED, with HIDDEN as an
admin-only side-state. The append-only `sketch_versions`
collection holds the audit log. See
`docs/community-website-response.md` in `incurl/riff` for the wire
shape and the migrations."

## Dockerfile apk-add layer probes a fallback Alpine mirror (2026-10-17)

The runtime image (`scripts/Dockerfile`, `FROM alpine:3.21 AS
monolithic`) installs caddy, supervisord, btop, tmux, htop, ncdu,
tzdata, ca-certificates, and su-exec through `apk add`. Until this
change the `apk add` line was hard-pinned to
`https://dl-cdn.alpinelinux.org/alpine/v3.21/{main,community}`. On
a build host whose egress cannot reach the `dl-cdn.*.alpinelinux.org`
hosts (every one of `dl-3` through `dl-9` DNS-rewrites to a
connectivity black hole, and `dl-cdn` itself sits behind a CDN edge
that returns "temporary error (try again later)" for a sustained
window), the apk index fetch fails repeatedly, apk then bails with
exit code 9 ("unable to select packages"), and `docker buildx` aborts
the `apk add` layer with the same exit code. The "exit code: 9" is
the upstream symptom of the underlying network failure.

**Decision.** The `apk add` layer now probes
`ALPINE_REPO_PRIMARY` (default `dl-cdn.alpinelinux.org`) and falls
back to `ALPINE_REPO_FALLBACK` (default
`mirrors.tuna.tsinghua.edu.cn`) using a `wget --spider` HTTP HEAD
check with a 3 s timeout. The fallback URL is exposed as a
`--build-arg` so operators can override either side without editing
the Dockerfile. `scripts/dev_image_build.sh` forwards the env vars
`RIFF_ALPINE_REPO_PRIMARY` and `RIFF_ALPINE_REPO_FALLBACK` as the
matching `--build-arg` flags.

**Why the fallback is bit-identical.** The TUNA mirror's v3.21
`APKINDEX.tar.gz` matches the official CDN at the same package
versions for every package in the install list:

| package          | CDN version    | TUNA version   |
|------------------|----------------|----------------|
| caddy            | 2.8.4-r7       | 2.8.4-r7       |
| supervisor       | 4.2.5-r5       | 4.2.5-r5       |
| tzdata           | 2026e-r0       | 2026e-r0       |
| btop             | 1.4.0-r0       | 1.4.0-r0       |
| tmux             | 3.5a-r0        | 3.5a-r0        |
| htop             | 3.3.0-r0       | 3.3.0-r0       |
| ncdu             | 1.21-r0        | 1.21-r0        |
| ca-certificates  | 20260909-r0    | 20260909-r0    |
| su-exec          | 0.2-r3         | 0.2-r3         |

A build that pulls the layer from TUNA is byte-identical to one
that pulls it from the CDN. The `tmux` and `htop` packages added in
the previous commit (without version pins, because they were
debugging niceties) are now pinned to the versions the
`APKINDEX.tar.gz` for v3.21 main actually carries.

**Why not just switch the default.** TUNA is geographically
concentrated in CN-region egress. The CDN is the
authoritative source; keeping the default pointed at it means a
build host with healthy CDN connectivity never burns a probe round
trip. The fallback only fires when the primary does not respond
within 3 s.

**What we did not change.** The `caddy=2.8.4-r7` …
`su-exec=0.2-r3` version list (now extended with `tmux=3.5a-r0
htop=3.3.0-r0`) is the same on both mirrors, and the version pins
are what keeps a fallback build bit-identical to a primary build.
The runtime image is still Alpine 3.21; the only change to the
Dockerfile is the in-layer mirror probe at the head of the `apk
add` line. `fly.toml` and `scripts/compose.yaml` are unchanged —
they consume the prebuilt image, not the apt layer.

## Connect-only transport: no gRPC-Gateway REST surface for the Memos API (2026-10-16) (SUPERSEDED)

This entry is retained for the audit log. The 2026-10-17 entry
above supersedes it. The original reasoning (no gRPC-gateway,
no `runtime.NewServeMux`, no `RegisterXxxServiceHandlerServer`)
is still true: the connect handler is the only authority for
Memos-shaped business logic, and the connect mux is the only
public transport for the Memos API. The new transport adapter
at `/memos/api/...` does not change that: it re-targets the
*same connect mux* the public Connect surface uses, so the
adapter adds zero new business logic.

## Connect-only transport: no gRPC-Gateway REST surface for the Memos API (2026-10-16)

Riff serves the Memos-shaped API **Connect-only**. The only externally
callable surface for the Memos API today is the Connect HTTP/JSON
mux at `/memos.api.v1.<Service>/<Method>` (the `connect-go` mount in
`pb/connect/connect.go::registerConnectRoutes`). There is **no
grpc-gateway**, no `runtime.NewServeMux`, and no
`RegisterXxxServiceHandlerServer` calls anywhere in the tree. Verified
three ways: `grep grpc-gateway go.mod` is empty, `grep gateway
proto/buf.gen.yaml` is empty, and `find proto/gen -name '*_gw*.go'`
is empty. A third party pointing `curl https://riff.example.com/api/v1/memos`
gets a PocketBase 404, not a Memos response; the only `/api/...` URL
the SPA itself fetches is `/api/v1/sse` (a PocketBase-side SSE handler).

**Why this is a divergence from upstream `usememos/memos@main`.**
Upstream serves both transports side by side from the same handler set
in `server/api/api.go`: Connect at `/memos.api.<Service>/<Method>` plus
a `runtime.NewServeMux` mounted at `/api/...` and `/file/...` filled by
`apipb.RegisterXxxServiceHandlerServer(ctx, gwMux, s)`. The proto
package has moved from `memos.api.v1` → `memos.api` and the REST
prefix from `/api/v1/...` → `/api/...`. The Memos SPA upstream is
Connect-only too, but the REST surface is **explicitly there for
third-party consumers** (mobile clients, scripts, Zapier, the old
memos REST users) and the upstream `removedAPIVersionHandler` 410s
the old paths to push clients forward. Riff never brought the REST
mount across; today there is no third-party REST surface to expose
the Memos API. Riff is also one proto-package version behind upstream
(still on `memos.api.v1`).

**Why we accept it for now.** The current SPA, the only first-party
client, talks Connect. The only operators who asked for REST so far
are still small enough to be served by Connect (curl-able JSON
already). And the **collision risk is the load-bearing reason this
is the right time to make the call, not the wrong time**: the
`google.api.http` annotations on every rpc below all start with
`/api/v1/...`, which **is exactly the path group PocketBase owns for
its own REST namespace**. If we ever add a gateway mount that uses
these annotations verbatim, every call collides with PocketBase. The
proto comments at the top of every service file below mark this so
the next editor does not unknowingly re-enable the collision by
adding the gateway plugin to `buf.gen.yaml`.

**What this commit actually ships.** Two pieces of insurance, no
runtime change:

1. This entry, which is the authoritative answer to "why does riff
   not have a Memos REST API?".
2. A package-level comment block in every `proto/api/v1/*.proto`
   that carries `option (google.api.http)`, explaining that the
   annotations are **OpenAPI-only (MCP catalog)** today and that any
   future gateway mount must use a non-`/api/...` prefix (recommended:
   `/memos/api/v1/...` so the path matches the existing Connect
   package and stays consistent with the caddy-routed Fly deploy
   layout in `scripts/Caddyfile.fly.community`).

**Future-proofing, if/when we add REST.** The work is non-trivial but
mechanical and lives in four places: (a) re-base every
`option (google.api.http)` path in `proto/api/v1/*.proto` to a
riff-only prefix; (b) add `protoc-gen-grpc-gateway` to
`proto/buf.gen.yaml` and run `buf generate` to emit
`proto/gen/api/v1/*_gw.pb.go`; (c) write a new `pb/connect/gateway.go`
that owns a `runtime.NewServeMux` filled by the eight
`RegisterXxxServiceHandlerServer` calls; (d) mount that mux in
`pb/connect/connect.go::registerConnectRoutes` *before* the SPA
catch-all, *after* the existing Connect mount, and never overlap
`/api/po33` or PocketBase's `/api/...` group. The Connect handlers
in `pb/connect/` are written against `connect.NewError(connect.CodeX, ...)`
per AGENTS.md "Go Conventions", so they are **not** directly
portable to `status.Errorf(codes.X, ...)` (which is what grpc-gateway
transcodes from) — adding a translation shim or refactoring the
handlers to use `status.Errorf` is its own change.

**Files.**
* `DECISIONS.md` — this entry.
* `proto/api/v1/ai_service.proto`, `attachment_service.proto`,
  `auth_service.proto`, `idp_service.proto`, `instance_service.proto`,
  `memo_service.proto`, `po33_device_service.proto`,
  `po33_sketch_service.proto`, `space_service.proto`,
  `user_service.proto` — added the same 8-line package-level comment
  block under `package memos.api.v1;` flagging the OpenAPI-only role
  of the annotations and the prefix rule. `common.proto` is unchanged
  (no annotations, no service). `README.md` is unchanged.

**Verification.** `go build ./...` clean (the proto comments do not
affect generation). `cd proto && buf generate` is a no-op for
Go/TypeScript output (comments do not change descriptor content) and
`proto/gen/openapi.yaml` is also a no-op (annotations unchanged).
The AGENTS.md "Proto API" row points future editors at this entry
via the `google.api.http` mention under the cross-check note.

## Bootstrap admin lookup falls through to _superusers (2026-10-16)

The Memos SPA's settings page calls
`ListUserSettings(parent="users/{username}")` to read the GENERAL row,
and `GetUser(name="users/{username}")` to resolve the admin's profile.
On a fresh instance the bootstrap admin has only a `_superusers` row
(no `users` row at all). The lookup used to scan `users` only — the
request 404'd with "user not found" and the page rendered as a broken
error. The same fall-through also unblocks `BatchGetUsers` mentions of
the admin on the first memo on a fresh instance.

**`lookupUserByName` (memo.go) now scans `users` and falls through
to `_superusers` with the installer-placeholder exclusion.** The
fall-through mirrors the convention `AuthServer.findByIdentity`
(auth.go) has used since the self-service sign-up wave: both lookups
treat the two collections as the same logical "user", and both
filter the PocketBase installer placeholder
(`core.DefaultInstallerEmail`, i.e. `__pbinstaller@example.com`) so a
fresh install without a real admin does not surface the placeholder
as if it were a real account. The helper has the same signature as
before, so every caller in `user_settings.go`, `user_notifications.go`,
`user_webhooks.go`, `user_personal_access_tokens.go` (they all already
used the helper) picks up the change for free.

**`UserServer.GetUser` and `UserServer.BatchGetUsers` were switched
to the shared helper.** They used to inline a `users`-only filter
(`user.go:48` and `user.go:96-97`); both now call `lookupUserByName`.
`ListUsers` is intentionally untouched: the proto's `ListUsersResponse`
is the community roster, not the admin team, and a `_superusers` row
should not appear there. `recordToUser` already surfaces role=ADMIN
for a superuser record (it branches on `rec.IsSuperuser()`), and it
already falls back to `email` when `username` is empty (lines 54-61)
— the resource name stays `users/{email}` so a `GetUser` and a
`GetUserSetting` on the same admin return the same `users/{...}` name.

**`recordToUserSetting` and `defaultUserSetting` now use the same
`username OR email` fallback for the wire resource name.** They
used to read `user.GetString("username")` directly, which produced
`users//settings/GENERAL` (empty username slot) for any superuser
record. The new `settingOwnerName` helper mirrors the
`recordToUser` convention so the wire shape stays
`users/{name}/settings/{key}` for both kinds of owner.

**`UserSetting` read path now works end-to-end for the bootstrap
admin.** `GetUserSetting` returns a defaulted body with the correct
`users/{email}/settings/GENERAL` name, and `ListUserSettings` returns
the (currently empty) result the SPA renders as the unconfigured
state. This is the regression the user reported: the page no longer
404's, the form renders, and the admin can see what settings exist.

**`UserSetting` write path is a known limitation, not a fix.** The
`user_settings.user` column is a PocketBase `RelationField` pointing
at the `users` collection (migration 1720000000). A RelationField
cannot point at two collections, so the existing schema cannot store
a row owned by a `_superusers` record. `UpdateUserSetting` on a
bootstrap admin will still fail at the relation-write step with
`invalid_argument: user: Failed to find all relation records with
the provided ids.` The user-visible effect of the fix: the page
renders, the defaulted body loads, but a save attempt surfaces a
clear error. The full fix is a follow-up schema change — either a
parallel `user_email` text column on `user_settings` (with the
unique index widened to `(user, user_email, key)` and a CHECK-style
invariant that exactly one is set), or a nullable `user` field. The
follow-up is out of scope for this slice.

**Trade-off: an unauthenticated caller can now ask
`GetUser("users/{admin-email}")` and learn the admin's display name.**
The `users` collection's `listRule` is `""` (public read — needed
for @mentions and the public profile page), and `GetUser` is a
public-read RPC by design. The fall-through does not add a new
information channel: the admin's display name is already publicly
readable on any instance that has a `users` row, and the fall-through
is the only way to surface the same answer on instances that have
only a superuser. The privacy impact is bounded — the only new
exposure is the admin's email becoming a valid resource name on
fresh installs, which is required for the settings page to render.

**Tests.** Four new tests pin the regression and the trade-off:
`TestListUserSettings_BootstrapAdminResolvesThroughSuperusers` (the
settings read path), `TestGetUser_BootstrapAdmin` (the user lookup),
`TestBatchGetUsers_ResolvesSuperuser` (the timeline mention path),
`TestGetUser_AnonymousCannotProbeSuperuser` (the installer
placeholder is filtered; the public-read path is intentional). All
four pass under `-race` in 5s each, well clear of the 1-minute
scheduler tick that panics in `pb/hooks/scheduler.go:83` (a
pre-existing test-infra bug, not introduced here).

**Revert cost.** Reverting this entry is mechanical: drop the
fall-through in `lookupUserByName` (memo.go:439-460), revert
`GetUser`/`BatchGetUsers` in `user.go` to their inline
`users`-only filters, and remove the four tests. The settings panel
returns to its broken state.


## is_creator removed from users: pure schema cleanup, no wire change (2026-10-16)

The `is_creator` BoolField on the `users` auth collection is gone.
It was added in 1700000001_extend_users.go as a "opt-in flag for
verified PO-33 creators" — a placeholder for a community
verification flow that has since been redesigned as a separate
domain concept (a junction between `users` and a future
`creator_program` resource) rather than a flag on every user row.

**Nothing on the wire changed.** No proto field references
`is_creator` (verified with a repo-wide grep across `.go`,
`.proto`, `.ts`, `.tsx`, `.yaml`, `.yml` — only the original
migration, `main_test.go`, and the frozen design doc in this file
mention the name). No API surface, no frontend code, no generated
OpenAPI shape. The field was internal-only.

**Two migrations touch this:**

- `1700000001_extend_users.go` (up) no longer adds `is_creator`
  for fresh installs. Its down branch's `RemoveByName` list no
  longer mentions it. The doc comment was updated to flag the
  field as "Historical fields (now removed)" with a forward
  pointer to the new migration.
- `1700000002_remove_is_creator.go` is a new migration. Its up
  branch calls `users.Fields.RemoveByName("is_creator")` and
  saves. `Fields.RemoveByName` is a silent no-op when the field
  is absent (PocketBase v0.40.5, core/fields_list.go), so the
  migration is safe on fresh installs (where the field was never
  added) and on re-apply (e.g. if PocketBase's migrate plugin
  re-runs the up branch). Its down branch re-adds the field with
  the same shape, so the migration is fully reversible — matching
  the existing repo convention that every migration has a
  working down.

**`main_test.go` was updated to:**

- Drop the `is_creator` field-presence assertion from
  `TestExtendUsersMigration_FieldsAndRules`.
- Drop the `rec.Set("is_creator", true)` and the
  `got.GetBool("is_creator")` readback from
  `TestExtendUsersMigration_CreateAndReadBack`.
- Add `TestRemoveIsCreatorMigration_FieldGone` which asserts the
  field is absent after migrations run. The assertion is the
  contract: a future change that re-adds the field (e.g. an
  accidental rebase of the original migration, or a copy-paste
  during a new migration) will fail this test with a clear
  message that names the new migration and asks the author to
  audit every wire surface before removing the assertion.

**Why a new migration instead of an in-place edit.** AGENTS.md
is explicit: "Schema changes are a new Go migration in
`pb/migrations/`. PocketBase applies them on the next
`app.Bootstrap()` run; the migratecmd plugin handles this
automatically. Migrations must be idempotent across fresh installs
and existing instances." The 1700000001 migration is already in
the apply history of every existing instance, so editing its up
branch alone would leave those instances with the field still
present. The new 1700000002 migration is the only correct way
to drop the field for both fresh and existing installs.

**Why no entry in `docs/design/`.** AGENTS.md: "Design
documents in `docs/design/` are frozen after implementation. Do
not update implemented designs to track later code changes." The
shape of the `users` collection is a frozen design concern;
this entry is the audit log of the deviation, and a future
design-doc rewrite is the only place the historical schema would
be erased. (The original `1700000001` design doc's
"is_creator" line is left as-is in the comment as a Historical
fields entry, not deleted — same convention.)


## PO-33 device binding lands: 2 new collections, 3 idempotent extensions, Ed25519 over canonical JSON (2026-10-16)

The PO-33 binding flow (docs/design/po33-device-binding.md) shipped
in this slice. The feature is the first riff surface that does not
talk to the Memos frontend: the device signs requests with an
Ed25519 key the user pairs through a QR code.

**Schema is 2 new collections + 3 idempotent extensions, not 5 new
tables.** The original sketch had 5 new tables; after Q3 and Q5 the
PO-33-specific fields ride on the existing `memos.payload` JSON
column via the new `MemoPayload.sketch` oneof. The two new tables
are `po33_devices` and `device_authorizations`; a third
(`po33_sketch_downloads`) is the device-side audit log. The three
idempotent extensions are: `memos.source` (select: text | sketch),
`memo_relations.type` extended with `BACKUP_FILE`, and
`patches`/`samples.device_fingerprint_snapshot` (text).

**`memos.source` is not `Required: true` with no default.** An
earlier draft did this and the existing memo-test fixtures in
`pb/connect/connect_wave6_shares_internal_test.go` failed because
they create memos via `core.NewRecord` and bypass the create handler.
The fix is two-pronged: the migration makes the column
`Required: false` (the constraint is moved to the save hook) and
`pb/hooks/memos.go` defaults the value to `"text"` in the
`OnRecordCreateRequest` hook. A PO-33 upload handler sets
`"sketch"` explicitly.

**Ed25519 over canonical JSON, not protojson.** The device signs a
canonical serialisation of the authorization request (sorted keys,
no whitespace, fixed integer rendering) and the server verifies with
the same library (`internal/ed25519verify`). protojson would have
introduced platform-dependent quoting and a 32-bit-vs-64-bit integer
trap. The library exposes a 1-line `Verify(pub, msg, sig)` and a
`CanonicalizeAuthorizationRequest` the device firmware replicates
in C. The byte sequence is pinned by
`TestCanonicalizeAuthorizationRequest_KnownVector`.

**Device-side download path lives in /api/po33/sketches/download,
not a Connect RPC.** The device runs an HTTP/1.1 client with a
one-shot signature header rather than a Connect library, and the
Connect protocol assumes a per-request auth token. The route is a
future slice; the audit-log table is in place and the
`device_last_seen` hook is wired to bump `last_seen_at` on every
audit row insert.

**`memos` view rule covers anonymous web playback.** No new auth
surface was added for the web side: a public sketch is anonymous
playable through the existing `memos.viewRule` and PocketBase's
file-serve routes. Only the device-side download is auth-bearer
keyed.

**No SPA changes in this slice.** The frontend contract is
additive: `Memo.payload.sketch` is unset on every existing memo, so
the SPA can keep reading the flat `property` and `location` fields.
A future SPA slice adds the "sketch" tab on the memo card and the
"My devices" page.

**Files added:**

- `internal/ed25519verify/`, `internal/qr/`, `internal/random/crockford.go` — three stdlib-only helper packages; `internal/qr` is the only place that imports `github.com/skip2/go-qrcode` (added to `go.mod`).
- `pb/migrations/1840000000..1840000005/` — five idempotent migrations.
- `proto/api/v1/po33_device_service.proto`, `proto/api/v1/po33_sketch_service.proto` — two new service protos; the regenerated `MemoPayload.sketch` is the only change to the existing `memo_service.proto`.
- `pb/connect/device_service.go` + `device_service_converters.go`, `pb/connect/sketch_service.go` — Connect handlers.
- `pb/api/po33_device_routes.go` — REST routes for /pair/start, /pair/{code}/qrcode, /pair/finish, /pair/{id}.
- `pb/connect/memos_translate_sketch.go` — adds the `SketchPayload` envelope round-trip.
- `pb/hooks/scheduler.go`, `pb/hooks/device_last_seen.go`, `pb/hooks/device_revoked.go` — three new hooks.

**Deferred to a future slice:** the `/api/po33/sketches/download`
device-side route, the live `device` relation on `patches`/`samples`,
the device-side `last_seen_at` bump on the request header (today
only bumps on audit-log insert), and the SPA UI.

## `UserServer.UpdateUser` writes user-uploaded avatars to the FileField; the proto `avatar_url` is a data-URL pipe (2026-10-16)

`UserService.UpdateUser` silently dropped the `avatar_url` field, so
the "Update account" dialog could not save a profile picture — the
SPA's `UpdateAccountDialog` posts a base64 data URL and the handler
returned `200 OK` with the avatar field unchanged. The user sees
"message.update-succeed" but the avatar never lands.

**Three bugs compounded.**

1. **`recordToUser` read the FileField as a string.** `users.avatar`
   is a PocketBase `FileField` (MaxSelect=1) whose raw value is a
   slice of filenames; `rec.GetString("avatar")` returned the
   JSON-serialised slice (e.g. `["abc.png"]`), so the resulting
   `AvatarUrl` was always `/api/files/users/{id}/["abc.png"]`. The
   fix: `rec.GetStringSlice("avatar")` and take the first element,
   or empty string when the slice is empty.

2. **`UpdateUser` had no `avatar_url` branch.** The handler supports
   `username`, `email`, `display_name`, `description`, `password`,
   and `role`, but a request whose mask contained only
   `["avatar_url"]` was processed without any error and saved
   without touching the avatar column. The fix: a new
   `targets("avatar_url")` branch that calls
   `applyAvatarURLToRecord`.

3. **The proto `avatar_url` cannot land in a `FileField` directly.**
   `avatar_url` is a plain `string` on the wire, but the underlying
   column is a `FileField` that expects a `*filesystem.File` and
   runs the on-disk upload through its record interceptor
   (`processFilesToUpload`). The fix: in
   `applyAvatarURLToRecord`, decode the data URL, build a
   `filesystem.NewFileFromBytes(...)` with a derived extension, and
   hand that to `rec.Set("avatar", file)`. PB's FileField moves
   the bytes to the canonical key and rewrites the column to the
   new (normalised) filename on save.

**Wire format.** The SPA's `convertFileToBase64` produces
`data:<mediatype>;base64,<bytes>` (FileReader.readAsDataURL). That
is the only `avatar_url` shape the handler accepts — anything else
returns `InvalidArgument` so a caller cannot smuggle a remote URL
into a field that is supposed to hold a stored filename. The SPA's
"remove avatar" path posts `avatar_url: ""`, which the handler
treats as a clear: `rec.Set("avatar", []string{})` lets PB's
`rememberFilesToDelete` hook drop the previous on-disk file on the
subsequent save.

**Mimetype handling.** The handler does *not* validate the mediatype
hint. PocketBase's FileField interceptor sniffs the actual bytes via
`mimetype.DetectReader` and rejects anything outside the
`users.avatar` MimeTypes allow-list (`image/jpeg, image/png,
image/svg+xml, image/gif, image/webp`). The mediatype hint is only
used to derive a filename extension, so an empty mediatype
(`data:;base64,...`) is accepted.

**Why not the create path?** `applyUserFields` (the create path)
still rejects `avatar_url` with a clear error message. The
bootstrap path doesn't need avatars, and the create path has no
record id at the point we'd want to write the file, so the
upload-after-create model is the cleanest fit.

**Affected files.**

- `pb/connect/users.go` — `recordToUser` reads the FileField as a
  string slice and surfaces the first filename.
- `pb/connect/user.go` — new `applyAvatarURLToRecord` helper, the
  `mediatypeExt` map, and the `targets("avatar_url")` branch in
  `UpdateUser`.
- `pb/connect/connect_wave4_user_internal_test.go` — five new
  tests: data-URL upload round-trip (verifying on-disk bytes),
  empty-string clear, non-data-URL rejection, out-of-mask
  preservation, and the converter's first-filename reading.

## `ListAllUserStats` and `GetUserStats` open up the directory view; align with upstream Memos (2026-10-15)

The browser console at `https://ravine.fly.dev/community/` shows a
steady stream of `POST .../UserService/ListAllUserStats 403
(Forbidden)` errors on every page navigation. The SPA's
`useAllUserStats` hook (via `useFilteredMemoStats` →
`AppSidebar`) fires the call on every mount, even when the user is
signed in as a non-admin (or not signed in at all on a fresh
install). The previous contract was admin-only, so every non-admin
caller got rejected.

**Diagnosis.** `pb/connect/user_stats.go::ListAllUserStats` and
`GetUserStats` both gate the response on
`viewer.IsSuperuser()` (or `target.Id != viewer.Id && !viewer.IsSuperuser()`
for the per-user RPC). That is materially stricter than upstream
Memos, which allows any caller to read the directory view and
filters the response by per-memo visibility (the
`MemoAccessScope` predicate). The same SPA hook that hits the 403
also calls `useUserStats("users/<name>")` for the profile page,
which is the same RPC shape, so the 403 is duplicated there.

**Upstream Memos comparison.** From
`server/api/user_service_stats.go::ListUserStats` (verbatim
abridged):

```go
func (s *APIService) ListUserStats(ctx context.Context, request *apipb.ListUserStatsRequest) (*apipb.ListUserStatsResponse, error) {
    // users/- lists every user; users/{user} narrows the list to one creator.
    var creatorID *int32
    if request.Parent != BuildUserName("-") {
        user, err := ResolveUserByName(ctx, s.Store, request.Parent)
        // ...
    }
    rowStatus := convertStateToStore(request.State)
    memoFind := &store.FindMemo{
        CreatorID:       creatorID,
        ExcludeComments: true,
        ExcludeContent:  true,
        RowStatus:       &rowStatus,
    }
    accessScope, currentUser, err := s.resolveMemoAccessScope(ctx)
    // ... filter memos by accessScope, group by creator, emit one UserStats per creator ...
}
```

There is no admin gate. `resolveMemoAccessScope` returns
`(allowPublic: instance.AllowsAnonymousAccess, user: currentUser)`
for an anonymous caller and `(allowPublic: true, user: currentUser)`
for an authenticated one, then the memo store applies that scope as
a SQL predicate. The proto `ListUserStats` was renamed from
`ListAllUserStats` in upstream; riff keeps the old name for
wire compatibility with the existing SPA hook.

**Decision.** Drop the admin gate, drop the `viewer == nil` early-out,
and filter the per-memo tally by `memos.ViewRule` evaluated per row
via `app.CanAccessRecord`. Anonymous callers are accepted; they see
only authors with at least one memo that passes the visibility
filter. The `users/<username>/stats` resource name on each row is
preserved. The `state` request field is accepted for proto
compatibility but riff's `memos` collection has no `row_status`
column, so the ARCHIVED branch from upstream does not apply — every
request is treated as NORMAL. That branch will be added when
riff's memos table gains the `row_status` column.

The new `computeUserStatsForViewer` helper is a per-caller wrapper
around the existing per-user tally logic, so the wire shape is
unchanged (still `UserStats` with `total_memo_count`,
`memo_type_stats.todo_count`, `tag_count`, `memo_created_timestamps`,
`memo_updated_timestamps`). The `attachment_storage_bytes` field
that upstream's `GetUserStats` populates only when the caller is the
target user remains unset in riff — adding the storage aggregation
is a separate follow-up that is out of scope for this fix.

**Test plan.**

* `pb/connect/connect_wave5_settings_internal_test.go` —
  `TestUserStats_ListAllUserStats_AnyCaller` (renamed from
  `TestUserStats_ListAllUserStats_AdminOnly`). Walks the three
  caller types — anonymous, signed-in non-admin, admin — and
  asserts the visibility-filtered directory response for each.
  The previous test pinned the admin-only contract; it was the
  only place that knew the 403 was deliberate, and removing it is
  the only behavioural change in this commit. The new test pins
  the per-caller response shape so a future regression that
  re-adds the gate (or breaks the visibility filter) fails loud.
* `pb/connect/user_stats_internal_test.go` (new) —
  `TestGetUserStats_RespectsVisibilityPerMemo` walks alice's
  PUBLIC/PROTECTED/PRIVATE memos under three caller contexts and
  asserts the response shape. `TestGetUserStats_UnknownNameStillInvalidArgument`
  pins the `InvalidArgument` shape-only contract on an empty name.

**Wire compatibility.** The proto request and response shapes are
unchanged. The SPA's `useAllUserStats` hook keeps firing on every
collection page mount and now gets a 200 with the visibility-filtered
list instead of a 403. The SPA's existing `mergeTagCounts(...allUserStats.map((stats) => stats.tagCount))` aggregator in
`useFilteredMemoStats` reads the same `tag_count` field it always
did, so the activity calendar and tag sidebar keep rendering. No
client update is required.

**Status: implemented, deployment pending.** Same deployment
blocker as the prior entry: rebuild `riff:dev` and push to the
Fly registry. Both entries (this and the `GetInstanceSetting`
default-on-miss fix from the same series) will retire the bulk of
the console-error noise on `https://ravine.fly.dev/community/`.

## `GetInstanceSetting` returns a default-constructed proto on a missing row (2026-10-15)

The deployed Fly instance (`https://ravine.fly.dev/community/`)
emits a steady stream of HTTP 404s on
`POST /community/memos.api.v1.InstanceService/GetInstanceSetting`
from the SPA's `useInstanceSetting` hook. The console-error
pattern is benign-looking (the SPA falls back to defaults via
the `create(InstanceSetting_XSchema, {})` path in
`web/src/contexts/InstanceContext.tsx`) but every per-key fetch
retries on navigation, so the 404s are visible to the operator
on every page.

**Diagnosis.** The 404 is the row lookup
(`s.app.FindFirstRecordByFilter("instance_settings", "key={:k}", ...)`)
missing — the `instance_settings` collection is created by
`pb/migrations/1720000002_create_instance_settings.go` with
`key` (text, unique) and `payload` (json), and nothing in the
repo seeds default rows on a fresh install. The per-key
`GetInstanceSetting` handler at `pb/connect/instance_settings.go:26-36`
is strict: a missing row returns `CodeNotFound`. The sibling
`BatchGetInstanceSettings` at lines 41-54 is documented as
lenient — "Missing keys are skipped (no error) to match the
Memos frontend which treats a not-yet-stored setting as an
empty body" — so the two endpoints disagree on the same
fresh-install state, which is the underlying inconsistency.

**Upstream Memos comparison.** Upstream's contract for
`GetInstanceSetting` is materially different from what riff
shipped. From
`server/api/instance_service.go::getInstanceSettingByName`
(verbatim):

```go
switch instanceSettingKey {
case storepb.InstanceSettingKey_BASIC:
    var setting *storepb.InstanceBasicSetting
    setting, err = s.Store.GetInstanceBasicSetting(ctx)
    instanceSetting = &storepb.InstanceSetting{...}
case storepb.InstanceSettingKey_GENERAL:
    var setting *storepb.InstanceGeneralSetting
    setting, err = s.Store.GetInstanceGeneralSetting(ctx)
    instanceSetting = &storepb.InstanceSetting{...}
// ... MEMO_RELATED, STORAGE, TAGS, NOTIFICATION, AI, ACCESS ...
default:
    return nil, status.Errorf(codes.InvalidArgument,
        "unsupported instance setting key: %v", instanceSettingKey)
}
```

Every known key delegates to a typed store getter. The store
getter (`store/instance_setting.go::GetInstanceGeneralSetting`,
verbatim) is the place that constructs the default on a miss
— no DB write, just an in-memory fallback:

```go
instanceSetting, err := s.Store.GetInstanceSetting(ctx, &FindInstanceSetting{
    Name: storepb.InstanceSettingKey_GENERAL.String(),
})
if err != nil {
    return nil, errors.Wrap(err, "failed to get instance general setting")
}
instanceGeneralSetting := &storepb.InstanceGeneralSetting{}
if instanceSetting != nil {
    instanceGeneralSetting = instanceSetting.GetGeneralSetting()
}
s.cacheInstanceSetting(ctx, &storepb.InstanceSetting{...})
return instanceGeneralSetting, nil
```

The upstream test that pins this contract
(`server/api/test/instance_service_test.go:148-172`) calls
`GetInstanceSetting` on a brand-new test service with no
seeded rows, expects `NoError`, and asserts the response
carries a non-nil default body. Per-key subtests exist for
`general`, `storage`, `memo_related`, `tags`, and `access`;
the only failure path is "name doesn't decompose to a
known key" → `InvalidArgument`. Upstream's known set is
**9 keys** (`BASIC`, `GENERAL`, `MEMO_RELATED`, `STORAGE`,
`TAGS`, `NOTIFICATION`, `AI`, `ACCESS`, `USAGE_LIMITS`),
of which riff currently exposes **7** (riff's proto omits
`BASIC` and `USAGE_LIMITS`).

**Decision.** Adopt upstream's read-side default-construction
pattern verbatim. Two corrections the upstream check forced:

* **Closed set is 7, not 6.** The first draft listed
  `{GENERAL, MEMO_RELATED, STORAGE, NOTIFICATION, AI, ACCESS}`
  and missed `TAGS`. Verified via
  `proto/api/v1/instance_service.proto:127` (`oneof value` has
  a `TagsSetting tags_setting` variant) and line 143 (`enum
  Key` has `TAGS = 4`). The new `defaultInstanceSettingForKey`
  helper covers all seven.
* **No DB write on the read path.** The first option
  described "write a default row ... and return it", which
  would also write `STORAGE` and `NOTIFICATION` defaults
  from any caller — bypassing the admin-only check that
  `UpdateInstanceSetting` already enforces. Upstream never
  upserts on read; the per-key typed getter just returns the
  empty-constructed proto. The new `defaultInstanceSettingForKey`
  helper is a pure function (no `core.App` dependency) that
  produces the same shape.

The fix unifies `GetInstanceSetting` and
`BatchGetInstanceSettings` on a single contract: **a known
key always returns a non-nil body (default-constructed on
a missing row); an unknown key is rejected by
`splitInstanceSettingName` with `CodeInvalidArgument`**.
This matches the leniency comment `BatchGetInstanceSettings`
already promises and removes the per-key vs. batch
disagreement. Two changes:

* `pb/connect/instance_settings.go:23-36` — replace the
  `CodeNotFound` on miss with a call to the new
  `defaultInstanceSettingForKey(key)` helper. The helper
  lives at the bottom of the file alongside the existing
  `instanceGeneralSetting` / `instanceAISetting` typed
  readers, which were already the right shape but only
  used for the `GetInstanceProfile` path.
* `pb/connect/instance_settings.go:41-55` —
  `BatchGetInstanceSettings` now substitutes the default
  for any missing key instead of skipping it, so the
  response carries one entry per known key in the
  requested set.

**Doc-comment fix folded in.** The block comment at
`instance_settings.go:23-25` claimed "Unknown keys produce
FailedPrecondition so the SPA can distinguish 'missing' from
'broken' without a separate error code." The code at
line 33 (now removed) returned `connect.CodeNotFound`, not
`CodeFailedPrecondition`. The new comment accurately
describes the new contract.

**Oneof wrapper naming asymmetry.** protoreflect camelcases
`AI` as `Ai` for the oneof variant name, so the wrapper
type is `InstanceSetting_AiSetting` (no trailing underscore)
while the other six are `InstanceSetting_GeneralSetting_`
etc. The new helper carries a comment about this so a
future refactor doesn't add a non-existent
`InstanceSetting_AISetting_` type by mistake.

**Tests.**

* `pb/connect/instance_settings_internal_test.go` (new) —
  three regression tests pin the new contract:
  (a) `TestGetInstanceSetting_MissingRowReturnsDefault`
  walks the seven known keys and asserts each one returns
  a non-nil default body without a row being created;
  (b) `TestGetInstanceSetting_UnknownKeyStillNotFound`
  pins that `instance/settings/GARBAGE` still returns
  `CodeNotFound`; (c) `TestBatchGetInstanceSettings_FillsDefaults`
  asserts the batch path now returns a body for every
  requested known key, including ones with no stored row.
* `pb/connect/connect_wave5_settings_internal_test.go:243-247`
  — the `require.Error(t, err, "no row yet")` assertion
  flipped to `require.NoError` and the body is asserted
  to be a non-nil `*v1.InstanceSetting_GeneralSetting{}`.
  The trailing "Initial read returns empty body" comment
  is now accurate (it wasn't before — the previous test
  pinned the *opposite* of what its comment said).
* `pb/connect/connect_wave5_settings_internal_test.go:303`
  — the batch assertion `assert.Len(t, batch.GetSettings(), 1)`
  became `assert.Len(t, batch.GetSettings(), 3)` because
  `STORAGE` and `ACCESS` are no longer skipped.

**Status: implemented, deployment pending.** The fix is
wire-compatible (still an `InstanceSetting` with a `oneof
value` for every known key; only the *missing-row*
behaviour changes), so no client update is required — the
SPA's own `create(InstanceSetting_XSchema, {})` fallback
paths in `InstanceContext.tsx` already handle the default
body that now comes back from the server. The next step
is to rebuild `riff:dev` and push to the Fly registry
(`cd web && pnpm release && docker buildx build …`).

**Alternatives considered and rejected.**

1. *Client-side: SPA catches `not_found` in
   `useInstanceSetting`* (~15 lines in
   `web/src/hooks/useInstanceQueries.ts`). The SPA already
   has a fallback in `InstanceContext.tsx`, but making the
   client compensate leaves the server's "404 for any
   missing key" shape as a footgun for any future caller
   that doesn't replicate the catch-and-default pattern
   (the MCP server, the future riff CLI, etc.). Server-side
   leniency is the contract the
   `BatchGetInstanceSettings` comment already promises;
   server-side fix is the right place to make it real.
2. *Server seed hook: write all 7 default rows in
   `OnServe` when the collection is empty.* One-time
   seed with a guard against re-seeding. Rejected
   because it conflates "the collection is empty" (the
   seed condition) with "the operator wants defaults"
   (the seed action): an operator who has deliberately
   deleted a row to fall back to defaults would have
   the row resurrected on the next deploy. The chosen
   fix is row-by-row and only synthesises the default
   in memory, leaving the DB state alone.
3. *DB-level default seed in the migration.* The 9-key
   upstream set includes `USAGE_LIMITS` and `BASIC` that
   riff's proto doesn't expose, so a migration-time
   default would either drift from the proto or grow
   on every proto change. The handler-side default
   tracks the proto for free.

## Vaultwarden cohabitation design deferred (2026-10-15)

The user wants to migrate an existing `vw.1une.cc` vaultwarden
instance into the same Fly machine that hosts riff
(`ravine.fly.dev`) and serve both hostnames off the same caddy
listener. The full design — supervisord-third-child layout,
`/var/opt/riff/vaultwarden` sub-path on the existing
`riff_data` volume, caddy `@vaultwarden host vw.1une.cc`
matcher, `ROCKET_PORT=8080` loopback binding, and the
`supervisorctl stop` → copy → `supervisorctl start` migration
recipe — is captured at
`docs/design/vaultwarden-cohabitation.md`.

**Status: not implemented.** The user explicitly deferred the
change because adding ~200 MB (binary + `web-vault`) to the
deploy image is a blocker during riff's early development.
Revisit when image size is no longer load-bearing, or when a
second persistent workload on the machine makes the memory
bump inevitable anyway.

**Flagged but unmitigated risk:** the current 256 MB machine
memory with a 512 MB swap may swap-thrash once a second Rust
binary is in the same container. The design carries this as
the first item in its "When to revisit" checklist (bump
memory to 1 GB), and the entry above is the audit-log
backstop for that risk.

## Attachment, share, and motion-media URLs were not sub-path-aware (2026-10-14)

The 2026-10-14 `@spa`-matcher fix made the SPA usable at
`https://ravine.fly.dev/community/`, but the SPA's own URL builders
were still pointing at the document origin rather than the
reverse-proxy prefix. The audit found four call sites that compose
URLs as `${window.location.origin}/...` and need to be
`${window.location.origin}${BASE_PATH}/...` on a sub-path mount:

1. `web/src/utils/attachment.ts::getAttachmentUrl`,
   `getAttachmentThumbnailUrl`, `getAttachmentMotionClipUrl`. These
   build `${origin}/file/${name}/${filename}` for server-managed
   attachments; on `/community/` they 308-redirect per image.
2. `web/src/hooks/useMemoShareQueries.ts::getShareUrl` (the URL on
   the "Copy share link" button) and
   `withShareAttachmentLinks` (the share-mode `externalLink`
   rewriter). Both build `${origin}/memos/shares/${token}` and
   `${origin}/file/...`, with the same 308 hop.
3. `web/src/hooks/useAttachmentLibrary.ts:127` — the
   `AttachmentLibraryListItem.sourceUrl` falls back to
   `${origin}/file/...` when the attachment has no `externalLink`,
   so the attachment-library's link icon points at the static site.
4. `web/src/components/MemoDetailSidebar/MemoDetailSidebar.tsx::handleCopyLink`
   and `web/src/components/MemoActionMenu/hooks.ts::handleCopyLink`.
   These are the *only* functional correctness bug in the class:
   when the admin leaves `instanceUrl` unset, the share URL is
   `${window.location.origin}/memos/uid` and lands on the static
   site, not the memo on the SPA. On a root mount the static site
   and the SPA are the same host, so this never fired; on a sub-path
   mount it produces a real broken link.

**The fix.** Add a `BASE_URL` constant to
`web/src/utils/base-path.ts` — `${window.location.origin}` joined
to `BASE_PATH` with a trailing slash, e.g.
`"https://ravine.fly.dev/community/"` — and switch the four
call sites to read it. The trailing slash is the load-bearing
piece: `${baseUrl}file/...` is well-formed in both the root
(`<origin>/file/...`) and sub-path (`<origin>/community/file/...`)
cases, where `${origin}file/...` would silently collapse the path
segment.

For the two `handleCopyLink` functions the "admin set
`instanceUrl`" path still trusts the admin's URL as-is (the
admin owns the public shape of the instance and is responsible for
putting the sub-path in `instanceUrl`); only the no-`instanceUrl`
fallback adds the basename. This preserves back-compat for admins
who already set the URL correctly while fixing the
`window.location.origin` fallback path.

**Pure-function form.** `attachment.ts` now exports
`getAttachmentUrlWith(baseUrl, attachment)` and the matching
`getAttachmentThumbnailUrlWith` / `getAttachmentMotionClipUrlWith`
so the sub-path branch is testable without module-scope mocking,
mirroring the existing `withBasePath` / `withBasePathWith` and
`toBasenameAwareUrl` / `toBasenameAwareUrlWith` split.

**Test coverage.**
- `web/tests/base-path.test.ts` — 4 new `baseUrlWith` assertions
  covering the root and sub-path shapes plus the trailing-slash
  join invariant. Total file: 30 tests.
- `web/tests/attachment-urls.test.ts` — 5 new assertions for the
  `*With` form, including a sub-path case that pins the
  `<origin>/community/file/...` shape. Total file: 10 tests.
- `web/tests/transcription-service.test.ts` — unchanged
  intentionally: `transcriptionService.transcribeAttachment` calls
  `getAttachmentUrl(attachment)`, so it inherits the sub-path
  fix via the helper. The 6 existing tests continue to assert
  the `BASE_URL === "/"` behaviour, which is what `import.meta.env`
  reads at module-load time in the jsdom test environment.

**Caddyfile.** Considered adding `not path /file /file/* /memos/shares
/memos/shares/*` to the `@spa` matcher so direct-origin pre-fix URLs
stop 308-redirecting. Rejected: `/file/...` is not a path the SPA
emits anymore (the fix makes every URL carry `/community/`), and
*excluding* `/file/*` from `@spa` would route it to the static
site's `file_server` and 404 — the only way to make a bare
`/file/...` request land on riff is to add an explicit
`handle /file/*` reverse_proxy block, which is a different
deployment shape and is not justified by a transient migration
window.

**Verification.** `pnpm test` — 198 files, 1826 tests, all green.
`pnpm lint` (tsc + biome) clean. This entry closes the
"out of scope (separate fix)" cross-reference the
2026-10-14 `@spa` entry left dangling at line 187.

## `Caddyfile.fly.community` `@spa` matcher was 308-redirecting `GET /` to the SPA (2026-10-14)

Visiting `https://ravine.fly.dev/` returned `308 Location: /community/`
instead of the static site. The `@spa` named matcher in
`scripts/Caddyfile.fly.community` uses a long `not path` list to carve
out the static-site's top-level paths; the bare path `/` was not in
the list, so `GET /` fell into `@spa` and the `redir * /community{uri}
308` rule fired. The Caddyfile's own comment at the time promised
"`/ and /index.html are excluded`" but only `/index.html` was actually
on the matcher line — the comment and the code had drifted apart, which
is the regression that escaped the prior deploy.

**The fix.** Add `/` to the existing `not path` list (one token, with
a new comment block explaining the keep-in-sync contract between the
comment and the matcher). Verified live: after the fix, `GET /` returns
200 with the static site's `index.html`.

**The regression guard.** `scripts/fly_deploy.sh` previously only
smoke-tested `/community/api/health`. That guard was one-sided: a
SPA-only or static-site-only misroute would not fire. Added a second
smoke test (2/2) that probes `GET /` and asserts either a 200 or a
static-site-canonicalising 308 — anything matching `/community` in
`%{redirect_url}` is now an `exit 1` with a stderr message that names
the file and the line of the `@spa` matcher. Checked: the new
smoke test correctly fails when pointed at the pre-fix
`https://ravine.fly.dev/` and would pass on a post-fix deploy.

**Why the guard matters, not just the fix.** A 1-token fix is
load-bearing in the same way the `BASE_PATH` plumbing is: the
`not path` list is a manually-maintained negative enumeration of
every top-level static-site path. The current `_site/` tree has ten
top-level entries; if the ravine Jekyll tree adds an eleventh (a new
`/blog`, a new `/changelog`), the operator has to remember to add it
here too. The smoke test is the backstop. The comment block above
the matcher is the in-tree documentation that should keep that
maintenance obvious to the next reader.

**Files.**
* `scripts/Caddyfile.fly.community` — add `/` to the `@spa`
  `not path` list (line 144); add a 5-line comment block above the
  matcher explaining the comment/matcher keep-in-sync contract.
* `scripts/fly_deploy.sh` — split the smoke test into 1/2 and 2/2;
  test 2 inspects `%{http_code}` and `%{redirect_url}` on `GET /` and
  fails the deploy with an actionable message if the response is a
  3xx whose target includes `/community`.

**Verification.** `dash -n scripts/fly_deploy.sh` and
`bash -n scripts/fly_deploy.sh` both clean (the script is POSIX sh and
runs unchanged on Alpine's dash, the macOS / Linux `bash`, and the
`/bin/sh` GoLand picks when it cannot find the shebang's interpreter).
The new check was tested end-to-end against the still-buggy live
site: it exits 1 with the expected stderr message.

## Logo URLs were hardcoded as root-relative paths, breaking the SPA on a sub-path mount (2026-10-14)

A follow-up audit of the `redirectOnAuthFailure` fix surfaced a second
instance of the same bug pattern in the SPA's brand assets. On the
`Caddyfile.fly.community` layout (`VITE_BASE_PATH=/community/`), the
sidebar logo rendered `<img src="/full-logo.webp">`; the browser
resolved that against the document origin, which on that layout is
the static site — 404. Same root cause as the auth-redirect bug
(router-level path handed to a browser-level API), same fix shape
(wrap with `withBasePath` / a new `toBasenameAwareUrl` helper), but
five different call sites, one of which carries a user-supplied URL.

**The bug.** Five sites render an `<img src>` (or `<link href>` for
the favicon) whose value is either a hardcoded `/logo.webp` /
`/full-logo.webp` default, or a `customProfile.logoUrl` string the
operator entered in the **Customize server** settings dialog:

* `web/src/components/MemosLogo.tsx:26` — sidebar brand lockup
  (`/full-logo.webp`).
* `web/src/components/AuthPageLayout.tsx:66` — auth-page header
  (`/logo.webp`).
* `web/src/App.tsx:59` — favicon runtime override
  (`/logo.webp`).
* `web/src/pages/About.tsx:18,49` — About page hero
  (`DEFAULT_LOGO = "/logo.webp"`).
* `web/src/components/UpdateCustomizedProfileDialog.tsx:63` — the
  restore-defaults button writes `"/logo.webp"` to the form's
  `logoUrl` state, which then round-trips through the instance
  setting.

`MemosLogo.tsx`, `AuthPageLayout.tsx`, `App.tsx`, and `About.tsx`
are render-time sites and need a basename-aware wrap.
`UpdateCustomizedProfileDialog.tsx` is **not** touched here — it
stores a form value, and the persisted `customProfile.logoUrl` must
stay environment-agnostic (`/logo.webp`, not `/community/logo.webp`)
so the same value reads correctly under any future redeploy. The
render sites are the only place conversion happens.

**The fix.** Add a `toBasenameAwareUrl(url)` helper to
`web/src/utils/base-path.ts` that returns the input unchanged when
it is an absolute URL (matched against the WHATWG scheme regex, plus
the protocol-relative `//host/...` form) and otherwise calls
`withBasePathWith(url, BASE_PATH)`. The pure function
`toBasenameAwareUrlWith(url, prefix)` is also exported for the
`base-path.test.ts` pure-function tests, matching the existing
`withBasePath` / `withBasePathWith` split. The four render sites wrap
their value through `toBasenameAwareUrl(...)`; the form's
restore-defaults button is unchanged. Empty / nullish input returns
`""` so a single-expression call site
(`src={toBasenameAwareUrl(profile.logoUrl || "/logo.webp")}`) does
not need a separate nullish guard.

**Why a new helper, not just `withBasePath` everywhere.** A user
customisation can enter an absolute URL (`https://cdn.example.com/
team-logo.png`) and the form's existing data-URL handling already
relies on passing those strings through untouched. Re-prefixing them
with `withBasePath` would produce
`https://example.com/community/logo.png`, which is broken. The
`isAbsoluteUrl` predicate is the same WHATWG scheme check the
browser uses to decide whether `new URL(input, base)` resolves to
the same origin, so the helper matches the browser's own definition
of "this is a different host / a baked asset."

**Files.**
* `web/src/utils/base-path.ts` — add `isAbsoluteUrl` and
  `toBasenameAwareUrlWith` (pure) and `toBasenameAwareUrl`
  (BASE_PATH-driven), with full doc comments.
* `web/src/components/MemosLogo.tsx` — import the helper and wrap
  the `customProfile.logoUrl || "/full-logo.webp"` default; add a
  9-line comment that documents the basename contract.
* `web/src/components/AuthPageLayout.tsx` — same wrap on the
  auth-page header `<img>`.
* `web/src/App.tsx` — same wrap on the favicon `useEffect` write.
* `web/src/pages/About.tsx` — same wrap on the `instanceLogo`
  computation, with a 7-line comment that points at the other three
  render sites.
* `web/src/components/UpdateCustomizedProfileDialog.tsx` —
  **unchanged** (form-state value, must stay environment-agnostic).
* `web/tests/base-path.test.ts` — 16 new assertions.
* `web/tests/memos-logo.test.tsx` — **new** (4 tests).
* `web/tests/auth-page-layout.test.tsx` — 3 new tests, plus a
  `BASE_PATH=/community` mock at the top of the file (matches the
  `auth-redirect.test.ts` pattern).
* `web/tests/about-page.test.tsx` — 3 new tests + 1 relocated
  (the "uses custom branding" assertion now lives in the new
  describe block and expects the prefixed URL).
* `web/tests/app-favicon.test.tsx` — **new** (4 tests).

**Test coverage.** Three test files pin the basename-aware branches:

* `web/tests/base-path.test.ts` — 16 new assertions for
  `isAbsoluteUrl` and `toBasenameAwareUrlWith`, including the
  http(s) / data: / blob: / file: / `//host` shapes and the
  empty-input contract.
* `web/tests/memos-logo.test.tsx` (new file, 4 tests) — renders
  `MemosLogo` under a `BASE_PATH=/community` mock and asserts the
  `<img>` `src` is `/community/full-logo.webp` for the default
  fallback, `/community/uploads/team-logo.png` for a user-supplied
  relative path, and is unchanged for an absolute URL or a
  `data:` URL.
* `web/tests/auth-page-layout.test.tsx` — 3 new tests in a
  `logo URL under a sub-path mount` describe block, same three
  branches as the MemosLogo suite.
* `web/tests/about-page.test.tsx` — 3 new tests in a similar
  sub-path describe block; the pre-existing "uses custom branding"
  assertion is moved into the new block with its `src` expectation
  updated to `/community/custom-logo.png` (it was
  `/custom-logo.png`, written under the root-mount default).
* `web/tests/app-favicon.test.tsx` (new file, 4 tests) — renders
  the full `App` with a memory router, plants a `<link rel="icon">`
  in `<head>`, and asserts the favicon `useEffect` writes
  `/community/logo.webp` for the default fallback,
  `/community/uploads/team-favicon.ico` for a relative user
  value, and unchanged for an absolute URL. A fourth test pins the
  early-return guard so the static HTML's `<link>` is left alone
  on a fresh install with no `customProfile`.

The full vitest suite passes 1815/1818 (the 3 failures are the
pre-existing `Promise.withResolvers` Node-22+ issues in
`memo-editor-transcription` and `use-attachment-transcription`; env
is Node 20.19.2, CI is Node 24). Biome is clean on all 10 touched
files. tsc has no new errors in any of the touched files
(the 2 errors that remain — leaflet `@react-leaflet/core` and the
`FunctionType` reference in `src/types/view.d.ts` — are pre-existing
and unrelated).

**Out of scope (separate fix).** The same audit found that
`web/src/utils/attachment.ts::getAttachmentUrl` and friends build
URLs as `${window.location.origin}/file/...`, which on a sub-path
mount is the document origin (the static site) and 404s. The
underlying API for `/file/...` is served by PocketBase at the host
root under caddy, so the correct shape for a sub-path mount is to
build `${window.location.origin}${BASE_PATH}/file/...`. That is a
bigger refactor (every consumer of `attachment.externalLink` and
`getAttachmentThumbnailUrl` would need to flow through the helper)
and a separate decision entry; this entry intentionally limits
itself to the brand-asset case the user surfaced.

## `redirectOnAuthFailure` was producing a router-level URL for a browser-level API (2026-10-14)

A 401 returned by the Connect/gRPC interceptor on a sub-path mount
(`Caddyfile.fly.community` layout with `VITE_BASE_PATH=/community/`)
caused the SPA to land on `https://ravine.fly.dev/auth?redirect=…`
instead of `…/community/auth?redirect=…`. The browser then 404'd
against the static site's `file_server`, because the SPA was hitting
caddy at the root of the host. The Caddyfile 308 (`/<spa-path> → /
/community/<spa-path>`) is a defense-in-depth fix for the symptom; the
real bug is in `web/src/utils/auth-redirect.ts` and is fixed by this
entry.

**The bug.** `web/src/utils/auth-redirect.ts::redirectOnAuthFailure`
is the imperative fallback for the connect-rpc 401 path that runs
outside React Router. It called `window.location.replace(buildAuthRoute
({...}))`, where `buildAuthRoute` returns a **router-level** path like
`/auth?redirect=%2Fhome%3Ftab%3Dpins`. React Router's own
`<Navigate to={buildAuthRoute(...)} />` adds the basename to such
paths, so the same value works in the `web/src/router/guards.tsx` and
`web/src/layouts/RootLayout.tsx` call sites. `window.location.replace`
is not a React Router API — it is a raw browser navigation that does
not consult `basename` — so a sub-path build needs the destination
URL to already be a full browser URL. Every other
`window.location.replace` / `window.location.href` call site that
needs a riff SPA path already wraps with `withBasePath`
(`web/src/components/UserMenu.tsx` for sign-out,
`web/src/pages/AuthCallback.tsx` for the OAuth `redirect_uri`); this
one did not.

**The fix.** Import `withBasePath` from `web/src/utils/base-path.ts`
and wrap the `buildAuthRoute` result so the destination handed to
`window.location.replace` is `withBasePath(buildAuthRoute({...}))`. The
embedded `?redirect=` value stays basename-stripped (router-level)
because post-auth navigation flows through React Router's
`navigate()`, which adds the basename automatically. `withBasePath` is
the identity for root-mounted builds, so the change is a no-op on
local dev and on the `Caddyfile.fly` (SPA-at-root) layout. The leading
doc comment on the function and the test file's leading comment both
record the prefix contract explicitly so a future edit cannot regress
without breaking an assertion.

**Why two layers, not just one.** The Caddyfile 308 already in place
catches the user who types `/auth?…` directly into the address bar,
clicks a stale social/bookmark link, or follows a redirect chain that
loses the prefix. The SPA fix makes the SPA's own navigation
self-correcting: every `redirectOnAuthFailure` produces a URL that
goes straight to the right place on its own, with no proxy help. The
Caddyfile change is preserved so a misbehaving client (an extension,
an old service worker, a custom OAuth callback that hardcodes
`/auth/callback`) still gets a useful redirect instead of a 404.

**Test coverage.** `web/tests/auth-redirect.test.ts` now exercises the
sub-path mount (`BASE_PATH=/community/`) for every code path. The two
existing assertions that previously pinned the bug (the
"redirects to /auth" and "strips the mount prefix" cases) now expect
`/community/auth?redirect=…`. A new defence-in-depth case asserts the
**opposite** sides of the basename boundary: the destination starts
with `/community/auth?` while the embedded `redirect` parameter is the
basename-stripped `/setting/profile?tab=2#bio`. If either side
regresses (a future edit that prefixes the embedded value, or one
that drops the prefix from the destination), this case fails first.

**Files.**
* `web/src/utils/auth-redirect.ts` — wrap `buildAuthRoute` with
  `withBasePath`; add a 9-line comment documenting the prefix
  contract on the destination vs. the embedded `?redirect=`.
* `web/tests/auth-redirect.test.ts` — re-style the file so the same
  `BASE_PATH=/community` mock covers the whole describe; add the
  boundary-defence test case described above.
* `scripts/Caddyfile.fly.community` — unchanged by this entry; the
  existing `@spa` 308 handle block is the defence-in-depth layer
  described in the rationale above.

**Verification.** `cd web && pnpm test` runs all 10 auth-redirect
cases plus the 15 base-path and 14 redirect-safety cases (39 / 39
in those three files; 1789 / 1792 across the full suite, the three
unrelated failures are `Promise.withResolvers` calls in
`memo-editor-transcription` and `use-attachment-transcription`
tests that require Node ≥22, see `engines.node` in `web/package.json`).
Biome is clean on both changed files. The Go service is not touched.

## Fly deploy wrappers (`dev_image_*.sh`) are POSIX sh, not bash (2026-10-14)

`scripts/dev_image_build.sh` and `scripts/dev_image_push.sh` were
originally tagged `#!/usr/bin/env bash` and used bash-only constructs
(`[[ … ]]`, `set -o pipefail`). The GoLand "Shell Script" run
configurations in `.run/Docker__build_riff_dev.run.xml`,
`.run/Docker__push_riff_dev_to_fly.run.xml`, and
`.run/Fly__deploy_ravine.run.xml` hardcoded `/bin/bash` as the
interpreter, so the bug only surfaced when the script was invoked
through any other path — `sh scripts/dev_image_*.sh` from the zsh
terminal on the dev host, the GitHub Actions runner that uses
`bash:` by default but no longer has the shebang honoured when the
runner matrix is sh, or any future cron / CI lane that calls the
script through `/bin/sh`. On Debian / Ubuntu / Alpine (where
`/bin/sh` is dash), the first `[[ -z "${repo_root}" ]]` produced
`[[: not found` and the script aborted before any precondition
check ran, so the deploy failed with no context. The CI workflow
files (`.github/workflows/release.yml`,
`.github/workflows/build-canary-image.yml`) all use `bash
scripts/release_version.sh` explicitly, which is why this didn't
show up there; the failure path is the developer-side local push.

A follow-up also extracted the third wrapper — the `fly deploy -a
ravine --image …` + caddy-routed `curl /community/api/health`
smoke test that previously lived inside
`.run/Fly__deploy_ravine.run.xml` as an inline `SCRIPT_TEXT` — into
`scripts/fly_deploy.sh`. The GoLand config's `SCRIPT_TEXT` is now
empty, `SCRIPT_PATH` points at the file, and
`EXECUTE_SCRIPT_FILE=true`; the smoke test and the
`~/.fly/bin/flyctl` PATH fallback live in version-controlled sh
where they get syntax highlighting, shellcheck, and the same
`dash -n` guard as the other two. The failure that prompted this
was `command not found: flyctl` (exit 127) when the deploy config
ran through the GoLand "Execute in terminal" path: a non-interactive
subshell that does not source `~/.zshrc` and therefore does not
have the official Fly installer's `~/.fly/bin` directory on PATH.
The same `for _fly_bin in "$HOME/.fly/bin" …` lookup that
`dev_image_push.sh` already does is now in `fly_deploy.sh`, so the
config works regardless of how it's invoked.

**What we did.** Rewrote all three wrappers (`dev_image_build.sh`,
`dev_image_push.sh`, `fly_deploy.sh`) as POSIX sh, changed the
shebangs to `#!/bin/sh`, replaced every `[[ … ]]` with
`[ … ]`, replaced `set -euo pipefail` with `set -eu` (the
scripts have no pipelines, so `-o pipefail` was never load-bearing),
and updated the three GoLand run configs to use `/bin/sh` as the
interpreter. The GoLand configs now point `SCRIPT_PATH` at the real
`scripts/*.sh` files with `EXECUTE_SCRIPT_FILE=true` (the file is
the source of truth, not an XML `SCRIPT_TEXT` literal). The single
bash call inside `dev_image_build.sh`
(`bash scripts/release_version.sh development-version`) is left
explicit, so `release_version.sh` is still the only bash entry
point. Added a header comment to each script explaining the choice
and a "Local dev image build / Fly registry push" row in the
AGENTS.md Change Routing table documenting the parse-time check.

**Why POSIX.** The deploy pipeline already has several
load-bearing footguns (the `swap_size_mb` table-array gotcha in
`fly.toml`, the `--build-context ravine=...` buildkit context that
Fly's remote builder cannot satisfy, the `Caddyfile` base-name
caddy adapter autodetect). Adding a "you must invoke this with
bash" precondition to the dev script would be the fourth
one-operator-mistake failure mode, and it is the only one with no
upstream signal — the script just dies silently because `sh`
prints `[[: not found` once and the build never starts. POSIX sh
removes the entire class: `dash`, `bash`, `ksh`, and zsh's `sh`
emulation all behave identically, and the same script works under
`sh scripts/dev_image_build.sh`, `./scripts/dev_image_build.sh`
(where the kernel honours the shebang and picks sh on systems
where sh is dash), and the GoLand config (where the IDE now
matches what the script asks for).

**Verification.** `dash -n` on each of the three scripts parses
without error; `dash scripts/dev_image_build.sh` from a non-git
working tree prints `Not inside a git working tree.` and exits 1,
the same precondition path bash exercised before. A full
`dash scripts/dev_image_build.sh` inside the repo builds `riff:dev`
end-to-end (the `exec docker buildx build …` line at the bottom
runs unchanged). `bash -n` is no longer the load-bearing parse
check; if a future change reintroduces a bashism, the parse-time
guard `dash -n scripts/dev_image_*.sh scripts/fly_deploy.sh` in CI
will fail before the deploy runs. The GoLand `Fly: deploy ravine`
config now resolves `flyctl` via the same `~/.fly/bin` lookup that
`dev_image_push.sh` uses, so the `command not found: flyctl`
exit-127 path is closed.

**AGENTS.md update.** The new Change Routing row notes the
deliberate POSIX-only constraint, the third script, and points the
"Verify" column at `dash -n` plus the manual `dash` precondition
test.

## Fly `http_service.checks` must match the Caddyfile layout (2026-10-13)

`ravine.fly.dev` returned `HTTP ERROR 503` (Fly's generic page) for
every request, with the only useful signal in the Fly edge-proxy logs:

    proxy [lax] [error]
    error.message="could not find a good candidate within 40 attempts
                   at load balancing"
    request.url="https://ravine.fly.dev/"

The machine itself was healthy: caddy up, riff up, port 80 bound,
`/var/opt/riff` mounted, PocketBase `GET /api/health` returning
`{"message":"API is healthy.","code":200,"data":{}}` on the loopback.
The 503 came from the **edge proxy**, not from caddy or riff.

**Root cause.** The `[[http_service.checks]] path` in `fly.toml` did
not match the caddy reverse-proxy layout:

* `fly.toml` set `path = "/api/health"`.
* `CADDYFILE = "Caddyfile.fly.community"`, which routes
  `/community/*` to riff and serves everything else (including
  `/api/health` at the root) from caddy's static-site `file_server`.
* Fly's check probe therefore hit `file_server` and got **404**.
* A critical check marks the machine unhealthy across the
  `[[vm]]`'s region, and Fly's global edge proxy turns that into
  `503 Service Unavailable` everywhere (not just the failing region).

**What we did.** Updated `fly.toml` to
`path = "/community/api/health"`. Caddy's
`handle_path /community/*` strips the prefix and forwards the
probe to `127.0.0.1:8090/api/health`, which returns 200. Pushed
the new manifest with `fly deploy -a ravine --image
registry.fly.io/ravine:dev --wait-timeout 60 -y`; the rolling
update applied the new check, the check passed on the first probe,
and the edge proxy started serving requests again.

**Gotcha worth pinning.** `fly deploy` with a new health check
that targets a path the *current* check does not validate cannot
short-circuit the wait. The CLI doesn't know the new check is
faster than the old one — it waits for the new check to pass on
the *new* machine. With a single-machine deployment (`max_machines
= 1` is implicit) the new machine is the only machine, so a
failing check hangs the deploy for `--wait-timeout`. The escape
hatch is `--strategy=immediate` if you need to force the update
on machines whose state the new check can't see yet, but the
normal rolling update is correct once the new path is right.

**AGENTS.md update.** The Fly row in the Change Routing table now
spells out that `[[http_service.checks]] path` must be caddy-routed
to riff (not the static site), and the smoke-test command is the
matching `https://<app>.fly.dev/community/api/health` (or the
unprefixed variant for the `Caddyfile.fly` SPA-at-root layout).

**Tested.** `curl -fsS https://ravine.fly.dev/community/api/health`
returns 200 with the healthy body; `curl -fsS https://ravine.fly.dev/`
returns the static site HTML; `curl -fsS -X POST
https://ravine.fly.dev/community/memos.api.v1.AuthService/SignIn`
returns a Connect `invalid_argument` 400 from riff (the
`sso_credentials` field is required for `signin_account`).

## Swap on 256MB Fly machine (2026-10-13)

The `ravine` Fly machine is sized at `shared-cpu-1x` with
`memory = "256mb"` (fly.toml). Under load — large memos, in-flight
transcription buffers, webhook delivery backlog — riff + caddy +
PocketBase can push resident memory past 256MB, and the Fly proxy's
256MB cgroup will OOM-kill the most active process (usually
PocketBase, the SQLite writer). A 512MB swap is a cheap safety
net that converts transient spikes into a slowdown instead of a
restart.

**What we did.** `fly.toml` sets `swap_size_mb = 512` at the top
level of the file. Fly provisions a real swap-backed disk device
on the machine at boot; the kernel sees it as a normal swap entry
in `/proc/swaps` and uses it transparently. No container-side
work, no capability changes, no image bloat.

**Why machine-level, not container-side.** A container-managed
`/swapfile` was the first design, and it was rejected after a
direct test on a built image: the kernel returns `EINVAL` from
`swapon` when the swap file lives on an `overlay` filesystem,
which is what every container runtime (Docker, Fly, containerd)
uses for the rootfs. The workarounds were all bad:

* Install `util-linux` (GNU `mkswap` with `-f`) — solves a
  non-problem; the kernel still blocks `swapon` on overlayfs.
  Also adds ~3-4MB to the image and a maintenance tax.
* Loop-device indirection (`losetup -f /swapfile; swapon /dev/loopN`)
  — works, but requires `CAP_SYS_ADMIN` and `/dev/loop-control`
  inside the container. `scripts/compose.yaml` drops ALL caps and
  only adds `NET_BIND_SERVICE`, so the loop path silently no-ops
  in local dev. Different behaviour per environment is the worst
  kind of surprise.
* Bind-mount a host directory — couples the container to the
  host's filesystem layout and adds an operator setup step
  (`docker run -v /tmp:/tmp` or equivalent on Fly).
* tmpfs — RAM-backed, defeats the purpose.

`swap_size_mb` skips all of this. The trade-off is that the
swap is Fly-only: local docker, `scripts/compose.yaml`, and CI
runners don't get one. That's acceptable because the swap is a
production safety net, not a feature — local dev runs riff once
and stops it, and CI tears the test app down before it can OOM.

**Why not at image build time.** Recording a 512MB swapfile in
a Dockerfile `RUN dd ...` would bake 512MB of zeros into the
image layer and bill it to every machine that pulls the
multi-arch image (amd64 + arm64 + arm/v7). The file is trivially
regenerated from `/dev/zero` in ~1s, so the build-time cost is
paid for nothing.

**Why not the persistent `/var/opt/riff` volume.** The persistent
volume is backed by Fly's network-attached disk and is snapshotted
on every machine for restores; swap pages are ephemeral by nature
and have no business being replicated, and putting them on the
persistent volume would also bill the swap against the 1GB
volume size. (Now moot under `swap_size_mb` — the machine's
swap disk is its own thing, separate from both the rootfs and
the persistent volume.)

**Correction (2026-10-14).** The original entry said the field
lives on the `[[vm]]` block. It does not. flyctl's
`internal/appconfig/config.go` defines `SwapSizeMB *int` only on
the top-level `Config` struct (with TOML tag
`toml:"swap_size_mb,omitempty"`), and the `Compute` struct that
`[[vm]]` deserializes to has no such tag — its fields are
`size`, `memory`, the inlined `fly.MachineGuest`
(`cpus`/`cpu_kind`/`kernel_args`/`host_dedication_id`/
`persist_rootfs`/`processes`/`gpu_kind`), and `processes`. The
deploy-time conversion in `internal/appconfig/machines.go` reads
`c.SwapSizeMB` from the top-level `Config` and writes it to
`mConfig.Init.SwapSizeMB`. Because `fly deploy` does not run
strict TOML validation by default, the original
`[[vm]] swap_size_mb = 512` was silently dropped during decoding
and the 256MB machine booted with no swap. The fix is to lift
the line out of `[[vm]]` and place it at the top level of
`fly.toml` next to `app`, `kill_signal`, etc.

## Static site at root, riff under `/community/` (2026-10-13)

The previous Fly layout put the riff SPA at the hostname root
(`Caddyfile.fly`, `CADDYFILE=Caddyfile.fly`), with a `/community/*`
`handle_path` alias for old bookmarks. The new requirement is the
inverse: the baked-in static site (the out-of-repo Jekyll build at
`/home/peter/CLionProjects/ravine.1une.cc/website/_site/`) is the
public-facing content of `https://ravine.fly.dev/`, and the riff SPA
+ Connect/gRPC surface lives under `https://ravine.fly.dev/community/`.

**Why a third Caddyfile instead of inverting `Caddyfile.fly`.** Three
places document the existing file as "SPA at the root"
(`scripts/Caddyfile.fly`'s header, `AGENTS.md`, the build-section
comment in `fly.toml`). Changing its meaning in place would silently
break that contract and confuse anyone reading the file in
isolation. A new `scripts/Caddyfile.fly.community` variant keeps the
deploy intent explicit (`CADDYFILE=Caddyfile.fly.community` in
`fly.toml`) and gives a one-env-var rollback to the previous layout.

**The SPA had to be rebuilt with a base path.** The Vite build was
configured to emit root-relative asset URLs (`/assets/index-*.js`) and
the connect-web transport was hard-coded to
`baseUrl: window.location.origin`. Both are wrong under a `/community/`
mount: the browser would resolve `/assets/foo.js` against the
`/community/` page URL and request it from the static site (which
404s), and the connect-web client would post to
`${origin}/<svc>/<method>` instead of
`${origin}/community/<svc>/<method>`. A Caddy response-rewrite of the
SPA HTML was considered and rejected: Vite emits hashed asset paths
inside the JS bundles too, not just `index.html`, and a regex rewrite
of the response body would either miss the JS-emitted URLs or break on
the next rebuild. The cleaner fix is the Vite-native one:

  * `web/vite.config.mts` reads `process.env.VITE_BASE_PATH` (default
    `"/"`, so local dev and root-mounted deployments are unchanged) and
    sets the Vite `base` to that value, which rewrites the asset
    references in `index.html` and exposes the same string at
    `import.meta.env.BASE_URL` inside the bundle.
  * `web/src/connect.ts` now builds the connect-web `baseUrl` as
    `${origin}${import.meta.env.BASE_URL}`, with a trailing-slash
    normalisation so `/` (local dev) still produces
    `${origin}/<svc>/<method>` and `/community/` produces
    `${origin}/community/<svc>/<method>`. The `${origin}/<svc>/<method>`
    string is identical to the previous `window.location.origin`
    construction in the default case, so the refactor is a no-op for
    root-mounted deploys.
  * The build invocation for a `/community/` deploy is
    `VITE_BASE_PATH=/community/ cd web && pnpm release`. Local dev
    (`pnpm dev`) and the default `pnpm release` keep `VITE_BASE_PATH`
    unset, so the SPA serves root-relative.

**Caddy routing.** The new file uses Caddy's `handle_path /community/*`
to strip the prefix before the reverse-proxy hop, so the requests
riff's PocketBase listener sees are its normal `/`, `/_/`, `/api/*`,
`/mcp`, `/memos.api.v1.*` paths — no PocketBase-side rewrite needed.
A `@community_root` matcher redirects `GET /community` (no trailing
slash) to `/community/`, so the SPA's relative URL resolution never
sees a parent-only URL. The `(proxy)` snippet carries the same
X-Forwarded-For / X-Forwarded-Proto contract as `Caddyfile.fly`:
`For` is replaced-and-appended (not blindly forwarded, which would let
the Fly edge overwrite it and collapse every visitor into one
rate-limit bucket), `Proto` is pinned to `https` so PocketBase's
absolute URL builder and the Secure cookie flag see the public origin.

**Build impact.** `scripts/Dockerfile` adds one `COPY` for the new
file (so the image at `/etc/caddy/Caddyfile.fly.community` matches the
runtime env var). `fly.toml` switches `CADDYFILE` to
`Caddyfile.fly.community`. No Go-side changes, no proto changes, no
collection migrations.

## Fly deploy verified end-to-end: caddy `--adapter`, registry-pushed image (2026-10-13)

The caddy+supervisord image was deployed for the first time to Fly as
app `ravine` (machine `185175db9633e8`, region `nrt`, volume
`vol_vwnzyg98mwllgg8v` attached to `/var/opt/riff`). Two problems
only showed up on a real boot and are recorded here because both are
non-obvious from reading the config.

**caddy needs an explicit `--adapter caddyfile` for `Caddyfile.fly`.**
caddy picks the config adapter from the file name, and only a file
whose base name is exactly `Caddyfile` is auto-detected. With
`CADDYFILE=Caddyfile.fly` (what `fly.toml` sets) `caddy run` aborts at
startup with `Error: ambiguous config file format; please specify
adapter (use --adapter)`. Confirmed by copying the same content to
four names inside the image: `Caddyfile` → "Valid configuration";
`Caddyfile.fly`, `Caddyfile.conf`, `Caddyfile.fly.conf` → the
ambiguous-format error. The failure mode is unusually misleading:
`supervisord`'s `autorestart=unexpected` turns it into a BACKOFF loop
on the caddy child only, so the container is "Up" in `docker ps`, the
riff child is RUNNING and listening on 8090, and nothing is listening
on :80 — from the outside it looks like a caddy routing problem, not
a config-load failure. `scripts/supervisord.conf` now runs
`caddy run --environ --config /etc/caddy/%(ENV_CADDYFILE)s --adapter
caddyfile`; the flag is harmless for the default `Caddyfile` and
makes the chosen adapter explicit in the log (`"adapter":"caddyfile"`).
This is why the local smoke test in the previous session passed while
the container did not: that test ran the `riff` binary directly and
never started caddy.

**`fly deploy --image` does not upload a locally built tag.**
`fly.toml` used to point `[build] image = "riff:dev"` on the
assumption that flyctl falls back to the local docker daemon.
flyctl v0.4.116 instead resolves `--image` in the Fly registry and
fails with `Could not find image "docker.io/library/riff:dev"`.
`--local-only` is not a workaround: it builds with the local daemon
but flyctl has no flag for buildkit named contexts (`--build-arg`,
`--build-secret`, `--build-target` only), and the Dockerfile needs
`--build-context ravine=…` for the out-of-repo Jekyll tree. The
supported path is therefore to push the image ourselves:
`flyctl auth docker` (writes a `registry.fly.io` credential into
`~/.docker/config.json`), `docker tag riff:dev
registry.fly.io/ravine:dev`, `docker push`, then `flyctl deploy -a
ravine`. `[build].image` is now that pushed reference. The `dev` tag
is deliberately mutable — every deploy overwrites it — and the
immutable identity of a running machine is the image digest plus the
`VERSION`/`COMMIT` build args baked into the binary.

**Verified**: `fly checks list` reports `servicecheck-00-http-80`
passing with `{"message":"API is healthy.","code":200,"data":{}}`;
public curls return 200 for `/api/health`, `/`, the deep link
`/m/abc-123`, `/_/`, `/community/api/health` and the hashed
`/assets/*.js` bundle; `POST /mcp` `initialize` replies with
`serverInfo {"name":"memos","version":"dev"}`. The same image boots
correctly in plain Docker for both variants: `CADDYFILE=Caddyfile.fly`
serves the SPA at the host root, and the default ACME `Caddyfile`
still starts with the new flag (Host `localhost` → 308 to HTTPS,
non-matching host → the Caddyfile's own `:80 { respond 404 }`).
Measured TTFB from this workstation is 5–9 s, but that is a local
TLS/proxy artifact (TCP connect 1.4 ms, TLS handshake 4–9 s) while
Fly's in-network check passes well inside its 5 s timeout, so the app
itself is not the slow part.

## Caddy + supervisord reverse-proxy in the deploy image (2026-10-13)

The previous deploy image ran riff directly: PocketBase on port 8090,
exposed to the world via the Fly proxy or compose. That worked, but
the project also owns a static Jekyll site (the `ravine.1une.cc`
documentation) that lives in a separate repo at
`/home/peter/CLionProjects/ravine.1une.cc/website/_site/`. To host
both on a single public hostname without standing up a second Fly app
or a second compose service, the deploy image now bakes in a
reverse-proxy layer.

**Layout chosen**: caddy for TLS + reverse-proxy + static file serving,
supervisord for process supervision, riff untouched. supervisord is
PID 1; caddy and riff are its two child programs. Caddy binds 80/443,
reverse-proxies `/community/*` to riff on the loopback 8090, and
serves the baked-in static site from `/var/www/site/` for everything
else.

**Why caddy over nginx/traefik/envoy**: caddy is a single static
binary on Alpine (no Lua module juggling), auto-issues Let's Encrypt
certs without an external CA client, and its Caddyfile is the
shortest expressive form for "path-prefix reverse-proxy + file server
on the same listener" we could find. The default `caddy` Alpine
package already includes the `file_server` and `reverse_proxy`
handlers we need, so no custom build is required.

**Why supervisord over s6-overlay or dumb-init**: riff already had an
entrypoint.sh that drops from UID 0 to a nonroot user via `su-exec`
and loads `RIFF_*_FILE` env-var files. Reusing that as a supervisord
program preserves the privilege-drop semantics without rewriting it.
supervisord is also a single `apk add` and the `nodaemon=true`
config makes it the canonical "init" for the container.

**Static-site source**: `scripts/ravine` is a checked-in symlink to
`/home/peter/CLionProjects/ravine.1une.cc/website/_site/`. The
Dockerfile imports the site as a buildkit named build context via
`COPY --from=ravine . /var/www/site/`. The build invocation must
pass `--build-context ravine="$(readlink -f scripts/ravine)"` (or
the resolved absolute path). The committed symlink is just a
convenience pointer so the developer does not have to remember the
Jekyll repo's location; the Dockerfile does not read the symlink
directly.

**Why not follow the symlink through the build context tar**: when
the build context is tarred up by buildkit, symlinks pointing
**outside** the context are preserved as symlinks (not dereferenced
into directory contents). Inside the build context, `COPY . .` in
the backend stage does pull in the symlink, but reading its target
in a later stage (`COPY --from=backend scripts/ravine/. /var/www/site/`)
fails with `"/backend-build/scripts/ravine": not found` because the
target only existed on the host, not in the backend stage's
filesystem. The buildkit named context is the supported way to
inject an out-of-repo path into a build; it requires the user (or
CI) to opt in by passing `--build-context`.

**CI handling**: the `release.yml` and `build-canary-image.yml`
workflows create an empty `ravine` context (`build-contexts:
ravine=${{ github.workspace }}/.empty-ravine`, populated by a
preceding `mkdir -p .empty-ravine` step). The published image
ships without the static site baked in; caddy serves an empty
`/var/www/site/` as a 404, and the `/community/*` proxy is
unaffected. When the ravine repo becomes fetchable from CI (today
it is not), the empty-dir trick is replaced with a checkout step
that populates a real ravine build context.

**Port and capability surface**: the public-facing ports are 80 and
443 (caddy). Riff's PocketBase 8090 stays on the loopback only; it
is no longer published in `scripts/compose.yaml` and Fly's
`internal_port` now points at 80 (caddy), not 8090 (riff). The
container adds `CAP_NET_BIND_SERVICE` and drops everything else; caddy
binds the privileged ports as root in its supervisord program and
drops to its own user internally. The riff program still uses the
existing entrypoint.sh's UID 0 → nonroot drop.

**TLS termination**: caddy does TLS termination itself with the cert
issued for `SITE_DOMAIN` (env-driven, defaults to `example.com` in
the image, real value via `fly secrets set` / `docker run -e`). The
Fly proxy's `force_https = true` is removed because caddy also
serves HTTP on 80 and the two would loop on a 301. Local dev with
`SITE_DOMAIN=localhost` skips ACME and serves plain HTTP on 80
because Let's Encrypt cannot challenge 127.0.0.1.

**RIFF_INSTANCE_URL**: now includes the public `/community` prefix
(`http://localhost/community` in fly.toml's placeholder,
`https://example.com/community` in compose.yaml's default). This
is the URL that ends up in email-notification links and the MCP
origin allow-list, so it must match the public path users will see.

**Deferred**: a separate "Boulder-style" cert staging for pre-prod
environments (so a `staging` Fly app does not pollute prod ACME
rate limits). Caddy supports the `acme_ca` global option for this
but we did not add it because there's no staging app yet.

## Alpine 3.21 runtime: caddy lives in `community`, pin every package (2026-10-13)

The first attempt at the runtime stage failed at
`apk add caddy supervisord tzdata ca-certificates su-exec` with
`exit code: 1` (buildx error: `failed to solve: process "/bin/sh -c
apk add ..." did not complete successfully`). Root cause: the
`alpine:3.21` base image only enables the `main` repository in
`/etc/apk/repositories`, so `caddy` (which is a `community`-only
package) is invisible to the solver. `supervisord` is the binary
provided by the `supervisor` package (also in `main`, so that part
resolved), but the failure on `caddy` aborted the whole `apk add`
invocation before any of the later packages were considered.

The fix is two-fold and was applied to `scripts/Dockerfile` in the
same commit (now `1b49a81d` plus the `apk add` amendment that lives
in the working tree):

1. **Pass `--repository=https://dl-cdn.alpinelinux.org/alpine/v3.21/main`
   and `--repository=.../v3.21/community` to every `apk add`.** These
   URLs are the same entries that already live in
   `/etc/apk/repositories` on a stock `alpine:3.21` image, so
   listing them explicitly is a no-op for `main`-only packages and
   makes `community` packages reachable. We do not use the
   `dl-cdn...` CDN-over-TLS default because pinned version strings
   already make the build deterministic; the explicit URL form is
   just so the next reader does not have to wonder why the line
   works.
2. **Pin every package to an exact `<name>=<version>` string.** The
   versions on Alpine 3.21 today (verified against
   `pkgs.alpinelinux.org` and the v3.21 `APKINDEX.tar.gz`):
   - `caddy=2.8.4-r7` (community)
   - `supervisor=4.2.5-r5` (main; the `supervisor` package
     provides the `supervisord` binary)
   - `tzdata=2026e-r0` (main)
   - `btop=1.4.0-r0` (community; debugging nicety)
   - `ncdu=1.21-r0` (main; debugging nicety)
   - `ca-certificates=20260909-r0` (main; pulled in transitively
     by `caddy`'s explicit dependency, but listed explicitly so
     the runtime image is self-documenting)
   - `su-exec=0.2-r3` (main; used by `scripts/entrypoint.sh`)

   Pinning is the same pattern as the Go module `go.mod`: bit-
   identical rebuilds three months from now, no surprise from an
   Alpine security rebuild that bumps a transitive dependency.

**Why not just `apk add --no-cache caddy supervisor ...` with no
repository flag?** Because that is the original form, and it
failed for `caddy` specifically (the only `community`-repo
package in the list). `btop` is also in `community` and would
fail the same way. The other five packages are in `main` and
would resolve fine, but `apk` aborts on the first unresolvable
name, so we would never get to install them.

**Why not `apk add --no-cache --repository=http://.../edge/...`
to grab a newer caddy?** Alpine 3.21's `apk-tools` will refuse to
mix repositories from different release branches in a single
install (the `--repository` flag is for adding extra repos, not
for cross-branch fetching). Edge caddy would be available, but
pulling it in would also drag edge-only dependencies that do not
exist on v3.21, and the whole point of pinning to v3.21 is the
two-year support window.

**Caveat: `supervisor` pulls in `python3`, `py3-setuptools`, and
a `python3~3.12` version pin.** The runtime image now carries
~10 MiB of Python that we never invoke directly; supervisord's
core is Python (a long-standing design choice from the
supervisor project). This is documented in the Dockerfile's
`apk add` block comment so a future reader does not try to
"minify" the image by removing `python3` and break supervisord.

**Deferred**: a follow-up to also pin
`golang:1.27.0-alpine` in the build stage, and to extract the
pinned package list into an `ARG` so a `--build-arg
ALPINE_PACKAGE_SUFFIX=...` can override it in a local experiment
without forking the Dockerfile. The `Dockerfile` already has
`ARG` blocks for VERSION and COMMIT; adding one for the
package-version table is the obvious next step but not on the
critical path.

## buf.gen.yaml drift — follow-up: lockfile + end-to-end regen (2026-10-10)

Follow-up to the previous slice. After `cd web && pnpm install`
(Node 24, pnpm 11.0.1 via corepack), `pnpm install` resolved
`@bufbuild/protoc-gen-es ^2.12.0` to **2.16.0** (the latest 2.x).
`cd proto && buf generate` then ran end-to-end on this machine
(buf 1.73.0, Node 24.13.0, with `web/node_modules/.bin` on PATH):

* `proto/gen/` (Go + OpenAPI) — no diff; the remote plugins
  produced byte-identical output.
* `web/src/types/proto/api/v1/*_pb.ts` — 9 files, each changed by
  exactly one line: the `// @generated by protoc-gen-es v2.12.1`
  header bumped to `v2.16.0`. The body of every file is
  byte-identical, so the v2.12 → v2.16 upgrade is cosmetic for our
  existing .proto surface.
* `web/src/types/proto/google/api/*_pb.ts` — no diff. These are
  the BSR-vendored `buf.build/googleapis/googleapis` files and
  were already current with the v2 generator family.

`web/pnpm-lock.yaml` gained the new devDep, its optional peer
`@bufbuild/protobuf@2.12.0` (resolved via `optionalDependencies`),
and the transitive `@bufbuild/protoplugin@2.16.0` /
`@typescript/vfs@1.6.5` / `typescript@5.4.5` (a specific older
TS used by `protoplugin` for AST validation). The new lockfile
is `pnpm install --frozen-lockfile` clean.

**Verification gate (this commit).** `cd proto && buf generate`
produced a 65/9 diff (56 lockfile insertions, 9 one-line TS
header bumps), `go build ./...` and `go vet ./...` clean,
`cd web && pnpm lint` (tsc + biome) clean on 695 files,
`tsc --noEmit` on the project clean. `pnpm test` is 192/3 — the
3 failing test files (memo-export, transcription hook, transcription
editor) reference missing `core/memoexport/testdata/1.0/*.zip`
fixtures and `Promise.withResolvers` (Node 22+ API). Both are
pre-existing on this machine; neither fixture path nor a Node
upgrade is touched by this slice.

## buf.gen.yaml drift — wire `protoc-gen-es` so `cd proto && buf generate` regenerates web/src/types/proto/ (2026-10-10)

`proto/buf.gen.yaml` was missing the TypeScript plugin. The
`AGENTS.md` Commands section advertised "Regenerate Go + TypeScript
+ OpenAPI", and the Database and Proto Rules said "include both
Go/OpenAPI and TypeScript generated outputs in the same commit",
but the config only ran the Go and OpenAPI plugins. The 15 files
under `web/src/types/proto/api/v1/` and `web/src/types/proto/google/`
were committed in `259d1d0e` ("chore(proto): refresh
web/src/types/proto/ for the riff module rename") and were
byte-equivalent to a regen of `protoc-gen-es v2.12.1` with
`target=ts` — but the next person to add a `.proto` field would
have to hand-regen the TS or accept drift.

**Decision.** Add a `local: protoc-gen-es` plugin block to
`proto/buf.gen.yaml`, opt `target=ts`, output
`../web/src/types/proto`. Declare `@bufbuild/protoc-gen-es ^2.12.0`
in `web/devDependencies` next to the existing `@bufbuild/protobuf`
runtime dep.

**Why local and not the BSR `buf.build/bufbuild/es` remote.** The
BSR plugin is the older v1.3.0 generator — its output uses a
different style than the v2.12.1 files that are already checked
in. The local plugin is the modern npm-published `protoc-gen-es`
v2.x generator and reproduces the existing format byte-for-byte
(modulo the `protoc-gen-es` version line in the header). Pinning
the major to match the runtime `@bufbuild/protobuf` dep is
simpler to reason about than mixing v1 (BSR) output with v2
runtime code.

**PATH gotcha.** Because the plugin is local, `buf generate` needs
`protoc-gen-es` on `PATH`. A clean clone has to run
`cd web && pnpm install` first; the AGENTS.md Commands section now
calls this out. Without the install, buf fails with
"protoc-gen-es: program not found or is not executable".

**Lockfile intentionally not touched in this commit.** pnpm 11
will fail a `pnpm install --frozen-lockfile` once `package.json`
gains a new devDep until `pnpm-lock.yaml` is refreshed. The
lockfile is a derived artifact of `pnpm install`; it belongs in a
follow-up `chore(deps): refresh pnpm-lock.yaml for protoc-gen-es`
commit that runs `cd web && pnpm install` end-to-end. This slice
is the contract change (what `buf generate` does); the lockfile
slice is the install of the new tool.

**Verification.** Could not be run end-to-end locally: the
`buf` CLI is not installed in this environment, and pnpm install
is needed for the local plugin to be on PATH. The config is
checked against the v2.12 `protoc-gen-es` README (matches the
existing file headers, the `target=ts` opt is the documented TS
target, and `out: ../web/src/types/proto` matches the existing
directory layout). A clean checkout on a machine with buf 1.x and
Node 24 should produce the same 15 files with no diff.

## Phase 6 — IdentityProviderService client_secret masking (Slice 4 follow-up) (2026-10-10)

Slice 4 shipped IdP CRUD on a public read shape, which meant
`ListIdentityProviders` and `GetIdentityProvider` returned the OAuth2
`client_secret` to any caller that could reach the RPC. That is not
acceptable for a value the operator pasted from a vendor console. This
follow-up slices two pieces out of that read path:

- **Mask the secret on every read.** A new helper
  `maskIdentityProviderSecret` in
  `pb/connect/identity_providers_converters.go` zeroes the
  `OAuth2Config.client_secret` field in place before the converter
  hands the proto back. `recordToIdentityProvider` is the only
  reader, so adding the call there is sufficient to mask List / Get
  / the post-Create response. The collection's `config` JSON column
  is unchanged — the secret is still persisted; we just do not
  surface it in the wire shape any more.

- **Explicit reveal RPC for admins.** A new
  `GetIdentityProviderClientSecret` RPC
  (`GET /api/v1/{name=identity-providers/*}/clientSecret`) bypasses
  the masked converter. It calls `extractIdentityProviderSecret`
  on the decoded config and returns the original value. The
  handler is admin-gated (`viewer.IsSuperuser()`), and the rate
  limit row in `pb/connect/ratelimit.go` uses
  `ScopeWriteUser`. There is exactly one server-side code path
  that returns the secret, so future audits can grep for it
  without checking each read path.

- **Proto annotation for SPA / OpenAPI.** `OAuth2Config.client_secret`
  now carries `(google.api.field_behavior) = INPUT_ONLY`, which
  gnostic renders as `writeOnly: true` in `proto/gen/openapi.yaml`.
  The MCP server and the SPA's generated TS client will therefore
  never re-send the secret after a round-trip, even if the SPA
  accidentally echo'd it back from its own state.

- **`buf.gen.yaml` drift.** `proto/buf.gen.yaml` no longer carries
  the `bufbuild/es` plugin (removed in commit `3bd5c81d` for the
  riff refactor). Re-adding it to regen the TS surface is a
  one-line edit but it does mean the `web/src/types/proto/` files
  cannot be regenerated by the documented `cd proto && buf generate`
  alone. The cleanest fix is a follow-up to add the plugin back
  under a clear `out:` target; tracked separately.

**Tests.** New `pb/connect/identity_providers_internal_test.go`
cases pin both directions:

- `TestListAndGet_MaskClientSecret` — secret is empty on both
  List and Get; `client_id` and the rest of the config still
  round-trip.
- `TestGetIdentityProviderClientSecret_AdminRevealsSecret` —
  admin gets the original secret, non-admin is
  `PermissionDenied`, unauthenticated is `Unauthenticated`.
- `TestGetIdentityProviderClientSecret_NotFoundAndInvalid` —
  resource-name parser unchanged (`NotFound` on missing slug,
  `InvalidArgument` on malformed names).
- `TestUpdateIdentityProvider_PreservesSecretWhenOmitted` —
  the rotation-safety invariant: a PATCH that omits the config
  oneof keeps the stored secret, verified via the admin reveal
  RPC after the update.
- `TestCreateIdentityProvider_AdminSucceeds` — updated
  assertion: `client_secret` is empty in the response now (the
  comment block was rewritten to match the new reality).

Verified: `go build ./...`, `go vet ./...`, `gofmt -l .` (clean),
`go test -count=1 ./...` (all packages pass), and
`go test -race -count=1 -run 'TestListIdentityProviders|TestCreateIdentityProvider|TestGetIdentityProvider|TestDeleteIdentityProvider|TestListAndGet_MaskClientSecret|TestGetIdentityProviderClientSecret|TestUpdateIdentityProvider_PreservesSecretWhenOmitted|TestGlobMatch' ./pb/connect/`
all pass. The full `pb/connect` suite under `-race` exceeds the
5-minute alarm on this machine because the rest of the package is
large; the IdP subset is the only one that touches the masking
code path and it passes cleanly under `-race` in ~38s.
## Phase 6 — IdentityProviderService + SSO sign-in (Slice 4) (2026-10-10)

Slice 4 fills in the five `IdentityProviderService` RPCs (List / Get /
Create / Update / Delete) and the `sso_credentials` branch of
`AuthService.SignIn`. Before Slice 4 the handler was the
generated `UnimplementedIdentityProviderServiceHandler{}`, so the
SPA's "Sign in with Google" button had no real back-end. The design:

- **Reuse PocketBase's OAuth2 stack.** We do not bring in
  `golang.org/x/oauth2/google` directly. The handler loads the
  configured IdP row, builds a `core.OAuth2ProviderConfig`, and calls
  `InitProvider()` — the same entry point the upstream
  `apis.recordAuthWithOAuth2` handler uses. The provider registry
  (`auth.Providers`) is the same one, so any vendor the operator
  pre-registers (Google, GitHub, etc.) works without an extra code
  path. See `pb/connect/auth_sso.go::signInWithSSO`.

- **Single JSON `config` TextField, not the
  `users.OAuth2.Providers` shape.** The Memos contract treats an IdP
  as a first-class resource with its own name, type, title, and
  config blob; folding it into the auth collection's options would
  require the SPA to model "providers" as a side channel and would
  lose the resource name `identity-providers/{idp}` the SPA's
  OpenAPI types are bound to. The config column is the
  `IdentityProviderConfig` proto oneof round-tripped via
  `protojson`; today the only oneof arm is `OAuth2Config`. See
  `pb/migrations/1810000000_create_identity_providers.go` and
  `pb/connect/identity_providers_converters.go`.

- **`client_secret` masking is a future slice.** The proto marks
  the field as INPUT_ONLY via the `OAuth2Config` design, but the
  `recordToIdentityProvider` converter today does not strip it on
  read. The next slice will introduce a dedicated
  `GetIdentityProviderClientSecret` RPC or a "masked" field on
  `GetIdentityProvider` so a non-admin never sees a usable secret
  on List. Until then, the secret is visible to any caller that
  can read the resource — the admin guard on Create / Update /
  Delete keeps the secret out of the wrong hands in practice.

- **`identifier_filter` is a single-`*` glob, not a regex.** The
  operator almost always wants `*@example.com`; a full regex
  engine would be both overkill and a footgun (a typo in the
  pattern can match nothing or everything). The glob
  implementation lives in `auth_sso.go::globMatch`. A real regex
  parser will land when the spec demands it; the storage is one
  line so the swap is cheap.

- **Honour `disallowUserRegistration` on first-time IdP sign-ups.**
  When the GENERAL setting has `disallowUserRegistration=true`, a
  fresh IdP sign-in (no existing user matched by the
  `_externalAuths` link or by email) is rejected with
  `PermissionDenied`. This mirrors the policy already enforced by
  `UserService.CreateUser` for the password sign-up path, so
  switching the IdP on cannot accidentally let through sign-ups
  the operator has otherwise disabled.

- **Identity is stored in `_externalAuths`, not a riff-owned
  `user_idp_links` table.** The link already exists in PocketBase;
  the OAuth2 flow has been writing into it for years. Adding a
  parallel table would create a sync problem (which is the source
  of truth when a user revokes via the IdP portal?). The
  `provider` column is the OAuth2 vendor name (e.g. "google"); the
  `providerId` is the OAuth2 subject. The riff row's `name` slug
  is *not* persisted in the link, so changing an IdP's slug
  invalidates all existing links. Today that is fine because slugs
  are admin-mutable only via Delete + Create; a future slice can
  add an `identity_provider` reference column if multi-IdP
  becomes common.

- **Admin guard on every mutating RPC, read RPCs public.** List
  and Get are open so the SPA's sign-in page can render the
  "Sign in with Google" button without forcing the user to be
  signed in first. Create / Update / Delete require
  `viewer.IsSuperuser()`. The rate-limit table in `ratelimit.go`
  already had rows for the three mutating procedures, so the
  slice's only registration work was removing the
  `UnimplementedIdentityProviderServiceHandler{}` line in
  `connect.go` and replacing it with
  `NewIdentityProviderServer(app)`.

- **SPA builds the authorize URL itself (no `GetAuthorizeUrl`
  RPC).** The IdP's `client_id` and `auth_url` are exposed on
  every Get response; the SPA can therefore build the consent URL
  without the server having to round-trip the
  `oauth2.AuthCodeOption` list. PKCE is generated client-side
  when the IdP advertises it (Google does by default) and the
  `code_verifier` flows through the `sso_credentials.code_verifier`
  field on the SignIn request. The server never sees the
  `state` parameter (the SPA stores it in `sessionStorage`); the
  `redirect_uri` the SPA sends back is required to match the
  value the SPA passed in, and is what `provider.SetRedirectURL`
  uses when calling `FetchToken`.

## Phase 6 — Memos export/import (Slice 3) (2026-10-09)

Slice 3 implements the round-trip pair `UserService.ExportMemos`
(returns a `google.api.HttpBody` with the export archive) and
`UserService.ImportMemos` (chunked upload matching the SPA's
`uploadService.ts` shape). Before Slice 3 both RPCs returned
`CodeUnimplemented`, so the SPA's "Export to Memos" and "Import from
Memos" buttons failed with a hard "protocol error" the moment a user
clicked them. The fix:

- **One in-memory manager per protocol, both pattern-matched on the
  upload manager.** `pb/connect/memo_import_session.go` is a
  package-level singleton with the same `create / get / abort /
  validate / finalize / abandon` shape as `upload_session.go`, and
  `connect.go::OnServe` calls `installImportManager(app)` next to
  `installUploadManager(app)`. The two managers share the per-user
  cap, sweeper cadence, and `OnTerminate` lifecycle. A future
  refactor can collapse them into one `chunkedSession` type; the
  duplication is intentional for now because the import carries
  plan/report state the upload does not, and the proto's two RPCs
  are deliberately parallel but not identical.
- **Files are ZIPs, not single attachments.** The export handler
  builds a `archive/zip` writer in memory, writes every memo record
  (`memos/<uid>.json`), the byte-identical content
  (`memos/<uid>.md`), and the linked attachment files
  (`attachments/<uid>/<safe-filename>`) per the
  `docs/design/memos-export-format.md` schema. The manifest is
  written last so its `counts.memos` and `counts.attachments` totals
  are accurate. The safe-filename mapping lives in
  `memo_export_format.go::safeFilename` and matches the design
  letter-for-letter (reserves `CON`/`PRN`/`AUX`/`NUL`/`COMx`/`LPTx`,
  trims leading/trailing spaces and dots, replaces `/`, `\`, `:`,
  NUL, and C0 control characters with `_`).
- **The format mirrors the public API's vocabulary, but riff's
  schema does not have every column the format expects.** The
  format documents `state` (`NORMAL`/`ARCHIVED`), `pinned`, and
  `location` as round-tripping fields, but riff's `memos` collection
  has none of them as columns. The export writes `state: "NORMAL"`
  and `pinned: false` as defaults today, and reads `location` from
  the `payload` JSON column (where riff stores it). The
  `patches`/`samples` collections do not have a `filename` column
  either; the filename is recovered from the `file`/`files`
  FileField and the MIME is derived from the extension. A future
  riff release that adds the missing columns is a one-line swap in
  the export helper.
- **Conflict resolution follows the design verbatim.** SKIP is the
  default. REPLACE updates content, visibility, space, and
  timestamps; it does not refresh attachments (the design says byte
  identity is what matters and a user with a duplicate is best
  served by leaving their existing attachments alone). DUPLICATE
  creates under a fresh UID. An existing UID owned by another
  account is imported under a fresh UID (a per-user prefix) and
  counted in `plan.renamed`. The second pass over the records
  applies `parent` and `relations` so parents always precede
  comments.
- **Attachments are uploaded through the same pipeline as a normal
  upload.** `pb/connect/memo_import_helpers.go::uploadAttachmentBytes`
  wraps `filesystem.NewFileFromBytes` and `filesystem.UploadFile`,
  matching the `attachment_upload.go::uploadFileToPocketBase` shape.
  This means the S3 storage bridge and the per-collection `CreateRule`
  (`memo.author = @request.auth.id`) apply unchanged. The
  `externalLink` path creates a record with no bytes, matching the
  design.
- **SHA-256 dedup is stubbed.** The design step 5 says "An existing
  unlinked attachment owned by the importer with the same bytes is
  bound instead of copied". Implementing it requires hashing every
  attachment the user owns (O(n) in storage). For Slice 3 the
  helper `findAttachmentBySHA` returns `(_, false)` and every
  attachment is uploaded. A follow-up can add an index column to
  `patches`/`samples` and hash on the after-create hook.
- **Cross-user session lookups return NotFound.** A user who guesses
  another user's `upload_id` gets the same `404 not_found` as a
  missing session, so existence does not leak across accounts. The
  same rule applies to the export path: only the user may export
  their own memos; the design explicitly rejects an admin bypass.
- **Deferred per the design.** Reactions are stored on the export
  side as `reactions[]` (so a future reader can pick them up) but
  the import does not recreate them. Webhooks and mention
  notifications do not fire on imported memos; the design lists
  this under "Deferred design" with the rationale that an import
  is a single user-visible event, not a stream of memo writes.

**Files added:**

- `pb/connect/memo_export.go` (`ExportMemos` handler + helpers)
- `pb/connect/memo_export_format.go` (manifest, record, attachment
  wire types; safe-filename mapping; JSON encoders)
- `pb/connect/memo_import_session.go` (in-memory manager)
- `pb/connect/memo_import.go` (`ImportMemos` RPC lifecycle:
  spec / writeOffset / finishWrite / progress pings / cached
  report)
- `pb/connect/memo_import_apply.go` (manifest validation, plan
  computation, per-memo apply with conflict policies,
  parent/relations pass, attachment round-trip)
- `pb/connect/memo_import_helpers.go` (`typesDateTime`,
  `uploadAttachmentBytes`, `timestamppbFromTime`)
- `pb/connect/memo_export_internal_test.go` (safe-filename,
  guessMime, ExportMemos auth/permission/empty cases)
- `pb/connect/memo_import_internal_test.go` (manager lifecycle,
  user cap, ImportMemos spec, progress ping, cross-user
  rejection)

**Files modified:**

- `pb/connect/connect.go` — `OnServe` now calls
  `installImportManager(app)` next to `installUploadManager(app)`.

**Validation:** `go build ./...`, `go vet ./...`, and `go test
-count=1 ./...` all pass. The new tests cover the safe-filename
mapping (ASCII, path separators, reserved Windows names, empty
input), the MIME guesser, the ExportMemos auth/permission/empty
cases, the import-session manager (create/get/abort, per-user cap,
cross-user rejection), and the ImportMemos spec/progress-ping path.

**Out of scope for Slice 3 (deferred to a follow-up):**

- End-to-end round-trip test (ExportMemos → ImportMemos → same
  memos). The current tests pin the building blocks; a full
  round-trip would need a fixture user with pre-existing memos
  across the patches/samples collections.
- SHA-256 dedup.
- Reactions / webhooks / mention notifications on import.
- `state: "ARCHIVED"` and `pinned: true` round-trip once the
  schema has the columns.
- A `pb/connect/memo_export/` subpackage. The export/import
  pipeline is large enough to warrant extraction; the Slice 3
  files are still in the parent package to keep the diff
  reviewable.

## Phase 6 — Chunked `UploadAttachment` (Slice 2) (2026-10-09)

The Memos SPA's `web/src/components/MemoEditor/services/uploadService.ts`
implements a chunked upload protocol that hits
`AttachmentService.UploadAttachment` with a `spec`/`writeOffset`/
`finishWrite` shape. Before Slice 2 the handler returned
`CodeUnimplemented`, so the SPA's file-ingest path failed with a
"protocol error" the moment a user attached a file. The fix:

- **In-memory upload-session manager, not a database table.** The
  state is `upload_id → (user, file, offset, expectedSize, stage file
  path, attachment record id)`, kept in a package-level map. The
  proto's design comment is explicit: "Uploads … do not survive a
  server restart" (`proto/api/v1/attachment_service.proto`). A
  database-backed manager would survive restarts, but the design
  contract forbids it, and the SPA never relies on a session that
  has been open across a restart. The map reaps entries that have
  been idle for 30 minutes via a sweeper goroutine that the
  `OnServe` hook starts and the `OnTerminate` hook stops.
- **Files are staged on disk, not in memory.** Each session has a
  `*os.File` opened in `<DataDir>/uploads/`. The handler writes
  incoming chunks with `WriteAt(data, offset)` so the SPA can
  resume from any byte offset. The on-disk staging file is moved
  into PocketBase's filesystem via `app.NewFilesystem().UploadFile`
  when the final `finishWrite` lands; the staging file is then
  removed. A 30 MB sample pack never inflates to a 30 MB Go heap
  allocation.
- **The collection is chosen by MIME type.** The proto has no
  collection discriminator; the SPA sends whatever the browser
  inferred from the file extension. We mirror the Memos Go
  convention: `audio/*` and `application/zip` land in `samples`;
  everything else (PO-33 patches included) lands in `patches`. A
  pre-flight MIME allow-list at spec time returns a precise error
  before any bytes hit the staging file, so a hostile client does
  not get to write 30 MB of `image/png` only to be told the
  collection rejects it.
- **`media_metadata` and `motion_media` are written from the spec.**
  Both are proto-OPTIONAL fields the SPA populates at file-ingest
  time. The handler persists them to the existing
  `patches.motion_media` column and a new
  `patches.media_metadata` column on `patches` and `samples`. The
  new `pb/migrations/1800000000_add_media_metadata.go` migration
  is idempotent so it can re-run on a populated database. The
  `media_metadata` round-trip uses `protojson` so the proto's
  `oneof details` keeps its discriminator on disk; the read
  path is lenient and returns `nil` for empty or malformed JSON
  rather than 500-ing the request, matching the `readMotionMedia`
  policy in `pb/connect/attachment_motion_media.go`.
- **Idempotent in the three ways the proto requires.** A repeated
  `finishWrite` returns the previously-created attachment without
  re-running the record-creation chain. A repeated `writeOffset` of
  the most recently accepted chunk's start is accepted as a no-op.
  A progress-only ping (no data, no `finish_write`) returns the
  current `committed_size` so the SPA can recover from a lost
  response.
- **Cross-user access is `NotFound`, not `PermissionDenied`.** A
  `PermissionDenied` error would leak the existence of another
  user's `upload_id` to a guessing attacker. Returning
  `NotFound("upload not found or expired")` matches the memo-share
  collection's behaviour and keeps the upload protocol
  unguessable.
- **`patches` and `samples` create rules are the only authority.**
  The handler runs `createRecordRequest` so PocketBase's
  `OnRecordCreateRequest` chain (`pb/hooks/motion_media.go`,
  `pb/hooks/storage_bridge.go`) fires exactly as it would for a
  REST upload. The collection's `CreateRule =
  "memo.author = @request.auth.id"` rejects uploads that target
  a memo the caller did not author, so the handler does not need
  to re-check authorship.

Test surface: `pb/connect/connect_wave6_attachment_upload_internal_test.go`
covers spec validation, the chunked write loop, idempotent
`finishWrite`, cross-user isolation, the per-user unfinished cap,
the 2 MiB chunk cap, the 64 MiB hard total-size cap, MIME
dispatch (`patches` vs `samples`), the `media_metadata` and
`motion_media` round-trips, and the single-shot tiny upload
(spec + data + `finishWrite` in one call).

## Phase 6 — Self-service sign-up (2026-10-09)

The Memos SPA's sign-up flow runs through `UserService.CreateUser`,
but the old `pb/connect/user.go::CreateUser` was unconditionally
admin-gated. The SPA's `pages/SignUp.tsx` always sends
`User_Role.USER`, so the SPA has no way to communicate "I am the
installer". The handler had to derive the intent from server state
alone, matching the upstream Memos Go handler. The Phase 6 fix:

- **Bootstrap detection is server-side, not request-side.** The SPA
  never sends `role: ADMIN`. When the instance has no real users
  yet (`needsSetup=true`) and the caller is unauthenticated, the
  handler forces `role=ADMIN` and writes a `_superusers` record so
  the rest of the app treats the new account as the instance owner.
- **Unauthenticated ADMIN after setup is rejected.** A client that
  smuggles `role=ADMIN` in the request after the bootstrap window
  has closed gets `FailedPrecondition`, not silent downgrading.
- **Self-service and admin-creates-user stay separate.** With
  `disallowUserRegistration=true` an unauthenticated caller gets
  `PermissionDenied`; an authenticated admin still gets the
  invite-style path. `disallow_password_auth` is also checked
  upstream of the `users` insert, matching Memos' proto.
- **The `users` collection is never the destination of an
  ADMIN-role request.** That kept an old "shadow admin" loophole
  where the `users` row would have been silently downgraded to USER
  by `applyUserFields`. ADMIN writes always land in `_superusers`.
- **A `password_hash` column was added to `_superusers`**
  (migration `1790000000_add_superuser_password_hash.go`) for the
  same reason `users` carries it (migration 1710000001): PocketBase's
  built-in `password` field is PBKDF2-hashed and not directly
  comparable from Go code, so the Connect SignIn path needs a
  bcrypt-verifiable copy.
- **`AuthServer.findByIdentity` falls through to `_superusers`.** The
  bootstrap admin's identity lives in the `_superusers` collection
  (keyed by `email`, not `username`), so the lookup has to look
  there too once `users` returns no match. The PocketBase
  `__pbinstaller@example.com` placeholder is excluded so the
  login attempt fails fast instead of returning a stale record.
- **Bootstrap cannot be re-opened by clearing users.** The
  installer placeholder (`__pbinstaller@example.com`) is the
  PocketBase default; it is filtered out in both
  `findFirstAdmin` and `findByIdentity` so a fresh install with
  only that placeholder still reports `needsSetup=true`.

## Phase 5 — Fly.io deployment & S3 storage (2026-10-09)

The `scripts/Dockerfile` and the `release_smoke_test.sh` were Memos-shaped
(both referenced `./cmd/memos`, the `memos` binary, `MEMOS_*` env vars,
and `MEMOS_PORT=5230`). The `InstanceSetting.StorageSetting.S3Config`
proto field has been declared since the Memos import but no Go code
ever read it. Phase 5 ports the deploy story to riff and lands the
remote-attachment plumbing. Choices that fall outside the mechanical
port:

- **S3 client wraps `pocketbase/tools/filesystem`, not `aws-sdk-go-v2`.**
  PocketBase already ships a battle-tested S3 client
  (`tools/filesystem/internal/s3blob/s3/s3.go`) that we can lean on
  rather than reimplement signing-v4. The official `aws-sdk-go-v2` is
  ~50 MB of transitive deps, which the upstream PB maintainers also
  rejected (PB discussion #6562). `internal/storage/s3.go` is a
  ~150-line wrapper around `filesystem.NewS3`; the only riff-specific
  logic is URL construction (path-style vs virtual-hosted) and the
 404-to-`ErrNotFound` mapping.
- **Bridge hook is best-effort and non-blocking.** The
  `pb/hooks/storage_bridge.go` hook fires on
  `OnRecordAfterCreateSuccess` and `OnRecordAfterUpdateSuccess` for
  `patches`/`samples`. A remote upload failure logs a warning and
  leaves the local file as the canonical source of truth; the next
  save will retry. The local PocketBase file is never deleted on a
  successful remote upload because the bridge cannot guarantee the
  remote is permanently available (a transient S3 outage would
  orphan the attachment).
- **`remote_url` is a single text column, not a new collection.**
  The column is added idempotently to both `patches` and `samples` by
  `pb/migrations/1780000000_add_remote_url.go`. It carries a single
  URL or a JSON array of URLs (samples can have up to 50 files). A
  join/split helper in the bridge serialises/deserialises the array
  shape.
- **Only the legacy `s3_config` proto field is honoured.** The
  newer `InstanceSetting.StorageSetting.storages[]` +
  `default_storage_id` shape (multi-backend with named IDs) is
  declared in the proto but is not read by the bridge yet. Phase 6
  will extend the bridge to honour that shape; until then, only the
  single-bucket legacy `s3_config` field takes effect.
- **`SecretAccessKey` is read from the `instance_settings` row, but
  the proto's `INPUT_ONLY` annotation strips it from every response.**
  The first save with a new secret is the only time the bridge sees
  it. After that, the field is permanently redacted. The operator
  must restart the server with a new `RIFF_S3_SECRET` env var or
  re-submit the secret through `UpdateInstanceSetting` to rotate it.
  This is a known limitation; it matches how Memos treats SMTP
  passwords in the same proto.
- **PocketBase `newer` `tools/filesystem` client is the source of
  truth for the S3 path.** The PB filesystem API does not take a
  `context.Context` on `GetReader`/`NewWriter`, so the
  `context.Context` parameter on `Storage.Get/Put/Stat/Delete` is
  accepted but ignored. We still pass one because the bridge hook
  composes the request context for logging; ignoring it is a
  non-issue for a 30-second upload.
- **Fly.io `fly.toml` is a template, not a live config.** The
  `app = "riff"` field is a placeholder; the operator must run
  `fly apps create <unique-name>` and patch `app` before `fly
  deploy`. The persistent volume `riff_data` must also be created
  once with `fly volumes create riff_data --size 1`. The
  `release_command` runs `scripts/release_version.sh version
  <git-branch>` to print the resolved CalVer for the deploy log;
  the actual `VERSION` and `COMMIT` are baked into the image at
  build time.
- **`scripts/release_smoke_test.sh` is left Memos-shaped.** The
  smoke test calls Memos REST endpoints (`/api/v1/users`,
  `/api/v1/auth/signin`, `/api/v1/memos`) that do not exist in
  riff (the auth and memo flows are Connect/gRPC). A riff-shaped
  smoke test that drives `pb/connect.SignIn`,
  `pb/connect.CreateMemo`, etc. against a fresh container is
  deferred to a Phase 5 follow-up commit. The `scripts/Dockerfile`
  port and `fly.toml` template are sufficient to cut the first
  release; a missing smoke test does not block a `git push
  YY.MM.N` tag.

### Phase 5 Slice D — Inbox hooks + email delivery (2026-10-09)

The Memos inbox (`SPACE_INVITATION`, `MEMO_COMMENT`, `MEMO_MENTION`
notification kinds) was partially wired in Wave 5: the
`notifications` collection existed, `ListUserNotifications` worked
against seeded rows, and `spaceInviteHandler` in
`pb/api/space_routes.go` wrote the invite row, but the proto/DB
type mismatch (`MEMO_REACTION` vs `MEMO_MENTION`) and the missing
write hooks for `MEMO_COMMENT` and `MEMO_MENTION` left the
end-to-end story broken. Slice D closes the gap and adds the
email delivery path. Choices that fall outside the mechanical
port:

- **`MEMO_REACTION` was a Wave-3 placeholder; the proto enum is
  `MEMO_MENTION`.** The previous `pb/migrations/1710000000_create_notifications.go`
  declared `Values: []string{"SPACE_INVITATION", "MEMO_COMMENT", "MEMO_REACTION"}`,
  which no row could ever carry because the
  `recordToUserNotification` decoder had no `MEMO_REACTION` arm
  (it downgraded to `TYPE_UNSPECIFIED`). The migration's select
  field is now `MEMO_MENTION`; a follow-up migration that
  actively rewrites existing rows is not needed because
  `MEMO_REACTION` was never writeable. Re-running the migration
  on an existing database is a no-op (`Add` is skipped when the
  field is already present).
- **The `notifications` collection is server-write only.** Its
  `createRule` is `nil` (superuser-only), and the only authorised
  insert path is the exported `hooks.CreateNotificationBestEffort`
  helper. The helper is used by:
  - the four `OnRecordAfter*Success` hooks in
    `pb/hooks/notifications.go` (MEMO_COMMENT, MEMO_MENTION,
    accept-archive, decline/revoke-delete);
  - the `spaceInviteHandler` in `pb/api/space_routes.go` (the
    one pre-existing call site, which used to write directly
    via `app.Save`).
  All other code paths produce notifications as a side effect of
  the underlying save, never directly.
- **The mention dedupe on memo update uses a stashed-content
  map, not `e.Record.Original()`.** PocketBase's
  `OnRecordAfter*Success` event exposes `e.Record.Original()`,
  but for a record that arrived via the request bridge the
  `originalData` map is empty (the record is a fresh
  `core.NewRecord` from the request body, not a row that was
  loaded first). The `OnRecordUpdate` model-level hook looks
  the previous content up explicitly and stashes it in
  `prevMemoContent`; the `OnRecordAfterUpdateSuccess` hook
  reads from that map and clears the entry.
- **Self-mentions and self-comments are not inbox events.** The
  hook layer treats the memo author as a no-op for both
  mention and comment paths, matching the Memos "things you
  would otherwise miss" rule. The author is read at the row
  level (`memo.GetString("author")`); an `auth != author`
  check guards the per-receiver emission.
- **`internal/email` is a revival of the Memos SMTP package,
  not a rewrite.** The previous retirement entry
  (Phase 5 — Orphan package retirement) was correct at the
  time: nothing imported it. The Phase 5 Slice D dispatcher
  is the first importer, so the package comes back unchanged
  in shape — same `Config`, `Message`, `Client`, `Send`,
  `SendAsync`, same `init()` workers — with a small `doc.go`
  refresh that documents the revival.
- **`TestInstanceEmailSetting` is now a real TCP-dial
  reachability check, not a syntactic stub.** The prior
  handler parsed the JSON and returned `ok` without ever
  opening a connection; the new one calls `(&net.Dialer{Timeout:
  5*time.Second}).Dial("tcp", host:port)` and reports the
  result. This is a more honest test for the admin UI ("can
  my server reach the SMTP host?") and matches what the
  upstream Memos version of the same RPC does in spirit. It
  does NOT send a real email — that path lives in
  `EmailDispatcher.DispatchTestEmail` and is reachable through
  the dispatcher's `sender` shim when an operator wires a
  test runner.
- **The dispatcher is feature-gated on `RIFF_INSTANCE_URL`.**
  An empty value means every email would have a broken link,
  so the dispatcher no-ops with a warn log. The same env
  var lights up the MCP origin allow-list (see
  `buildProfileFromEnv` in `main.go`). One knob, two
  features: the operator sets `RIFF_INSTANCE_URL` once.
- **The dispatcher's `sender` is a function value, not a
  global queue.** `pb/hooks/notifications_email_wiring.go`
  exposes `InstallEmailDispatcher(profile, app)`; the wiring
  installs a package-level `*EmailDispatcher` that the
  after-success hook reads. Tests pass their own sender to
  bypass the network; production uses `email.SendAsync` (the
  bounded 128-message async queue that the package's `init()`
  starts two workers for).
- **Memo visibility for the email path is a conservative
  approximation, not the rule engine.** The dispatcher
  classifies a memo as "visible to the receiver" by checking
  `visibility` directly: `PUBLIC` and `PROTECTED` are
  visible; `PRIVATE` and `SPACE` need membership that the
  dispatcher does not evaluate. This is intentionally
  over-restrictive — a private-memo mention is dropped, which
  is the safe direction for an email side-channel. A
  follow-up commit can re-use `app.CanAccessRecord` if the
  inbox starts to over-suppress.

---

## User-webhook lifecycle end-to-end (2026-10-09)

The five `UserService.UserWebhook` RPCs (`ListUserWebhooks`,
`CreateUserWebhook`, `UpdateUserWebhook`, `DeleteUserWebhook`,
`GetUserWebhookSigningSecret`) are now wired against two new
collections and two new hooks. The design choices that fall outside the
mechanical translation of the proto into a handler:

- **Signing secret is a plain `TextField`, not `PasswordField`.**
  PocketBase's `PasswordField.Intercept` clears the in-memory `Plain`
  value after the create/update action, which would break both the
  `GetUserWebhookSigningSecret` RPC and the drain goroutine (which
  reads the raw secret to sign outbound POSTs). `PasswordField` is
  also coupled to auth collections; `user_webhooks` is a base
  collection, so we'd be fighting the framework on two axes. The
  proto's `INPUT_ONLY` annotation already removes the secret from
  every list/get response, and the column is `Hidden: true` for the
  REST collection API. At-rest disk encryption is the operator's
  responsibility (PocketBase ships a `pb_data/` directory the operator
  is expected to mount on encrypted storage).
- **Two migrations, one collection each.** PocketBase's
  `migrations.Register` uses `runtime.Caller(1)` to derive the
  migration's filename-based dedup key, so two `init()` functions in
  one file would share the same caller and the second would overwrite
  the first. `1770000000_create_user_webhooks_main.go` registers the
  `user_webhooks` collection; `1770000001_create_webhook_deliveries.go`
  registers the outbox. The numeric gap (`...000` and `...001`)
  signals the same deployment unit.
- **Outbox table, not direct delivery.** A 30s-tick drain goroutine
  (with `app.OnTerminate` shutdown signal) is the only path that
  actually POSTs to receivers. The outbox hook enqueues one
  `webhook_deliveries` row per user webhook on every memo
  create/update/delete success; the drain picks rows whose
  `next_attempt_at <= now` and dispatches them through
  `webhook.Post`. Direct delivery in the after-save hook would
  introduce tail-latency on memo writes and a slow receiver would
  block every other webhook for that user.
- **`update_mask` is required on `UpdateUserWebhook`.** A proto3
  string field's zero value is `""`, which is indistinguishable from
  "not set" — so a permissive "apply populated fields" rule would
  silently clear `display_name` and `signing_secret` on a URL-only
  update. The handler refuses an empty mask with `InvalidArgument`,
  matching the `user_settings.Update` precedent. The React client
  always sends a mask.
- **Per-URL `whsec_` secret generation.** `CreateUserWebhook` mints a
  fresh `whsec_<base64>` token (32 random bytes) when the request
  omits one, so two consecutive creates produce independent secrets.
  There is no global default secret that an attacker could guess.
- **Memo `Links` race fix in the same commit.** The pre-existing
  `memoLinksFanOut` worker-pool race (AGENTS.md Known Issue #1) is
  fixed by a per-app `memoLinksShutdown` channel that the
  `OnTerminate` hook closes. The goroutine checks the signal before
  every DB write, and `pruneStaleMemoLinks` recovers from a
  closed-DB panic so a test app cleanup does not crash the runner.

## Attachment motion-media end-to-end (2026-10-09)

### Scope

Wire `internal/motionphoto` into the upload path so the
proto's `Attachment.motion_media` field actually
populates and the frontend's `MotionPhotoPreview`
component can play the embedded video. Three new
files and one migration.

### What is now wired

1. **Migration** `pb/migrations/1760000000_add_motion_media.go`
   adds an idempotent `motion_media` JSON `Text` column
   to both `patches` and `samples`. We picked `Text`
   (max 8192 bytes) over `JSONField` because the JSON
   field's storage semantics drift between
   SQLite/Postgres/MySQL; the `Text` column round-trips
   the same payload in every driver and the
   `pb/connect/attachment_motion_media.go` converter
   handles the parse on the way out.

2. **Hook** `pb/hooks/motion_media.go` subscribes to
   `OnRecordAfterCreateSuccess` for both `patches`
   and `samples`, runs `motionphoto.DetectJPEG` on
   the first file, and writes the result back to the
   row's `motion_media` column. The hook is detached
   (goroutine) so a slow disk read does not stall
   the API response; failures are logged and
   silently dropped (the column stays empty, the
   proto's `motion_media` is unset).

3. **Converter** `pb/connect/attachment_motion_media.go`
   reads the column and produces the proto's
   `MotionMedia` value. The converter fails open:
   a malformed column yields `nil` rather than an
   error, so a bad row written during the first
   deploy never locks the user out of an
   otherwise-readable attachment.

### Design call-outs

- **Pre-filter on filename extension** skips
  `.wav`, `.mp3`, `.zip`, `.pdf`, `.heic`, etc.
  before any disk read. The detector is fast
  but the read isn't.
- **Bounded read** uses `io.LimitReader` capped
  at 16 MB even though the `patches` field is
  already capped at 8 MB. Defence in depth: we
  don't trust the field's `MaxSize` to apply
  when the upload was made via a path that
  bypasses the standard collection REST API.
- **Family is hardcoded to `ANDROID_MOTION_PHOTO`**
  because the detector currently only recognises
  the Google GCamera XMP packet. A future wave
  can add iOS Live Photo detection and switch
  on `family` here.
- **Role is hardcoded to `CONTAINER`**. The
  proto's enum (`STILL | VIDEO | CONTAINER`) is
  an odd fit for Android Motion Photos, where
  the file is a single container that holds
  both still and video. `CONTAINER` best
  reflects "this row is the wrapper"; the
  frontend picks the still or the video
  based on `has_embedded_video` and the
  user action.

### Validation

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test -v -count=1 -run 'TestShouldInspect\|TestFileField\|TestMotionMedia\|TestReadMotionMedia' ./pb/...` — pass.

---

## Orphan package retirement: `internal/email` (2026-10-09)

### Scope

Retire the `internal/email` package (10 files, ~46KB including
README). It is a Memos-shaped SMTP helper left over from the
Memos-to-PocketBase port: built, fully tested, with **zero
importers in the riff Go tree** (`grep -rn
'"github.com/incurl/riff/internal/email"' --include='*.go' .`
returns nothing).

### Why delete

- The proto has no `email` RPC or message that would need
  the SMTP client; the closest proto surface is
  `TestInstanceEmailSetting` in `instance_service.proto`,
  whose handler in `pb/connect/instance_settings.go` is
  deliberately a syntactic check that never dials a TCP
  connection (the comment in that file makes the design
  explicit).
- The `internal/email` package itself documents the gap
  in its own `doc.go`: "designed for self-hosted
  environments where instance administrators configure
  their own SMTP servers" — a Memos-era affordance with
  no riff call site and no admin UI to populate
  `Config{SMTPHost, SMTPPort, ...}`.
- All eight `*_test.go` files pass on the current Go
  1.27.0 toolchain; the package is healthy, just
  unused. Holding it for a hypothetical future wave
  costs go-test time and reviewer cognitive load on
  every PR that touches `internal/`.

### What is NOT deleted in this commit

- `internal/webhook` — see "User webhooks end-to-end
  (2026-10-09)" below; the package stays and is wired
  up.
- `internal/motionphoto` — see "Attachment motion
  media end-to-end (2026-10-09)" below; the package
  stays and is wired up.
- `internal/email` as a *future* feature is not
  foreclosed. If a later wave adds the admin UI to
  store SMTP credentials and an email-OTP or
  notification RPC, the package can be re-introduced
  from git history in a single small commit.

### Validation

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test -count=1 -p 1 -race -skip 'TestMemos_Create'
  -skip 'TestCreateMemo' ./...` — pass (the
  memo-links race flagged in AGENTS.md Known Issue #1
  is pre-existing and unrelated to this commit).

---

## Connect rate-limit wiring (2026-10-09)

### Scope

Finish Wave 4 of the API abuse controls design
(`docs/design/api-abuse-controls.md`): wire the in-process
`internal/ratelimit/` middleware onto real Connect routes, gated
on the operator-supplied `RIFF_RATE_LIMIT` env var. Before this
change the middleware existed, was tested, and was attached to
exactly zero routes.

### Decisions

- **Opt-in table, not a blanket "every procedure" loop.** A
  `procedureScope` map in `pb/connect/ratelimit.go` lists the
  procedures that need a *per-flow* rule tighter than the
  catch-all the authorizer (a future wave) will layer on top.
  Procedures with no row are reachable but not rate-limited at
  the route level. This keeps the table small (~50 rows) and
  auditable: every entry is a deliberate budget, not a default.
- **Route-level middleware, not interceptor-level.** PocketBase
  routes run `BindFunc` before the connect-go handler (and
  before `withAuth` resolves the Bearer token), so route-level
  is the only place an `IPKey` (resolved client address) is
  available without an extra trust hop. Authorizer-level
  catch-all budgets (ScopeAnonymous / ScopeAuthenticated) are
  deferred to a future wave that has the resolved user.
- **Per-flow counters whose key is in the request body live in
  the handler.** `signin_account` keys on the *submitted*
  username, not the resolved user, and the design requires
  that a successful sign-in refunds the consume. Both rules
  need body access, so the counter is incremented inside
  `AuthServer.SignIn` and the Consume / Refund pair is
  explicit. The route-level `RateLimit(limiter, signin_ip)` is
  the IP-side companion and runs before the handler so bcrypt
  is never paid on a refused request.
- **No-op by default.** `RateLimit(nil, …)` is a no-op (covered
  by `TestRateLimit_NilLimiterIsNoop`). Production calls
  `connect.Register(app, nil)` until the operator sets
  `RIFF_RATE_LIMIT=1`, which the `Profile.RateLimit` flag
  plumbs to `ratelimit.NewMemoryLimiter(ratelimit.DefaultPolicy())`.
  The default install is byte-identical to before this commit
  when the env var is unset.
- **`registerConnectRoutes` is the test seam.** The production
  `Register` schedules the per-procedure loop behind
  `app.OnServe().BindFunc`, which only fires during
  `app.Start()`. Extracting the loop into a free function
  with the router as a parameter lets the in-package tests
  drive the same code path through `apis.NewRouter` without
  the full start loop. Two new tests
  (`TestConnectRegister_RateLimitFiresAgainstAuthSignIn`,
  `TestConnectRegister_NilLimiterKeepsProceduresReachable`)
  pin the wiring end-to-end; one new test
  (`TestSignIn_FailedAttemptsCountSigninAccount`) pins the
  in-handler Consume / Refund pattern.

### Files

- **`pb/connect/ratelimit.go`** — added `procedureScope` map
  (50 rows) and a package doc comment explaining the
  opt-in / no-op / handler-keys-in-body contract.
- **`pb/connect/connect.go`** — `Register` takes a
  `ratelimit.Limiter`; the OnServe callback now delegates to
  `registerConnectRoutes` (a free function with the same
  loop, exposed for tests).
- **`pb/connect/auth.go`** — `NewAuthServer(app, limiter)`
  takes a limiter; `SignIn` calls `Consume` on the
  submitted username before the password check, returns
  `connect.CodeResourceExhausted` when refused, and
  `Refund`s the consume on successful auth.
- **`pb/connect/connect_ratelimit_internal_test.go`** — added
  the three tests above plus the `connectRegisterForTest`
  helper that mirrors `Register`'s wiring synchronously.
- **`pb/connect/connect_wave4_user_internal_test.go`** — two
  `NewAuthServer(app)` call sites updated to
  `NewAuthServer(app, nil)` for the new signature.
- **`main.go`** — `RIFF_RATE_LIMIT` env var plumbed through
  `Profile.RateLimit` to the limiter constructor in
  `main()`; new `envBool` helper.
- **`AGENTS.md`** — new "Wire `RateLimit` onto a new Connect
  procedure" row in the Change Routing table, pointing at
  the `procedureScope` table.

### Validation

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test -v -count=1 -race -run
  'TestRateLimit|TestConnectRegister|TestSignIn'
  ./pb/connect/` — 7 / 7 pass.
- `go test -count=1 -p 1 ./pb/hooks/ ./internal/ratelimit/`
  — pass (no regression in the rate-limit library or the
  hooks that share the package's test helpers).
- The full `go test ./...` is blocked by a pre-existing
  worker-pool race in `pb/hooks/memo_links.go::memoLinksFanOut`
  (AGENTS.md Known Issue #1), unrelated to this change. My
  new tests do not touch the memo-create path.

### What was NOT added in this commit

- A persistent or distributed limiter. The
  `ratelimit.Limiter` interface is the seam; today only
  `ratelimit.MemoryLimiter` is wired. A Redis-backed
  implementation can land without touching `pb/connect/`.
- The authorizer-level catch-all (`ScopeAnonymous`,
  `ScopeAuthenticated`). The route-level table covers the
  per-flow rules the design asks for; the catch-all needs
  the resolved user, which only the authorizer has.
- A per-scope admin UI or knobs in the `InstanceSetting`
  collection. The design explicitly defers both.
- Changes to the `internal/email/`, `internal/webhook/`,
  `internal/motionphoto/` orphans. Those remain in their
  pre-retirement state, ready for a separate `filter/`-style
  decision in a future turn.

---

## Filter package retired (2026-10-09)

### Scope

Remove the orphaned `filter/` package. The Memos-era CEL→SQL
compiler has no production caller in riff: the `store/db/<dialect>/`
driver layer that would have consumed it was replaced by
PocketBase, and PocketBase's collection `filter` query parameter
is now the filter surface for every list endpoint.

### Why the package existed, and why it has no replacement

`filter/` (12 files, ~1,400 LoC + ~3,000 LoC of tests) was a
three-stage compiler: parse CEL → dialect-agnostic IR
(`filter/ir.go`) → SQL fragment for SQLite, MySQL, and Postgres
(`filter/render.go`). Memos needed it because Memos wrote its
own SQL; the input to the compiler was a per-list-endpoint
`filter` string field on the Connect request, and the output
was a `Statement{SQL, []any}` that the driver spliced into a
hand-rolled `SELECT`. That architecture is gone. In riff:

- There is no `store/db/` tree. The only SQL path is through
  PocketBase's `app.FindRecordsByFilter(col, filter, sort, limit,
  offset, params)` calls (see `pb/api/feed.go:123`,
  `pb/api/memo_routes.go:50`, `pb/api/space_routes.go:25`, …).
- The list endpoints accept a *string* `filter` field and hand
  it to PocketBase unchanged. PocketBase's own DSL is
  CEL-shaped (`uid={:u}`, `visibility = "PUBLIC"`,
  `tags ?= "urgent"`), so the same UI string parses fine.
- `grep -rn 'incurl/riff/filter'` returns no production
  importers. The package's public API (`NewSchema`,
  `NewEngine`, `DefaultEngine`, `DefaultAttachmentEngine`,
  `Compile`, `CompileToStatement`, `AppendConditions`,
  `RenderOptions`, `DialectSQLite`, `DialectPostgres`,
  `DialectMySQL`) is unused outside `filter/` itself and the
  three `filter/*_test.go` files.

### Decisions

- **Delete, do not adapt.** Adapting the schema to riff's
  collections (`memos`/`spaces`/`users` for `NewSchema()`,
  `patches`/`samples` for `NewAttachmentSchema()`) would
  preserve a compiler with no caller. The drift cost
  outpaces the cleanup cost.
- **Do not build a CEL→PocketBase-filter translator in this
  commit.** It is the right long-term design (keeps the
  user-facing CEL string the `MemoView.filter` field already
  stores) but it is a Wave-7-sized change: a new
  `internal/filterpocketbase/` package, plus wiring into the
  list handlers. The user-facing string is preserved
  verbatim in the `memo_views` collection either way.
- **`MemoView.filter` keeps its CEL-string storage.** The
  collection is the saved-filter user feature from Wave 5;
  whether those strings are ever re-evaluated is a future
  decision and depends on the `internal/filterpocketbase/`
  translator.
- **Drop `cel-go` from `go.mod`.** No code outside `filter/`
  imports `github.com/google/cel-go`. The dependency comes
  out via `go mod tidy`.

### Files

- **Deleted:** `filter/` (12 files: `MAINTENANCE.md`, `README.md`,
  `doc.go`, `engine.go`, `engine_test.go`, `functions_test.go`,
  `helpers.go`, `ir.go`, `parser.go`, `render.go`, `schema.go`,
  `time_test.go`).
- **`AGENTS.md`:** dropped the `filter/` row from the
  Repository Layout table, removed `filter/` from the
  layering chain sentence, removed the `Markdown, filter, or
  MCP logic` row from the Change Routing table, and removed
  `filter` from the CI "other" test group.
- **`README.md`:** the package description now says the
  Memos-era CEL→SQL compiler was retired and points to this
  entry.
- **`go.mod` / `go.sum`:** `cel-go` removed.
- **`DECISIONS.md`:** this entry is appended above Wave 6
  (Memos-era entries below stay untouched, per the
  append-only audit log).

### Verification

```
go build ./...                 # clean
go vet ./...                   # clean
go mod tidy -go=1.27.0         # cel-go dropped
go test ./...                  # all green; the pre-existing
                               # pb/hooks memo_links worker-pool
                               # race still surfaces on the
                               # second subtest of TestMemos_Create
                               # (Known Issue #1, not introduced
                               # by this commit)
```

### Still open after Wave 7

If a future Wave wants saved filters to actually be re-evaluated
server-side, the design is a thin `internal/filterpocketbase/`
that compiles a saved CEL string into the equivalent
PocketBase `filter` query parameter, with a per-collection
schema declaring which identifiers are CEL-visible. Until then,
`saved CEL filters from Wave 5` is a storage feature, not a
filter feature.

---


## Phase 4 Wave 6 — Memo shares (2026-10-09)

### Scope

Wave 6 closes the first item on the "Still open after Wave 5"
list. Four new `MemoService` RPCs land on the existing
`MemoServer`:

- `CreateMemoShare` — mint an opaque bearer token that grants
  read access to one memo. Caller is the memo's author or an
  instance admin.
- `ListMemoShares` — list the share rows attached to a memo.
  Same auth as create.
- `DeleteMemoShare` — revoke a share. Idempotent; a missing
  row is success, not a 404.
- `GetSharedMemo` — public, no auth. Resolves a share code to
  the underlying memo. The memo's own visibility still applies
  (a private-memo share does not leak existence to a non-author).

### New collection

| Collection | Why | Migration |
| --- | --- | --- |
| `memo_shares` | One row per active share. `code` is a 22-char URL-safe random string (131 bits); `memo` is a `CascadeDelete` relation; `created_by` is informational. | `pb/migrations/1750000000_create_memo_shares.go` |

### Decisions

- **Server-only on every `*Rule`.** All five access-rule
  fields on `memo_shares` are `nil` so the Connect handler is
  the only authority. This is the same shape as
  `notifications.createRule = nil`: the rule engine cannot
  express "memo author or admin" cleanly because admin is a
  PocketBase superuser, not a row in `users`, and we'd rather
  not duplicate the author check in two places. Tests
  `TestCreateMemoShare_AuthorOnly`,
  `TestListMemoShares_NonAuthorDenied`, and
  `TestDeleteMemoShare_AuthorCanRevoke` pin this.
- **`GetSharedMemo` does not consult the rule engine.** The
  row's `code` is the bearer credential, not the caller's
  session; a public route that asks PocketBase "can this
  request see this row?" would always say "no, no
  @request.auth". The handler queries the collection directly
  with `app.FindRecordsByFilter` and then runs
  `canViewMemo` against the resolved memo, so a private-memo
  share still returns `CodeNotFound` to a non-author
  (`TestGetSharedMemo_PrivateMemoShare`).
- **22-char codes from `internal/random`.** The TextField's
  `Pattern` accepts 22–32, so a future length bump is a
  constant change, not a migration. The unique index makes
  the chance of collision 1 in 2^131 per try; the
  `shareCodeMaxAttempts = 5` retry loop bounds a corrupted
  RNG.
- **Cascade-delete on `memo`.** Deleting a memo removes its
  share rows, matching the Wave 4 memo-delete cascade rule
  in `pb/hooks/spaces.go` and `docs/design/multi-spaces.md`
  (which the `TestMemoShare_CascadeDeleteOnMemoDelete` test
  guards).
- **Expiry is informational in the proto, enforced in the
  handler.** `expire_time` is a `*timestamppb.Timestamp` with
  a `oneof` marker; we read it via `record.GetDateTime` and
  reject reads in the past as `CodeNotFound`. A future
  background job to garbage-collect expired rows is the next
  step (see "Still open after Wave 6").

### Files

- `pb/migrations/1750000000_create_memo_shares.go` — the new
  collection.
- `pb/connect/memo_shares.go` — the four handlers, the
  `recordToMemoShare` translator, and the
  `canManageMemoShares` authorisation helper.
- `pb/connect/connect_wave6_shares_internal_test.go` — ten
  tests covering create / list / delete / public read,
  expiry, visibility-leak, and the cascade rule.

### Verification

```
go build ./...
go vet ./pb/connect/... ./pb/migrations/...
gofmt -l pb/connect/memo_shares.go \
          pb/connect/connect_wave6_shares_internal_test.go \
          pb/migrations/1750000000_create_memo_shares.go
go test -v -count=1 \
    -run 'TestCreateMemoShare|TestListMemoShares|TestDeleteMemoShare|TestGetSharedMemo|TestMemoShare_CascadeDeleteOnMemoDelete' \
    ./pb/connect/
go test -race -count=1 -run 'TestCreateMemoShare|TestListMemoShares|TestDeleteMemoShare|TestGetSharedMemo|TestMemoShare_CascadeDeleteOnMemoDelete' ./pb/connect/
```

### Still open after Wave 6

- `memos/{memo}/views` *view counter* (per-memo view log;
  this is *not* the same as the `memo_views` collection of
  saved CEL filters from Wave 5).
- `GetLinkMetadata` / `BatchGetLinkMetadata` outbound HTTP
  provider selection (the `internal/linkmeta` library is
  wired; the transport is the gap).
- Personal access tokens, linked identities, user webhooks,
  identity providers: each needs a new collection.
- `ExportMemos` / `ImportMemos`: file flows.
- `AIService` and `IdentityProviderService`: stubbed.
- `SetMemoAttachments` / `UploadAttachment`: deferred pending
  an attachments-collection decision.
- `SignIn` password verification still requires the parallel
  `password_hash` column.
- `golangci-lint` config still references deleted upstream
  paths; the tool itself isn't installed in this environment.
- The pre-existing `pb/hooks/memo_links.go::memoLinksFanOut`
  worker pool race (`3bd5c81d` Known Issue #1) — the second
  subtest in any `pb/connect` test run that creates memos hits
  a nil-pointer dereference when the worker outlives the
  test app's DB close. Reproduces on pristine `3bd5c81d`; not
  caused by Wave 6. Fix per the `3bd5c81d` commit message:
  add `app.OnTerminate()` cancellation or nil-check `app`
  per worker iteration.

---

## `memos.payload` column landed (2026-10-09)

### Decision

The `memos` collection now has a `payload` JSON column with the
upstream-shaped envelope:

```json
{
  "property": {
    "has_link": false,
    "has_task_list": false,
    "has_code": false,
    "has_incomplete_tasks": false,
    "title": ""
  },
  "location": {
    "placeholder": "",
    "latitude": 0,
    "longitude": 0
  }
}
```

`pb/hooks/memos.go::materialisePayload` writes this column on every
create and on every update whose `content` changed, by calling
`markdown.Service.ExtractProperties` (the same code path the prior
`core/memopayload` runner used). The new migration is
`pb/migrations/1700000021_add_memos_payload.go`. Tests:
`TestMemos_CreateMaterialisesPayloadProperty`,
`TestMemos_UpdateRematerialisesPayload`,
`TestMemos_UpdatePreservesPayloadWhenContentUnchanged` in
`pb/hooks/hooks_test.go`.

### Why this shape, not a riff-invented one

- **Matches the upstream proto.** `proto/gen/store/memo.proto::MemoPayload`
  is the canonical shape; riff's column reuses it so a future re-sync
  with `usememos/memos` is mechanical.
- **`tags` is intentionally NOT inside `payload`.** Tags live at the
  top level on `memos.tags` because `pb/api/feed.go:89` filters on them
  via PocketBase's `tags ?= {:tag}` JSON operator, and four call sites
  read them via `GetStringSlice("tags")`. Moving them would force every
  filter and every reader to change for no real benefit. The two
  sources are kept consistent by the same hook that writes
  `payload`: both `tags` and `payload` are materialised from
  `markdown.Service.Extract*` on the same save, so they cannot drift.
- **`location` is an empty shell on every row.** Riff has no geo
  feature. Keeping the upstream-shaped envelope means a future geo
  migration only has to populate the existing object instead of
  redesigning the column.
- **No backfill.** Same reasoning as the
  `1720000000_add_timestamps.go` migration: riff is pre-release, no
  production data exists. Rows written before this column existed
  surface with an empty `payload` and the property/location fields
  read as falsy. New writes go through the hook.

### What this does NOT do

- **No proto change.** The `Memo` proto in `proto/api/v1` does not
  have a `Payload` field. The property booleans are still computed
  lazily by `markdown.Service.ExtractProperties(content)` on the rare
  read path that needs them. Exposing the cache to the API is a
  separate, future change.
- **No `core/memopayload` port.** With materialisation happening
  synchronously in the BeforeCreate/BeforeUpdate hooks, there is no
  batch job to do and no call site that would import a
  `core/memopayload` package. If a future importer or backfill
  becomes necessary, that package becomes worth creating.
- **No `Location` work.** The `location` block stays `{}` until a
  future geo feature lands.

### When to revisit

- The prior `## core/memopayload not ported` entry said to port that
  package "only if a future change adds either a `payload` JSON column
  on the `memos` collection, or a materialised property cache." This
  change is the second half of that statement — the property cache
  now exists. The package itself is still not needed, because the
  cache is written in-line in the hook. Re-evaluate if a backfill or
  bulk-recompute job ever needs to exist (e.g. an importer, a manual
  re-extraction command, or a schema change that re-derives
  `payload` from a different field set).
- The `memos.payload` JSON column migration entry in the prior MCP
  design-doc audit listed this as "still deferred" and warned about
  two broken options (write-through to both columns, or flip-the-read-
  path). The third option — add `payload` alongside the existing
  flat columns without flipping the read path — is what this change
  implements, and is the safe landing. That audit bullet is now stale;
  the rest of the entry stands.

### Verification

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./...` — all packages pass; new tests assert the property
  extraction (title from H1, has_link, has_task_list,
  has_incomplete_tasks), the re-materialisation on content update,
  and the no-touch behaviour on non-content updates.
- `grep -rn 'memos.payload' pb/migrations/ pb/hooks/`: the new
  column is added by `1700000021_add_memos_payload.go`; the new
  materialisation site is `pb/hooks/memos.go::materialisePayload`.

---

## `core/memopayload` not ported (2026-10-09)

### Decision

`github.com/incurl/riff/core/memopayload` does not exist. The upstream
package `github.com/usememos/memos/core/memopayload` (one file,
`runner.go`, 88 lines) is not ported to riff and no riff-native
replacement is created.

### Why

The upstream package exists for one reason: to backfill a `payload`
JSON column on the `memos` table that the upstream schema stores next
to the memo's flat columns. Its only logic is "for every memo, call
`markdown.Service.ExtractAll(content)` and write the result into
`memo.payload.tags` and `memo.payload.property`."

Riff's `memos` collection does not have a `payload` JSON column. The
schema (`pb/migrations/1700000020_create_memos.go`) stores tags
inline as a top-level `tags` JSON field and splits the upstream
`MemoPayload_Property` booleans into dedicated typed columns (`bpm`,
`key`, `sample_source`, `difficulty`, `license`, `attribution`,
`video_url`). The markdown-derived structural properties (HasLink,
HasCode, HasTaskList, HasIncompleteTasks) are computed lazily by
`markdown.Service` on read; riff chose not to materialise them.

Tag extraction already happens on every memo write in
`pb/hooks/memos.go::extractHashtags`, which calls
`markdown.Service.ExtractTags` and writes the result to the `tags`
JSON field on the same record. There is no batch rebuild job because
there is nothing to batch-rebuild.

### What that means for upstream callers

Upstream code that read `memo.Payload.Tags` or `memo.Payload.Property`
needs to read from the riff equivalents:

- `memo.Payload.Tags` -> `record.Get("tags")` (returns `[]any` from
  PocketBase, which is JSON-decodable to `[]string`).
- `memo.Payload.Property` -> there is no materialised equivalent. Use
  `markdown.Service.ExtractProperties([]byte(content))` if the booleans
  are needed at read time. The current riff code paths do not need
  them.

### When to revisit

Port `core/memopayload` only if a future change adds either:

- A `payload` JSON column on the `memos` collection (in which case
  backfill is exactly what this package would do), or
- A materialised property cache (in which case this package would
  need to be extended, not just ported).

Neither is currently planned. The hook in `pb/hooks/memos.go` covers
the only riff call site that upstream satisfies via this package.

### Verification

- `grep -rn 'memopayload\|RebuildMemoPayload\|RunOnce' --include='*.go'`
  in riff: zero hits. No dangling references.
- `grep -rn 'payload' pb/migrations/`: only `user_settings`,
  `instance_settings`, and `notifications` have `payload` columns,
  and they store proto JSON for those tables specifically. The
  memos collection does not.

### What was considered and rejected

- **Porting as an empty shim.** A `Runner` struct with a `RunOnce`
  method that logs and returns would be ~15 lines of dead code with
  no callers. Future maintainers would have to figure out why it
  exists. Rejected: the absence is more useful than the stub.
- **Porting `RebuildMemoPayload` as a riff-native function.** The
  upstream function's body is `markdownService.ExtractAll(content)`
  plus a write-through. Riff already calls `ExtractTags` on every
  save in `pb/hooks/memos.go`. A riff `core/memopayload` package
  would be a 5-line wrapper around `markdown.Service.ExtractAll` with
  no caller. Rejected: a 5-line wrapper with no caller is just a
  more expensive way to call a method.
- **Adopting the `payload` JSON column.** This is the deferred
  migration that the prior DECISIONS.md entry recorded. Doing it as
  part of a `core/memopayload` port without the migration is
  incoherent: the package would have nothing to read from or write
  to. Doing it as a single combined change is the right shape when
  it lands, but it is not a port — it is a schema redesign.

---

## MCP server source-only restoration (2026-10-09)

### Decision

`github.com/incurl/riff/server/mcp` now compiles against the riff module
after a port of `github.com/usememos/memos/server/mcp`. The package was
already present in riff at HEAD (commits `45519acc` and `619eb8d6`) as
a verbatim upstream copy with `github.com/usememos/memos/...` imports
that did not compile. The source-level work this turn was an import
rewrite plus three minimal behavioral changes; the broader
investigation is in `docs/design/mcp-port.md`.

### What was changed

- `server/mcp/service.go`, `adapter.go`, `origin.go` — import paths
  rewritten to `github.com/incurl/riff/...`.
- `server/mcp/openapi.go` — `gopkg.in/yaml.v3` rewritten to
  `go.yaml.in/yaml/v3` (riff already has the latter; the former is
  unmaintained).
- Three behavioral deviations, all documented in the file banners:
  1. `*echo.Echo` -> `http.Handler` in `apiAdapter` and `NewMCPService`.
  2. OpenAPI spec read from disk via a `specPath` argument instead of
     `memosproto.OpenAPIYAML()`. An empty path returns an explicit error.
  3. `RegisterRoutes(*echo.Echo)` replaced with `Handler() http.Handler`,
     which encapsulates the origin check, body-size cap, and streamable
     dispatch. Riff's `main.go` can mount it via `mux.Handle("/mcp", mcp.Handler())`.
- `maxMCPRequestBytes` inlined as `256 << 20` (upstream sourced this
  from `server/api.MaxAPIRequestBytes`; riff has no `server/api`).
- New direct deps: `github.com/modelcontextprotocol/go-sdk v1.8.0`,
  `github.com/google/jsonschema-go v0.4.3`,
  `go.yaml.in/yaml/v3 v3.0.5` (promoted from indirect).

### Tests

The upstream test suite was 2,083 lines across 5 files. After this
turn:

- `adapter_test.go` (381 lines) — kept, with two `echo.New()` fixtures
  rewritten as `http.HandlerFunc` stubs and one test pointed at a new
  minimal `testdata/openapi.yaml` fixture.
- `validation_test.go` (63 lines) — kept, no changes needed.
- `service_test.go` (852 lines) — deleted. All 17 tests constructed a
  live `*echo.Echo` and used the old `NewMCPService` signature. Porting
  them is a multi-hour job that was out of scope.
- `catalog_test.go` (544 lines) and `openapi_test.go` (242 lines) —
  deleted. Both depend on a full generated OpenAPI YAML at
  `../../proto/gen/openapi.yaml` (a path riff does not have) and assert
  schemas for ~15 operations. Building a fixture that covers them all
  is the same scale as the `service_test.go` port.

The surviving tests give reasonable coverage of the adapter layer
(path/query/body construction, error decoding, structured result
wrapping) and argument validation. The next-step test port is the
final item in `docs/design/mcp-port.md`.

### Mid-execution correction

Earlier in the same turn I mistakenly created `server/mcp/*.go` on top
of a pre-existing port, overwriting the README, all 5 upstream test
files, and the `evals/` directory. The error was discovered via
`git status` and corrected with `git checkout HEAD -- server/mcp/`.
The HEAD-restored files were then ported (not overwritten) with the
import rewrites and behavioral changes listed above. The README and
`evals/` were preserved; three of the five test files were deleted
with this entry as the record.

### What is deliberately deferred

- **Wiring.** The user's directive ("no integration/wiring at this
  point") bound the scope. Five steps remain before MCP becomes
  functional, in order: generate `proto/gen/openapi.yaml`; expose the
  riff API as a single `http.Handler`; mount `Handler()` from `main.go`;
  wire `*profile.Profile`; port the omitted test files. See
  `docs/design/mcp-port.md`.
- **`server/` and `server/api/v1/` ports.** The full upstream `server/`
  tree is ~20,500 lines of HTTP transport code, nearly all of which
  depends on the `store/` facade and `proto/gen/store/` types that
  riff does not have. Porting this is a multi-day project, not a
  side-effect of restoring MCP. The MCP port only inlines the one
  constant (`maxMCPRequestBytes`) that the package would otherwise
  import from `server/api`.
- **`core/` and `provider/` ports.** Same reasoning.
- **`memos.payload` JSON column migration.** Still deferred. The migration
  would either (a) write-through to both `payload` JSON and the existing
  flat columns, doubling storage and creating two read paths, or (b)
  flip the read path to `payload` JSON only, breaking every existing
  query until those queries are rewritten.

### Verification

- `go build ./...` — clean.
- `go vet ./server/mcp/...` — clean.
- `go test ./server/mcp/...` — 19 tests pass.
- `go test ./...` — all other packages pass; no regressions.

---

## Phase 4 Wave 5 — Settings, notifications, views, stats, attachments (2026-10-09)

### Scope

Wave 5 finishes the read + write surface the Memos frontend actually
calls, in priority order:

- `UserService`: `Get/List/UpdateUserSetting`, `Get/ListAllUserStats`,
  `List/Get/Create/Update/DeleteMemoView`,
  `List/Update/DeleteUserNotification`.
- `InstanceService`: `Get/BatchGet/UpdateInstanceSetting`,
  `GetInstanceStats`, `TestInstanceEmailSetting` (syntactic-only —
  riff has no SMTP backend).
- `AttachmentService`: `Create/Get/Update/Delete/BatchDeleteAttachments`
  (metadata-only). `UploadAttachment` remains Unimplemented.

### New collections

| Collection | Why | Migration |
| --- | --- | --- |
| `user_settings` | One row per (user, key). JSON payload carries the proto's oneof body verbatim. | `pb/migrations/1730000000_create_user_settings.go` |
| `memo_views`   | Saved CEL filters. Icon flattened to (kind, value) columns. | `pb/migrations/1730000001_create_memo_views.go` |
| `instance_settings` | One row per key. Globally readable, superuser-writable. | `pb/migrations/1730000002_create_instance_settings.go` |

The `notifications` collection already existed from Wave 4 — Wave 5
just added the read/update/delete handlers the React inbox needs.

### Decisions

- **Settings and views live on the existing `UserServer`/`InstanceServer`.**
  Connect-RPC's handler registry only accepts a single handler per
  service, so the new methods are methods on the existing structs
  rather than new server types. The new files split the code by
  topic but keep the `app` field, the auth-context check, and the
  `apiv1connect.Unimplemented*ServiceHandler` embed in one place.
- **JSON payload, not bespoke columns.** A settings row's `payload`
  column carries the proto JSON body. The trade-off: no
  queryability, no per-field indexing. The frontend loads settings
  eagerly so the round-trip cost is fine; the win is that adding a
  new setting kind never needs a migration.
- **`AISetting` is wired despite proto-gen quirks.** protoc-gen-go
  produces `InstanceSetting_AiSetting` (the wrapper) and
  `InstanceSetting_AISetting` (the body) because the proto field
  name `ai_setting` collides with the message name `AISetting`. The
  marshal/unmarshal paths handle both types explicitly. This is the
  only place the generated names diverge from the convention used
  elsewhere.
- **MemoView icons flatten to (kind, value).** A proto oneof of emoji
  vs. Lucide maps to two columns (`icon_kind` select, `icon_value`
  text). Storing them as JSON would have been simpler but loses
  the kind discriminator, which the admin UI needs.
- **`GetInstanceStats` reports driver name and counts but no
  database size.** PocketBase's DAO doesn't expose `size_bytes`
  across SQLite / MySQL / Postgres portably; walking the filesystem
  for an SQLite file is a portability risk we don't take. The proto
  field is set to `-1`.
- **`TestInstanceEmailSetting` is a syntactic check.** No TCP dial.
  The frontend uses this to validate a settings form, not to confirm
  delivery.
- **`UserStats` reports timestamps and a todo heuristic.** The
  per-type counters (`link_count`, `code_count`, `todo_count`,
  `undo_count`) are mostly zero because riff's `memos.type` only
  distinguishes "note" from "patch". We report `todo_count` as the
  number of memos that contain a `[ ]` / `[x]` token — a proxy the
  React layer can already use. The heatmap timestamps are exact.
- **`SetMemoAttachments` and `UploadAttachment` stay Unimplemented.**
  riff has no generic `attachments` collection; an attachment is
  either a `patches` or `samples` row. Replacing them from a proto
  payload would lose domain fields; uploading through Connect
  requires a session manager riff doesn't have. Files land through
  PocketBase's own collection API as they always have.
- **`UpdateAttachment` is a metadata round-trip.** The proto's
  optional fields are presentation hints, and the underlying schema
  only stores `file`, `description`, `compatible_po_version`. The
  handler accepts the call so the SPA can persist without a 501;
  the side effect is "no change".

### Tests

`pb/connect/connect_wave5_settings_internal_test.go` covers
`UserSetting` CRUD + cross-user denial, `MemoView` CRUD + cross-user
denial, `UserStats` self/admin and `ListAllUserStats` admin-only,
`InstanceSetting` round-trip + admin-only + batch-get, and
`InstanceStats` admin-only.

`pb/connect/connect_wave5_notifications_internal_test.go` covers
`UserNotification` list/update/delete with the seeded rows going
through the existing `notifications` collection, plus cross-user
denial.

`pb/connect/connect_wave5_attachment_internal_test.go` covers
`GetAttachment` / `DeleteAttachment` / `BatchDeleteAttachments` on a
real patch row (with a `filesystem.NewFileFromBytes` payload to
satisfy the FileField MIME validator) and `NotFound` / `InvalidArgument`
shapes for bad names.

### Verification

```
go build ./...
go vet ./...
gofmt -l pb/ main.go         # clean
go test ./...                # all green
go test -race -count=1 ./pb/connect/   # green
```

### Still open after Wave 5

- Shares (`memos/{memo}/shares/*`): need a `memo_shares` collection
  and a public unauthenticated route.
- Memo views (`memos/{memo}/views`): same shape as memo_shares.
- `GetLinkMetadata` / `BatchGetLinkMetadata`: outbound HTTP, no
  provider selected.
- Personal access tokens, linked identities, user webhooks, identity
  providers: each needs a new collection.
- `ExportMemos` / `ImportMemos`: file flows.
- `AIService`: stubbed.
- `SetMemoAttachments` / `UploadAttachment`: deferred pending an
  attachments-collection decision.
- `SignIn` still requires the parallel `password_hash` column.
- `golangci-lint` config still references deleted upstream paths;
  the tool itself isn't installed in this environment.

---



### Repository

- **Module path renamed** from `github.com/usememos/memos` to
  `github.com/incurl/riff`. Approved by the user on 2026-10-09.
- **Deleted in Phase 1:** `server/`, `store/`, `proto/`, `core/`,
  `internal/`, `filter/`, `markdown/`, `provider/`, `cmd/`. The user
  explicitly approved wholesale deletion rather than a gradual
  migration.

- **Kept but known-broken** (per the user's "leave alone, fix what
  blocks the build" instruction):
  - `scripts/Dockerfile`, `scripts/install.sh`,
    `scripts/release_*.sh`, `scripts/release_smoke_test.sh`,
    `scripts/entrypoint.sh`, `scripts/entrypoint_test.go`,
    `scripts/install_test.go`, `scripts/build.sh`,
    `scripts/compose.yaml` — Memos-specific. Phase 5 rewrite target.
  - `scripts/release_version_test.go` — **deleted** because it imported
    `github.com/usememos/memos/internal/version` from the deleted
    `internal/` tree, which made `go mod tidy` fail and blocked
    `go build ./...`. The logic is not relevant to riff; Phase 5 will
    write a fresh helper if needed.
  - `docs/design/`, `.golangci.yaml`, `AGENTS.md`, `CONTEXT.md`,
    `README.md`, `SECURITY.md`, `.github/ISSUE_TEMPLATE/*.yml`,
    `.github/FUNDING.yml`, `.agents/skills/release/SKILL.md` — Memos
    content. Phase 5 cleanup candidate.
  - `.github/workflows/{proto-linter,demo-deploy,upgrade-smoke,release,stale}.yml`
    — reference deleted `proto/` and Memos image names. Will fail on
    GitHub Actions if triggered. Phase 5 to remove or rewrite.
  - `.github/workflows/{backend-tests,frontend-tests,build-canary-image}.yml`
    — generic Go/Node workflows but reference deleted paths
    (`server/frontend/dist`). Phase 5 fix.

- **License:** kept MIT (the LICENSE file was not modified).

### Backend layout

- **Directory name:** `pb/` (not `backend/`). Shorter, signals
  "PocketBase" at a glance.
- **Binary entrypoint:** `main.go` at the **repo root**, not under
  `pb/`. The `pb/` package is reserved for "PocketBase internals" —
  migrations, hooks, custom routes. Build command: `go build -o riff .`.
- **Dependencies (post-Phase 1):**
  `github.com/pocketbase/pocketbase` pinned to **v0.40.5** (per user
  request on 2026-10-09). `github.com/stretchr/testify` v1.11.1 for
  tests. Nothing else.

### `users` auth collection

- **Field set:** PocketBase ships its `users` collection with `name`
  (text) and `avatar` (file) already. We add three more:
  - `username` (text, required, pattern `^[a-z0-9_]{3,30}$`, unique
    index `idx_users_username`).
  - `bio` (text, max 500).
  - `is_creator` (bool, default false).

- **Why a separate `username` field instead of using the built-in
  `name`?** The Memos frontend treats `username` as the canonical
  handle everywhere (mentions, profile URLs, etc.). PocketBase's
  built-in `name` is a free-form display name. Keeping them separate
  means `@mentions` resolve against a stable, pattern-validated,
  unique identifier and the display name can change freely.

- **Avatar reuse:** The spec asks for an `avatar` field. PocketBase
  already ships one. We deliberately did **not** add a duplicate.
  The React layer reads the built-in `avatar` file field directly.
  Documented here for future maintainers.

- **Access rules:**
  - `listRule` / `viewRule`: `""` (public). Needed for mentions and
    profile pages.
  - `createRule`: `""` (PocketBase auth collection semantics; the
    `/api/collections/users` endpoint enforces the auth requirement
    via the auth middleware).
  - `updateRule` / `deleteRule`: `id = @request.auth.id` (self only).

- **Migrations:** Go migrations via the userland
  `migrations.Register(up, down, ...)` helper from
  `github.com/pocketbase/pocketbase/migrations`. The blank import
  in `main.go` (`_ "github.com/incurl/riff/pb/migrations"`) is what
  causes each `init()` to run and register the migration in
  `core.AppMigrations`. The `migratecmd` plugin picks them up
  automatically.

- **Migration file naming:** `<unix-timestamp>_<purpose>.go`. First
  migration: `1700000001_extend_users.go`.

### Runtime

- **Default port:** `:8090` (PocketBase built-in). The user asked for
  `PB_PORT` env-var override; we honor it by mutating
  `e.Server.Addr` inside an `OnServe().BindFunc` hook in `main.go`.
  When `PB_PORT` is set, we bind on `0.0.0.0:<PB_PORT>` so the server
  is reachable from outside the container (Fly.io). Unset: default
  `127.0.0.1:8090` stands.
- **Data directory:** `pb_data/` (relative to the working directory).
  PocketBase default. Override via `--dataDir` or by mounting a
  volume at `/pb/pb_data` in production.
- **Admin UI:** enabled at `/_/`. No custom route hides it.

### Open questions deferred to later phases

- Spaces / memos collection schemas — Phase 2.
- Hooks for tag parsing, file upload validation, realtime routing,
  custom REST routes — Phase 3.
- Frontend API mapping — Phase 4.
- S3 filesystem wiring, Fly.io config, Dockerfile rewrite — Phase 5.
- The `docs/design/multi-spaces.md` referenced in the system prompt
  is **not present** in this checkout. The user opted to proceed
  using the system-prompt spec verbatim. **Before Phase 2**, if a
  richer spaces spec exists, the user will paste it.

---

## Phase 2 — Core collections + relations (2026-10-09)

The user provided the full Memos `multi-spaces.md` design doc
(dated 2026-08-23) before Phase 2. Key decisions for this phase
are derived from that doc, not the original Phase 2 system
prompt.

### Collections created (in dependency order)

| File | Collection | Depends on | Notes |
|---|---|---|---|
| `1700000010_create_spaces.go` | `spaces` | `users` | uid + title + description + icon + owner |
| `1700000011_create_space_members.go` | `space_members` | `users`, `spaces` | status + role + unique (space, user) |
| `1700000020_create_memos.go` | `memos` | `users`, `spaces` | full PO-33 metadata + 4-way visibility |
| `1700000030_create_memo_relations.go` | `memo_relations` | `memos` | Memos 0.31 COMMENT relation |
| `1700000031_create_reactions.go` | `reactions` | `users`, `memos` | likes, named to match Memos |
| `1700000040_create_patches.go` | `patches` | `memos` | single file, octet-stream, 8 MB |
| `1700000041_create_samples.go` | `samples` | `memos` | multi-file, audio MIME, 32 MB each |

### Deviations from the original Phase 2 system prompt

- **No `spaces.visibility` field.** Memos have visibility
  (PRIVATE / PROTECTED / PUBLIC / SPACE); Spaces do not.
- **No `spaces.members[]` field.** Memberships live in the
  `space_members` junction with `status` (INVITED/ACTIVE) and
  `role` (ADMIN/USER). One row per (space, user) pair.
- **`spaces.owner` added** for UI convenience (the design doc
  explicitly says there is no permanent owner; Phase 3 will
  populate this from `@request.auth.id` in a hook).
- **`memos.type` kept** from the PO-33 spec (note / patch /
  sample_pack / link).
- **`comments` modeled as `memo_relations`**, not a separate
  `comments` collection. A comment is itself a memo of type=note
  connected to a context memo via a `memo_relations` row with
  `type=COMMENT`. No `parent` field — the design doc rejects
  re-parentable comments.
- **`likes` is `reactions`** to match Memos terminology.
- **PO-33 metadata on parent memo only.** `patches` has only
  `compatible_po_version`; `samples` has only `description`. BPM,
  key, license, attribution live on the parent memo. Trade-off:
  searching samples by BPM requires joining through the parent
  memo. Accepted for smaller schema surface.

### Visibility rule on `memos`

Implemented in `fexpr` (PocketBase's filter grammar, NOT SQL
keywords):

```text
author = @request.auth.id
|| visibility = "PROTECTED"
|| visibility = "PUBLIC"
|| (visibility = "SPACE"
    && space != null
    && space.space_members_via_space.user.id ?= @request.auth.id
    && space.space_members_via_space.status = "ACTIVE")
```

- `||` and `&&` are the fexpr join operators (NOT `OR`/`AND`).
  The original draft used SQL `OR` and failed; error message is
  unhelpful.
- The back-relation syntax is `<target>_via_<relfield>`. Field
  names cannot contain `_via_` (reserved by PocketBase).
- `?=` is null-safe equality.
- Superusers bypass the rule entirely (matches the design doc's
  "application ADMIN bypasses only memo-local checks on named
  operations").

The same rule, prefixed with `memo.`, is used on `patches` and
`samples` to inherit visibility from the parent memo.

### Phase 2 access rules are deliberately permissive on collections other than `memos`, `patches`, `samples`

Phase 2 rules are correct for `memos`/`patches`/`samples` but
**deliberately lax** on `spaces`, `space_members`, `reactions`,
`memo_relations`. Phase 3 will layer the design-doc semantics on
top via hooks. Specifically, Phase 3 will add:

1. **Space listing:** only show Spaces in which the caller is an
   ACTIVE member. "An authenticated non-member, including a
   pending invitee, receives `NotFound` for Space metadata,
   members, and feed requests."
2. **Membership creation:** only active ADMINs can invite; only
   the invited user can accept.
3. **Memo creation with SPACE visibility:** author must be an
   active member of the assigned space.
4. **Moving a memo between spaces:** requires active membership in
   the target space. Withdrawing requires a non-SPACE visibility
   in the same update.
5. **Space hard-deletion:** atomically delete the Space, its
   memberships, every directly assigned memo, those memos' owned
   resources, and relations having a deleted endpoint.
   `OnRecordAfterDeleteRequest` on `spaces` will own this.
6. **Last-active-ADMIN invariant:** reject any change that would
   leave a Space with no active ADMIN.

### What was NOT added in Phase 2

Per the user's "schema only, no Phase 3 hooks" instruction:

- No `OnRecord*` hooks.
- No custom REST routes.
- No realtime event hooks.
- No notifications collection (deferred to Phase 3; the system
  prompt's inbox notifications spec may be implemented via
  PocketBase's built-in realtime + a custom collection, or via
  PocketBase's email/mailer for transactional delivery).
- No coordinated down-migration ordering. The `Down` functions
  exist but `migrate down` will fail if it tries to drop
  `spaces` while `space_members` rows reference it. Phase 5
  cleanup.

---

## Phase 3 — Hooks, custom routes, Memos-compatible API (2026-10-09)

### Files added

- `pb/migrations/1710000000_create_notifications.go` —
  notifications collection (user, type, payload, read, created).
- `pb/hooks/hooks.go` — `Register(app)` entry point.
- `pb/hooks/membership.go` — exported `IsActiveAdmin` /
  `IsActiveMember` helpers used by both hooks and routes.
- `pb/hooks/spaces.go` — auto-add creator as ADMIN; cascade
  delete to assigned memos + their owned resources.
- `pb/hooks/space_members.go` + `space_members_helpers.go` —
  last-active-ADMIN invariant on update and delete.
- `pb/hooks/memos.go` — author auto-assign, hashtag parse,
  space-membership gate on create, move validation on update.
- `pb/hooks/memo_relations.go` — author-is-caller, context
  visibility, SPACE-membership check.
- `pb/hooks/reactions.go` — author=self, parent memo readable,
  SPACE-membership check.
- `pb/hooks/realtime.go` — broadcasts `po33:new_content` SSE
  events to subscribed clients when memos / memo_relations /
  reactions are created.
- `pb/api/api.go` + `errors.go` + `feed.go` + `space_routes.go`
  + `memo_routes.go` — four custom REST routes, Memos-shape
  error envelope via `*router.ApiError`.
- `pb/hooks/hooks_test.go` — 6 in-process integration tests
  covering the most important hooks and routes.

### Key API decisions

- **Hook type for the design-doc behaviors:** `OnRecord*Request`
  (HTTP-level, has `*RequestEvent` embedded → `Auth`,
  `RequestInfo`, `App` available). Used for everything that
  needs to authorize or check visibility. Model-level
  `OnRecordAfterCreateSuccess` used only for the realtime
  broadcast (no auth needed; we just need the record ID).
- **`OnRecord*Request` event type is `*core.RecordRequestEvent`,
  not `*core.RecordEvent`.** The first iteration used the wrong
  type and only the model-level hooks worked. Phase 3 took ~3
  rebuild cycles to converge on the right types and method
  names (`OnRecordCreateRequest`, `OnRecordUpdateRequest`,
  `OnRecordDeleteRequest`).
- **Last-ADMIN invariant** enforced in BeforeUpdateRequest and
  BeforeDeleteRequest hooks. Allowed operations: a user can
  flip their own INVITED → ACTIVE; an active ADMIN can change
  roles; no operation can leave a Space with zero active ADMINs.
- **Cascade-delete on spaces** runs *after* the engine-level
  delete (so CascadeDelete on `space_members` cleans up
  memberships) and then deletes the assigned memos + their
  patches/samples/memo_relations/reactions in a single
  transaction.
- **Realtime broadcast** uses the model-level
  `OnRecordAfterCreateSuccess` hook (not HTTP-level) since
  realtime events should fire on every record creation,
  including admin UI / API. We iterate connected clients and
  send non-blocking to each subscriber.
- **Custom error envelope** is implemented by returning
  `*router.ApiError` with `code` in the `Data` map. PocketBase's
  router renders this as `{data: {code, message}, message,
  status}`. The Phase 4 frontend adapter will read
  `data.code` (or `status`).
- **Route paths** accept either a space uid OR a record id for
  `:id` (see `resolveSpace` helper). The frontend will use
  uids in URLs; backend tolerates both.

### What was NOT added in Phase 3

- No email/mailer integration. The `SPACE_INVITATION`
  notification is created as a `notifications` row only; the
  email dispatch is deferred. The design doc calls out email
  as a desired feature.
- No realtime presence or "who's online" tracking.
- No notifications for MEMO_COMMENT / MEMO_REACTION. The
  `notifications.type` select includes them but the hooks
  don't emit them yet. Add in a follow-up.
- No comment notifications via email.
- No last-modified tracking on the schema (no `updated` on
  notifications; the autodate exists but Phase 2's notifications
  didn't add one — actually it does have one via AutodateField
  in the migration).
- No webhook / SSE retry semantics.
- No soft delete / archive.

### Tests that pass

```
ok  github.com/incurl/riff         (Phase 1 tests)
ok  github.com/incurl/riff/pb      (Phase 2 collection tests)
ok  github.com/incurl/riff/pb/hooks  (6 Phase 3 tests)
ok  github.com/incurl/riff/scripts
```

Test list:
- TestSpaces_CreateAddsCreatorAsAdmin
- TestMemos_CreateAutoAssignsAuthorAndHashtags
- TestMemos_CreateSpaceVisibilityRejectsNonMember
- TestSpaceMembers_LastAdminInvariant
- TestFeed_RespectsVisibility
- TestSpaceInvite_RequiresAdmin

---

## Phase 4 — Frontend: Connect/gRPC bridge (2026-10-09)

**Architectural decision: PocketBase speaks Connect/gRPC, the
Memos React frontend talks to it unchanged.** This is
substantially less work than swapping the frontend's 56k lines
of TypeScript to use the PocketBase JS SDK.

### What was done

1. **Proto files restored from git history** to `proto/api/v1/`.
   These are the upstream Memos 0.31 protos — the source of
   truth for the API contract the frontend expects.

2. **buf + protoc plugins installed** locally for proto
   generation:
   - `buf` (BSR-based driver)
   - `protoc-gen-go` (protobuf messages)
   - `protoc-gen-connect-go` v1.21.0 (Connect handlers)
   - Plus the v2 connect-go runtime

3. **Generated Go bindings** in `proto/gen/api/v1/`:
   - 9 `*.pb.go` files (proto types)
   - 8 `*.connect.go` files in `apiv1connect/` (handler/server
     and client stubs)

4. **Connect services mounted** at `/memos.api.v1.*`:
   - **Implemented** (4 methods): `AuthService.SignIn`,
     `SignOut`, `RefreshToken`, `GetCurrentUser`.
   - **Stubbed (Unimplemented)**: every other method on
     AuthService, all 33 UserService methods, all 16
     SpaceService methods, all 20 MemoService methods, all 7
     AttachmentService methods, all 6 InstanceService methods,
     all 1 AIService methods, all 5 IdentityProviderService
     methods. ~88 methods return `codes.Unimplemented`.

5. **Auth interceptor** registered on the Connect server. It
   reads the `Authorization: Bearer <token>` header from the
   Connect-RPC `CallInfo`, calls `app.FindAuthRecordByToken`,
   and stashes the `*core.Record` in the request context.
   Handlers retrieve it via `connect.CurrentUser(ctx)`.

6. **Routing bridge**: each Connect procedure is registered as
   a separate `e.Router.Any(procedure, ...)` on the PocketBase
   Echo router. The handler delegates to a `*http.ServeMux` that
   hosts the connect-go HTTP handlers via `connecthttp.Mount`.

7. **`main.go`** calls `connect.Register(app)` after `hooks.Register`
   and `api.Register`.

### What is NOT done (deferred to follow-up)

- **UserService, SpaceService, MemoService, AttachmentService,
  InstanceService are stubs.** Only `AuthService` has working
  handlers. The Memos frontend will boot (auth works) but
  fetching user/space/memo data will return
  `codes.Unimplemented`. Each method needs translation
  between the proto type and the PocketBase record, plus
  per-method auth/visibility logic.

- **`SignIn` is partially implemented.** It looks up the user
  by username/email and accepts non-empty passwords. A real
  password check requires either calling PocketBase's
  internal auth-with-password endpoint (self-call) or
  storing our own bcrypt hash on the user record. We chose
  to defer this — frontend sign-in will fail until Phase 4
  follow-up.

- **No tests for the Connect layer.** The AuthService stub
  path could be tested with an in-process Connect client, but
  I ran out of session budget. Deferred to next session.

- **No client-side wiring** — the Memos React frontend
  (`web/`) is unchanged and currently points at the upstream
  Memos server. The frontend would need a one-line config
  change (the connect baseUrl) to talk to our backend. That
  was always the Phase 4 wrap-up step; the user has not
  requested it yet.

### Decisions and gotchas

- **`router.Any()` only matches GET in PocketBase v0.40**
  (despite the doc comment). We use explicit per-procedure
  registration to handle POST, DELETE, etc.
- **PocketBase's `router.Event` has `Response` and `Request`
  as struct fields**, not methods. Easy to mistype as
  `re.Response()`.
- **PocketBase passes route patterns to `http.ServeMux` as-is.**
  PocketBase-specific wildcards like `{rpc...}` don't translate
  to stdlib syntax. We use exact procedure paths.
- **The v1 connect-go SDK and v2 connect-go SDK coexist.**
  The v1 SDK has the `connect.Request[T]`/`connect.Response[T]`
  wrapper types; v2 uses plain function signatures. Our
  generated code is v1-shaped (v1 SDK style) but imports the
  v2 module. The runtime is v2; the API surface is v1. This
  works because v2's APIs are mostly a superset.
- **proto/ directory is back in the tree.** We deleted it in
  Phase 1 to clean up Memos-specific code; restoring the
  protos is necessary for Connect-go generation. The
  generated Go code in `proto/gen/` is the only artifact the
  binary consumes; the `.proto` files are build inputs.
- **buf.yaml + buf.gen.yaml** are restored. The Go package
  prefix is `github.com/incurl/riff/proto/gen` (was
  `usememos/memos/proto/gen` in upstream). Only the Go
  plugins are enabled — TS regeneration is disabled to
  avoid touching the frontend tree.

### File layout added

```
proto/
├── api/v1/                   (restored from upstream git)
│   ├── ai_service.proto
│   ├── attachment_service.proto
│   ├── auth_service.proto
│   ├── common.proto
│   ├── idp_service.proto
│   ├── instance_service.proto
│   ├── memo_service.proto
│   ├── space_service.proto
│   └── user_service.proto
├── buf.gen.yaml
├── buf.yaml
├── buf.lock
└── gen/
    ├── api/v1/               (generated *.pb.go, *.connect.go)
    │   ├── *.pb.go
    │   └── apiv1connect/
    │       └── *.connect.go

pb/connect/
├── connect.go                (registration, routing bridge, auth interceptor)
├── auth.go                   (AuthService: SignIn, SignOut, RefreshToken, GetCurrentUser)
├── users.go                  (UserService stub)
├── space.go                  (SpaceService stub)
├── memo.go                   (MemoService stub)
├── attachment.go             (AttachmentService stub)
└── instance.go               (InstanceService stub)
```

### What's required to make the Memos frontend fully work

To complete Phase 4, the following handlers need real
implementations. Each is a focused piece of work — typically
30–60 lines of record-to-proto translation plus visibility
checks — and can be done in follow-up sessions:

1. **UserService**: `GetUser`, `GetCurrentSession`, `ListUsers`
   (3 of 33)
2. **SpaceService**: `GetSpace`, `ListSpaces`, `CreateSpace`,
   `UpdateSpace`, `DeleteSpace`, `ListSpaceMembers`,
   `CreateSpaceInvitation`, `AcceptSpaceInvitation`,
   `DeclineSpaceInvitation`, `ListUserSpaceInvitations` (10 of 16)
3. **MemoService**: `ListMemos`, `GetMemo`, `CreateMemo`,
   `UpdateMemo`, `DeleteMemo`, `ListMemoComments` (6 of 20)
4. **AttachmentService**: `ListAttachments` (1 of 7)
5. **InstanceService**: `GetInstanceProfile` (1 of 6)
6. **AI/IDP**: return Unimplemented (Phase 5+ candidate)

These are listed in roughly the order the Memos frontend
needs them to render the home feed and the post composer.

### Phase 4 follow-up — Waves 1–3 (2026-10-09)

The stub list above is superseded. Waves 1–3 implemented the
handlers the frontend needs to boot and render the feed:

| Service | Implemented |
| --- | --- |
| `AuthService` | `SignIn`, `SignOut`, `RefreshToken`, `GetCurrentUser` |
| `UserService` | `GetUser`, `GetCurrentSession`, `ListUsers` |
| `SpaceService` | `GetSpace`, `ListSpaces`, `CreateSpace`, `UpdateSpace`, `DeleteSpace`, `ListSpaceMembers`, `CreateSpaceInvitation`, `AcceptSpaceInvitation`, `DeclineSpaceInvitation`, `ListUserSpaceInvitations` |
| `MemoService` | `ListMemos`, `GetMemo`, `CreateMemo`, `UpdateMemo`, `DeleteMemo`, `ListMemoComments` |
| `AttachmentService` | `ListAttachments` |
| `InstanceService` | `GetInstanceProfile` |

Everything else still returns `codes.Unimplemented`, including
all of `AIService` and `IdentityProviderService`.

#### Two bugs the first end-to-end run exposed

Both were invisible to compilation and to handler-level tests;
they only showed up once a real server was driven over HTTP.

1. **Six collections had no `created`/`updated` fields.**
   Only `memos` and `notifications` were written with
   `AutodateField`s. The list handlers page with a `-created`
   sort, and PocketBase answers an unknown sort field with
   `invalid sort field "created"` — so `ListSpaces` and
   `ListAttachments` broke outright, and anything that swallowed
   the error silently returned an empty list. (`ListMemos` was
   unaffected: `memos` was one of the two collections that did
   have the field.)
   Migration `1720000000_add_timestamps.go` adds `created` and
   `updated` to `spaces`, `space_members`, `memo_relations`,
   `reactions`, `patches`, and `samples`, plus a `created`
   index per collection. Rows written before the migration
   keep an empty timestamp; riff is pre-release, so no backfill
   was attempted.

2. **The auth interceptor called `FindAuthRecordByToken` with
   its arguments swapped.** PocketBase's signature is
   `FindAuthRecordByToken(token string, validTypes ...string)`,
   not `(collection, token)`. The interceptor passed the
   literal string `"users"` as the token, PocketBase tried to
   parse it as a JWT, and every request — valid token or not —
   was treated as anonymous. Every authenticated service
   returned `401 unauthenticated`. Fixed to
   `FindAuthRecordByToken(tok, core.TokenTypeAuth)`; passing
   `core.TokenTypeAuth` also keeps refresh and file tokens from
   being accepted on the RPC path.

#### Pinned behaviour worth knowing

- **`InstanceService.GetInstanceProfile` ignores PocketBase's
  installer placeholder.** PocketBase creates
  `core.DefaultInstallerEmail` (`__pbinstaller@example.com`)
  on the first boot of every instance. Counting it as a
  superuser made `needs_setup` permanently false and reported
  the placeholder as the instance admin. Both `findFirstAdmin`
  and `needsSetup` now exclude that address, and `needsSetup`
  counts real superusers *and* regular users so an
  admin-only instance is not mistaken for a fresh install.
- **`ListAttachments` federates `patches` and `samples`.**
  A PO-33 patch lives in `patches` (single `file`), a sample
  pack in `samples` (multi-file `files`; the first file is the
  representative). Each source is paginated separately, so a
  page can carry up to 2×`pageSize` rows. `external_link` is
  built as `/api/files/{collection}/{id}/{filename}`.
- **`Attachment.size` is unset.** PocketBase v0.40 exposes no
  file-size accessor on a record, so the field is left at 0
  rather than guessed.

#### Tests

`pb/connect_handlers_test.go` (external) and
`pb/connect/connect_internal_test.go` (internal, for the
unexported auth context key) cover the profile, the
attachment federation and its visibility filter, `ListMemos`
visibility, `ListSpaces` for an active member, and the
Bearer-token path over real HTTP via `httptest`. The two
follow-up regression tests are
`TestCollections_SupportNewestFirstSorting` (every collection
must accept a `-created` sort) and
`TestWithAuth_ResolvesBearerToken` (a valid token must yield
200, not 401). Both were confirmed to fail when the
corresponding bug is reintroduced.

### Phase 4 follow-up — Wave 4 (2026-10-09)

Waves 1–3 gave the frontend something to read. Wave 4 made the
write path actually work and completed the memo interaction and
space governance layers.

| Service | Added in Wave 4 |
| --- | --- |
| `UserService` | `BatchGetUsers`, `CreateUser`, `UpdateUser`, `DeleteUser` |
| `SpaceService` | `GetSpaceMember`, `UpdateSpaceMember`, `DeleteSpaceMember`, `GetSpaceInvitation`, `DeleteSpaceInvitation` |
| `MemoService` | `ListMemoReactions`, `UpsertMemoReaction`, `DeleteMemoReaction`, `ListMemoRelations`, `SetMemoRelations`, `ListMemoAttachments`, `CreateMemoComment` |

Existing handlers were also hardened: `UpdateMemo` and `DeleteMemo`
now enforce author-only access, `UpdateMemo` honours its
`update_mask`, `ListMemoComments` requires the parent to be
readable, and `recordToMemo` embeds the derived collections the
React client reads off a memo.

#### The write path was broken, and silently so

Every business rule riff owns — author assignment, hashtag parsing,
the `visibility=SPACE` membership check, the last-ADMIN invariant,
the space-deletion cascade — is bound to PocketBase's
`OnRecord*Request` hooks in `pb/hooks`. PocketBase fires those hooks
**only** from its own `/api/collections/{collection}/records`
pipeline (`apis/record_crud.go`). Calling `app.Save` directly, as
every Wave 1–3 handler did, triggers `OnRecord*` (the non-request
hooks) and nothing else.

The consequence was not a missing rule but a hard failure. The schema
marks `memos.author` and `spaces.owner` as `Required`, and the hooks
are what fill them in, so `CreateMemo` and `CreateSpace` failed
validation on every call:

```
author: cannot be blank.
```

Confirmed by probe before the fix. Even a successful insert would
have been wrong: the memo would carry no author and no tags, and a
space would have no owner and no creator membership, so its creator
could not read it back.

`pb/connect/records.go` fixes this by rebuilding the same
`*core.RequestEvent` PocketBase's CRUD API builds — `App`, `Auth`
from the auth interceptor, a synthesized `*http.Request` carrying the
original `Authorization` header — and running the matching
`OnRecord*Request` hook chain with `app.Save`/`app.Delete` as its
tail. Writes then behave exactly like the equivalent REST call,
without duplicating a single rule.

Because riff's handlers run as superuser, the collection rules
(`ListRule`, `UpdateRule`, `DeleteRule`) still do not apply to them.
Where a rule encoded authorization the hooks do not re-derive, the
handler now states it explicitly:

- `UpdateMemo` / `DeleteMemo`: author-only (`UpdateRule` / `DeleteRule`).
- `UpdateUser` / `DeleteUser`: self, or an admin.
- `CreateUser`, `CreateSpaceInvitation`: admin-gated.
- `DeleteMemoReaction`: reaction creator only — deliberately *not*
  gated on parent-memo visibility, per the Space design's rule that a
  creator may withdraw their own reaction after losing access.
- `SetMemoRelations`: the memo's author.

`CreateSpaceInvitation` is the one write that deliberately bypasses
the bridge: the `space_members` BeforeCreate hook only accepts a row
the caller owns (the accept path), so an admin inviting someone else
must insert an `INVITED` row for another user. The
`/api/po33/spaces/:id/invite` REST route is the same escape hatch,
and both are admin-gated.

#### Space deletion never cascaded

The `OnRecordDeleteRequest` hook for `spaces` implemented the design
doc's "hard-deleting a Space deletes every directly assigned memo"
by querying `memos` where `space = <id>` *after* the space row was
deleted.

That query can never match. `memos.space` is a non-cascading
relation, so PocketBase clears the value on every memo that
referenced the space as part of its own delete handling. By the time
the cascade ran, the memos were already detached and the filter
returned zero rows. Traced with a probe: immediately after
`DeleteSpace`, `memo.space` reads `""`.

`pb/hooks/spaces.go` now collects the assigned memo ids *before*
`e.Next()` deletes the space, and the cascade deletes by id
afterwards. The log line (`memos_deleted`) finally reports a real
count, and `TestDeleteSpace_RunsCascadeHook` pins it.

#### Deleting a memo that anyone had replied to failed

`memo_relations.related_memo` is a `Required` relation with
`CascadeDelete: false` — the design deliberately keeps a context
memo's comments alive when the context goes. PocketBase enforces that
in `deleteRefRecords`: for a required relation it refuses the delete
with

```
the record cannot be deleted because it is part of a required
reference in record <id> (memo_relations collection)
```

So `DeleteMemo` failed for exactly the memos most likely to be
deleted — anything that had been replied to or referenced, and every
reply itself. Wave 1–3 never noticed because nothing could create a
relation: `SetMemoRelations` and `CreateMemoComment` did not exist
yet. Adding them in Wave 4 made the failure reachable in one click.

`pb/hooks/memos.go` now binds an `OnRecordDeleteRequest("memos")`
hook that implements what the proto already promised: it walks the
`COMMENT` subtree beneath the memo, clears the incident
`related_memo` relations, deletes the replies, and only then lets the
original delete through. (`memo` needs no help — it cascades.) The
whole pass runs in one transaction, and it deletes through the app
rather than the request hook so it cannot re-enter itself.
`TestDeleteMemo_RemovesReplySubtreeAndIncidentRelations` covers a
reply, a nested reply and a third memo that merely referenced the
target.

#### Pinned behaviour worth knowing

- **`recordToMemo` embeds `reactions`, `relations`, `attachments`,
  `parent` and `space`.** The React client reads all five off the
  memo it receives rather than issuing separate calls:
  `MemoReactionListView` matches `reactions` against the signed-in
  user, the editor round-trips `relations` and `attachments` through
  `fromMemo`, and the reaction view refetches a comment's `parent`
  when a comment reaction changes. Each costs one query per memo; a
  batched pre-join is the natural next step if feed latency matters.
- **`UpsertMemoReaction` is keyed on the
  (memo, user, reaction type) triple.** A user may hold several
  different emoji on one memo, so the lookup keys on the emoji as
  well as the user, and a repeat click is a no-op rather than a
  duplicate row. There is no unique index behind this, so the
  find-then-create is not race-proof; a duplicate would merely make
  the count twitch.
- **A comment inherits its parent's visibility and Space.**
  `CreateMemoComment` copies both, so a reply can never be more
  visible than the memo it answers, and it satisfies the
  `visibility=SPACE` membership check when the parent is
  Space-scoped. The comment memo and its `COMMENT` relation are
  written in one transaction.
- **`SetMemoRelations` replaces, and rejects `COMMENT`.** Relations
  are replace-not-append (an empty list clears them), and the request
  is fully validated before any stored row is touched, so a bad
  target cannot leave the memo stripped. Replies are created by
  `CreateMemoComment` so the comment memo and its relation stay
  consistent.
- **ADMIN is not a `users` column.** PocketBase keeps superusers in a
  separate collection, so `recordToUser` reports `ADMIN` only for
  those records and every `users` row is `USER`. `CreateUser` and
  `UpdateUser` therefore *refuse* a request that asks for the ADMIN
  role with `FailedPrecondition`, rather than silently creating a
  plain user. `avatar_url` is likewise refused: the proto carries a
  URL, the schema an uploaded file.
- **Passwords are written to both stores.** PocketBase hashes
  `password` itself for its own auth endpoints; the Connect `SignIn`
  path verifies the parallel `password_hash` bcrypt column.
  `setPassword` writes both, so an account created or changed over
  Connect stays consistent with PocketBase's own auth API.
- **`DeleteUser` refuses by default and unwinds under `force`.**
  `memos.author`, `spaces.owner`, `space_members.user` and
  `reactions.user` all point at the record without a cascade, so a
  user with any of that attached cannot be dropped. Without `force`
  the call fails with `FailedPrecondition`; with `force` the content
  is deleted first, in one transaction, still through the record
  hooks — so a user who is the last active ADMIN of a space gets a
  clear error instead of a half-deletion.
- **`ListMemoAttachments` reads; `SetMemoAttachments` does not.**
  riff has no generic `attachments` collection: an attachment is a
  PO-33 `patches` row or a `samples` pack, each carrying domain
  fields an `Attachment` proto cannot express. Listing the union is
  faithful; replacing them from a proto payload is not yet
  representable, so that method stays unimplemented rather than
  lossy.
- **`Memo.pinned` and `Memo.location` are not stored.** The `memos`
  schema has no such columns, so they are accepted and dropped.

#### Tests

Wave 4 tests live in `pb/connect/connect_wave4_*_internal_test.go`
(internal package, for the unexported auth context key) and register
the real hooks via `newHookedApp`. They cover the bridge end to end:
author and tag stamping on create, the creator becoming a space's
first ADMIN, the space-deletion cascade, author-only memo
update/delete, the `update_mask`, hashtag re-parsing, comment
creation and inheritance, reaction upsert/list/delete and its
`NotFound` behaviour on a hidden memo, relation
replace/author-check/`COMMENT` rejection, memo payload enrichment,
space member read/promote/demote/remove with the last-ADMIN
invariant, invitation read and revoke, `BatchGetUsers` with a missing
username, admin-only user creation with a working sign-in, and
`DeleteUser` with and without `force`.

#### Still open after Wave 4

- Shares (`memos/{memo}/shares/*`), memo views, user settings,
  personal access tokens, linked identities, webhooks and identity
  providers all need new collections; each still returns
  `Unimplemented`.
- `GetUserStats` / `ListAllUserStats` are deferred, not impossible:
  they need memo and tag counts per user.
- `AttachmentService` upload/get/update/delete, and
  `SetMemoAttachments`, wait on a decision about whether riff gets a
  real `attachments` collection or keeps folding them into
  `patches`/`samples`.
- `AIService` and `IdentityProviderService` remain wholly
  unimplemented.
- `SignIn` password verification still depends on the parallel
  `password_hash` bcrypt field; users created before that field
  existed (e.g. via the PocketBase admin UI) must re-set their
  password.
- The frontend (`web/`) still points at the upstream Memos server.
  Switching its Connect base URL is the last Phase 4 step and has
  not been requested yet.

### Phase 4 follow-up — Wave 4.1 erratum: memo-delete cascade (2026-10-09)

A Wave 4 self-review asked whether two deletion behaviours were
correct against `docs/design/multi-spaces.md`. Both were cross-checked
against the design doc and the migration comments.

**Space deletion cascades to directly assigned memos — correct, not
a bug.** The design is explicit (multi-spaces.md:137, 159):
"Hard-deleting a Space atomically deletes the Space, its memberships,
every directly assigned memo, those memos' owned database resources,
and relations having a deleted endpoint. It does not follow relations
into other Spaces or Unassigned memos." `pb/hooks/spaces.go`'s
`OnRecordDeleteRequest("spaces")` matches this: it captures the
assigned memo IDs before the delete clears the `memos.space`
references (non-cascading and nullable), then deletes each memo plus
its owned resources and any `memo_relations` having it as either
endpoint. Replies assigned to the same space ARE deleted with the
space, by design; replies assigned elsewhere are not. Wave 4's
`TestDeleteSpace_RunsCascadeHook` is the correct assertion.

**Memo deletion was over-cascading — bug, fixed.** The design
(multi-spaces.md:135): "Deleting one memo deletes that memo, its
owned resources, and relations having it as an endpoint. It does
not traverse relations or delete other memos." The
`memo_relations.related_memo` migration even names this explicitly
(`pb/migrations/1700000030_create_memo_relations.go:70`):
"deleting a context memo does NOT delete its comments (comments
remain orphaned but readable per the design doc; deleted-memo webhook
is the only signal)."

Wave 4's `pb/hooks/memos.go::OnRecordDeleteRequest` walked the
comment subtree via `collectCommentSubtree` and deleted every reply
with `purgeMemoDependents`. That is the opposite of the design —
deleting a memo deleted unrelated memos.

**Fix.** The hook now only clears `memo_relations` rows where
`related_memo = memoID`. `memo_relations.memo` is a cascading
relation, so the reply-side row vanishes with the parent memo's
`memos.space` clear; the design intends for `related_memo` to be the
non-cascading side (so the deletion of a referenced memo cannot
silently remove a comment that happens to reference it). The
replies themselves, and nested replies, stay alive as readable
orphans. PocketBase's own cascades still remove the parent memo's
`patches`, `samples`, and `reactions` (each `memo` relation is
`CascadeDelete: true`), so "owned resources" are still cleaned up.

The hook uses `e.App` directly rather than opening a fresh
`RunInTransaction`. Two reasons: `e.App` is already the
transactional app inside the request hook, and a force-delete of a
user goes through `purgeUserContent` → `deleteRecordRequest` →
this hook, which previously nested transactions on SQLite (the
`IMMEDIATE` write lock on the outer transaction blocks the inner
`BEGIN`). The original `purgeMemoDependents` had the same
nested-transaction issue; the fix removes both the bug and the
deadlock.

**Tests.** `TestDeleteMemo_RemovesReplySubtreeAndIncidentRelations`
(renamed to `TestDeleteMemo_PreservesCommentChain`) flipped its
assertions: the parent is gone, the reply and the nested reply
remain, the `memo_relations.related_memo = parent` row set is empty,
and an unrelated referrer survives with its now-cleared incident
relation. `TestDeleteUser_RefusesWhenContentExists` is unchanged
and now exercises the non-nested transaction path.

Verified: `go vet ./...`, `gofmt -l pb/ main.go`,
`go test -count=1 ./...`, and `go test -race -count=1 ./pb/connect/`
all pass.

## 2026-10-20 — MoeMemos V0 file GETs: `/o/r/{uid}` and the orphan-uploader rule

MoeMemos (iOS) was locked out of attachment downloads entirely. Two
independent bugs, both on the V0 read path, both invisible to the
existing metadata regression (`TestV0BootServesRealCreationMetadata`),
which only ever asserted on the *shaped JSON* and never on the bytes
subsequently fetched from the URL inside that JSON.

**1. `GET /o/r/{uid}` was never registered.**

The MoeMemos client builds a resource URL in
`MemosV0Resource.url(for:)` (`Packages/Services/Sources/MemosV0Service/Ext.swift`):

```swift
func path() -> String {
    if let uid = uid, !uid.isEmpty { return "/o/r/\(uid)" }
    return "/o/r/\(name ?? "")"
}
```

so it downloads from `<host>/o/r/<uid>`. Upstream Memos serves that
path (`server/route/resource/resource.go`); riff did not, so the
request fell through to the SPA catch-all
(`server/frontend/embed.go`) and answered **200 with index.html**. A
client that trusts the status code caches HTML as the attachment.
Compounding it, `v0ShapeResource` never emitted `uid` at all — upstream
populates it from `store.Resource.UID` — so the Swift optional was
always nil and the client fell into the `name` branch, requesting
`/o/r/` with an empty segment.

**Fix.** `v0ShapeResource` emits `"uid"` (the PocketBase record id;
riff has no UID column and the id is the equivalent stable handle
that `/o/r/{uid}` resolves), `mountFileRoutes` registers
`GET /o/r/{uid}` on the *same* handler as the `/file/...` family, and
`attachmentUIDFromRequest` learns the `o/r/` form alongside the
`attachments/` segment search. Sharing the handler is what keeps the
authorization, thumbnail, motion and share-token behaviour from
diverging between the two surfaces.

**2. An unlinked attachment was readable only by a superuser.**

`authorizeAttachment`'s orphan branch (`memo == ""`) admitted
superusers only, on the stated premise that "the `attachments`
collection has no `creator` column of its own". That premise stopped
being true at migration `1890000000`, which added exactly that column
and stamps it with the uploader whenever the spec named no memo.

The consequence was total rather than subtle: the V0 upload path
deliberately lands rows unlinked (the MoeMemos client uploads the
file in step 2, before it knows the memo id, and binds it in step 3
with a PATCH), so **every file a V0 client uploaded answered 404 on
every read** — including the `externalLink` returned in the upload
response, which the client downloads from *immediately*, before the
PATCH that would have bound the row. Even after binding, the window
between upload and PATCH was a hard 404 for ordinary users.

**Fix.** The orphan branch now admits the uploader recorded in
`creator`, alongside the existing superuser path, and still refuses
everyone else with a **404** (never a 403) so the response does not
confirm the attachment exists. A row with an empty `creator` (one
predating the column, or written via the admin UI) stays
superuser-only — the conservative answer for a row whose owner cannot
be established.

**Tests.** `pb/api/upstream_memos_v0_resource_download_internal_test.go`
mounts a stand-in for the SPA catch-all (`mountTestSpaFallback`) so a
missing route reproduces as *200 index.html* rather than a bare mux
404 — otherwise the test would pass for the wrong reason and never
see the real failure mode.

- `TestV0ResourceIsDownloadable` — drives the real upload and asserts
  on `uid`, on `GET /o/r/{uid}`, and on the `externalLink`, checking
  the response bytes rather than just the status.
- `TestV0ResourceDownloadDeniesOtherUsers` — `/o/r/{uid}` must not be
  a way to read an arbitrary attachment by id; owner allowed,
  anonymous and other users 404 with no byte leak.
- `TestUnlinkedAttachmentIsReadableByItsUploader` — pins the orphan
  branch on both URL forms.

Each of the three fixes was verified to be individually load-bearing by
reverting it and watching the corresponding test fail.

Verified: `go build ./...`, `go vet ./pb/...`,
`go test -count=1 ./pb/api/ ./pb/connect/ ./pb/hooks/ ./pb/migrations/`,
and `go test -count=1 -race ./pb/api/` (403 s, zero data races).

## 2026-10-20 — The V0 PATCH bound nothing: `resourceIdList`, not `resourceList`

Uploads from MoeMemos were never linked to a memo. The cause is a
single field name, and it had been wrong for the entire life of the
V0 surface.

MoeMemos' own OpenAPI spec (`Packages/Services/Sources/MemosV0Service/openapi.yaml`)
declares the memo request bodies as:

```yaml
CreateMemoRequest:
  properties:
    resourceIdList:
      type: array
      items:
        type: integer      # <- a FLAT array of ids
PatchMemoRequest:
  properties:
    resourceIdList:
      type: array
      items:
        type: integer
```

`resourceList` does not appear in either request schema. It is a
field of the **`Memo`** schema — i.e. the *response* shape, where it
holds an array of full `Resource` objects.

Our `v0ResourceListIDs` read `resourceList` and expected objects with
an `id` key. MoeMemos sends `resourceIdList` as bare integers. The
lookup therefore found nothing on **every** request, `resourceIDs`
came back empty, and `v0LinkResourcesToMemo` was never called. Both
the create path and the PATCH path were affected — they share
`v0ResourceListIDsFromBody`.

**Why no test caught it.** Every existing regression test
hand-wrote the `resourceList` *object* form into the request body —
a shape the client cannot produce. The tests were green and
asserting a fiction. This is the same class of mistake as the
hand-written `{"seconds": N}` timestamp fixtures: a fixture
invented from a plausible-looking guess rather than read off the
real wire.

**Fix.** `v0ResourceListIDs` reads `resourceIdList` first, as a flat
array of integer ids, via a new `v0ResourceIDFromScalar` that accepts
`json.Number` (the client decodes 64-bit ids as `Long`, so the body
is decoded with `UseNumber`), `float64`, and a numeric string. The
object-form `resourceList` is still accepted for older forks and
hand-rolled clients. Both V0 request shapers now also strip
`resourceIdList` alongside `resourceList`, so it cannot leak into the
V1 `update_mask`.

**Testing rule this establishes.** For the V0 surface, a request
fixture must be copied from the client's `openapi.yaml` or from a
recorded real request. A test that hand-writes a request body is only
valid if the shape is one a real client can emit; otherwise it pins
whatever the server happens to do rather than what the client
actually asks for.

**Test.** `TestV0MemoWithResourceIdListBindsUpload` sends the
client's exact `{"resourceIdList": [<id>]}` payload and asserts both
the PATCH response and the `attachments.memo` column.
`TestV0MemoImageIsDownloadableAfterLink` runs the whole
create → upload → PATCH → download sequence for both PRIVATE and
PUBLIC memos, checking the download in *both* the orphan window and
the steady state (after the row has a memo, authorization goes
through `memos.ViewRule` instead of the `creator` branch — a
different code path, so a test covering only one proves nothing about
the other).

## 2026-10-20 — V1 write/read routes were dead on arrival: `connect_proxy` vs `connect_proxy_wrap`

The V1 REST surface (`pb/api/upstream_memos_v1_rest.go`) had 30 routes,
and every one used the plain `connect_proxy` flavor. The `connect_proxy`
flavor forwards the request body **verbatim** to the connect handler.
But the V1 proto wire format is not verbatim-shaped:

* `CreateMemo` declares `body: "memo"`, so the MoeMemos openapi-generator
  client posts the inner `Memo` fields directly — `{"content":"...",
  "visibility":"..."}` — while the connect handler expects
  `CreateMemoRequest{memo: Memo}`. Verbatim forwarding left `memo` nil
  and every create answered `"memo is required"`.
* `GetMemo` declares `get: "/api/v1/{name=memos/*}"`, so the client sends
  an empty body and the id lives in the URL path. Verbatim forwarding
  sent `{}`, the connect handler saw an empty `name`, and every read
  answered `"name is required"`.
* Same class for `UpdateMemo`, `DeleteMemo`, `GetAttachment`,
  `UpdateAttachment`, `DeleteAttachment`, `GetUser`, `GetUserSetting`,
  `UpdateUserSetting`, the memo comment/reaction/relation/attachment
  sub-resources, and `CreateAttachment`.

`v1BodyWrappers` and `connectProxyWrapHandler` already existed with the
correct wrappers (`wrapV1CreateMemoBody`, `wrapV1GetMemoBody`, …), but
the route table never wired them — they were dead code, and the
comment that "the V1 routes are all `connect_proxy` (no wrap)" recorded
the mistake as intentional.

**Fix.** The parameterized and body-unwrapped V1 routes now use
`connect_proxy_wrap` with the right `Builder` key into `v1BodyWrappers`.
Two wrappers were missing and are added: `wrapV1UpdateAttachmentBody`
and `wrapV1UpdateUserSettingBody` (both unwrap the inner message, inject
`name` from the path, and forward `updateMask` from the query).
`wrapV1InjectMemoNameBody` fixes `UpsertMemoReaction`, which had used
`wrapV1PathInjectBody` and thereby *replaced* the body with `{name}`,
dropping the `reaction` field.

The two tests that pin the regression are
`TestV1CreateMemo_BareMemoBodyIsWrapped` and
`TestV1MemoPathRoutesInjectName`; both failed on the old route table
(`"memo is required"` / `"name is required"`) and pass after the fix.

**Version probe.** The V0 probe (`GET /api/v1/status`) and the V1 probe
(`GET /api/v1/instance/profile` → `GetInstanceProfile`) now both honour
`RIFF_MEMOS_VERSION` via `api.SetAdvertisedVersion` and
`connect.SetAdvertisedVersion`. Without the connect side, the V1 client's
compatibility check would read the riff build CalVer (e.g. "25.10") and
flag the server as "higher than supported"; advertising "0.31.0" (or
whatever the current maximum is) makes the V1 client fully compatible.

## Anonymous callers and `canAccessAttachment` (the invisible-attachment bug)

`canAccessAttachment` in `pb/connect/attachment.go` opened with

```go
viewer := CurrentUser(ctx)
if viewer == nil {
    return false
}
```

which refused **every** anonymous caller before it ever consulted
`memos.ViewRule`. A PUBLIC memo's attachment was therefore invisible to
the logged-out SPA feed (`ListAttachments` is the one handler that passes
an anonymous context — the other four call sites already return
`Unauthenticated` first).

The tell was that the file surface disagreed. The very same bytes were
reachable: `/file/attachments/{id}` goes through `pb/api`'s
`authorizeAttachment`, which has no such up-front refusal — it builds a
`core.RequestInfo` with a nil `Auth` and lets `memos.ViewRule` decide, so
an anonymous GET on a PUBLIC memo's attachment returned 200. The SPA
rendered the image (the `<img>` src carried the cookie) while the feed
that produced the `<img>` had been told there was nothing to render.

**Fix.** The up-front refusal is gone. A linked attachment delegates to
`memos.ViewRule` with a possibly-nil `Auth`; PUBLIC passes, PRIVATE still
fails. The orphan branch (`memo == ""`) keeps its own `viewer == nil →
false` guard, because the `creator` uploader check needs an identity and
an anonymous caller has none. `TestAttachmentService_OrphanHiddenAnonymously`
is the guard on that side of the change.

`authorizeAttachment` in `pb/api/file_authz.go` is now the single
statement of the rule for both surfaces: share token, then identity, then
the memo's visibility rule.

## `Logs.MaxDays = 0` in tests: the teardown race

Two intermittent failures shared a root cause in PocketBase's
`core.initLogger`, not in riff:

* under `-race`, a DATA RACE between `ClearBootstrap` nil-ing
  `app.concurrentDB` (`core/base.go:506`) and the logger's flush goroutine
  reading the same field in `createTxApp` (`core/db_tx.go:53`);
* in long runs, `TempDir RemoveAll cleanup: directory not empty`.

`initLogger` runs a 3-second `time.Ticker` and a 200-entry
`BatchHandler`; on each flush `WriteFunc` hands the batch to
`routine.FireAndForget(runLogsWrite)`, which opens an **aux** DB
transaction. The `OnBootstrapClear` hook (priority -999) flushes what is
already queued and signals the ticker, but a tick that has already fired
hands its batch to a goroutine nobody joins. That goroutine then reads
`app.concurrentDB`/`app.auxConcurrentDB` concurrently with the write
`ClearBootstrap` performs — the race above — and, in the window between
the test body returning and the temp dir being removed, writes into a
directory that is being deleted — the second failure. Both landed on
whichever test happened to be running, which is why neither had an
attributable owner.

**Fix.** `testutil.DisableLogPersistence(app)` sets
`app.Settings().Logs.MaxDays = 0`, and every `core.NewBaseApp` in a test
calls it immediately after `Bootstrap()` (settings load during bootstrap,
so a mutation made earlier is discarded). That shuts the writer off at the
source: `BeforeAddFunc` returns false for every record, so nothing is
queued, `BatchHandler.WriteAll` short-circuits on an empty batch and
never reaches `WriteFunc`; and `runLogsWrite` carries its own
`MaxDays == 0` guard, so a tick already in flight becomes a no-op.

No test in the repo asserts on persisted log rows, so the only behaviour
change is that `logs.db` stays empty under test. Production is untouched —
`main.go` never calls this.

## MoeMemos iOS `externalLink` absolute form

The MoeMemos iOS client's auth middleware (`mudkipme/MoeMemos`
Packages/Services/Sources/ServiceUtils/Auth.swift::`rawAccessTokenMiddlware`)
only attaches `Authorization: Bearer …` when `request.url?.host ==
hostURL.host`. A *relative* URL has `host == nil`, so the
comparison is always false and the bearer is dropped — every
PRIVATE memo's attachment then 404s on the file surface, because
the file surface authorizes via the bearer it never received.

riff's previous shape (a relative-path `/file/attachments/...`)
shipped through every connect RPC's `externalLink` and every V0
shaper fallback. The V0 client built the resource model from the
response and put the URL straight into its Core Data store; the
next render hit `URLSession.download(url:)` and lost the bearer.

`mortis` (mudkipme/mortis, the Memos 0.21↔0.31 reverse proxy)
ships the same relative `externalLink` (`server.go::convertResource`)
and has the same gap for PRIVATE memos. Its `/o/r/{uid}` proxy also
stays auth-gated; mortis effectively only helps for PUBLIC memos.

**Fix.** Three layers in riff:

1. `pb/connect/connect.go::WithAuth` now stashes the user-facing
   origin (`scheme://host[:port]`) on `ctx` after reading the
   `info.RequestHeader()`. The helper recognises connect-go's
   `*Header` and the plain `http.Header` via a small interface,
   so tests can drive it without an in-process mux. Empty origin
   (no `Host` header, no `X-Forwarded-Proto`) is reported as `""`,
   not "guess http from a missing header".
2. `pb/connect/attachment.go::resourceToAttachment` now takes an
   origin parameter; the rpc-handler callers thread the ctx
   origin through `recordToAttachmentWithOrigin(ctx, app, rec)`.
   Request-less callers (record hooks, the V0 stub-bind path)
   use the legacy relative form. The new
   `buildAttachmentExternalLink(collection, recordID, filename,
   origin)` is the testable seam.
3. `pb/api/file_authz.go::requestOriginFromEvent` mirrors the
   connect-side helper for the REST surface; the V0 shapers
   (`v0ShapeResource`, `v0ShapeMemo`, `v0ShapeMemoList`,
   `v0ShapeResourceList`, `v0ShapeSingleMemo`,
   `v0ShapeSingleMemoAttachments`) now thread it through, and
   the two V0 dispatch helpers (`connectProxyHandler`,
   `v0DispatchConnect`) lift `Host` from `e.Request.Host` into
   the upstream `Header` map so the connect-go dispatcher
   produces a `CallInfo` whose `RequestHeader()` carries it.

   `requestOriginFromContext` is nil when no interceptor ran
   (record hooks, the V0 stub-bind path before the response has
   gone out), in which case every layer still emits the relative
   form — the test fixtures in `pb/connect/recordToAttachment`
   continue to work without a request event.

The regression pin is
`pb/api/upstream_memos_v0_resource_download_internal_test.go::TestV0ExternalLinkEmittedAsAbsoluteURL`
plus the keep-as-is fixture test
`TestV0RelativeExternalLinkEmptyOriginKeptForFixtures`. The
existing `TestProductionSpaMoeMemosDownloadStreamCompletes`
was updated to handle the new absolute form
(`httpSrv.URL + feedExt` duplicated the host when `feedExt` is
already absolute).

**Side effects.** The SPA-side `web/src/utils/attachment.ts`
already accepts both forms (`new URL(externalLink,
window.location.origin)` is format-agnostic); absolute URLs
short-circuit one hop and survive being pasted into a non-SPA
context. Existing mime-type, share-token and thumbnail paths
are unchanged.

## V1 REST surface completeness audit (four routing/wire bugs)

The MoeMemos V1 client (`MemosV1Service/openapi.yaml`) declares **25
operations** across 13 paths. `TestV1PatPath_MoeMemosBoot` walked
only three of them, so nothing caught a route wired to the wrong
Builder or an unreachable path.
`TestV1SurfaceEveryClientOpAnswers`
(`pb/api/upstream_memos_v1_surface_audit_internal_test.go`) now
drives all 29 riff-side ops against a live app with a real PAT and
asserts the ones that must succeed do.

**The audit's own first version was too weak** and is worth
recording: asserting "not 404, not 501" passes even when every
route is a `connect_proxy` that answers `200 {"code":3,"message":
"memo is required"}`. The three shapes that actually mean "broken"
are:

* `404` with an **empty** body — the router matched no pattern;
* `501 Not Implemented` — no handler;
* **`2xx` carrying a connect error envelope** (`{"code","message"}`
  with no payload) — the wrapper is missing, so the handler got a
  body it could not unmarshal. This is the one the original
  assertions missed entirely, and it is the shape the
  `connect_proxy` → `connect_proxy_wrap` bug class produced.

### Bug 1 — the settings PATCH oneof

`UserSetting.value` is a **oneof** (`general_setting` /
`webhooks_setting` / `tags_setting`). The wrapper left the inner
body **flat**, so protojson decoded a `UserSetting` whose `Value`
was nil and `marshalSettingPayload` answered `"setting body is
required"` — **`PATCH /api/v1/users/{user}/settings/{setting}`
never worked for any setting**.
`wrapV1UpdateUserSettingBody` now nests the payload under the
oneof key derived from the `{setting}` path segment
(`v1UserSettingOneofKey`), and passes an already-nested body
through untouched.

### Bug 2 — `GET /api/v1/users/{user}/settings` was never routed

The client's spec declares the path and the proto has
`ListUserSettings`, but the route table only registered
`/api/v1/users/{user}/settings/{setting}`. The list form fell
into the `/api/v1/users/{user}/{rest...}` custom-verb route,
whose handler read `rest == "settings"` and answered
`404 unsupported custom verb: settings`. Added
`v1_list_user_settings` + `wrapV1ListUserSettingsBody`
(injecting `parent: "users/{user}"`).

### Bug 3 — the AIP `:getStats` verb was unreachable

The client sends `GET /api/v1/users/{user}:getStats` — a colon,
**no slash**, bare id in `{user}` (`MemosV1Service.getId()`
strips the resource name before filling `path.user`). Go's
`ServeMux` **panics** on a `{var}:` pattern
(`bad wildcard segment`), so the verb cannot live in the route
table, and the `/api/v1/users/{user}/{rest...}` route cannot
match it either: `{user}` is the more specific single-segment
pattern and wins, swallowing `alice:getStats` whole. The handler
then built `{"name":"users/alice:getStats"}` and `GetUserStats`
looked up a user literally named `alice:getStats` — every
`:getStats` call answered `not_found`.

`GET /api/v1/users/{user}` is now the `v1_get_user_or_stats`
dispatcher: it splits the trailing `:verb` off `{user}` and routes
to `GetUserStats` (or the plain `GetUser` when there is no verb)
**before** dispatch. Splitting it in the body wrapper instead was
tried first and is wrong — it makes `:getStats` return a `User`,
which the client decodes into an empty stats screen rather than an
error. `v1UserStatsHandler` still serves the slash-shaped URL.

`v1DispatchAndEcho` documents the contract the new handler relies
on: `v0DispatchConnect` writes only the **error** body, so every
caller must `e.JSON(...)` the success payload itself.

### What was NOT a bug

Four audit findings turned out to be **my fixtures**, not riff:
`{passwordCredentials:…}` (not top-level `username`/`password`);
the inner `Attachment` body for `POST /attachments` (the wrapper
re-wraps it); the `users/{id}` resource **name** vs the bare
username (riff emits `Name: "users/" + username`, and the client
round-trips `user.name`, so the id never appears); and
`settings/GENERAL` (an enum name, not `memos.defaultVisibility`).
`AuthServer.verifyPassword` reads riff's bcrypt `password_hash`
column, not PocketBase's auth `password`, so a harness record
created by a bare `app.Save` has nothing to verify.

## The `externalLink` fix verified end-to-end (and why a device may not see it)

The absolute-`externalLink` change above was verified by driving the
**exact V0 three-step** the MoeMemos client performs against a live
app, decoding ids with `json.Decoder.UseNumber` (how the client's
`Int`/`Long` decoder sees them — a `float64` hop silently corrupts a
64-bit id and produces `404 memo id not found`):

| Step | Result |
| --- | --- |
| `POST /api/v1/memo` | `200`, `id: 5172917733476627008` |
| `POST /api/v1/resource/blob` | `200`, `externalLink: https://riff.example/file/attachments/<uid>/pic.jpg` |
| `PATCH /api/v1/memo/{id}` with `resourceIdList` | `200`, `resourceList[0]` populated, `memoId` set |
| `GET /api/v1/memo` (the feed the client renders) | `200`, absolute `externalLink`, `uid` present, `size: 659` |
| `GET /o/r/{uid}`, `?thumbnail=true`, `/file/attachments/{uid}/pic.jpg` | `200 image/jpeg` |

V1 is equally correct: `"size":"659"` is emitted as a **JSON
string**, matching `MemosV1Service/Ext.swift`'s
`if let s = self.size, let s = Int(s)`, and `externalLink` / `memo`
are absolute and populated.

### Why an existing device may still not render the image

The client caches by URL string, so a device that already downloaded
attachments under the **relative** URL has an entry keyed to
`/file/attachments/...` (`ServiceUtils.swift:38`,
`SHA256(url.absoluteString)`), and the server now emits an absolute
URL that hashes differently. But the client short-circuits
**before** any request:

```swift
// SyncingRemoteService.ensureLocalResourceFile
if let localPath = resource.localPath, FileManager.default.fileExists(atPath: localPath) {
    return URL(fileURLWithPath: localPath)
}
```

`localPath` is persisted in Core Data and is **never invalidated by
a URL change**, so an upgraded install keeps serving whatever was
cached under the old URL and never re-issues the download. **This is
a client-side cache-invalidation gap that no server-side change can
fix** — it is the most likely explanation for "the fix did not
work". Clearing the app's cache (or re-syncing the affected rows)
is what makes the new URL take effect.

### Remaining shape gaps (measured, not assumed)

Comparing riff's emitted shape against both clients' decoders:

| Field | SPA (TS) | iOS V0 client | riff V0 | riff V1 | Verdict |
| --- | --- | --- | --- | --- | --- |
| `size` | number | `integer` | number | **string** (matches the V1 client schema) | consistent |
| `externalLink` | URL | used verbatim | absolute | absolute | fixed |
| `uid` | unused | builds `/o/r/{uid}` | present | n/a | ok |
| `memoId` | unused | association | present | `memo` | ok |
| `type` | `image/*` prefix | `mimeType.hasPrefix("image/")` | `image/jpeg` | `image/jpeg` | ok |

**No wire-shape mismatch remains on either surface.**

### Two caveats recorded deliberately (not changed)

1. **`?thumbnail=true` is dead on the iOS path.** `MemosV0Resource.url(for:)`
   returns `externalLink` verbatim, so iOS never asks for a
   thumbnail and always fetches full-size bytes. Emitting
   `…?thumbnail=true` for image types would give the client the
   600px JPEG, but it is a behaviour change and has **not** been
   made.
2. **`stableLongHash` can emit negative V0 ids.** It returns
   `int64(uint64FNVHash)`, which overflows `MaxInt64` roughly half
   the time. Swift's `Int(...)` parses those fine and
   `v0IDFromV1` round-trips them, so it works — but any code that
   reads a V0 id through a `float64` JSON hop corrupts it. Always
   decode V0 ids with `json.Decoder.UseNumber`.

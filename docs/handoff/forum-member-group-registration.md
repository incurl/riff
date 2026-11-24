# Handoff: "grant forumgroupmember membership at registration"

**Status: investigation complete; the recommended path (§5.3) is IMPLEMENTED.**
No member-group row is stored — see §5. The follow-up that WAS taken is
the frontend group-id constants fix (§4a), which is now done and pinned
by a test. Everything below is verified against upstream Flarum v2.0.0
and against riff's live behaviour.

**The single most important finding: upstream Flarum v2.0.0 does NOT store a
member-group row, and the requested change would not reproduce upstream.**
Read §1 before doing anything. The requested change is not wrong — it is
*different from upstream*, and it has consequences the requester may not
have intended. §5 gives the recommended path, which satisfies the request's
*intent* (a registered user is visibly a member) without the parts that
diverge from upstream.

---

## 1. Upstream v2.0.0's model: the member group is IMPLICIT

Upstream keeps the member group **out of the database entirely**. It is
derived at permission-check time.

`framework/core/src/User/User.php:659-668`:

```php
public function permissionGroupIds(): array
{
    $groupIds = [Group::GUEST_ID];

    // If a user's account hasn't been activated, they are essentially no
    // more than a guest. If they are activated, we can give them the
    // standard 'member' group, as well as any other groups they've been
    // assigned to.
    if ($this->is_email_confirmed) {
        $groupIds = array_merge($groupIds, [Group::MEMBER_ID], $this->groups->pluck('id')->all());
    }

    foreach (static::$groupProcessors as $processor) {
        $groupIds = $processor($this, $groupIds);
    }

    return $groupIds;
}
```

`framework/core/src/User/User.php:680-682`:

```php
public function permissions(): Builder
{
    return Permission::query()->whereIn('group_id', $this->permissionGroupIds());
}
```

### Evidence that no row is ever written for the member group

Exhaustively searched upstream core for writes to the `group_user` table.
There is exactly **ONE** insert in the whole of core, and it is for the
**administrator** group, not the member group:

```
framework/core/src/Install/Steps/CreateAdminUser.php:38:  $this->database->table('group_user')->insert([
framework/core/src/Install/Steps/CreateAdminUser.php:39:      'user_id' => $uid,
framework/core/src/Install/Steps/CreateAdminUser.php:40:      'group_id' => Group::ADMINISTRATOR_ID,
```

The other `group_user` reference is a JOIN in the search filter
(`User/Search/Filter/GroupFilter.php:52`), not a write.

So: **upstream registration creates NO group row.** A freshly registered
upstream user has zero rows in `group_user`, and still holds every member
permission, because `permissionGroupIds()` adds `MEMBER_ID` on the fly.

### Upstream's admin UI explicitly hides the member group from assignment

`framework/core/js/src/common/components/EditUserModal.tsx:41`:

```tsx
this.availableGroups = sortGroups(app.store.all<Group>('groups').filter((group) => ![Group.GUEST_ID, Group.MEMBER_ID].includes(group.id()!)));
```

Upstream's own group editor **filters out** `GUEST_ID` and `MEMBER_ID` —
they are not assignable through the UI. So "assign the member group at
registration" is not something upstream does anywhere.

### Upstream's activation gate

Note the `is_email_confirmed` condition above: upstream grants the member
group only to **activated** accounts. An unactivated account is treated as
a guest. riff has no `verified` column on `users` (see §4), so riff has no
equivalent gate — every signed-in riff account is a member.

---

## 2. riff's model: the member group is ALSO implicit (already correct)

riff already matches upstream's model. **The requested change is not
needed to make a registered user a member.**

`pb/flarum/domain/permissions.go:251-277` — `Actor.groupSet()`:

```go
// groupSet returns the actor's effective groups.
//
// The implicit groups are added: an anonymous caller is a guest, a
// signed-in caller is a member as well.
func (a Actor) groupSet() []string {
	...
	if !a.IsAuthenticated() {
		add(GroupGuestID)
		return groups
	}

	add(GroupMemberID)
	for _, id := range a.GroupIDs {
		add(id)
	}
	return groups
}
```

`groupSet()` is what `lookup()` consults (`permissions.go:202-208`), so
every signed-in account already holds the member group's permission rows.

`pb/flarum/repository/repository.go:319-324` documents the counterpart:

```go
// UserGroupIDs returns the ids of the groups a user belongs to.
//
// The implicit groups (guest, member) are not included: they are a
// permission concept, not a membership row.
```

### Empirical proof (a probe test was run and deleted)

A throwaway test (`pb/flarum/zz_probe_test.go`, since removed) created a
fresh user through the real `users` collection and drove the real forum
routes. The output:

```
PROBE user resource: 200
PROBE body: {"data":{"type":"users","id":"zv35bzncv78ginf",...,"relationships":{"groups":{"data":[]}}}}
PROBE start discussion: 201 {"data":{"type":"discussions","id":"t623oy4a5lvcmqo",...}}
PROBE forum_group_users rows: 0
```

Three facts, all decisive:

1. **`forum_group_users` rows: 0** — a fresh user has no stored membership.
2. **`POST /api/discussions` → 201** — the fresh user **can** start a
   discussion. That power lives in the member group's permission rows
   (`startDiscussion`, `discussion.reply`, `searchUsers`). So the member
   group **is** in effect for them.
3. **`relationships.groups.data` → `[]`** — this is the *only* observable
   difference from upstream-with-a-row. The member group is **not listed**.

### So the actual gap is display, not authorisation

| | upstream v2.0.0 | riff today |
| --- | --- | --- |
| member permission rows apply | yes (implicit) | yes (implicit) |
| `group_user` row | none | none |
| `groups` relationship lists Member | **no** | **no** |
| admin UI can assign Member | **no** (filtered out) | **no** (filtered out) |

riff and upstream agree on all four rows. **There is no behavioural gap to
close.** The request presumably comes from looking at the admin UI or the
`groups` relationship and concluding "the member group was never granted".

---

## 3. What actually breaks if you DO store the row

Storing a `forum_group_users` row for `forumgroupmembr` is implementable
(it is one call to the existing `Repository.AddUserToGroup`), but it
diverges from upstream in three ways, one of which is a **real defect**.

### 3a. The frontend's group-id constants do not match riff's ids

This is the important one, and it is a **pre-existing bug** independent of
this task.

`pb/flarum/frontend/packages/core/src/common/models/Group.ts:5-7` — the
vendored upstream frontend:

```ts
static ADMINISTRATOR_ID = '1';
static GUEST_ID = '2';
static MEMBER_ID = '3';
```

riff's backend ids (`pb/flarum/domain/models.go:278-296`):

```go
GroupGuestID     = "forumgroupguest"
GroupMemberID    = "forumgroupmembr"
GroupAdminID     = "forumgroupadmin"
GroupModeratorID = "forumgroupmoder"
```

The frontend was **never patched** — `grep -rn "forumgroupadmin\|forumgroupmembr\|forumgroupguest" pb/flarum/frontend/packages/` returns **nothing**.

Consequences today:
- `EditUserModal.tsx:270` — `userIsAdmin()` checks
  `g?.id() === Group.ADMINISTRATOR_ID` (`'1'`). riff's admin group id is
  `forumgroupadmin`, so **this check never matches**. It is, however,
  guarded by `actor.IsAdmin` server-side, so it is a UI wart, not a hole.
- `EditUserModal.tsx:165` — the "disable the admin-group checkbox" logic
  never matches either.
- `EditUserModal.tsx:43` — the filter that hides `GUEST_ID`/`MEMBER_ID`
  is a **no-op** in riff, because no group has id `'2'`/`'3'`. The admin
  UI therefore **already offers** guest and member for assignment, and
  riff's server rejects them
  (`pb/flarum/api/handlers_mutations.go::setUserGroups` — only
  `forumgroupmoder` and `forumgroupadmin` are assignable). So the admin
  UI and the server already disagree.

**If you store a member row, the badge would render**: the frontend's
`User.tsx:135-146` `badges()` iterates `this.groups()` and renders a
`GroupBadge` for each. A stored member group therefore shows a "Member"
badge on every post. Upstream shows **no** such badge (its relationship
excludes the member group). If the goal is upstream fidelity, storing the
row makes the display *less* upstream-like.

### 3b. `CanAsModerator` would need re-checking

`pb/flarum/domain/permissions.go:162-190` — `CanAsModerator` deliberately
uses `actor.GroupIDs` (the **explicit** groups) rather than `groupSet()`:

> A row for the implicit member group therefore carries no information
> about the individual — every signed-in caller holds it — so a check that
> relies on it alone would let any member perform the action on any
> subject.

If a member row is stored, `actor.GroupIDs` would then contain
`forumgroupmembr` for every user, and `CanAsModerator` would start
resolving the member group's rows. Today the member group holds
`viewForum`, `startDiscussion`, `reply`, `likePost`, `searchUsers`
(`pb/flarum/migrations/seed.go:103-109`) — none of which are moderation
powers, so **no escalation today**. But the *safety property* the comment
describes ("the explicit groups only") would be silently lost: any future
row added to the member group would immediately become a global grant.
The moderator check would have to filter `forumgroupmembr` out explicitly.

### 3c. `setUserGroups` would have to preserve the member row

`pb/flarum/api/handlers_mutations.go::setUserGroups` replaces the
membership: it removes every assignable group the account has and adds the
requested ones. A stored member row is **not** in the assignable set, so
it survives today. But if the member row becomes the norm, the code needs
an explicit rule that it is preserved (and that a client cannot request
its removal) — otherwise the admin's next group edit silently strips every
user's membership.

---

## 4. Gaps that are real and adjacent (worth a follow-up)

These are NOT the requested task, but the investigation surfaced them.

### 4a. The frontend's group-id constants are wrong (a real bug) — FIXED

See §3a. The vendored frontend compared group ids against `'1'`/`'2'`/`'3'`
while riff uses `forumgroupadmin`/`forumgroupguest`/`forumgroupmembr`.
They now carry riff's ids (`pb/flarum/frontend/packages/core/src/common/models/Group.ts`),
and `TestForum_TheGroupIDsMatchTheFrontend` reads them back out of the
TypeScript source so the two sides cannot drift apart again. With the
constants right, the admin group editor hides guest/member, and the
admin-checkbox guard works.

### 4b. `member_count` is never written

`forum_groups.member_count` exists (`pb/flarum/migrations/core.go:150`)
and is read (`pb/flarum/repository/repository.go:417`) but **never
written** — `grep -rn 'member_count' --include='*.go' pb/flarum/` finds
only the collection definition and the read. The probe shows every group
with `memberCount: 0`.

Upstream v2.0.0 **has no `member_count` column at all** — its `groups`
table is `(id, name_singular, name_plural, color, icon, is_hidden,
created_at, updated_at)` (`framework/core/migrations/sqlite-install.dump:2`).
So riff invented the column. If you store member rows, `member_count`
would need maintaining — and it would then count *every* user, which is
the number upstream deliberately does not expose.

### 4c. riff has no `verified` / activation gate

Upstream grants the member group only when `is_email_confirmed`. riff's
`users` collection has no `verified` column
(`pb/migrations/1700000001_extend_users.go:43` says verified-creator state
"will be modelled by a follow-up migration when the community verification
flow lands"). So riff's every signed-in account is a member from the
moment it registers, with no activation gate.

This is the one place where a stored row could be **justified** as an
upstream-shaped idea: upstream's `is_email_confirmed` gate is real, and
riff has no equivalent. But a stored row is not how upstream expresses it.

### 4d. There is no group-filter on the users list

Upstream has `GroupFilter` (`User/Search/Filter/GroupFilter.php`) which
JOINs `group_user` — so upstream's group filter **cannot** find the member
group either, because there is no row for it. riff's
`listUsersHandler` (`pb/flarum/api/handlers_discussions.go:253`) has no
group filter at all. Consistent with upstream, but worth noting.

---

## 5. Recommendation

**Do not store a member row.** It would not reproduce upstream (upstream
stores none), it would not change any permission outcome (§2 — the member
group is already in effect), and it would diverge on display (§3a: a
"Member" badge upstream does not show) and on the `CanAsModerator` safety
property (§3b).

Recommended, in order:

1. **Confirm the requester's actual symptom.** Most likely candidates:
   - *"the admin UI does not show the users as members"* → the fix is in
     the admin UI / the frontend id constants (§4a), not in registration.
   - *"the `groups` relationship is empty"* → **this is upstream's
     behaviour**; upstream's relationship also omits the member group.
     Do not "fix" it.
   - *"a user cannot post"* → that is a **permission** problem, not a
     membership problem, and the probe shows posting works.

2. **If the goal is that the member group is visible** (a plausible,
   legitimate reading of the request), the upstream-shaped way is to keep
   it implicit in the permission check and **include it in the serialized
   relationship** — i.e. `userResourceWithGroups` lists the member group
   for a signed-in account. But note this **diverges from upstream's
   relationship**, which omits it, and it would make the frontend render a
   "Member" badge on every post. Upstream deliberately does not. **Ask the
   requester before doing this.**

3. **Fix the frontend id constants** (§4a) as a separate, clearly-scoped
   change — it is a genuine bug and it makes the admin group editor
   behave (hiding guest/member, and the admin-checkbox guard).

4. Leave `member_count` alone unless something consumes it.

---

## 6. If the requester insists on the stored row anyway

The implementation, if it is required, is small but **must** cover all of
these — each was found in this investigation and each is a silent
regression if missed:

- **Where to hook.** The forum has no user-creation endpoint; accounts
  arrive through riff's own `pb/connect/user.go::CreateUser` (three
  paths: bootstrap superuser, admin-created user, self-service sign-up) or
  through PocketBase's own API. The forum module binds an `OnServe` hook
  (`pb/flarum/register.go:144`), so the natural place is
  `app.OnRecordAfterCreateSuccess("users")` bound there — this covers
  **every** creation path, including PocketBase's native API, which the
  Connect handler does not. Note `pb/flarum` must not import `pb/connect`
  (the layering rule: `pb/` → `pb/flarum/` is a sibling, and
  `pb/flarum` may import only `core/` and `internal/`), so the hook must
  live **inside** `pb/flarum` and use `pb/flarum/repository`.
- **The superuser path** creates a `_superusers` record, not a `users`
  record. Upstream's admin DOES get a `group_user` row (for
  `ADMINISTRATOR_ID`, `CreateAdminUser.php:38`). **RESOLVED (2026-11-24):
  riff's admin flag is now the union of the two sources** — a PocketBase
  superuser AND membership in the administrator group — because upstream
  derives `isAdmin()` from the group alone
  (`User::isAdmin()` is `$this->groups->contains(Group::ADMINISTRATOR_ID)`).
  Deriving it from the collection alone made `forumgroupadmin` a row that
  granted nothing: a promoted account still got 403 on `/forum/admin`.
  See DECISIONS.md "An administrator is a MEMBER OF THE ADMIN GROUP, not a
  superuser". Upstream's admin group is *not* the member group, so the
  member-group question below is unaffected.
- **Backfill existing users.** A hook only covers *new* users. Existing
  accounts need a one-off backfill if the member row is to be the norm.
- **`CanAsModerator` must filter `forumgroupmembr` out of
  `actor.GroupIDs`** (§3b) or the safety property is lost.
- **`setUserGroups` must preserve the member row** (§3c) and must reject a
  client that asks for it to be removed.
- **`member_count` would need maintaining** (§4b) if anything reads it.
- **The badge regression** (§3a) is a **visible** change: every post by
  every user would gain a "Member" badge. Upstream shows none.

---

## 7. Verification commands

```bash
# The forum package (build, vet, lint, test, race).
go build ./...
go vet ./pb/flarum/...
PATH="$HOME/go/bin:$PATH" golangci-lint run ./pb/flarum/...
go test -count=1 ./pb/flarum/...
go test -race -count=1 -timeout=900s ./pb/flarum/...
```

**The race suite takes ~843s against a 900s timeout** (measured on the
committed tree at `153671004`). The default 600s Go timeout **fails** it.
The suite boots a PocketBase app and runs all migrations per test; sharing
one app across the package would fix this properly but is separate work.

Upstream source for re-verification (v2.0.0, cloned at `/tmp/fu`):

```bash
git clone --depth 1 --branch v2.0.0 https://github.com/flarum/framework.git /tmp/fu
# The member group's implicit resolution:
sed -n '655,690p' /tmp/fu/framework/core/src/User/User.php
# The only group_user write in core:
grep -rn "table('group_user')" /tmp/fu/framework/core/src/
# The admin UI hiding guest/member:
sed -n '40,46p' /tmp/fu/framework/core/js/src/common/components/EditUserModal.tsx
```

---

## 8. Repository state

- The recommended path (§5.3) is **implemented**; no member-group row is
  stored. The §4a frontend group-id fix is done.
- The admin surface (`forum_settings` / permissions / group order) was
  brought in line with upstream v2.0.0 in the same change — see
  `DECISIONS.md` "The forum's admin surface speaks upstream's command
  wire format".
- Prior commits: `c71c73dfd` (the upstream permission alignment),
  `16364f51b` (ownership checks + the login timing fix),
  `4f86c6cde` (the colour-preview swatch),
  `153671004` (assigning the moderator group).
- `build`, `vet`, `golangci-lint` (0 issues) and the full test suite
  pass. The new admin-surface tests are pinned by
  `go test -count=1 -run 'TestForum_TheAdmin|TestForum_TheSettings|TestForum_ThePermission|TestForum_AMemberMayNotSave|TestForum_SavingAPermission|TestForum_TheGroupIDs' ./pb/flarum/`.

### Reference: riff's key files

| Concern | File |
| --- | --- |
| implicit group resolution | `pb/flarum/domain/permissions.go:251` (`groupSet`) |
| the moderator-only check | `pb/flarum/domain/permissions.go:162` (`CanAsModerator`) |
| explicit memberships only | `pb/flarum/repository/repository.go:319` (`UserGroupIDs`) |
| add/remove membership | `pb/flarum/repository/repository.go:345,362` |
| admin group assignment | `pb/flarum/api/handlers_mutations.go::setUserGroups` |
| group ids | `pb/flarum/domain/models.go:278-296` |
| the seed | `pb/flarum/migrations/seed.go:75-120` |
| the user resource | `pb/flarum/api/serialize.go:73` (`userResourceWithGroups`) |
| the users list | `pb/flarum/api/handlers_discussions.go:253` |
| the module's hooks | `pb/flarum/register.go:128` (`Register`), `:144` (`OnServe`) |
| the vendored frontend's group ids | `pb/flarum/frontend/packages/core/src/common/models/Group.ts` (now the port's ids) |
| the admin surface | `pb/flarum/api/handlers_admin.go`, `pb/flarum/api/routes.go` |
| permission-key translation | `pb/flarum/domain/permission_keys.go` |
| the boot payload's permissions | `pb/flarum/routes.go::adminPermissions` |

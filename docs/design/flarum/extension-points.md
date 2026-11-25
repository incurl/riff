# The extension points

Flarum lets an extension patch a class at runtime. Go has no runtime
method patching, so every point Flarum would patch is declared in
advance as one of:

- a **container binding** — an interface a plugin can swap,
- an **event** — a declared interception point,
- an **extender** — a registration call a plugin makes.

## The extender vocabulary

`plugin.Extenders` is the accumulator a plugin registers into. Each
method corresponds to one extender area.

| Method | Extender area | What it does |
| --- | --- | --- |
| `AddRoute` | Routes | Mount an HTTP route under the module root. |
| `AddMigration` | Migrations | Declare a forward-only schema or seed step. |
| `AddAttribute` | Models/fields | Contribute an attribute to a JSON:API resource. |
| `AddRelationship` | Models/fields | Contribute a relationship to a JSON:API resource. |
| `AddPermission` | Permissions | Register a permission handler. |
| `AddAsset` | Frontend assets | Declare the plugin's JS/CSS bundles. |
| `AddSettings` | Settings | Declare the admin settings the plugin reads. |

The container and the event bus are fields on the extender, so a plugin
binds services and subscribes to events through the same object.

### `AddSettings` and the admin form

`AddSettings` declares the settings a plugin **reads**. The declared
schema is the server's half of the contract: it names each setting by
its FULL key (`flarum-likes.like_own_post`, not a name within a group)
and says what the plugin falls back to when the setting is unset.

The module publishes the schema in the admin boot payload with those
defaults, so the form shows the value the forum would actually use.

The form itself is the plugin's frontend bundle's job: the admin
application renders a setting that a bundle registered with
`Extend.Admin().setting(...)` and nothing else. A declared setting
with no bundle is a setting the operator cannot reach, and a registered
setting the server does not read is a control that changes nothing —
so the two halves must agree on the KEY.

A setting the server does not read must NOT be declared. The
`TestForum_ThePluginSettingsAreTheOnesTheServerReads` and
`TestForum_TheAdminBundlesCarryNoDeadSettings` tests pin both
directions. See [the deviations](deviations.md#10a-the-plugin-settings-are-upstreams-keys-but-only-the-ones-the-server-reads).

## The container

A service is bound by its interface type, so a binding is type-safe:

```go
flarumcore.BindPriority[domain.PermissionChecker](ext.Container, myChecker, flarumcore.PriorityPluginHigh)
```

The override order is **core < plugin default < plugin high-priority
override**. Two bindings at the same priority are resolved by
registration order, so the last one registered wins.

## The event bus

Two kinds of hook:

- **Before** — receives a mutable `*flarumcore.Event`; may alter the
  data, attach metadata, or return an error that interrupts the flow.
- **After** — observes a completed action, read-only.

```go
ext.Bus.SubscribeBefore(flarumcore.EventPostCreated, "acme/audit", 0,
    func(ctx context.Context, event *flarumcore.Event) error {
        event.Data["audited"] = true
        return nil
    })
```

The core dispatches at these points:

`forum.user.registered`, `forum.user.updated`,
`forum.discussion.created`, `forum.discussion.locked`,
`forum.discussion.hidden`, `forum.post.created`, `forum.post.edited`,
`forum.post.deleted`, `forum.discussion.tagged`,
`forum.discussion.untagged`, `forum.post.liked`, `forum.post.unliked`,
`forum.permission.checked`, `forum.settings.saved`,
`forum.plugin.booted`.

Every listener is dispatched through a recovered wrapper: a plugin that
panics is logged with its plugin id and does not take the forum down.

## The permission service

`domain.PermissionChecker` is the interface a plugin can replace. The
core implementation resolves Flarum's model: a permission row names a
group, an action and an optional scope, and says whether the group is
allowed. A caller holds the permissions of every group they belong to,
plus the implicit guest and member groups.

A scoped rule applies to its subject only; an unscoped rule applies to
every subject. A scoped allow beats an unscoped deny, which is what
makes a restricted tag work.

## The migration framework

Each plugin declares ordered migration steps. The applied versions live
in `forum_plugin_migrations`, keyed by plugin id and step name, so a
step runs once. The core's migrations run first, then the plugins' in
dependency order.

Migrations are forward-only and idempotent. Rolling a destructive change
back is out of scope for v1.

// Package repository resolves logical forum model names to the physical
// PocketBase collection names that hold them.
//
// The forum shares a PocketBase instance with the rest of the host
// application, so a hard-coded physical collection name is a collision
// hazard: the host may already own a `tags` or a `settings` collection.
// Every forum-owned collection therefore carries a reserved prefix
// (`forum_` by default), and business code never names a physical
// collection directly — it goes through [Registry].
//
// The shared PocketBase `users` auth collection is the deliberate
// exception: it is owned by the host, is not prefixed, and is the forum's
// single account source of truth.
package repository

import (
	"slices"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// DefaultPrefix is the reserved prefix for every forum-owned collection.
const DefaultPrefix = "forum_"

// SharedUsersCollection is the PocketBase built-in auth collection the
// forum reuses as its account source of truth.
//
// It is NOT prefixed and NOT forum-owned: the host owns it, the forum only
// reads the permitted public fields and writes its own extras into
// forum_user_profiles. See the package documentation.
const SharedUsersCollection = "users"

// ExtensionPrefix separates plugin-owned collections from core ones.
const ExtensionPrefix = "forum_ext_"

// Registry resolves logical model names to physical collection names.
//
// The zero value is not usable; call [NewRegistry].
type Registry struct {
	// mu guards the model map.
	mu sync.RWMutex
	// prefix is the reserved prefix for forum-owned collections.
	prefix string
	// models maps a logical name (e.g. "discussions") to its physical
	// collection name (e.g. "forum_discussions").
	models map[string]string
}

// NewRegistry returns a registry that resolves the core logical names.
//
// The prefix defaults to [DefaultPrefix] when empty.
func NewRegistry(prefix string) *Registry {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	r := &Registry{
		prefix: prefix,
		models: map[string]string{},
	}
	for logical, physical := range coreModels {
		r.models[logical] = prefix + physical
	}
	return r
}

// coreModels is the logical-to-physical suffix map for the core forum
// models. The prefix is prepended by NewRegistry.
var coreModels = map[string]string{
	"user_profiles":     "user_profiles",
	"groups":            "groups",
	"group_users":       "group_users",
	"discussions":       "discussions",
	"posts":             "posts",
	"tags":              "tags",
	"discussion_tags":   "discussion_tags",
	"likes":             "likes",
	"permissions":       "permissions",
	"settings":          "settings",
	"plugin_state":      "plugin_state",
	"plugin_migrations": "plugin_migrations",
	"notifications":     "notifications",
	"activity_log":      "activity_log",
}

// coreOwnedModels are the models the CORE migrations create.
//
// The plugin-owned collections (tags, likes) are created by their
// plugins' migrations, not by the core ones, so the core's collision
// pre-flight does not look for them; each plugin checks its own
// collection when it creates it.
var coreOwnedModels = []string{
	"user_profiles",
	"groups",
	"group_users",
	"discussions",
	"posts",
	"discussion_tags",
	"permissions",
	"settings",
	"plugin_state",
	"plugin_migrations",
	"notifications",
	"activity_log",
}

// Prefix returns the reserved prefix for forum-owned collections.
func (r *Registry) Prefix() string {
	if r == nil {
		return DefaultPrefix
	}
	return r.prefix
}

// Physical returns the physical collection name for a logical model name.
//
// An unknown logical name resolves to the prefixed name itself, which is
// what a plugin wants when it declares its own collection: "bookmarks"
// becomes "forum_bookmarks". Callers that need strictness should use
// [Registry.Has].
func (r *Registry) Physical(logical string) string {
	if r == nil {
		return DefaultPrefix + logical
	}
	logical = strings.TrimSpace(logical)
	if logical == "" {
		return ""
	}
	r.mu.RLock()
	physical, known := r.models[logical]
	r.mu.RUnlock()
	if known {
		return physical
	}
	if strings.HasPrefix(logical, r.prefix) || strings.HasPrefix(logical, ExtensionPrefix) {
		// Already physical.
		return logical
	}
	return r.prefix + logical
}

// Has reports whether a logical model name is known to the registry.
func (r *Registry) Has(logical string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.models[strings.TrimSpace(logical)]
	return ok
}

// ExtensionPhysical returns the physical collection name for a
// plugin-owned entity.
//
// The shape is `forum_ext_<slug>_<entity>`, e.g. the acme/bookmarks plugin's
// `bookmarks` entity becomes `forum_ext_acme-bookmarks_bookmarks`.
func (*Registry) ExtensionPhysical(pluginID, entity string) string {
	slug := PluginSlug(pluginID)
	if slug == "" || entity == "" {
		return ""
	}
	return ExtensionPrefix + slug + "_" + entity
}

// PluginSlug reduces a plugin ID to the slug used in collection names.
//
// "acme/bookmarks" becomes "acme-bookmarks"; "acme/tags" becomes "acme-tags".
func PluginSlug(pluginID string) string {
	return strings.ReplaceAll(strings.TrimSpace(pluginID), "/", "-")
}

// ExtensionID reduces a plugin ID to the id the admin application
// addresses an extension by.
//
// It is upstream's `Extension::nameToId()`, and the admin surface
// depends on the exact spelling: the id is a PATH SEGMENT in the
// admin app's extension URL, and the extension map is keyed by it.
//
//	"flarum/tags"               -> "flarum-tags"
//	"flarum/flarum-ext-suspend" -> "flarum-suspend"
//	"some-vendor/foo"           -> "some-vendor-foo"
//
// The `flarum-` and `flarum-ext-` prefixes are stripped because
// upstream strips them, and the plugin's own id is what the catalogue
// and the composer name agree on.
//
// It is NOT [PluginSlug]: that one names a COLLECTION, and a
// collection's name is not a path segment, so it keeps the prefix.
func ExtensionID(pluginID string) string {
	id := strings.TrimSpace(pluginID)
	if id == "" {
		return ""
	}
	if id == "flarum/core" {
		return "core"
	}

	id = strings.Replace(id, "/flarum-ext-", "-", 1)
	id = strings.Replace(id, "/flarum-", "-", 1)
	id = strings.Replace(id, "/", "-", 1)

	return id
}

// Core returns the logical names of every core model, sorted.
func (r *Registry) Core() []string {
	r.mu.RLock()
	names := make([]string, 0, len(r.models))
	for name := range r.models {
		names = append(names, name)
	}
	r.mu.RUnlock()
	slices.Sort(names)
	return names
}

// PhysicalNames returns the physical collection names of the core-owned
// models, sorted. The migration pre-flight check enumerates these to
// detect a collision with a collection the host already owns.
func (r *Registry) PhysicalNames() []string {
	names := make([]string, 0, len(coreOwnedModels))
	for _, name := range coreOwnedModels {
		names = append(names, r.Physical(name))
	}
	slices.Sort(names)
	return names
}

// IsForumOwned reports whether a physical collection name belongs to
// the forum module.
//
// The shared `users` collection is deliberately NOT forum-owned: it is
// the host's. The pre-flight collision check treats a non-forum `users`
// as allowed (it is the documented exception) and every other non-forum
// name as a collision.
//
// A name counts as forum-owned when it carries the forum's prefix or
// the extension prefix: those names are reserved for the forum, so the
// forum may create them and may find them already there from an earlier
// run.
func (r *Registry) IsForumOwned(physical string) bool {
	if physical == SharedUsersCollection {
		return false
	}
	if strings.HasPrefix(physical, r.prefix) || strings.HasPrefix(physical, ExtensionPrefix) {
		return true
	}
	return false
}

// OwnershipMarker is the field a forum collection carries to record
// that the forum created it.
//
// Ownership cannot be inferred from the name: the host may own a
// collection called `forum_tags` without the forum having created it.
// The marker is what the collision check reads.
const OwnershipMarker = "forum_owned"

// MarkOwned records that the forum owns a collection.
//
// The marker is a real field on the collection, because PocketBase
// stores a collection's schema and nothing else: there is no place to
// hang an out-of-band annotation. A collection without the marker was
// not created by the forum, whatever its name says.
//
// The marker is a TEXT field rather than a boolean because a field's
// VALUE lives on the records, not on the collection: a boolean field
// cannot say "this collection is mine" without every record carrying
// the value. The marker's PRESENCE is the signal, and its value names
// the forum version that created the collection.
func MarkOwned(app core.App, collection *core.Collection) error {
	if collection == nil {
		return errors.New("forum: nil collection")
	}
	if collection.Fields.GetByName(OwnershipMarker) == nil {
		collection.Fields.Add(&core.TextField{
			Name: OwnershipMarker,
			Max:  64,
		})
		if err := app.Save(collection); err != nil {
			return err
		}
	}
	return nil
}

// IsOwned reports whether a collection carries the forum's ownership
// marker.
//
// A collection without the marker was not created by the forum, whatever
// its name says.
func IsOwned(collection *core.Collection) bool {
	if collection == nil {
		return false
	}
	return collection.Fields.GetByName(OwnershipMarker) != nil
}

// Register records an explicit logical-to-physical mapping.
//
// It exists for the cases the default map cannot express: a collection
// whose physical name does not follow the prefix convention, such as a
// collection created before the prefix was configured.
func (r *Registry) Register(logical, physical string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models[logical] = physical
}

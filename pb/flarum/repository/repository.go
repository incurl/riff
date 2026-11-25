// Package repository is the forum's PocketBase access layer.
//
// Handlers never write PocketBase-specific queries: they call a service,
// the service calls a repository, and the repository owns the record
// access. Every physical collection name goes through the [Registry], so
// no business code names a collection directly.
//
// # The shared users collection
//
// The forum reuses the PocketBase built-in `users` auth collection as its
// single account source. This package reads the permitted public fields
// from it and NEVER writes to it: the forum's own extras live in
// forum_user_profiles. The reserved auth fields (email, passwordHash,
// tokenKey, verified) are not exposed by any repository method.
package repository

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/incurl/riff/pb/flarum/domain"
)

// Repository is the forum's data access layer.
type Repository struct {
	// app is the PocketBase app.
	app core.App
	// registry resolves logical names to physical collections.
	registry *Registry
}

// New returns a repository.
func New(app core.App, registry *Registry) *Repository {
	return &Repository{app: app, registry: registry}
}

// App returns the underlying PocketBase app.
func (r *Repository) App() core.App { return r.app }

// Registry returns the name registry.
func (r *Repository) Registry() *Registry { return r.registry }

// collection returns a forum collection by logical name.
//
// The lookup goes through the registry, so the physical name is never
// written down in business code.
func (r *Repository) collection(logical string) (*core.Collection, error) {
	physical := r.registry.Physical(logical)
	collection, err := r.app.FindCollectionByNameOrId(physical)
	if err != nil {
		return nil, errors.Wrapf(err, "forum: collection %q is missing", physical)
	}
	return collection, nil
}

// --- users ---------------------------------------------------------------

// UserProfile is the forum-side extras of a shared account.
//
// The account itself lives in the shared `users` collection; this is the
// forum's own record about it.
type UserProfile struct {
	// UserID is the shared record's id.
	UserID string
	// DisplayName overrides the account's display name, empty when the
	// account uses its username.
	DisplayName string
	// Preferences holds the account's forum preferences.
	Preferences map[string]any
	// JoinedAt is when the account joined the forum.
	JoinedAt time.Time
	// LastSeenAt is when the account was last seen.
	LastSeenAt time.Time
	// IsActive reports whether the account is active in the forum.
	IsActive bool
	// IsBlocked reports whether the account is blocked from the forum.
	IsBlocked bool
}

// FindUserProfile returns a user's forum profile.
//
// A user without a profile row is not an error: the profile is created
// lazily, so an account that has never posted has no row yet.
func (r *Repository) FindUserProfile(userID string) (*UserProfile, error) {
	collection, err := r.collection("user_profiles")
	if err != nil {
		return nil, err
	}
	record, err := r.app.FindFirstRecordByFilter(collection, "user = {:user}", map[string]any{"user": userID})
	if err != nil {
		// The user has no profile row yet: it is created lazily.
		return nil, nil //nolint:nilerr // an absent profile is not an error
	}
	return profileFromRecord(record), nil
}

// SaveUserProfile writes a user's forum profile.
//
// It writes only to the forum's own collection: the shared `users`
// record is never touched.
func (r *Repository) SaveUserProfile(profile *UserProfile) error {
	collection, err := r.collection("user_profiles")
	if err != nil {
		return err
	}

	record, err := r.app.FindFirstRecordByFilter(collection, "user = {:user}", map[string]any{"user": profile.UserID})
	if err != nil {
		record = core.NewRecord(collection)
		record.Set("user", profile.UserID)
	}

	record.Set("display_name", profile.DisplayName)
	record.Set("preferences", profile.Preferences)
	record.Set("is_active", profile.IsActive)
	record.Set("is_blocked", profile.IsBlocked)
	if !profile.JoinedAt.IsZero() {
		record.Set("joined_at", profile.JoinedAt)
	}
	if !profile.LastSeenAt.IsZero() {
		record.Set("last_seen_at", profile.LastSeenAt)
	}

	return r.app.Save(record)
}

// decodePreferences reads the preferences column into a map.
//
// The column is a PocketBase JSONField, and `record.Get` hands back the
// stored document as `types.JSONRaw` (a []byte of JSON), NOT as a
// `map[string]any`. A reader that only handles the map therefore reads
// EVERY profile as having no preferences — the values are written
// correctly and then never read back, so a user's privacy, colour
// scheme and haptic preferences silently reset on every page load.
//
// The other shapes are accepted too: a record that has just been
// created in memory carries the value it was set with, and a plain
// string is what a raw column read produces.
func decodePreferences(raw any) map[string]any {
	switch value := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return value
	case types.JSONRaw:
		return decodePreferences([]byte(value))
	case []byte:
		var decoded map[string]any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return nil
		}
		return decoded
	case string:
		var decoded map[string]any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return nil
		}
		return decoded
	default:
		return nil
	}
}

// profileFromRecord projects a forum_user_profiles record.
func profileFromRecord(record *core.Record) *UserProfile {
	profile := &UserProfile{
		UserID:      record.GetString("user"),
		DisplayName: record.GetString("display_name"),
		IsActive:    record.GetBool("is_active"),
		IsBlocked:   record.GetBool("is_blocked"),
	}
	profile.Preferences = decodePreferences(record.Get("preferences"))
	if joined := record.GetDateTime("joined_at"); !joined.IsZero() {
		profile.JoinedAt = joined.Time()
	}
	if seen := record.GetDateTime("last_seen_at"); !seen.IsZero() {
		profile.LastSeenAt = seen.Time()
	}
	return profile
}

// UserByID returns a shared account as a forum user.
//
// Only the permitted public fields are read. The account's forum-side
// extras are merged in from the profile.
func (r *Repository) UserByID(id string) (*domain.User, error) {
	users, err := r.app.FindCollectionByNameOrId(SharedUsersCollection)
	if err != nil {
		return nil, errors.Wrap(err, "forum: the shared users collection is missing")
	}
	record, err := r.app.FindRecordById(users, id)
	if err != nil {
		return nil, errors.Wrap(err, "forum: user not found")
	}
	return r.userFromRecord(record)
}

// ErrUserNotFound reports a lookup that matched no account.
//
// It is a sentinel so a caller can tell "there is no such user" (an
// empty result) from "the lookup failed" (a real error).
var ErrUserNotFound = errors.New("forum: user not found")

// UserBySlug returns the account whose slug is the given value.
//
// The forum's slug is the username (`userResource` reports it as the
// `slug` attribute), and the frontend asks for a profile by slug —
// `GET /api/users/<slug>?bySlug=true` — because the route param is the
// username, not the record id.
func (r *Repository) UserBySlug(slug string) (*domain.User, error) {
	if slug == "" {
		return nil, errors.New("forum: empty user slug")
	}

	users, err := r.app.FindCollectionByNameOrId(SharedUsersCollection)
	if err != nil {
		return nil, errors.Wrap(err, "forum: the shared users collection is missing")
	}

	record, err := r.app.FindFirstRecordByFilter(users, "username = {:slug}", map[string]any{"slug": slug})
	if err != nil {
		return nil, ErrUserNotFound
	}

	return r.userFromRecord(record)
}

// UsersByIDs returns several shared accounts at once.
//
// It is the batch form of [Repository.UserByID], used to resolve the
// authors of a page of posts without one query per post.
func (r *Repository) UsersByIDs(ids []string) (map[string]*domain.User, error) {
	users := map[string]*domain.User{}
	if len(ids) == 0 {
		return users, nil
	}

	unique := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}
	if len(unique) == 0 {
		return users, nil
	}

	collection, err := r.app.FindCollectionByNameOrId(SharedUsersCollection)
	if err != nil {
		return nil, errors.Wrap(err, "forum: the shared users collection is missing")
	}

	for _, id := range unique {
		record, err := r.app.FindRecordById(collection, id)
		if err != nil {
			continue
		}
		user, err := r.userFromRecord(record)
		if err != nil {
			continue
		}
		users[id] = user
	}
	return users, nil
}

// userFromRecord projects a shared account onto a forum user.
//
// The administrator flag is the union of two sources: a PocketBase
// superuser (the instance's operator) and membership in the
// administrator GROUP.
//
// The group matters because upstream derives the flag from group
// membership alone (`User::isAdmin()` is
// `$this->groups->contains(Group::ADMINISTRATOR_ID)`), and the forum's
// administrator group is a real, assignable row. An account an operator
// promoted into that group is an administrator, so the flag has to say
// so — otherwise the `isAdmin` attribute the user resource carries says
// "no" for an administrator, and the frontend's own admin checks
// (`app.session.user?.isAdmin()`) disagree with the server.
func (r *Repository) userFromRecord(record *core.Record) (*domain.User, error) {
	username := strings.TrimSpace(record.GetString("username"))
	if username == "" {
		username = record.Email()
	}

	user := &domain.User{
		ID:          record.Id,
		Username:    username,
		DisplayName: username,
		IsAdmin:     record.Collection().Name == core.CollectionNameSuperusers,
	}

	if avatar := record.GetString("avatar"); avatar != "" {
		user.AvatarURL = avatar
	}

	// The suspension columns live on the shared users record, added by
	// the Suspend plugin's migration. A record that predates them reads
	// as not suspended.
	if until := record.GetDateTime("suspended_until"); !until.IsZero() {
		user.SuspendedUntil = until.Time()
	}
	user.SuspendReason = record.GetString("suspend_reason")
	user.SuspendMessage = record.GetString("suspend_message")

	// The forum-side extras live in the forum's own collection.
	profile, err := r.FindUserProfile(record.Id)
	if err == nil && profile != nil {
		if profile.DisplayName != "" {
			user.DisplayName = profile.DisplayName
		}
		if !profile.JoinedAt.IsZero() {
			user.JoinedAt = profile.JoinedAt
		}
		if !profile.LastSeenAt.IsZero() {
			user.LastSeenAt = profile.LastSeenAt
		}
	}

	groups, err := r.UserGroupIDs(record.Id)
	if err == nil {
		user.Groups = groups
		for _, groupID := range groups {
			if groupID == domain.GroupAdminID {
				user.IsAdmin = true
				break
			}
		}
	}

	return user, nil
}

// --- groups --------------------------------------------------------------

// UserGroupIDs returns the ids of the groups a user belongs to.
//
// The implicit groups (guest, member) are not included: they are a
// permission concept, not a membership row.
func (r *Repository) UserGroupIDs(userID string) ([]string, error) {
	collection, err := r.collection("group_users")
	if err != nil {
		return nil, err
	}
	records, err := r.app.FindRecordsByFilter(collection, "user = {:user}", "", 0, 0, map[string]any{"user": userID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.GetString("group"))
	}
	return ids, nil
}

// AddUserToGroup adds a user to a group.
//
// It is idempotent: adding a user who is already in the group succeeds
// without creating a second row.
func (r *Repository) AddUserToGroup(userID, groupID string) error {
	collection, err := r.collection("group_users")
	if err != nil {
		return err
	}
	existing, err := r.app.FindFirstRecordByFilter(collection, "user = {:user} && group = {:group}",
		map[string]any{"user": userID, "group": groupID})
	if err == nil && existing != nil {
		return nil
	}
	record := core.NewRecord(collection)
	record.Set("user", userID)
	record.Set("group", groupID)
	return r.app.Save(record)
}

// RemoveUserFromGroup removes a user from a group.
func (r *Repository) RemoveUserFromGroup(userID, groupID string) error {
	collection, err := r.collection("group_users")
	if err != nil {
		return err
	}
	record, err := r.app.FindFirstRecordByFilter(collection, "user = {:user} && group = {:group}",
		map[string]any{"user": userID, "group": groupID})
	if err != nil {
		// The membership is already gone: removing it twice is not a
		// failure.
		return nil //nolint:nilerr // an absent membership is already removed
	}
	return r.app.Delete(record)
}

// FindGroup returns a group by id.
func (r *Repository) FindGroup(id string) (*domain.Group, error) {
	collection, err := r.collection("groups")
	if err != nil {
		return nil, err
	}
	record, err := r.app.FindRecordById(collection, id)
	if err != nil {
		return nil, errors.Wrap(err, "forum: group not found")
	}
	return groupFromRecord(record), nil
}

// ListGroups returns every group.
func (r *Repository) ListGroups() ([]*domain.Group, error) {
	collection, err := r.collection("groups")
	if err != nil {
		return nil, err
	}
	records, err := r.app.FindAllRecords(collection)
	if err != nil {
		return nil, err
	}
	groups := make([]*domain.Group, 0, len(records))
	for _, record := range records {
		groups = append(groups, groupFromRecord(record))
	}
	return groups, nil
}

// groupFromRecord projects a groups record.
func groupFromRecord(record *core.Record) *domain.Group {
	return &domain.Group{
		ID:           record.Id,
		NameSingular: record.GetString("name_singular"),
		NamePlural:   record.GetString("name_plural"),
		Color:        record.GetString("color"),
		Icon:         record.GetString("icon"),
		IsHidden:     record.GetBool("is_hidden"),
		Position:     record.GetInt("position"),
		MemberCount:  record.GetInt("member_count"),
	}
}

// SetGroupOrder writes the groups' positions.
//
// The order is the list of group ids, and a group's position is its
// index in that list — upstream's `OrderGroupsController` writes the
// array index to `position` and clears every position first, so a group
// that is not named ends up with no position and sorts last
// (`sortGroups` in the frontend puts a null position after every
// numbered one).
//
// This port's `position` is a PocketBase NumberField and cannot hold
// null, so "no position" is expressed instead as a position AFTER the
// named ones, keeping the unnamed groups in their previous relative
// order. The result is the same total order upstream produces: the
// named groups first, in the requested order, then everything else.
//
// The writes are in one transaction: a partially applied reorder would
// leave the admin's group bar in an order that matches neither the
// request nor the old one.
//
// A group id that does not exist is ignored rather than rejected: the
// request names the groups the admin can see, and upstream's write is
// per-group, so an id that is not there simply matches no row.
func (r *Repository) SetGroupOrder(order []string) error {
	collection, err := r.collection("groups")
	if err != nil {
		return err
	}

	return r.app.RunInTransaction(func(txApp core.App) error {
		records, err := txApp.FindAllRecords(collection)
		if err != nil {
			return err
		}

		byID := make(map[string]*core.Record, len(records))
		for _, record := range records {
			byID[record.Id] = record
		}

		positioned := make(map[string]bool, len(order))
		next := 0
		for _, id := range order {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			record, ok := byID[id]
			if !ok {
				// The group is gone: upstream's per-group update
				// matches no row either.
				continue
			}
			if positioned[id] {
				// A duplicated id keeps its first position.
				continue
			}
			record.Set("position", next)
			if err := txApp.Save(record); err != nil {
				return err
			}
			positioned[id] = true
			next++
		}

		// The groups the request did not name follow, in their
		// previous order. They are ordered by their current position
		// first, then by id, so two groups that share a position
		// still land in a stable order.
		unnamed := make([]*core.Record, 0, len(records))
		for _, record := range records {
			if positioned[record.Id] {
				continue
			}
			unnamed = append(unnamed, record)
		}
		slices.SortStableFunc(unnamed, func(left, right *core.Record) int {
			if diff := cmp.Compare(left.GetInt("position"), right.GetInt("position")); diff != 0 {
				return diff
			}
			return strings.Compare(left.Id, right.Id)
		})

		for _, record := range unnamed {
			record.Set("position", next)
			if err := txApp.Save(record); err != nil {
				return err
			}
			next++
		}

		return nil
	})
}

// --- discussions ---------------------------------------------------------

// TagUntagged is the pseudo-tag `filter[tag]` accepts for "carries no tag
// at all".
//
// It is upstream's `untagged` value (`TagFilter::constrain`), and it is
// resolved before any slug lookup, so a real tag whose slug happens to be
// `untagged` cannot shadow it.
const TagUntagged = "untagged"

// TagFilter is one OR-GROUP of `filter[tag]`.
//
// A discussion satisfies it when it carries at least one of the members —
// upstream's nested `where` closure, whose members are
// `whereIn(..., 'or', $negate)`.
type TagFilter struct {
	// Members are tag record ids, or [TagUntagged].
	Members []string
	// Negated inverts every membership test (`filter[-tag]`).
	//
	// Each MEMBER is negated separately and the results are still OR-ed,
	// exactly as upstream's `whereIn(..., 'or', $negate)` renders it — so
	// `-tag:a,b` excludes only the discussions that carry BOTH. Faithful to
	// upstream rather than intuitive, because a client's filter has to mean
	// the same thing on both sides.
	Negated bool
}

// DiscussionFilter narrows a discussion listing.
type DiscussionFilter struct {
	// UserID limits the listing to discussions the user started.
	UserID string
	// TagFilters is upstream's `filter[tag]`, one entry per OR-GROUP.
	//
	// A discussion must satisfy EVERY entry — upstream's `TagFilter` nests
	// one `where` closure per group and Laravel ANDs the closures, which is
	// why the client sends `{tag: ['foo,bar', 'baz']}` for `tag:foo,bar
	// tag:baz` ("(foo OR bar) AND baz"). Two filters with the same key but
	// opposite negation (`tag:a -tag:b`) are two entries here, because
	// upstream keeps one Filter object per value.
	TagFilters []TagFilter
	// State limits the listing to discussions in that state.
	State string
	// Search matches the title or the first post's content.
	Search string
	// IncludeHidden includes hidden discussions. Only a moderator may
	// ask for them.
	IncludeHidden bool
	// ExcludeHiddenTags drops every discussion that carries a HIDDEN tag.
	//
	// It is upstream's `HideHiddenTagsFromAllDiscussionsPage`, and it applies
	// to the ALL-DISCUSSIONS page only: the caller sets it when no filter was
	// asked for. A tag page must keep it off, or the tag's own page goes blank
	// as soon as an operator hides the tag.
	ExcludeHiddenTags bool
	// Sort is the ordering the caller asked for.
	//
	// It is upstream's sort name, with a leading "-" for a descending
	// order (`-lastPostedAt`, `-commentCount`, `createdAt`, `title`).
	// The frontend's index page sends one of those, and upstream's
	// `DiscussionResource::sorts()` names the same set. An unknown sort
	// falls back to the most recent activity.
	Sort string
}

// discussionOrderBy maps a sort name to the database ordering.
//
// The names are upstream's (`DiscussionResource::sorts()`): a bare name
// sorts ascending, a leading "-" sorts descending. They are the values
// the frontend's index page sends (`map.latest = '-lastPostedAt'`,
// `map.top = '-commentCount'`, `map.newest = '-createdAt'`,
// `map.oldest = 'createdAt'`, `map.az = 'title'`, `map.za = '-title'`).
//
// The column names are the forum's own: the API speaks upstream's names
// and the database speaks the collection's.
//
// An unknown sort falls back to the most recent activity, which is
// upstream's default (`defaultSort('-lastPostedAt')`).
func discussionOrderBy(sort string) string {
	descending := strings.HasPrefix(sort, "-")
	name := strings.TrimPrefix(sort, "-")

	direction := "ASC"
	if descending {
		direction = "DESC"
	}

	switch name {
	case "lastPostedAt":
		return "last_posted_at " + direction
	case "commentCount":
		return "comment_count " + direction
	case "createdAt":
		return "created " + direction
	case "title":
		return "title " + direction
	default:
		return "last_posted_at DESC"
	}
}

// ListDiscussions returns the discussions matching a filter.
//
// The listing is sorted by the most recent activity, which is what the
// frontend's index page expects.
func (r *Repository) ListDiscussions(filter DiscussionFilter, limit, offset int) ([]*domain.Discussion, int, error) {
	collection, err := r.collection("discussions")
	if err != nil {
		return nil, 0, err
	}

	clauses := []string{}
	params := map[string]any{}
	extra := []dbx.Expression{}
	if filter.UserID != "" {
		clauses = append(clauses, "user = {:user}")
		params["user"] = filter.UserID
	}
	if filter.State != "" {
		clauses = append(clauses, "state = {:state}")
		params["state"] = filter.State
	} else if !filter.IncludeHidden {
		clauses = append(clauses, "state != {:hidden}")
		params["hidden"] = domain.DiscussionHidden
	}
	if filter.Search != "" {
		clauses = append(clauses, "title LIKE {:search}")
		params["search"] = "%" + filter.Search + "%"
	}

	// A hidden TAG hides the discussions that carry it — upstream's
	// `HideHiddenTagsFromAllDiscussionsPage`. The caller sets the flag only
	// for the unfiltered listing, so a tag page still shows its own tag's
	// discussions even when that tag is hidden.
	if filter.ExcludeHiddenTags {
		hiddenTagIDs, err := r.hiddenTagIDs()
		if err != nil {
			return nil, 0, err
		}
		if len(hiddenTagIDs) > 0 {
			exclusion, err := r.tagMembershipExpression(hiddenTagIDs, true)
			if err != nil {
				return nil, 0, err
			}
			extra = append(extra, exclusion)
		}
	}

	// The tag filter is a clause too, not a post-filter over the fetched
	// page: a page-level filter would only ever see `limit` rows, so a tag
	// page could show nothing while the tag has discussions, and `total`
	// would count every discussion rather than the tagged ones.
	//
	// One clause per OR-group, AND-ed — upstream's `filter[tag]` semantics.
	for index, tagFilter := range filter.TagFilters {
		membership, err := r.tagGroupExpression(index, tagFilter.Members, tagFilter.Negated)
		if err != nil {
			return nil, 0, err
		}
		extra = append(extra, membership)
	}

	// The clauses are joined with SQL's `AND`, not PocketBase's filter
	// `&&`: `CountRecords` and `RecordQuery` take dbx expressions, which
	// reach the driver as written. With two clauses the filter used to be
	// `a && b`, which SQLite rejects with "near \"&\": syntax error" — so
	// any listing combining two filters (`filter[user]` with the state
	// clause, or a search term) answered 500.
	filterExpr := strings.Join(clauses, " AND ")

	// The count carries the extra clauses too, so `total` counts the rows the
	// page can actually contain.
	total, err := r.countRecords(collection, filterExpr, params, extra...)
	if err != nil {
		return nil, 0, err
	}

	query := r.app.RecordQuery(collection)
	if filterExpr != "" {
		query = query.AndWhere(dbxExp(filterExpr, params))
	}
	for _, expression := range extra {
		query = query.AndWhere(expression)
	}
	query = query.OrderBy(discussionOrderBy(filter.Sort)).Limit(int64(limit)).Offset(int64(offset))
	var records []*core.Record
	if err := query.All(&records); err != nil {
		return nil, 0, err
	}

	discussions := make([]*domain.Discussion, 0, len(records))
	for _, record := range records {
		discussions = append(discussions, r.discussionFromRecord(record))
	}

	return discussions, total, nil
}

// FindDiscussion returns a discussion by id.
func (r *Repository) FindDiscussion(id string) (*domain.Discussion, error) {
	collection, err := r.collection("discussions")
	if err != nil {
		return nil, err
	}
	record, err := r.app.FindRecordById(collection, id)
	if err != nil {
		return nil, errors.Wrap(err, "forum: discussion not found")
	}
	return r.discussionFromRecord(record), nil
}

// discussionFromRecord projects a discussions record.
func (*Repository) discussionFromRecord(record *core.Record) *domain.Discussion {
	discussion := &domain.Discussion{
		ID:               record.Id,
		Title:            record.GetString("title"),
		Slug:             record.GetString("slug"),
		UserID:           record.GetString("user"),
		LastPostedUserID: record.GetString("last_posted_user"),
		LastPostNumber:   record.GetInt("last_post_number"),
		CommentCount:     record.GetInt("comment_count"),
		ParticipantCount: record.GetInt("participant_count"),
		State:            record.GetString("state"),
		FirstPostID:      record.GetString("first_post"),
		LastPostID:       record.GetString("last_post"),
		ViewCount:        record.GetInt("view_count"),
	}

	if created := record.GetDateTime("created_at"); !created.IsZero() {
		discussion.CreatedAt = created.Time()
	}
	if lastPosted := record.GetDateTime("last_posted_at"); !lastPosted.IsZero() {
		discussion.LastPostedAt = lastPosted.Time()
	}
	if hidden := record.GetDateTime("hidden_at"); !hidden.IsZero() {
		discussion.HiddenAt = hidden.Time()
		discussion.IsHidden = true
	}
	discussion.HiddenUserID = record.GetString("hidden_user")

	// Locking and pinning are their own columns, as upstream models them
	// (`is_locked`, `is_sticky`).
	//
	// The STATE check is a fallback for rows written before those columns
	// existed, when the two were encoded in `state`: without it an
	// already-locked discussion would silently become replyable on
	// upgrade. The migration backfills the columns, so this only matters
	// for a row the backfill could not see.
	discussion.IsLocked = record.GetBool("is_locked") || discussion.State == domain.DiscussionLocked
	discussion.IsSticky = record.GetBool("is_sticky") || discussion.State == domain.DiscussionSticky

	return discussion
}

// --- posts ---------------------------------------------------------------

// ListPosts returns the posts of a discussion, in order.
func (r *Repository) ListPosts(discussionID string, limit, offset int) ([]*domain.Post, error) {
	collection, err := r.collection("posts")
	if err != nil {
		return nil, err
	}
	query := r.app.RecordQuery(collection).
		AndWhere(dbxExp("discussion = {:discussion}", map[string]any{"discussion": discussionID})).
		OrderBy("number ASC").
		Limit(int64(limit)).
		Offset(int64(offset))
	var records []*core.Record
	if err := query.All(&records); err != nil {
		return nil, err
	}
	posts := make([]*domain.Post, 0, len(records))
	for _, record := range records {
		posts = append(posts, postFromRecord(record))
	}
	return posts, nil
}

// PostsByIDs returns several posts at once.
//
// It is the batch form of [Repository.FindPost], and it answers the
// frontend's `filter[id]` lookup: the post stream asks for a WINDOW of
// posts by id (`app.store.find('posts', loadIds)` builds
// `?filter[id]=a,b,c`), because the discussion's `posts` relationship
// names them all but carries no attributes.
//
// The posts come back in the order they were asked for, because the
// stream sorts by the post NUMBER and the numbers are the order the
// discussion holds them in.
func (r *Repository) PostsByIDs(ids []string) ([]*domain.Post, error) {
	collection, err := r.collection("posts")
	if err != nil {
		return nil, err
	}

	posts := make([]*domain.Post, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		record, err := r.app.FindRecordById(collection, id)
		if err != nil {
			// A post that is gone is simply absent: the stream shows
			// what is left.
			continue
		}
		posts = append(posts, postFromRecord(record))
	}
	return posts, nil
}

// ListPostsByAuthor returns a user's posts, newest first.
//
// The user-page filter is `filter[author]=<slug>`, where the slug is the
// username, and a post stores its author as the `user` relation — so the
// slug is resolved to a record id first and the query filters on that.
//
// `postType` narrows by the post's `type` (the frontend sends `comment`
// to leave out event posts); an empty value means every type.
//
// An unknown slug yields an empty page rather than an error: the feed is
// a listing, and a user with no posts and a user that does not exist
// read the same way to the caller.
func (r *Repository) ListPostsByAuthor(slug, postType string, limit, offset int) ([]*domain.Post, error) {
	collection, err := r.collection("posts")
	if err != nil {
		return nil, err
	}

	// No such user is an empty page: the feed is a listing, and a user
	// with no posts and a user that does not exist read the same way to
	// the caller. A real failure still surfaces.
	author, lookupErr := r.UserBySlug(slug)
	if errors.Is(lookupErr, ErrUserNotFound) {
		return []*domain.Post{}, nil
	}
	if lookupErr != nil {
		return nil, lookupErr
	}

	filter := "user = {:user}"
	params := map[string]any{"user": author.ID}
	if postType != "" {
		filter += " AND type = {:type}"
		params["type"] = postType
	}

	query := r.app.RecordQuery(collection).
		AndWhere(dbxExp(filter, params)).
		OrderBy("created_at DESC").
		Limit(int64(limit)).
		Offset(int64(offset))

	var records []*core.Record
	if err := query.All(&records); err != nil {
		return nil, err
	}

	posts := make([]*domain.Post, 0, len(records))
	for _, record := range records {
		posts = append(posts, postFromRecord(record))
	}
	return posts, nil
}

// FindPost returns a post by id.
func (r *Repository) FindPost(id string) (*domain.Post, error) {
	collection, err := r.collection("posts")
	if err != nil {
		return nil, err
	}
	record, err := r.app.FindRecordById(collection, id)
	if err != nil {
		return nil, errors.Wrap(err, "forum: post not found")
	}
	return postFromRecord(record), nil
}

// postFromRecord projects a posts record.
func postFromRecord(record *core.Record) *domain.Post {
	post := &domain.Post{
		ID:           record.Id,
		DiscussionID: record.GetString("discussion"),
		UserID:       record.GetString("user"),
		Number:       record.GetInt("number"),
		Content:      record.GetString("content"),
		ContentHTML:  record.GetString("content_html"),
		ContentType:  record.GetString("content_type"),
		EditedUserID: record.GetString("edited_user"),
		HiddenUserID: record.GetString("hidden_user"),
		IsHidden:     record.GetBool("is_hidden"),
		IPAddress:    record.GetString("ip_address"),
		Type:         record.GetString("type"),
	}
	if created := record.GetDateTime("created_at"); !created.IsZero() {
		post.CreatedAt = created.Time()
	}
	if edited := record.GetDateTime("edited_at"); !edited.IsZero() {
		post.EditedAt = edited.Time()
	}
	if hidden := record.GetDateTime("hidden_at"); !hidden.IsZero() {
		post.HiddenAt = hidden.Time()
	}
	return post
}

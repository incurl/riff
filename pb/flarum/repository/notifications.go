package repository

import (
	"time"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/domain"
)

// ListUsers returns the accounts of the shared users collection.
//
// The forum does not own this collection, so the listing is read-only
// and unfiltered: the shared collection's own rules decide who may read
// an account, and the forum's permission service decides who may see
// the listing at all.
func (r *Repository) ListUsers(limit, offset int) ([]*domain.User, error) {
	collection, err := r.app.FindCollectionByNameOrId(SharedUsersCollection)
	if err != nil {
		return nil, err
	}

	query := r.app.RecordQuery(collection).OrderBy("username ASC").Limit(int64(limit)).Offset(int64(offset))
	var records []*core.Record
	if err := query.All(&records); err != nil {
		return nil, err
	}

	users := make([]*domain.User, 0, len(records))
	for _, record := range records {
		user, err := r.userFromRecord(record)
		if err != nil {
			continue
		}
		users = append(users, user)
	}
	return users, nil
}

// ListNotifications returns a user's notifications, newest first.
func (r *Repository) ListNotifications(userID string, limit, offset int) ([]*domain.Notification, int, error) {
	collection, err := r.collection("notifications")
	if err != nil {
		return nil, 0, err
	}

	filter := "user = {:user}"
	params := map[string]any{"user": userID}

	total, err := r.countRecords(collection, filter, params)
	if err != nil {
		return nil, 0, err
	}

	query := r.app.RecordQuery(collection).
		AndWhere(dbxExp(filter, params)).
		OrderBy("created_at DESC").
		Limit(int64(limit)).
		Offset(int64(offset))
	var records []*core.Record
	if err := query.All(&records); err != nil {
		return nil, 0, err
	}

	notifications := make([]*domain.Notification, 0, len(records))
	for _, record := range records {
		notifications = append(notifications, notificationFromRecord(record))
	}
	return notifications, total, nil
}

// MarkNotificationRead marks one of a user's notifications as read.
//
// It refuses a notification that belongs to somebody else, so the
// caller cannot touch another account's inbox.
func (r *Repository) MarkNotificationRead(userID, notificationID string) error {
	collection, err := r.collection("notifications")
	if err != nil {
		return err
	}
	record, err := r.app.FindRecordById(collection, notificationID)
	if err != nil {
		return err
	}
	if record.GetString("user") != userID {
		return errNotOwned
	}
	if record.GetDateTime("read_at").IsZero() {
		record.Set("read_at", nowUTC())
	}
	return r.app.Save(record)
}

// MarkAllNotificationsRead marks every notification of a user as read.
func (r *Repository) MarkAllNotificationsRead(userID string) error {
	collection, err := r.collection("notifications")
	if err != nil {
		return err
	}
	records, err := r.app.FindRecordsByFilter(collection, "user = {:user}", "", 0, 0, map[string]any{"user": userID})
	if err != nil {
		return err
	}
	for _, record := range records {
		if !record.GetDateTime("read_at").IsZero() {
			continue
		}
		record.Set("read_at", nowUTC())
		if err := r.app.Save(record); err != nil {
			return err
		}
	}
	return nil
}

// DeleteAllNotifications removes every notification of a user.
//
// It is upstream's `DeleteAllNotifications` command: the frontend's
// notification page has a "delete all" button, and it calls
// `DELETE /api/notifications`.
//
// Only the caller's OWN notifications are removed: the filter names the
// user, so one account can never delete another's.
func (r *Repository) DeleteAllNotifications(userID string) error {
	collection, err := r.collection("notifications")
	if err != nil {
		return err
	}

	records, err := r.app.FindRecordsByFilter(collection, "user = {:user}", "", 0, 0, map[string]any{"user": userID})
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := r.app.Delete(record); err != nil {
			return err
		}
	}
	return nil
}

// notificationFromRecord projects a notifications record.
func notificationFromRecord(record *core.Record) *domain.Notification {
	notification := &domain.Notification{
		ID:           record.Id,
		UserID:       record.GetString("user"),
		Type:         record.GetString("type"),
		DiscussionID: record.GetString("discussion"),
		PostID:       record.GetString("post"),
		Subject:      record.GetString("subject"),
		Excerpt:      record.GetString("excerpt"),
		FromUserID:   record.GetString("from_user"),
	}
	if read := record.GetDateTime("read_at"); !read.IsZero() {
		notification.ReadAt = read.Time()
		notification.IsRead = true
	}
	if created := record.GetDateTime("created_at"); !created.IsZero() {
		notification.CreatedAt = created.Time()
	}
	return notification
}

// CreateNotification inserts a notification.
//
// It is the single authorized insert path: the collection's create rule
// is nil, so only the server writes rows.
//
// The recipient's PREFERENCE is consulted here rather than at each call
// site, because every kind the forum sends has to honour it and a check that
// lives beside one producer is a check the next producer forgets. A reader
// who turned the kind off gets no row at all — upstream's
// `NotificationSyncer`, which drops a notification whose
// `notify_{type}_{driver}` preference is false.
func (r *Repository) CreateNotification(notification *domain.Notification) error {
	if !r.NotificationEnabled(notification.UserID, notification.Type) {
		return nil
	}

	collection, err := r.collection("notifications")
	if err != nil {
		return err
	}
	record := core.NewRecord(collection)
	record.Set("user", notification.UserID)
	record.Set("type", notification.Type)
	if notification.DiscussionID != "" {
		record.Set("discussion", notification.DiscussionID)
	}
	if notification.PostID != "" {
		record.Set("post", notification.PostID)
	}
	record.Set("subject", notification.Subject)
	record.Set("excerpt", notification.Excerpt)
	if notification.FromUserID != "" {
		record.Set("from_user", notification.FromUserID)
	}
	return r.app.Save(record)
}

// errNotOwned reports an attempt to touch another account's row.
var errNotOwned = errors.New("forum: the record belongs to another account")

// NotificationPreferenceKey is the preference key for one notification
// kind on one channel.
//
// It is upstream's `NotificationGrid::preferenceKey` spelling —
// `notify_{type}_{driver}` — because the settings page WRITES that key and
// the server has to read the same one. `driver` is "alert" for the bell,
// "email" for the mail.
func NotificationPreferenceKey(kind, channel string) string {
	return "notify_" + kind + "_" + channel
}

// SetNotificationDefaults records the enabled notification preferences.
//
// The module calls it after the plugins load, with one key per declared
// (kind, channel) pair. It REPLACES whatever was there, so a reload is
// idempotent.
func (r *Repository) SetNotificationDefaults(defaults map[string]bool) {
	r.notificationDefaults = defaults
}

// NotificationPreferences returns a user's notification preferences with the
// forum's defaults filled in.
//
// A preference the reader has never touched is absent from the profile, so
// the map the settings page reads would render every checkbox disabled. The
// defaults supply the missing keys; the reader's own answer wins where there
// is one.
//
// It returns the full preferences map (not only the notification keys),
// because the settings page saves the map it was given: a merged map that
// dropped the rest would erase the colour-scheme and haptic preferences with
// the next save.
func (r *Repository) NotificationPreferences(userID string) map[string]any {
	preferences := map[string]any{}

	for key, enabled := range r.notificationDefaults {
		preferences[key] = enabled
	}

	profile, err := r.FindUserProfile(userID)
	if err != nil || profile == nil {
		return preferences
	}

	for key, value := range profile.Preferences {
		preferences[key] = value
	}

	return preferences
}

// NotificationEnabled reports whether a user wants a notification kind, on
// the alert channel.
//
// A key that is not set answers the DEFAULT: on. That is the direction the
// forum must fail in — a preference that cannot be read is a preference the
// reader kept, and swallowing a notification because a profile row is
// missing would lose a message nobody asked to lose.
func (r *Repository) NotificationEnabled(userID, kind string) bool {
	if userID == "" || kind == "" {
		return false
	}

	key := NotificationPreferenceKey(kind, "alert")

	profile, err := r.FindUserProfile(userID)
	if err != nil || profile == nil {
		return r.defaultNotificationEnabled(key)
	}

	if value, ok := profile.Preferences[key]; ok {
		return truthyPreference(value)
	}

	return r.defaultNotificationEnabled(key)
}

// defaultNotificationEnabled answers a preference key from the defaults,
// treating an undeclared key as enabled.
func (r *Repository) defaultNotificationEnabled(key string) bool {
	if r.notificationDefaults == nil {
		return true
	}

	enabled, ok := r.notificationDefaults[key]
	if !ok {
		return true
	}

	return enabled
}

// truthyPreference reads a stored preference.
//
// The `preferences` column is a PocketBase JSONField, so a boolean arrives as
// a bool — but a client may also have written it as a number or a string, and
// a reader that only handled one of those would silently ignore the answer.
func truthyPreference(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != "" && typed != "0" && typed != "false"
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return true
	}
}

// nowUTC returns the current time, used for the read marker.
func nowUTC() time.Time { return time.Now().UTC() }

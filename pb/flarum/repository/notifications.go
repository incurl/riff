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
func (r *Repository) CreateNotification(notification *domain.Notification) error {
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

// nowUTC returns the current time, used for the read marker.
func nowUTC() time.Time { return time.Now().UTC() }

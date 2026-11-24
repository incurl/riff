package likes

import (
	"context"

	"github.com/pocketbase/pocketbase/core"

	flarumcore "github.com/incurl/riff/pb/flarum/core"
	"github.com/incurl/riff/pb/flarum/domain"
	"github.com/incurl/riff/pb/flarum/repository"
)

// NotificationType is the kind of notification a like produces.
//
// It is upstream's `PostLikedBlueprint::getType()`, and the forum's
// notifications collection already allows it.
const NotificationType = "postLiked"

// notifyPostLiked tells the post's author that somebody liked it.
//
// It is upstream's `SendNotificationWhenPostIsLiked`: the AUTHOR of
// the post is notified, and NOT the liker — a like on one's own post
// notifies nobody.
//
// A failure is not fatal: the like is recorded either way, and a
// notification that could not be written is not worth failing the
// request for.
func notifyPostLiked(_ context.Context, app core.App, repo *repository.Repository, postID, actorID string) {
	if repo == nil {
		return
	}

	author, err := postAuthor(app, postID)
	if err != nil || author == "" || author == actorID {
		// No author, or the liker IS the author: nobody is notified.
		return
	}

	discussionID, err := postDiscussion(app, postID)
	if err != nil {
		return
	}

	subject, err := postExcerpt(app, postID)
	if err != nil {
		return
	}

	notification := &domain.Notification{
		UserID:       author,
		Type:         NotificationType,
		DiscussionID: discussionID,
		PostID:       postID,
		Subject:      subject,
		FromUserID:   actorID,
	}

	// The write is best-effort: the like is already recorded, and a
	// notification that could not be written must not undo it.
	_ = repo.CreateNotification(notification)
}

// postDiscussion returns the discussion a post belongs to.
func postDiscussion(app core.App, postID string) (string, error) {
	collection, err := app.FindCollectionByNameOrId("forum_posts")
	if err != nil {
		return "", err
	}

	record, err := app.FindRecordById(collection, postID)
	if err != nil {
		return "", err
	}

	return record.GetString("discussion"), nil
}

// postExcerpt returns the summary a notification carries.
//
// It is the post's first line, because a notification shows a short
// excerpt of what was liked.
func postExcerpt(app core.App, postID string) (string, error) {
	collection, err := app.FindCollectionByNameOrId("forum_posts")
	if err != nil {
		return "", err
	}

	record, err := app.FindRecordById(collection, postID)
	if err != nil {
		return "", err
	}

	content := record.GetString("content")
	if len(content) > 120 {
		content = content[:120]
	}

	return content, nil
}

// dispatchLiked announces that a post was liked.
//
// It is upstream's `PostWasLiked` event, and the notification it
// produces is upstream's `SendNotificationWhenPostIsLiked`.
func (p *Plugin) dispatchLiked(e *core.RequestEvent, postID, actorID string) {
	if p.bus != nil {
		p.bus.DispatchAfter(e.Request.Context(), flarumcore.Event{
			Name:   flarumcore.EventPostLiked,
			Actor:  actorID,
			Target: postID,
		})
	}

	notifyPostLiked(e.Request.Context(), e.App, p.repo, postID, actorID)
}

// dispatchUnliked announces that a post was unliked.
//
// It is upstream's `PostWasUnliked` event. There is no notification
// for it: upstream's `SendNotificationWhenPostIsUnliked` DELETES the
// notification the like produced, and this port does not remove a
// notification the reader has already seen.
func (p *Plugin) dispatchUnliked(e *core.RequestEvent, postID, actorID string) {
	if p.bus == nil {
		return
	}

	p.bus.DispatchAfter(e.Request.Context(), flarumcore.Event{
		Name:   flarumcore.EventPostUnliked,
		Actor:  actorID,
		Target: postID,
	})
}

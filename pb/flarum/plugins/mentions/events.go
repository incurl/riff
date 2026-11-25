package mentions

import (
	"github.com/incurl/riff/markdown"
	flarumcore "github.com/incurl/riff/pb/flarum/core"
	"github.com/incurl/riff/pb/flarum/domain"
)

// handlePostWritten syncs a post's mentions and notifies the accounts it
// mentions.
//
// It runs AFTER the post was committed, so a failure here must not fail
// the write: the post is already saved, and a mention that could not be
// stored is not worth reporting to the author.
//
// The mentions are re-extracted from the post's STORED content rather
// than from the request, so:
//   - an edit that changed nothing keeps the same set;
//   - a post created by another path is covered too.
func (p *Plugin) handlePostWritten(event flarumcore.Event) {
	if p.repo == nil || event.Target == "" {
		return
	}

	post, err := p.repo.FindPost(event.Target)
	if err != nil || post == nil {
		return
	}

	usernames := extractMentions(post.Content)
	resolved := p.resolveMentions(usernames)

	// The set is REPLACED, not merged: upstream's
	// `UpdateMentionsMetadataWhenVisible` syncs on Posted/Revised/
	// Restored, so a mention the edit removed is gone. A merge would
	// keep it.
	added, err := syncMentions(p.repo.App(), post.ID, resolved)
	if err != nil {
		return
	}

	// Only the mentions that are NEW are notified: a mention the post
	// already had is not a new mention.
	p.notifyMentioned(post, added)
}

// extractMentions returns the usernames a post's content mentions.
//
// The parser is riff's own markdown engine, the same one the memos
// layer uses. It recognises the BARE form only (`@username`), which is
// the subset this plugin stores.
func extractMentions(content string) []string {
	extracted, err := markdown.NewService(markdown.WithMentionExtension()).
		ExtractAll([]byte(content))
	if err != nil {
		return nil
	}
	return extracted.Mentions
}

// resolveMentions turns the usernames into account ids.
//
// A username that does not resolve is DROPPED: a mention of a missing
// account is inert — it must not fail the save, and it must not notify
// anybody.
//
// The result is deduplicated: a post that mentions the same account
// twice mentions it once, and the unique index would refuse the second
// row anyway.
func (p *Plugin) resolveMentions(usernames []string) []string {
	resolved := make([]string, 0, len(usernames))
	seen := make(map[string]struct{}, len(usernames))

	for _, username := range usernames {
		userID, ok := resolveUsername(p.repo.App(), username)
		if !ok {
			continue
		}
		if _, duplicate := seen[userID]; duplicate {
			continue
		}
		seen[userID] = struct{}{}
		resolved = append(resolved, userID)
	}

	return resolved
}

// notifyMentioned tells the mentioned accounts that they were mentioned.
//
// It is upstream's `SendNotificationWhenPostIsMentioned`: the MENTIONED
// account is notified, and NOT the author — a mention of oneself in
// one's own post notifies nobody.
//
// **ONLY A MENTION THAT IS NEW IS NOTIFIED.** The notification is one
// per (post, mentioned account): a post that is edited, and edited
// again, must not fill the reader's inbox with copies of the same
// mention. The `added` list is what the sync reports as new, so a
// mention the post already had is not notified again.
//
// The write is best-effort: the mention is already recorded, and a
// notification that could not be written must not undo it.
func (p *Plugin) notifyMentioned(post *domain.Post, added []string) {
	if p.repo == nil || len(added) == 0 {
		return
	}

	for _, userID := range added {
		if userID == post.UserID {
			// The author mentioned itself: a real reference, and one
			// that is stored — but not one that is notified.
			continue
		}

		notification := &domain.Notification{
			UserID:       userID,
			Type:         NotificationType,
			DiscussionID: post.DiscussionID,
			PostID:       post.ID,
			Subject:      postExcerpt(post.Content),
			FromUserID:   post.UserID,
		}

		_ = p.repo.CreateNotification(notification)
	}
}

// postExcerpt returns the summary a notification carries.
//
// It is the post's first line, because a notification shows a short
// excerpt of what was mentioned.
func postExcerpt(content string) string {
	if len(content) > 120 {
		content = content[:120]
	}
	return content
}

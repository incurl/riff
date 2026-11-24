package likes

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
)

// likeOwnPostAllowed reports whether an account may like its own post.
//
// It is upstream's `LikePostPolicy::like()`: the author of a post may
// not like it UNLESS the `flarum-likes.like_own_post` setting permits
// it, and upstream's default is TRUE.
//
// The setting is read from the forum's settings table, and an UNSET
// setting falls back to upstream's default — so a fresh instance
// behaves like an upstream one.
func likeOwnPostAllowed(e *core.RequestEvent) bool {
	return likeOwnPostAllowedAt(e.App, SettingLikeOwnPost, true)
}

// likeOwnPostAllowedAt reads the setting without a request event.
//
// The attribute contributor has no request event: it runs from the
// serializer, and it is the `canLike` attribute the frontend reads.
// The two have to answer the SAME question, or the button appears
// where the endpoint refuses.
func likeOwnPostAllowedAt(app core.App, key string, fallback bool) bool {
	value, err := api.SettingValueAt(app, key, fallback)
	if err != nil {
		return fallback
	}

	return api.SettingTruthy(value, fallback)
}

// postAuthor returns the account that wrote a post.
func postAuthor(app core.App, postID string) (string, error) {
	collection, err := app.FindCollectionByNameOrId("forum_posts")
	if err != nil {
		return "", err
	}

	record, err := app.FindRecordById(collection, postID)
	if err != nil {
		return "", err
	}

	return record.GetString("user"), nil
}

// refuseOwnLike refuses a like for the author's own post.
//
// It is the plugin's form of upstream's `LikePostPolicy::like()`, and
// it is checked BEFORE the like is recorded: the setting governs
// whether the act is permitted at all.
func refuseOwnLike(e *core.RequestEvent, postID, actorID string) error {
	// The setting PERMITS the act. When it does not, the author of the
	// post may not like it — which is upstream's rule: the policy
	// denies the author unless `like_own_post` is on.
	if likeOwnPostAllowed(e) {
		return nil
	}

	author, err := postAuthor(e.App, postID)
	if err != nil {
		return api.WriteError(e, http.StatusInternalServerError, api.CodeInternal, "The like could not be recorded.")
	}

	if author == actorID {
		return api.WriteError(e, http.StatusForbidden, api.CodePermissionDenied, "You cannot like your own post.")
	}

	return nil
}

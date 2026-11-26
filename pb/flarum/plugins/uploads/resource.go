package uploads

import (
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/domain"
)

// TypeFile is the JSON:API resource type of an uploaded file.
//
// It is upstream's `fof/upload` type name: the extension's
// `FileResource::type()` returns `'files'`. The name matters because
// the frontend's file manager and the admin's file list both look the
// resource up by its type.
const TypeFile = "files"

// fileResource renders an uploaded file as a JSON:API resource.
//
// The attributes are upstream's `FileResource` fields, ALL of them
// (`src/Api/Resource/FileResource.php::fields()`, `2.x`):
//
//	baseName, path, url, type, size, humanSize, createdAt, uuid, tag,
//	hidden, bbcode, shared, imageWidth, imageHeight, thumbnailUrl,
//	thumbnailWidth, thumbnailHeight, isPrivateShared, canViewInfo,
//	canHide, canDelete
//
// Four of them are constants rather than stored values, and each is
// upstream's own answer for a feature this port does not implement:
// the three thumbnail fields are NULL (the dimensions are stored, no
// WebP thumbnail is generated, and the frontend reads null as "use the
// original"), `isPrivateShared` is FALSE (there is no private shared
// storage), and `canViewInfo` is FALSE for everybody — upstream's
// `FilePolicy::viewInfo()` is a bare `deny()` too.
//
// The `url` is the forum's OWN file route, because PocketBase's native
// file URL lives at the root of the listener, which the forum does not
// own (the same reason the avatar route exists).
func fileResource(e *core.RequestEvent, record *core.Record) api.Resource {
	file := fileFromRecord(record)

	// The download URL, computed once: both `url` and `bbcode` carry it,
	// and it depends on the module's mount path rather than on the
	// record.
	url := fileURL(e, file)

	attributes := map[string]any{
		"baseName":    file.Filename,
		"path":        file.Blob,
		"url":         url,
		"type":        file.MimeType,
		"size":        file.Size,
		"humanSize":   humanSize(file.Size),
		"createdAt":   fileCreatedAt(record),
		"uuid":        file.ID,
		"tag":         "",
		"hidden":      file.Hidden,
		"bbcode":      bbcodeForFile(file, url),
		"shared":      file.Shared,
		"canViewInfo": false,

		// Upstream's dimensions are NULLABLE, so a file that is not an
		// image carries null — not 0, which the frontend cannot tell
		// apart from a zero-pixel image.
		"imageWidth":      nullableInt(file.ImageWidth),
		"imageHeight":     nullableInt(file.ImageHeight),
		"thumbnailUrl":    nil,
		"thumbnailWidth":  nil,
		"thumbnailHeight": nil,
		"isPrivateShared": false,

		// The two affordance flags ask the SAME questions the endpoints
		// ask (`FilePolicy::hide()` / `::delete()`): a flag that says
		// "yes" where the endpoint says "no" is a button the reader
		// clicks and the server refuses.
		"canHide":   mayHideFile(e, record),
		"canDelete": mayDeleteFile(e, record),
	}

	relationships := map[string]api.Relationship{
		"actor": {Data: api.Identifier{Type: api.TypeUser, ID: file.ActorID}},
	}

	return api.Resource{
		Type:          TypeFile,
		ID:            file.ID,
		Attributes:    attributes,
		Relationships: relationships,
	}
}

// fileCreatedAt renders the file's creation time the way every other
// resource does.
//
// The record's `created_at` COLUMN is PocketBase's internal date layout
// (`2006-01-02 15:04:05.000Z`), while JSON:API carries RFC 3339 — the
// difference is invisible until a client parses the value, and the
// frontend's `Model.transformDate` does (`new Date(...)`): the internal
// layout parses as a LOCAL time on some engines and as an invalid date on
// others. The upstream attribute is a `Schema\\DateTime`, so it is ISO
// 8601 there; this makes it the same here.
func fileCreatedAt(record *core.Record) string {
	created := record.GetDateTime("created_at")
	if created.IsZero() {
		return ""
	}
	return created.Time().UTC().Format(time.RFC3339)
}

// nullableInt renders a zero as JSON null.
//
// Upstream's four dimension fields are `->nullable()`, so a file with no
// dimensions carries `null` rather than `0`.
func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

// bbcodeForFile renders the text a reader would paste into a post.
//
// Upstream's `Util::getBbcodeForFile()` renders the file's TEMPLATE, and
// its default template for an image is an HTML `<a><img></a>` pair. This
// port's posts are Markdown and the composer inserts this string
// verbatim, so the value is the MARKDOWN embed: `![](url)` for an image,
// and the file's name linked to its URL otherwise. The key is upstream's;
// the syntax follows the forum's own renderer, because an HTML embed
// pasted into a Markdown post would be escaped — the forum renders raw
// HTML inert on purpose.
func bbcodeForFile(file fileRecord, url string) string {
	if strings.HasPrefix(file.MimeType, "image/") {
		return "![](" + url + ")"
	}
	return "[" + file.Filename + "](" + url + ")"
}

// mayHideFile reports whether the caller may hide a file from its owner.
//
// It is upstream's `FilePolicy::hide()`: a SHARED file belongs to the
// forum, so writing one asks for `upload-shared-files`; otherwise the
// uploader may hide their own and a moderator may hide anybody's.
func mayHideFile(e *core.RequestEvent, record *core.Record) bool {
	file := fileFromRecord(record)

	if file.Shared {
		return canUploadSharedFiles(e)
	}

	if ownsFile(e, file) {
		return true
	}

	return canDeleteUserUploads(e)
}

// mayDeleteFile reports whether the caller may delete a file.
//
// It is upstream's `FilePolicy::delete()`: a shared file asks for
// `upload-shared-files`; a moderator may delete anybody's; and the
// uploader may delete their own ONLY while the forum grants
// `deleteOwnUploads`. That last condition is the one that is easy to
// miss — ownership alone is not enough upstream, because an operator can
// let members upload without letting them delete.
//
// It is the same rule the delete endpoint enforces. The flag has to
// agree with the endpoint, or the frontend offers a control the API
// refuses.
func mayDeleteFile(e *core.RequestEvent, record *core.Record) bool {
	file := fileFromRecord(record)

	if file.Shared {
		return canUploadSharedFiles(e)
	}

	if canDeleteUserUploads(e) {
		return true
	}

	return ownsFile(e, file) && can(e, domain.PermissionDeleteOwnUploads)
}

// ownsFile reports whether the caller uploaded the file.
//
// The comparison is on the actor id, so a file whose uploader was deleted
// belongs to nobody rather than to a guest-like actor.
func ownsFile(e *core.RequestEvent, file fileRecord) bool {
	actor, err := api.CurrentActor(e)
	if err != nil || actor.User == nil || file.ActorID == "" {
		return false
	}
	return file.ActorID == actor.User.ID
}

// can answers one of the plugin's GLOBAL permissions.
//
// The subject is the forum itself: every upload power is forum-wide (the
// per-file question is ownership, which the policy helpers ask
// separately).
func can(e *core.RequestEvent, permission string) bool {
	allowed, err := api.Can(e, permission, api.TypeForum, "1")
	return err == nil && allowed
}

// canUploadSharedFiles reports whether the caller may write a shared file.
func canUploadSharedFiles(e *core.RequestEvent) bool {
	return can(e, domain.PermissionUploadSharedFiles)
}

// canDeleteUserUploads reports whether the caller may write anybody's file.
func canDeleteUserUploads(e *core.RequestEvent) bool {
	return can(e, domain.PermissionDeleteUserUploads)
}

// fileURL is the URL a file's bytes are served at.
//
// It is the forum's own file route, under the module's base path, so
// every URL the frontend builds stays relative to the mount point.
//
// The base path comes from the API server rather than from the
// request: the request's path does not carry it (the route is
// registered at an absolute path, and a reverse proxy may rewrite
// it), while the server knows where the forum is mounted.
func fileURL(e *core.RequestEvent, file fileRecord) string {
	base := "/forum"
	if server := api.ServerFrom(e); server != nil {
		base = server.BasePath()
	}
	return base + "/files/" + file.ID
}

// humanSize renders a byte count the way upstream does.
//
// Upstream's `File::humanSize()` is the same "one decimal, the unit
// chosen by the magnitude" shape, and the frontend shows it next to
// the file's name.
func humanSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}

	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	units := []string{"kB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"}
	if exp >= len(units) {
		exp = len(units) - 1
	}

	return fmt.Sprintf("%.1f %s", float64(size)/float64(div), units[exp])
}

// filesForPostIDs returns the ids of the files attached to a post.
func filesForPostIDs(app core.App, physical, postID string) ([]string, error) {
	records, err := filesForPost(app, physical, postID)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.Id)
	}
	return ids, nil
}

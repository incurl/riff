package uploads

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/pb/flarum/api"
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
// The attributes are upstream's `FileResource` fields, so a frontend
// written against `fof/upload` finds what it expects:
//
//	baseName, path, url, type, size, humanSize, createdAt, uuid,
//	tag, hidden, shared, imageWidth, imageHeight, thumbnailUrl,
//	canHide, canDelete
//
// The `url` is the forum's OWN file route, because PocketBase's native
// file URL lives at the root of the listener, which the forum does not
// own (the same reason the avatar route exists).
func fileResource(e *core.RequestEvent, record *core.Record) api.Resource {
	file := fileFromRecord(record)

	attributes := map[string]any{
		"baseName":    file.Filename,
		"path":        file.Filename,
		"url":         fileURL(e, file),
		"type":        file.MimeType,
		"size":        file.Size,
		"humanSize":   humanSize(file.Size),
		"createdAt":   record.GetString("created_at"),
		"uuid":        file.ID,
		"tag":         "",
		"hidden":      file.Hidden,
		"shared":      false,
		"imageWidth":  file.ImageWidth,
		"imageHeight": file.ImageHeight,
		"canHide":     false,
		"canDelete":   mayDeleteFile(e, record),
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

// mayDeleteFile reports whether the caller may delete a file.
//
// It is the same rule the delete endpoint enforces: the uploader may
// delete their own file, and so may an administrator. The flag has to
// agree with the endpoint, or the frontend offers a delete control that
// the API refuses.
func mayDeleteFile(e *core.RequestEvent, record *core.Record) bool {
	actor, err := api.CurrentActor(e)
	if err != nil || actor.User == nil {
		return false
	}
	return record.GetString("actor") == actor.User.ID || actor.User.IsAdmin
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

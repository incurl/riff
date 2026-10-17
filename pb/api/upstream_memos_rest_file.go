package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// fileServerHandler returns a handler that serves one of
// the three upstream /file/... routes. The builder key
// selects the kind of file: "attachment" reads a
// patches or samples row by UID, "user_avatar" reads a
// users row by username or email.
//
// Upstream Memos has more elaborate content negotiation
// (signed URLs, thumbnail cache, motion-photo clipping)
// than riff. The 2026-10-17 design lands the simple
// "find the record, stream the file" path. The richer
// behaviour is a future slice.
func fileServerHandler(app core.App, builder string) func(*core.RequestEvent) error {
	switch builder {
	case "attachment":
		return attachmentFileHandler(app)
	case "user_avatar":
		return userAvatarFileHandler(app)
	default:
		return unknownFlavorHandler("file_server:" + builder)
	}
}

// attachmentFileHandler resolves /file/attachments/{uid}
// or /file/attachments/{uid}/{filename} to a patches or
// samples row and streams the primary file.
//
// The UID is the PocketBase 15-char id; riff also accepts
// the upstream "name" form (the `attachments/{uid}`
// segment) so a Memos client can paste a known-good URL.
func attachmentFileHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		parts := strings.Split(strings.TrimPrefix(e.Request.URL.Path, "/memos/api/file/attachments/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			return fileError(e, http.StatusNotFound, "attachment not found")
		}
		uid := parts[0]
		rec, err := findAttachmentRecord(app, uid)
		if err != nil {
			return fileError(e, http.StatusNotFound, "attachment not found")
		}
		filename := rec.GetString("file")
		if filename == "" {
			return fileError(e, http.StatusNotFound, "attachment has no file")
		}
		return streamRecordFile(e, app, rec, filename)
	}
}

// userAvatarFileHandler resolves
// /file/users/{identifier}/avatar. Today the riff users
// schema has no avatar field; the handler returns 404
// for now. The route is registered so the upstream
// Memos frontend does not 404 on a missing route, and
// a future slice can attach the storage field.
func userAvatarFileHandler(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		parts := strings.Split(strings.TrimPrefix(e.Request.URL.Path, "/memos/api/file/users/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			return fileError(e, http.StatusNotFound, "user not found")
		}
		identifier := parts[0]
		_, err := findUserByIdentifier(app, identifier)
		if err != nil {
			return fileError(e, http.StatusNotFound, "user not found")
		}
		return fileError(e, http.StatusNotFound, "avatar not implemented")
	}
}

// findAttachmentRecord returns the patches or samples
// record whose id matches uid. Patches first because
// most upstream Memos clients use the patches
// collection for sample/song attachments.
func findAttachmentRecord(app core.App, uid string) (*core.Record, error) {
	for _, collection := range []string{"patches", "samples"} {
		rec, err := app.FindRecordById(collection, uid)
		if err == nil {
			return rec, nil
		}
	}
	return nil, errors.New("attachment not found")
}

// findUserByIdentifier resolves a user by username, email,
// or id. The lookup order matches the upstream Memos
// avatar route.
func findUserByIdentifier(app core.App, identifier string) (*core.Record, error) {
	rec, err := app.FindRecordById("users", identifier)
	if err == nil {
		return rec, nil
	}
	rec, err = app.FindFirstRecordByFilter("users",
		"username = {:u} || LOWER(username) = LOWER({:u})", map[string]any{"u": identifier})
	if err == nil {
		return rec, nil
	}
	rec, err = app.FindFirstRecordByFilter("users",
		"email = {:e} || LOWER(email) = LOWER({:e})", map[string]any{"e": identifier})
	if err == nil {
		return rec, nil
	}
	return nil, errors.New("user not found")
}

// streamRecordFile opens the named file on the
// record's primary filesystem and streams its bytes
// to the response. PocketBase stores file-field blobs
// at <collectionId>/<recordId>/<filename>; the patches
// and samples collections have MaxSelect:1 so the
// simple form applies. The Cache-Control is
// `private, no-store` to match the upstream Memos
// default; the Content-Type is sniffed from the file
// extension.
func streamRecordFile(e *core.RequestEvent, app core.App, rec *core.Record, filename string) error {
	fs, err := app.NewFilesystem()
	if err != nil {
		return fileError(e, http.StatusInternalServerError, "filesystem unavailable")
	}
	defer fs.Close()

	fileKey := rec.Collection().Id + "/" + rec.Id + "/" + filename
	blob, err := fs.GetFile(fileKey)
	if err != nil {
		return fileError(e, http.StatusNotFound, "attachment not found")
	}
	defer blob.Close()

	contentType := sniffContentType(filename)
	// ETag from record id + filename + size so a
	// conditional GET short-circuits.
	hash := sha256.Sum256([]byte(rec.Id + filename + fmt.Sprint(blob.Size())))
	etag := `"` + hex.EncodeToString(hash[:16]) + `"`
	if match := e.Request.Header.Get("If-None-Match"); match == etag {
		e.Response.Header().Set("ETag", etag)
		e.Response.WriteHeader(http.StatusNotModified)
		return nil
	}

	e.Response.Header().Set("Content-Type", contentType)
	e.Response.Header().Set("Content-Length", fmt.Sprint(blob.Size()))
	e.Response.Header().Set("Cache-Control", "private, no-store")
	e.Response.Header().Set("ETag", etag)
	e.Response.WriteHeader(http.StatusOK)
	_, err = io.Copy(e.Response, blob)
	return err
}

// sniffContentType returns a Content-Type for the file.
// It prefers the file extension's registered MIME
// type, falling back to application/octet-stream.
func sniffContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	case ".txt", ".md":
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// fileError writes a Memos-shape JSON error and
// returns nil so the PocketBase handler chain sees a
// completed response.
func fileError(e *core.RequestEvent, status int, message string) error {
	e.Response.Header().Set("Content-Type", "application/json")
	e.Response.WriteHeader(status)
	_, _ = io.WriteString(e.Response,
		fmt.Sprintf(`{"code":5,"message":%q}`, message))
	return nil
}

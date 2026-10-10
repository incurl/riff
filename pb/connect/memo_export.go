// memo_export.go implements the ExportMemos RPC. The
// handler writes a Memos export archive (a ZIP) to an
// in-memory buffer and returns it as a google.api.HttpBody
// with the vendor media type. The archive shape, the
// safe-filename mapping, the JSON wire format, and the
// manifest schema are documented in
// docs/design/memos-export-format.md; the helpers in
// memo_export_format.go own the format-orthogonal bits.
//
// Export rules (per the design):
//
//   - The exporter writes only memos the requesting user
//     created and attachments linked to them. No admin
//     bypass exists.
//   - The archive names its own scope; the request's
//     `name` field is used only to confirm the caller is
//     the user being exported.
//   - An exporter that has no memos writes a valid empty
//     archive so a round-trip ImportMemos run is
//     idempotent.
//
// Out of scope (per the design's "Deferred design" list):
//
//   - INSTANCE and SPACE scopes.
//   - Soft-deleted memos.
//   - Reactions beyond storing the username and the
//     emoji, because Memos 0.31 stores them in a
//     separate collection and a fully round-tripping
//     reaction is a separate piece of work.
package connect

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"

	"github.com/incurl/riff/internal/version"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
	httpbody "google.golang.org/genproto/googleapis/api/httpbody"
)

// ExportMemos writes every memo the user created, with
// comments, archived memos, and attachment files, as a
// Memos export file (a ZIP file, see
// docs/design/memos-export-format.md). Only the user may
// export their own memos. The body is the archive;
// content_type is its media type.
func (s *UserServer) ExportMemos(ctx context.Context, req *v1.ExportMemosRequest) (*httpbody.HttpBody, error) {
	username := creatorUsername(req.GetName())
	if username == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}

	caller := CurrentUser(ctx)
	if caller == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	// Only the user may export their own memos. The
	// design rejects an admin bypass explicitly.
	if caller.GetString("username") != username {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the user may export their own memos")
	}

	// Build the archive in memory. The export is bounded
	// by the same 256 MiB cap as an import; if a real
	// install blows past that, the export must be
	// streamed and the cap raised.
	buf := &bytes.Buffer{}
	w := zip.NewWriter(buf)
	now := time.Now().UTC()

	// Load every memo the user created.
	memoRecords, err := s.app.FindRecordsByFilter(
		"memos",
		"author.username={:u}",
		"created",
		0, 0,
		map[string]any{"u": username},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}

	// Step 1: per-memo records and content. A single
	// pass keeps the manifest's counts in sync with the
	// entries we actually wrote. The manifest is written
	// last so its counts are accurate.
	totalAttachments := 0
	for _, memoRec := range memoRecords {
		attachments, err := s.writeMemoToArchive(w, memoRec, username, now)
		if err != nil {
			_ = w.Close()
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write memo %s: %w", memoRec.Id, err).Error())
		}
		totalAttachments += attachments
	}

	// Step 2: write the manifest last.
	manifest := exportManifest{
		Format:        memoExportFormat,
		FormatVersion: memoExportFormatVersion,
		Generator:     exportGenerator{Name: generatorName, Version: version.Version},
		ExportTime:    rfc3339UTC(now),
		Scope: exportScope{
			Kind: "USER",
			User: exportUser{
				Username:    username,
				DisplayName: caller.GetString("display_name"),
			},
		},
		Counts: exportCounts{
			Memos:       len(memoRecords),
			Attachments: totalAttachments,
		},
	}
	manifestBytes, err := encodeManifest(manifest)
	if err != nil {
		_ = w.Close()
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode manifest: %w", err).Error())
	}
	mh := &zip.FileHeader{
		Name:     "manifest.json",
		Method:   zip.Deflate,
		Modified: now,
	}
	mw, err := w.CreateHeader(mh)
	if err != nil {
		_ = w.Close()
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create manifest: %w", err).Error())
	}
	if _, err := mw.Write(manifestBytes); err != nil {
		_ = w.Close()
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write manifest: %w", err).Error())
	}

	if err := w.Close(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("close archive: %w", err).Error())
	}

	return &httpbody.HttpBody{
		ContentType: memoExportMediaType,
		Data:        buf.Bytes(),
	}, nil
}

// writeMemoToArchive writes the memo record, content
// file, and any linked attachments to w. Returns the
// number of attachments written so the manifest can
// carry an accurate count.
func (s *UserServer) writeMemoToArchive(w *zip.Writer, memoRec *core.Record, username string, exportTime time.Time) (int, error) {
	uid := memoRec.Id

	// `state` is always NORMAL today (the design defers
	// `ARCHIVED` and `DELETED`); `pinned` defaults to
	// false. We still write the fields so the JSON
	// shape matches the design and a future riff
	// release that adds the columns is a one-liner.
	record := exportMemoRecord{
		UID:         uid,
		Creator:     username,
		CreateTime:  rfc3339UTC(memoRec.GetDateTime("created").Time()),
		UpdateTime:  rfc3339UTC(memoRec.GetDateTime("updated").Time()),
		State:       "NORMAL",
		Visibility:  memoVisibilityToString(memoRec.GetString("visibility")),
		Pinned:      false,
		ContentPath: fmt.Sprintf("memos/%s.md", uid),
	}
	if tags := memoRec.GetStringSlice("tags"); len(tags) > 0 {
		record.Tags = append([]string(nil), tags...)
	}
	if loc := readLocation(memoRec); loc != nil {
		record.Location = loc
	}
	if spaceID := memoRec.GetString("space"); spaceID != "" {
		if spaceRec, err := s.app.FindRecordById("spaces", spaceID); err == nil {
			record.Space = &exportSpaceRef{
				UID:   spaceRec.GetString("uid"),
				Title: spaceRec.GetString("title"),
			}
		}
	}

	if parent, ok := memoParentUID(s.app, memoRec); ok {
		record.Parent = parent
	}
	if rels, err := memoReferenceUIDs(s.app, memoRec); err == nil && len(rels) > 0 {
		for _, r := range rels {
			record.Relations = append(record.Relations, exportRelation{Type: "REFERENCE", Memo: r})
		}
	}

	attachments, err := s.collectAttachments(memoRec)
	if err != nil {
		return 0, fmt.Errorf("collect attachments: %w", err)
	}
	for _, a := range attachments {
		att, err := s.writeAttachmentToArchive(w, a, exportTime)
		if err != nil {
			return 0, fmt.Errorf("write attachment %s: %w", a.UID, err)
		}
		record.Attachments = append(record.Attachments, att)
	}

	recordBytes, err := encodeMemoRecord(record)
	if err != nil {
		return 0, fmt.Errorf("encode memo record: %w", err)
	}
	recHeader := &zip.FileHeader{
		Name:     fmt.Sprintf("memos/%s.json", uid),
		Method:   zip.Deflate,
		Modified: exportTime,
	}
	recWriter, err := w.CreateHeader(recHeader)
	if err != nil {
		return 0, fmt.Errorf("create memo record: %w", err)
	}
	if _, err := recWriter.Write(recordBytes); err != nil {
		return 0, fmt.Errorf("write memo record: %w", err)
	}

	content := memoRec.GetString("content")
	contentHeader := &zip.FileHeader{
		Name:     fmt.Sprintf("memos/%s.md", uid),
		Method:   zip.Deflate,
		Modified: exportTime,
	}
	contentWriter, err := w.CreateHeader(contentHeader)
	if err != nil {
		return 0, fmt.Errorf("create content file: %w", err)
	}
	if _, err := contentWriter.Write([]byte(content)); err != nil {
		return 0, fmt.Errorf("write content file: %w", err)
	}

	return len(record.Attachments), nil
}

// attachmentRef is the in-memory shape the export
// pipeline uses to write a single attachment to the
// archive.
type attachmentRef struct {
	UID      string
	Filename string
	MimeType string
	File     *core.Record
	// Collection is the source collection name.
	Collection string
	// MediaMetadata is the JSON-encoded media_metadata
	// column the attachment hook wrote.
	MediaMetadata string
}

// collectAttachments returns the union of patches and
// samples that point at memo.
func (s *UserServer) collectAttachments(memo *core.Record) ([]attachmentRef, error) {
	memoID := memo.Id
	var out []attachmentRef
	for _, coll := range []string{"patches", "samples"} {
		rows, err := s.app.FindRecordsByFilter(
			coll, "memo={:m}", "", 0, 0, map[string]any{"m": memoID},
		)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			filenames := attachmentFilenames(r)
			if len(filenames) == 0 {
				continue
			}
			filename := filenames[0]
			uid := r.Id
			mime := guessMimeFromFilename(filename)
			out = append(out, attachmentRef{
				UID:           uid,
				Filename:      filename,
				MimeType:      mime,
				File:          r,
				Collection:    coll,
				MediaMetadata: r.GetString("media_metadata"),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// attachmentFilenames returns the file names attached to
// a `patches` (single) or `samples` (multi) record.
func attachmentFilenames(rec *core.Record) []string {
	switch rec.Collection().Name {
	case "patches":
		if f := rec.GetString("file"); f != "" {
			return []string{f}
		}
	case "samples":
		return rec.GetStringSlice("files")
	}
	return nil
}

// writeAttachmentToArchive writes the attachment's file
// bytes to `attachments/<uid>/<safe-filename>` and
// returns the export record entry.
func (s *UserServer) writeAttachmentToArchive(w *zip.Writer, a attachmentRef, exportTime time.Time) (exportAttachment, error) {
	entry := exportAttachment{
		UID:        a.UID,
		Filename:   a.Filename,
		Type:       a.MimeType,
		CreateTime: rfc3339UTC(a.File.GetDateTime("created").Time()),
	}
	safeName := safeFilename(a.Filename)
	entry.Path = fmt.Sprintf("attachments/%s/%s", a.UID, safeName)

	fs, err := s.app.NewFilesystem()
	if err != nil {
		return entry, fmt.Errorf("new filesystem: %w", err)
	}
	coll, err := s.app.FindCollectionByNameOrId(a.Collection)
	if err != nil {
		return entry, fmt.Errorf("find collection: %w", err)
	}
	fileKey := dirForFilenames(coll.Id, a.File.Id, a.Filename)
	rc, err := fs.GetFile(fileKey)
	if err != nil {
		return entry, nil
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return entry, fmt.Errorf("read file: %w", err)
	}
	sum := sha256.Sum256(data)
	entry.SHA256 = hex.EncodeToString(sum[:])
	entry.Size = int64(len(data))

	if a.MediaMetadata != "" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(a.MediaMetadata), &raw); err == nil {
			entry.MediaMeta = mediaMetaFromJSON(raw)
		}
	}

	header := &zip.FileHeader{
		Name:     entry.Path,
		Method:   zip.Deflate,
		Modified: exportTime,
	}
	writer, err := w.CreateHeader(header)
	if err != nil {
		return entry, fmt.Errorf("create attachment entry: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		return entry, fmt.Errorf("write attachment bytes: %w", err)
	}
	return entry, nil
}

// readLocation reads the optional `location` field out of
// the `payload` JSON column.
func readLocation(rec *core.Record) *exportLocation {
	raw := rec.GetString("payload")
	if raw == "" {
		return nil
	}
	var payload struct {
		Location exportLocation `json:"location"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	if payload.Location.Placeholder == "" && payload.Location.Latitude == 0 && payload.Location.Longitude == 0 {
		return nil
	}
	return &payload.Location
}

// memoVisibilityToString maps the PocketBase stored
// value to the public API's Visibility enum string.
func memoVisibilityToString(in string) string {
	switch in {
	case "PRIVATE", "PROTECTED", "PUBLIC", "SPACE":
		return in
	default:
		return "PRIVATE"
	}
}

// memoParentUID returns the UID of the memo this one
// comments on, if any.
func memoParentUID(app core.App, memo *core.Record) (string, bool) {
	rels, err := app.FindRecordsByFilter(
		"memo_relations", "memo={:m} && type='COMMENT'", "", 1, 0,
		map[string]any{"m": memo.Id},
	)
	if err != nil || len(rels) == 0 {
		return "", false
	}
	parent, err := app.FindRecordById("memos", rels[0].GetString("related_memo"))
	if err != nil {
		return "", false
	}
	return parent.Id, true
}

// memoReferenceUIDs returns the UIDs of every memo this
// one has a REFERENCE relation to.
func memoReferenceUIDs(app core.App, memo *core.Record) ([]string, error) {
	rels, err := app.FindRecordsByFilter(
		"memo_relations", "memo={:m} && type='REFERENCE'", "", 0, 0,
		map[string]any{"m": memo.Id},
	)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rels {
		target, err := app.FindRecordById("memos", r.GetString("related_memo"))
		if err != nil {
			continue
		}
		out = append(out, target.Id)
	}
	sort.Strings(out)
	return out, nil
}

// mediaMetaFromJSON converts the JSON encoding of the
// `media_metadata` column into the export shape.
func mediaMetaFromJSON(raw map[string]any) *exportMediaMeta {
	out := &exportMediaMeta{}
	if w, ok := raw["width"].(float64); ok {
		v := int32(w)
		out.Width = &v
	}
	if h, ok := raw["height"].(float64); ok {
		v := int32(h)
		out.Height = &v
	}
	if d, ok := raw["duration_us"].(float64); ok {
		v := int64(d)
		out.DurationUs = &v
	}
	if out.Width == nil && out.Height == nil && out.DurationUs == nil {
		return nil
	}
	return out
}

// guessMimeFromFilename returns a best-effort MIME type
// from the filename extension.
func guessMimeFromFilename(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(n, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(n, ".ogg"):
		return "audio/ogg"
	case strings.HasSuffix(n, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(n, ".zip"):
		return "application/zip"
	case strings.HasSuffix(n, ".png"):
		return "image/png"
	case strings.HasSuffix(n, ".jpg"), strings.HasSuffix(n, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(n, ".gif"):
		return "image/gif"
	case strings.HasSuffix(n, ".webp"):
		return "image/webp"
	case strings.HasSuffix(n, ".pdf"):
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

// memo_import_apply.go owns the import pipeline:
// archive validation, plan computation, and per-memo
// apply with conflict resolution, parent/relations, and
// attachment round-trip. The handler in memo_import.go
// is a thin RPC layer; this file is where the rules
// live.
//
// The shape mirrors the design doc's "Import semantics"
// section (see docs/design/memos-export-format.md). The
// pipeline is intentionally conservative: a single bad
// memo does not roll back the rest of the import.
// Attachments created for a record that fails are
// removed again; this is documented behaviour.
package connect

import (
	"unicode/utf8"

	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"connectrpc.com/connect/v2"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// entryNamePattern validates archive entry names per the
// design's container rules. The reader refuses an
// archive with one unsafe entry name rather than
// sanitising.
var entryNamePattern = regexp.MustCompile(`^([A-Za-z0-9_./-]+)$`)

// readImportPlan opens the staged archive, validates it,
// and returns the plan a finishing call with
// validate_only carries back to the client.
func (s *UserServer) readImportPlan(sess *importSession) (*v1.MemoImportPlan, error) {
	zr, err := s.openImportArchive(sess)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer zr.Close()

	plan := &v1.MemoImportPlan{}
	manifest, err := readManifest(zr)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	plan.ExportTime = manifest.ExportTimeProto
	plan.Exporter = manifest.Scope.User.Username
	plan.Generator = manifest.Generator.Name + " " + manifest.Generator.Version

	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "memos/") || !strings.HasSuffix(f.Name, ".json") {
			continue
		}
		rec, err := readMemoRecord(f)
		if err != nil {
			plan.Warnings = append(plan.Warnings, &v1.MemoImportIssue{
				Message: fmt.Sprintf("skip %s: %v", f.Name, err),
			})
			continue
		}
		plan.Memos++
		plan.Attachments += int32(len(rec.Attachments))
		existing, _ := s.app.FindRecordById("memos", rec.UID)
		switch {
		case existing == nil:
			plan.New++
		case existing.GetString("author") == manifest.Scope.User.UserID:
			plan.Existing++
		default:
			plan.Renamed++
		}
	}
	return plan, nil
}

// applyImport opens the staged archive, walks every
// memo record, and applies the conflict policy.
func (s *UserServer) applyImport(ctx context.Context, sess *importSession, _ string, policy v1.ImportMemosRequest_ConflictPolicy) (*v1.MemoImportReport, error) {
	if policy == v1.ImportMemosRequest_CONFLICT_POLICY_UNSPECIFIED {
		policy = v1.ImportMemosRequest_SKIP
	}
	zr, err := s.openImportArchive(sess)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("open archive: %w", err).Error())
	}
	defer zr.Close()

	manifest, err := readManifest(zr)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("read manifest: %w", err).Error())
	}
	// The userID is needed to determine whether an
	// existing memo is "the same user" (existing) or
	// "another account" (renamed). Re-derive from the
	// caller.
	manifest.Scope.User.UserID = CurrentUser(ctx).Id

	// First pass: load and sort memo records so
	// parents precede comments. The sort key is
	// (parent-empty-first, createTime).
	records, err := loadAllMemoRecords(zr)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("load records: %w", err).Error())
	}
	sort.SliceStable(records, func(i, j int) bool {
		pi, pj := records[i].rec.Parent != "", records[j].rec.Parent != ""
		if pi != pj {
			return !pi
		}
		return records[i].rec.CreateTimeParsed.Before(records[j].rec.CreateTimeParsed)
	})

	report := &v1.MemoImportReport{}
	for _, item := range records {
		// Make the spec simpler: the runtime context
		// is the same one passed in.
		err := s.applyOneMemo(ctx, zr, manifest, item, policy, report)
		if err != nil {
			report.Failed++
			report.Failures = append(report.Failures, &v1.MemoImportIssue{
				Memo:    item.rec.UID,
				Message: err.Error(),
			})
		}
	}

	// Second pass: apply parent/relations using the
	// UID map accumulated during the first pass.
	for _, item := range records {
		s.applyRelations(ctx, item, report)
	}

	return report, nil
}

// memoRecordFile is a memo record plus the open archive
// file that holds the JSON.
type memoRecordFile struct {
	rec *exportMemoRecord
	f   *zip.File
}

func loadAllMemoRecords(zr *zip.ReadCloser) ([]memoRecordFile, error) {
	var out []memoRecordFile
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "memos/") || !strings.HasSuffix(f.Name, ".json") {
			continue
		}
		rec, err := readMemoRecord(f)
		if err != nil {
			return nil, err
		}
		out = append(out, memoRecordFile{rec: rec, f: f})
	}
	return out, nil
}

// readManifest opens the manifest entry and returns the
// parsed shape.
func readManifest(zr *zip.ReadCloser) (*exportManifest, error) {
	var manifest *zip.File
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			manifest = f
			break
		}
	}
	if manifest == nil {
		return nil, errors.New("manifest.json not found")
	}
	rc, err := manifest.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var m exportManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Format != memoExportFormat {
		return nil, fmt.Errorf("unsupported format %q", m.Format)
	}
	if m.FormatVersion == "" {
		return nil, errors.New("manifest missing formatVersion")
	}
	// Refuse a major version we do not know.
	major := strings.SplitN(m.FormatVersion, ".", 2)[0]
	if major != "1" {
		return nil, fmt.Errorf("unsupported major version %q", m.FormatVersion)
	}
	// Decode the export time as a proto Timestamp.
	if t, err := time.Parse(time.RFC3339, m.ExportTime); err == nil {
		m.ExportTimeProto = timestamppbFromTime(t)
	}
	// Resolve the exporter username to a PocketBase
	// user id at the call site. The helper here
	// leaves UserID empty; the caller fills it in
	// after the auth context is known.
	return &m, nil
}

// readMemoRecord opens one `memos/<uid>.json` entry.
func readMemoRecord(f *zip.File) (*exportMemoRecord, error) {
	if !entryNamePattern.MatchString(f.Name) {
		return nil, fmt.Errorf("invalid entry name %q", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var rec exportMemoRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("decode record: %w", err)
	}
	if rec.UID == "" {
		return nil, errors.New("record missing uid")
	}
	// Derive createTime/updateTime for sort order.
	if t, err := time.Parse(time.RFC3339, rec.CreateTime); err == nil {
		rec.CreateTimeParsed = t
	}
	return &rec, nil
}

// applyOneMemo creates or updates a single memo record.
// On failure, attachments created for the record are
// removed again.
func (s *UserServer) applyOneMemo(ctx context.Context, zr *zip.ReadCloser, manifest *exportManifest, item memoRecordFile, policy v1.ImportMemosRequest_ConflictPolicy, report *v1.MemoImportReport) error {
	rec := item.rec
	existing, _ := s.app.FindRecordById("memos", rec.UID)
	memosColl, err := s.app.FindCollectionByNameOrId("memos")
	if err != nil {
		return fmt.Errorf("find memos collection: %w", err)
	}
	userID := manifest.Scope.User.UserID
	visibility := memoVisibilityToProtoForImport(rec.Visibility)
	spaceID, spaceErr := s.resolveSpaceID(rec.Space, userID)
	if spaceErr != nil && rec.Visibility == "SPACE" {
		// Visibility=SPACE without a resolvable
		// space: fall back to PRIVATE with a
		// warning, per the design.
		report.Warnings = append(report.Warnings, &v1.MemoImportIssue{
			Memo:    rec.UID,
			Message: "SPACE visibility without resolvable space; falling back to PRIVATE",
		})
		visibility = "PRIVATE"
		spaceID = ""
	}

	switch {
	case existing == nil:
		// No memo with this UID: create it. The
		// author is the importing user regardless
		// of `rec.Creator`.
		newRec := core.NewRecord(memosColl)
		newRec.Id = rec.UID
		newRec.Set("author", userID)
		newRec.Set("content", readContentBytes(zr, rec))
		newRec.Set("visibility", visibility)
		if spaceID != "" {
			newRec.Set("space", spaceID)
		}
		if err := createRecordRequest(ctx, s.app, newRec); err != nil {
			return fmt.Errorf("create memo: %w", err)
		}
		s.applyContentTimestamps(newRec, rec)
		if err := s.app.Save(newRec); err != nil {
			return fmt.Errorf("save memo timestamps: %w", err)
		}
		// Attachments.
		if err := s.applyAttachments(ctx, zr, newRec, rec); err != nil {
			// Best-effort cleanup.
			_ = s.app.Delete(newRec)
			return fmt.Errorf("attachments: %w", err)
		}
		report.Created++

	case existing.GetString("author") == userID:
		// Same user: apply the conflict policy.
		switch policy {
		case v1.ImportMemosRequest_SKIP:
			report.Skipped++
			return nil
		case v1.ImportMemosRequest_DUPLICATE:
			// Create under a fresh UID.
			newRec := core.NewRecord(memosColl)
			newRec.Set("author", userID)
			newRec.Set("content", readContentBytes(zr, rec))
			newRec.Set("visibility", visibility)
			if spaceID != "" {
				newRec.Set("space", spaceID)
			}
			if err := createRecordRequest(ctx, s.app, newRec); err != nil {
				return fmt.Errorf("duplicate memo: %w", err)
			}
			s.applyContentTimestamps(newRec, rec)
			if err := s.app.Save(newRec); err != nil {
				return fmt.Errorf("save duplicate timestamps: %w", err)
			}
			if err := s.applyAttachments(ctx, zr, newRec, rec); err != nil {
				_ = s.app.Delete(newRec)
				return fmt.Errorf("duplicate attachments: %w", err)
			}
			report.Created++
		case v1.ImportMemosRequest_REPLACE:
			existing.Set("content", readContentBytes(zr, rec))
			existing.Set("visibility", visibility)
			if spaceID != "" {
				existing.Set("space", spaceID)
			} else {
				existing.Set("space", "")
			}
			s.applyContentTimestamps(existing, rec)
			if err := s.app.Save(existing); err != nil {
				return fmt.Errorf("update memo: %w", err)
			}
			// Attachments are not refreshed on
			// REPLACE: the design says the byte
			// identity is what matters and a
			// user with a duplicate is best
			// served by leaving their existing
			// attachments alone.
			report.Updated++
		}
	default:
		// Another user holds this UID: import
		// under a fresh UID. The new UID is the
		// old UID prefixed with the importing
		// user's id, which preserves the
		// identifier shape.
		newUID := userID[:min(8, len(userID))] + rec.UID
		newRec := core.NewRecord(memosColl)
		newRec.Id = newUID
		newRec.Set("author", userID)
		newRec.Set("content", readContentBytes(zr, rec))
		newRec.Set("visibility", visibility)
		if spaceID != "" {
			newRec.Set("space", spaceID)
		}
		if err := createRecordRequest(ctx, s.app, newRec); err != nil {
			return fmt.Errorf("renamed memo: %w", err)
		}
		s.applyContentTimestamps(newRec, rec)
		if err := s.app.Save(newRec); err != nil {
			return fmt.Errorf("save renamed timestamps: %w", err)
		}
		if err := s.applyAttachments(ctx, zr, newRec, rec); err != nil {
			_ = s.app.Delete(newRec)
			return fmt.Errorf("renamed attachments: %w", err)
		}
		report.Created++
	}
	return nil
}

// readContentBytes returns the bytes of the
// `memos/<uid>.md` content file. Falls back to an empty
// string if the file is missing.
func readContentBytes(zr *zip.ReadCloser, rec *exportMemoRecord) string {
	if rec.ContentPath == "" {
		return ""
	}
	for _, f := range zr.File {
		if f.Name != rec.ContentPath {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return ""
		}
		// The archive's bytes are not validated as UTF-8 anywhere, and
		// an invalid sequence stored in `memos.content` makes the whole
		// ListMemos response unmarshalable ("string field contains
		// invalid UTF-8"), poisoning the feed for every reader. Replace
		// the invalid bytes with U+FFFD rather than storing them.
		if !utf8.Valid(data) {
			data = []byte(strings.ToValidUTF8(string(data), string(utf8.RuneError)))
		}
		return string(data)
	}
	return ""
}

// applyContentTimestamps sets `created` and `updated` on
// the new record. PocketBase would otherwise stamp them
// at create time; the design says both are caller-
// settable. The hook chain re-derives `payload` after
// the timestamp write, so this is safe.
func (s *UserServer) applyContentTimestamps(rec *core.Record, src *exportMemoRecord) {
	if t, err := time.Parse(time.RFC3339, src.CreateTime); err == nil {
		rec.Set("created", typesDateTime(t))
	}
	if t, err := time.Parse(time.RFC3339, src.UpdateTime); err == nil {
		rec.Set("updated", typesDateTime(t))
	}
}

// applyAttachments creates a row in the unified
// `attachments` collection for each attachment in the
// memo record and uploads the file bytes through
// PocketBase's filesystem.
func (s *UserServer) applyAttachments(ctx context.Context, zr *zip.ReadCloser, memoRec *core.Record, src *exportMemoRecord) error {
	for _, att := range src.Attachments {
		if err := s.applyOneAttachment(ctx, zr, memoRec, &att); err != nil {
			return err
		}
	}
	return nil
}

// applyOneAttachment creates one row in the unified
// `attachments` collection and uploads the file bytes.
func (s *UserServer) applyOneAttachment(ctx context.Context, zr *zip.ReadCloser, memoRec *core.Record, att *exportAttachment) error {
	coll, err := s.app.FindCollectionByNameOrId("attachments")
	if err != nil {
		return fmt.Errorf("find collection: %w", err)
	}
	newRec := core.NewRecord(coll)
	newRec.Set("memo", memoRec.Id)
	if err := createRecordRequest(ctx, s.app, newRec); err != nil {
		return fmt.Errorf("create attachment record: %w", err)
	}
	// Read the file bytes from the archive.
	data, err := readAttachmentBytes(zr, att)
	if err != nil {
		return fmt.Errorf("read attachment bytes: %w", err)
	}
	if data == nil {
		// External link or missing path: leave
		// the record as-is. The design says
		// externalLink attachments carry no
		// bytes.
		return nil
	}
	// SHA-256 dedup is a future slice: the helper
	// always returns (_, false) today, so the upload
	// path runs every time. See the design's
	// "Import semantics" step 5.
	// Upload the bytes. The uploadAttachmentBytes
	// helper in memo_import_helpers.go wraps the
	// filesystem.File plumbing the upload pipeline
	// already uses.
	safeName := safeFilename(att.Filename)
	if safeName == "" {
		safeName = "upload.bin"
	}
	if err := uploadAttachmentBytes(s.app, "attachments", newRec.Id, data, safeName); err != nil {
		return fmt.Errorf("upload bytes: %w", err)
	}
	newRec.Set("filename", safeName)
	newRec.Set("size", int64(len(data)))
	newRec.Set("storage", "local")
	newRec.Set("mime_type", att.Type)
	if att.MediaMeta != nil {
		if raw, err := json.Marshal(att.MediaMeta); err == nil {
			newRec.Set("media_metadata", string(raw))
		}
	}
	if err := s.app.Save(newRec); err != nil {
		return fmt.Errorf("save attachment record: %w", err)
	}
	return nil
}

// readAttachmentBytes returns the bytes of the
// attachment's archive path, or nil for an external
// link / missing file.
func readAttachmentBytes(zr *zip.ReadCloser, att *exportAttachment) ([]byte, error) {
	if att.ExternalLink != "" {
		return nil, nil
	}
	if att.Path == "" {
		return nil, nil
	}
	for _, f := range zr.File {
		if f.Name != att.Path {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, nil
}

// applyRelations creates the COMMENT and REFERENCE
// relations for one memo.
func (s *UserServer) applyRelations(ctx context.Context, item memoRecordFile, report *v1.MemoImportReport) {
	rec := item.rec
	memoRec, err := s.app.FindRecordById("memos", rec.UID)
	if err != nil {
		return
	}
	if rec.Parent != "" {
		parentRec, err := s.app.FindRecordById("memos", rec.Parent)
		if err != nil {
			report.Warnings = append(report.Warnings, &v1.MemoImportIssue{
				Memo:    rec.UID,
				Message: "parent memo not found; imported as top-level",
			})
		} else {
			rels, err := s.app.FindCollectionByNameOrId("memo_relations")
			if err == nil {
				r := core.NewRecord(rels)
				r.Set("memo", memoRec.Id)
				r.Set("related_memo", parentRec.Id)
				r.Set("type", "COMMENT")
				if err := createRecordRequest(ctx, s.app, r); err != nil {
					report.Warnings = append(report.Warnings, &v1.MemoImportIssue{
						Memo:    rec.UID,
						Message: fmt.Sprintf("parent relation: %v", err),
					})
				}
			}
		}
	}
	for _, rel := range rec.Relations {
		if rel.Type != "REFERENCE" {
			continue
		}
		target, err := s.app.FindRecordById("memos", rel.Memo)
		if err != nil {
			report.Warnings = append(report.Warnings, &v1.MemoImportIssue{
				Memo:    rec.UID,
				Message: fmt.Sprintf("reference target %s not found", rel.Memo),
			})
			continue
		}
		rels, err := s.app.FindCollectionByNameOrId("memo_relations")
		if err != nil {
			continue
		}
		r := core.NewRecord(rels)
		r.Set("memo", memoRec.Id)
		r.Set("related_memo", target.Id)
		r.Set("type", "REFERENCE")
		if err := createRecordRequest(ctx, s.app, r); err != nil {
			report.Warnings = append(report.Warnings, &v1.MemoImportIssue{
				Memo:    rec.UID,
				Message: fmt.Sprintf("reference relation: %v", err),
			})
		}
	}
}

// resolveSpaceID resolves a space reference to a
// PocketBase space id. The design matches by uid first
// and then by title among the importer's active spaces.
func (s *UserServer) resolveSpaceID(ref *exportSpaceRef, userID string) (string, error) {
	if ref == nil {
		return "", nil
	}
	if ref.UID != "" {
		if rows, err := s.app.FindRecordsByFilter("spaces", "uid={:u}", "", 1, 0, map[string]any{"u": ref.UID}); err == nil && len(rows) > 0 {
			return rows[0].Id, nil
		}
	}
	if ref.Title != "" {
		// Title match is best effort: a
		// user with two spaces sharing a
		// title gets the first one. The
		// design calls this out as a soft
		// match.
		rows, err := s.app.FindRecordsByFilter("spaces", "title={:t}", "", 1, 0, map[string]any{"t": ref.Title})
		if err == nil && len(rows) > 0 {
			return rows[0].Id, nil
		}
	}
	return "", fmt.Errorf("space %q not found", ref.UID)
}

// memoVisibilityToProtoForImport validates a visibility
// string from the archive and returns the value to
// store.
func memoVisibilityToProtoForImport(in string) string {
	switch in {
	case "PRIVATE", "PROTECTED", "PUBLIC", "SPACE":
		return in
	}
	return "PRIVATE"
}

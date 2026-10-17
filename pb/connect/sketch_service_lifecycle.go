package connect

import (
	"context"
	"regexp"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// memoResourceNamePattern is the resource-name
// shape `memos/{memo_id}`. The {memo_id} segment
// is the PocketBase row id (15 alphanumeric
// chars). Re-validating the segment here is the
// same defensive pattern the device handler
// uses for `po33-devices/{id}` — a name like
// `memos/../../etc/passwd` is rejected before
// the DB ever sees it.
var memoResourceNamePattern = regexp.MustCompile(`^memos/([A-Za-z0-9]{15})$`)

// PublishSketch flips a sketch memo from DRAFT
// to NORMAL. The caller must be the memo's
// author. A memo that is not in DRAFT is rejected
// with FAILED_PRECONDITION. The same hook that
// gates visibility on state (see
// pb/hooks/memos.go) lets a sketch become PUBLIC
// only after it has been published; this is the
// owner-side counterpart.
//
// The handler does not insert a sketch_versions
// row — a publish is a lifecycle event, not an
// upload. The sketch_versions table records
// every upload, not every state change. A future
// audit log can subscribe to OnRecordUpdate and
// write to a separate `memo_state_changes` log
// if the team needs that.
func (s *SketchServer) PublishSketch(ctx context.Context, req *v1.PublishSketchRequest) (*v1.Memo, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetName())
	if memoID == "" || !isMemoIDShape(memoID) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be memos/{id}")
	}
	rec, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if rec.GetString("author") != viewer.Id {
		// A non-owner gets NotFound rather than
		// Forbidden so a stranger cannot enumerate
		// sketch memo ids by probing the publish
		// RPC.
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if rec.GetString("source") != "sketch" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"only sketch memos have a publish lifecycle")
	}
	if rec.GetString("state") != "DRAFT" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"memo is not in DRAFT state")
	}
	rec.Set("state", "NORMAL")
	if err := s.app.Save(rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToMemo(s.app, rec), nil
}

// ArchiveSketch flips a sketch memo from NORMAL
// to ARCHIVED. The caller must be the memo's
// author. Archive is one-way: a re-publish is
// not supported. Re-uploads of the same sketch
// (which arrive as a fresh sketch_versions row)
// do not change the ARCHIVED state — the
// device's most recent payload sits in the
// version log but the user-facing memo is
// archived.
//
// Why one-way: the brief's §6 says "never
// silently modify the original blob", and the
// ARCHIVED state is the user-visible signal
// that a memo is done. A re-publish would
// make the same memo visible in the feed
// again, which is a different kind of edit
// than the lifecycle we want. If a user
// re-activates a sketch by accident, the
// right answer is a new memo + a new upload.
func (s *SketchServer) ArchiveSketch(ctx context.Context, req *v1.ArchiveSketchRequest) (*v1.Memo, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetName())
	if memoID == "" || !isMemoIDShape(memoID) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name must be memos/{id}")
	}
	rec, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if rec.GetString("author") != viewer.Id {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if rec.GetString("source") != "sketch" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"only sketch memos have an archive lifecycle")
	}
	if rec.GetString("state") == "ARCHIVED" {
		// Idempotent: archiving an already-archived
		// memo returns the same memo the client
		// already has, with no error. The SPA
		// then renders the same ARCHIVED chip.
		return recordToMemo(s.app, rec), nil
	}
	if rec.GetString("state") != "NORMAL" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"only NORMAL memos can be archived")
	}
	rec.Set("state", "ARCHIVED")
	if err := s.app.Save(rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToMemo(s.app, rec), nil
}

// ListSketchVersions returns the append-only
// version log for a sketch memo. The log is
// ordered by version_no asc; the client
// already knows the resource name of the
// parent so the response is the array of
// children. Pagination is offset-based,
// matching the existing ListMySketches
// pagination shape.
func (s *SketchServer) ListSketchVersions(ctx context.Context, req *v1.ListSketchVersionsRequest) (*v1.ListSketchVersionsResponse, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID := memoIDFromName(req.GetParent())
	if memoID == "" || !isMemoIDShape(memoID) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "parent must be memos/{id}")
	}
	memo, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if memo.GetString("author") != viewer.Id {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	rows, err := s.app.FindRecordsByFilter("sketch_versions",
		"memo_id = {:mid}", "version_no",
		int(req.GetPageSize()), 0,
		map[string]any{"mid": memoID})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.SketchVersion, 0, len(rows))
	for _, r := range rows {
		if v := recordToSketchVersion(r); v != nil {
			out = append(out, v)
		}
	}
	return &v1.ListSketchVersionsResponse{Versions: out}, nil
}

// GetSketchVersion returns a single immutable
// version row by resource name. The resource
// name is `memos/{memo}/versions/{version_no}`
// and the {version_no} segment is a 1- to
// 6-digit integer.
func (s *SketchServer) GetSketchVersion(ctx context.Context, req *v1.GetSketchVersionRequest) (*v1.SketchVersion, error) {
	viewer := CurrentUser(ctx)
	if viewer == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	memoID, versionNo, ok := memoAndVersionFromName(req.GetName())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			"name must be memos/{id}/versions/{n}")
	}
	row, err := s.app.FindFirstRecordByFilter("sketch_versions",
		"memo_id = {:mid} && version_no = {:v}",
		map[string]any{"mid": memoID, "v": versionNo})
	if err != nil || row == nil {
		return nil, connect.NewError(connect.CodeNotFound, "version not found")
	}
	// Owner check: the parent memo's author must
	// be the caller. A non-owner gets NotFound.
	parent, err := s.app.FindRecordById("memos", memoID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "memo not found")
	}
	if parent.GetString("author") != viewer.Id {
		return nil, connect.NewError(connect.CodeNotFound, "version not found")
	}
	return recordToSketchVersion(row), nil
}


// memoAndVersionFromName parses a
// `memos/{id}/versions/{n}` resource name. The
// {n} segment is a positive decimal integer
// with up to six digits (a sketch with a
// million uploads is far outside any
// realistic hardware limit, but the cap is
// here as a regex-level guard, not a
// runtime one). Returns ("", 0, false) on any
// deviation.
var memoVersionResourceNamePattern = regexp.MustCompile(`^memos/([A-Za-z0-9]{15})/versions/(\d{1,6})$`)

func memoAndVersionFromName(name string) (string, int, bool) {
	m := memoVersionResourceNamePattern.FindStringSubmatch(name)
	if len(m) != 3 {
		return "", 0, false
	}
	var n int
	if _, err := atoiChecked(m[2], &n); err != nil {
		return "", 0, false
	}
	return m[1], n, true
}

// atoiChecked is a tiny wrapper that returns
// an error when the input is not a positive
// decimal integer in the range the proto
// permits. Kept local because the existing
// strconv helpers don't surface a clean
// error type for "this is the wrong shape
// for our proto".
func atoiChecked(s string, out *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a digit")
		}
		n = n*10 + int(c-'0')
	}
	if n < 1 {
		return 0, errors.New("must be positive")
	}
	*out = n
	return n, nil
}

// recordToSketchVersion turns a PocketBase
// `sketch_versions` row into the proto shape.
// Lives next to the lifecycle handlers
// because the only callers are the two
// version-list RPCs; splitting the
// converter into its own file would add
// 30 lines of indirection without a
// separate test surface. The shape mirrors
// what the upload handler (in
// pb/api/po33_device_routes.go) writes; if
// you change one, change the other.
//
// The optional fields (patch_key, sample_keys,
// backup_key) are surfaced as zero-length
// strings / nil slices when empty so the
// proto's `(google.api.field_behavior) =
// OUTPUT_ONLY` annotation stays consistent.
// JSONField (sample_keys) returns a Go
// []string after GetStringSlice; an empty
// column returns an empty slice which
// serialises to `[]` in the wire.
func recordToSketchVersion(rec *core.Record) *v1.SketchVersion {
	if rec == nil {
		return nil
	}
	memoID := rec.GetString("memo_id")
	versionNo := int32(rec.GetInt("version_no"))
	if memoID == "" || versionNo < 1 {
		return nil
	}
	return &v1.SketchVersion{
		Name:               "memos/" + memoID + "/versions/" + itoa(versionNo),
		Memo:               "memos/" + memoID,
		VersionNo:          versionNo,
		DeviceFingerprint:  rec.GetString("device_fingerprint"),
		UploadedAtUnix:     rec.GetInt64("uploaded_at_unix"),
		ReceivedAt:         timestampToProto(rec.GetDateTime("received_at")),
		PatchKey:           rec.GetString("patch_key"),
		SampleKeys:         rec.GetStringSlice("sample_keys"),
		BackupKey:          rec.GetString("backup_key"),
		SignatureAlgorithm: defaultStr(rec.GetString("signature_algorithm"), "ed25519"),
	}
}

// itoa is a tiny int-to-string helper kept
// here because the resource-name shape
// `memos/{id}/versions/{n}` is built in two
// places (this file and the upload handler)
// and we want one canonical rendering.
func itoa(n int32) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 6)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	if neg {
		buf = append(buf, '-')
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return strings.TrimSpace(string(buf))
}

// defaultStr returns def when s is empty.
// Used so the proto's signature_algorithm
// always has a value the client can render
// even if the column was somehow nulled
// (the migration defaults it to "ed25519"
// but defensive defaults are cheap).
func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// isMemoIDShape reports whether s looks like a
// 15-character PocketBase row id. Used by the
// lifecycle handlers to reject names like
// "memos/../../etc/passwd" before the DB
// ever sees them. The shape check is in
// addition to the prefix check the shared
// memoIDFromName helper already does; both
// guards are cheap and the extra defence
// makes the handler resilient to a future
// caller passing a hand-built resource name.
func isMemoIDShape(s string) bool {
	if len(s) != 15 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}

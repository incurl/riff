// Tests for the Phase 4 Connect handlers. These drive the handler
// methods directly (no HTTP round-trip) against a migrated
// in-process PocketBase app built from the same migrations the
// binary runs. They cover the read paths the Memos frontend needs
// on boot: InstanceService.GetInstanceProfile and
// AttachmentService.ListAttachments.
package pb_test

import (
	"context"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/connect"
	v1 "github.com/incurl/riff/proto/gen/api/v1"

	_ "github.com/incurl/riff/pb/migrations"
)

// --- InstanceService --------------------------------------------------------

// TestInstanceService_FreshInstall asserts a freshly migrated app
// reports needs_setup=true and no admin, so the frontend shows the
// installer.
func TestInstanceService_FreshInstall(t *testing.T) {
	app := newPhase2App(t)
	srv := connect.NewInstanceServer(app)

	resp, err := srv.GetInstanceProfile(context.Background(), &v1.GetInstanceProfileRequest{})
	require.NoError(t, err)

	assert.NotEmpty(t, resp.GetVersion(), "version must be reported in the footer")
	assert.True(t, resp.GetNeedsSetup(), "a brand new instance must need setup")
	assert.Nil(t, resp.GetAdmin(), "no admin should exist on a fresh install")
}

// TestInstanceService_WithAdmin asserts an instance that has an
// admin reports needs_setup=false and exposes the admin as a User
// with the ADMIN role.
func TestInstanceService_WithAdmin(t *testing.T) {
	app := newPhase2App(t)

	superusers, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	require.NoError(t, err)

	admin := core.NewRecord(superusers)
	admin.Set("email", "admin@example.com")
	admin.Set("password", "passw0rd!")
	require.NoError(t, app.Save(admin))

	srv := connect.NewInstanceServer(app)
	resp, err := srv.GetInstanceProfile(context.Background(), &v1.GetInstanceProfileRequest{})
	require.NoError(t, err)

	assert.False(t, resp.GetNeedsSetup(), "an instance with a user must not need setup")
	require.NotNil(t, resp.GetAdmin(), "first admin must be reported")
	assert.Equal(t, v1.User_ADMIN, resp.GetAdmin().GetRole(), "superusers map to the ADMIN role")
	assert.Equal(t, "admin@example.com", resp.GetAdmin().GetEmail())
}

// TestInstanceService_IgnoresInstallerPlaceholder asserts PocketBase's
// own installer account does not count as an admin or as a set-up
// instance. It is created on every first boot, so counting it would
// permanently hide the installer from the frontend and report a
// nonsense admin ("__pbinstaller@example.com") in the footer.
func TestInstanceService_IgnoresInstallerPlaceholder(t *testing.T) {
	app := newPhase2App(t)

	superusers, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	require.NoError(t, err)

	placeholder := core.NewRecord(superusers)
	placeholder.Set("email", core.DefaultInstallerEmail)
	placeholder.Set("password", "passw0rd!")
	require.NoError(t, app.Save(placeholder))

	srv := connect.NewInstanceServer(app)
	resp, err := srv.GetInstanceProfile(context.Background(), &v1.GetInstanceProfileRequest{})
	require.NoError(t, err)

	assert.True(t, resp.GetNeedsSetup(), "the installer placeholder must not count as setup")
	assert.Nil(t, resp.GetAdmin(), "the installer placeholder must not be reported as the admin")
}

// --- AttachmentService ------------------------------------------------------

// TestAttachmentService_Empty asserts an instance with no
// attachments returns an empty (non-nil) list rather than an error.
func TestAttachmentService_Empty(t *testing.T) {
	app := newPhase2App(t)
	srv := connect.NewAttachmentServer(app)

	resp, err := srv.ListAttachments(context.Background(), &v1.ListAttachmentsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetAttachments())
}

// TestAttachmentService_PublicAttachmentVisibleAnonymously asserts
// an attachment on a PUBLIC memo shows up for an anonymous caller,
// with the fields the frontend reads (name, filename, type, link).
func TestAttachmentService_PublicAttachmentVisibleAnonymously(t *testing.T) {
	app := newPhase2App(t)

	cols := mustCols(t, app, "users", "memos", "attachments")
	users, memos, attachments := cols[0], cols[1], cols[2]

	memo := newPublicMemo(t, app, users, memos, "alice", "patch")
	att := newAttachment(t, app, attachments, memo, []byte{0x01, 0x02, 0x03, 0x04})

	srv := connect.NewAttachmentServer(app)
	resp, err := srv.ListAttachments(context.Background(), &v1.ListAttachmentsRequest{})
	require.NoError(t, err)

	require.Len(t, resp.GetAttachments(), 1, "the public attachment must be listed")
	a := resp.GetAttachments()[0]

	assert.Equal(t, "attachments/"+att.Id, a.GetName())
	assert.True(t, strings.HasSuffix(a.GetFilename(), ".po33"), "filename = %q", a.GetFilename())
	assert.Equal(t, "application/octet-stream", a.GetType())
	// The external link points at riff's own `/file/...`
	// surface, not PocketBase's native `/api/files/...`
	// route: the native route wants a short-lived file
	// token for a `Protected` field, and serves the bytes
	// to anyone who knows the id for an unprotected one.
	assert.True(t, strings.HasPrefix(a.GetExternalLink(), "/file/attachments/"+att.Id+"/"),
		"external link = %q", a.GetExternalLink())
	assert.NotNil(t, a.GetCreateTime())
}

// TestAttachmentService_PrivateMemoHiddenAnonymously asserts an
// attachment whose parent memo is PRIVATE is filtered out for
// anonymous callers.
func TestAttachmentService_PrivateMemoHiddenAnonymously(t *testing.T) {
	app := newPhase2App(t)

	cols := mustCols(t, app, "users", "memos", "attachments")
	users, memos, attachments := cols[0], cols[1], cols[2]

	memo := newMemo(t, app, users, memos, "bob", "PRIVATE", "note")
	newAttachment(t, app, attachments, memo, []byte{0x09, 0x08, 0x07})

	srv := connect.NewAttachmentServer(app)
	resp, err := srv.ListAttachments(context.Background(), &v1.ListAttachmentsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetAttachments(), "a PRIVATE memo's attachment must not leak to anonymous callers")
}

// TestAttachmentService_ArchiveAttachmentVisible asserts that a
// single-file archive (an application/zip) lands in the unified
// `attachments` collection and surfaces the right MIME on the wire.
func TestAttachmentService_ArchiveAttachmentVisible(t *testing.T) {
	app := newPhase2App(t)

	cols := mustCols(t, app, "users", "memos", "attachments")
	users, memos, attachments := cols[0], cols[1], cols[2]

	memo := newPublicMemo(t, app, users, memos, "carol", "sample_pack")

	// A minimal ZIP header keeps the file field's MIME
	// validator happy without shipping a real audio fixture.
	zipBytes := []byte{0x50, 0x4B, 0x03, 0x04, 0x00, 0x00, 0x00, 0x00}
	f, err := filesystem.NewFileFromBytes(zipBytes, "kit.zip")
	require.NoError(t, err)

	rec := core.NewRecord(attachments)
	rec.Set("memo", memo.Id)
	rec.Set("filename", "kit.zip")
	rec.Set("size", int64(len(zipBytes)))
	rec.Set("storage", "local")
	rec.Set("blob", f)
	require.NoError(t, app.Save(rec))

	srv := connect.NewAttachmentServer(app)
	resp, err := srv.ListAttachments(context.Background(), &v1.ListAttachmentsRequest{})
	require.NoError(t, err)

	require.Len(t, resp.GetAttachments(), 1)
	a := resp.GetAttachments()[0]
	assert.Equal(t, "attachments/"+rec.Id, a.GetName())
	assert.Equal(t, "application/zip", a.GetType())
	assert.True(t, strings.HasPrefix(a.GetExternalLink(), "/file/attachments/"+rec.Id+"/"),
		"external link = %q", a.GetExternalLink())
}

// --- MemoService ------------------------------------------------------------

// TestMemoService_ListMemos_AnonymousVisibility asserts the feed the
// frontend renders on boot returns PUBLIC memos to anonymous callers
// and withholds PRIVATE ones. It also guards the `-created` sort,
// which fails outright if the memos timestamp field goes missing.
func TestMemoService_ListMemos_AnonymousVisibility(t *testing.T) {
	app := newPhase2App(t)

	cols := mustCols(t, app, "users", "memos")
	users, memos := cols[0], cols[1]

	newPublicMemo(t, app, users, memos, "dave", "note")
	newMemo(t, app, users, memos, "erin", "PRIVATE", "note")

	srv := connect.NewMemoServer(app)
	resp, err := srv.ListMemos(context.Background(), &v1.ListMemosRequest{})
	require.NoError(t, err)

	require.Len(t, resp.GetMemos(), 1, "only the PUBLIC memo is visible to anonymous callers")
	assert.Equal(t, v1.Visibility_PUBLIC, resp.GetMemos()[0].GetVisibility())
	assert.Contains(t, resp.GetMemos()[0].GetContent(), "dave")
}

// --- fixtures ---------------------------------------------------------------

// newMemo creates a memo authored by a freshly created user.
func newMemo(t *testing.T, app core.App, users, memos *core.Collection, username, visibility, memoType string) *core.Record {
	t.Helper()
	author := newAuthRecord(t, app, users, username, username+"@example.com", "passw0rd!")

	r := core.NewRecord(memos)
	r.Set("author", author.Id)
	r.Set("content", "test memo for "+username)
	r.Set("visibility", visibility)
	require.NoError(t, app.Save(r))
	return r
}

// newPublicMemo is newMemo with visibility=PUBLIC.
func newPublicMemo(t *testing.T, app core.App, users, memos *core.Collection, username, memoType string) *core.Record {
	t.Helper()
	return newMemo(t, app, users, memos, username, "PUBLIC", memoType)
}

// newAttachment inserts a row in the unified `attachments`
// collection with a real on-disk file backing the `blob`
// FileField. The collection's MimeTypes allow-list
// permits application/octet-stream by default, so a
// non-magic byte payload is fine.
func newAttachment(t *testing.T, app core.App, attachments *core.Collection, memo *core.Record, contents []byte) *core.Record {
	t.Helper()
	f, err := filesystem.NewFileFromBytes(contents, "kick.po33")
	require.NoError(t, err)

	r := core.NewRecord(attachments)
	r.Set("memo", memo.Id)
	r.Set("filename", "kick.po33")
	r.Set("size", int64(len(contents)))
	r.Set("storage", "local")
	r.Set("blob", f)
	require.NoError(t, app.Save(r))
	return r
}

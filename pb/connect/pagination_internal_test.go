package connect

import (
	"encoding/base64"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// ---------------------------------------------------------------------------
// The token helpers. Page tokens are opaque to clients: base64url of the
// result-set offset the previous page ended at.
// ---------------------------------------------------------------------------

func TestPageTokenRoundTrip(t *testing.T) {
	offset, err := pageOffset("")
	require.NoError(t, err)
	assert.Zero(t, offset, "an empty token is the first page")

	for _, want := range []int{0, 1, 30, 1000, 12345} {
		got, err := pageOffset(encodePageToken(want))
		require.NoError(t, err, "offset %d", want)
		assert.Equal(t, want, got)
	}
}

func TestPageTokenRejectsGarbage(t *testing.T) {
	for _, token := range []string{
		"!!not-base64!!",
		encodePageToken(-1), // negative offset
		base64.RawURLEncoding.EncodeToString([]byte("abc")),
		base64.RawURLEncoding.EncodeToString([]byte("1e3")),
	} {
		_, err := pageOffset(token)
		require.Errorf(t, err, "token %q must be rejected", token)
	}
}

func TestPageTokenWindow(t *testing.T) {
	limit, offset, err := pageWindow(0, "")
	require.NoError(t, err)
	assert.Equal(t, defaultPageSize, limit, "unset page_size uses the documented default")
	assert.Zero(t, offset)

	limit, _, err = pageWindow(-5, "")
	require.NoError(t, err)
	assert.Equal(t, defaultPageSize, limit)

	limit, _, err = pageWindow(5000, "")
	require.NoError(t, err)
	assert.Equal(t, maxPageSize, limit, "page_size is coerced down to the documented maximum")

	limit, offset, err = pageWindow(10, encodePageToken(30))
	require.NoError(t, err)
	assert.Equal(t, 10, limit)
	assert.Equal(t, 30, offset)
}

func TestNextPageTokenOnlyForFullPages(t *testing.T) {
	assert.Empty(t, nextPageToken(0, 10, 9), "a short page is the last page")
	assert.Empty(t, nextPageToken(0, 0, 0), "a zero page size has no successor")

	token := nextPageToken(20, 10, 10)
	require.NotEmpty(t, token)
	offset, err := pageOffset(token)
	require.NoError(t, err)
	assert.Equal(t, 30, offset)
}

// ---------------------------------------------------------------------------
// ListMemos / ListAttachments / ListMemoComments.
// ---------------------------------------------------------------------------

func TestListMemos_PageToken(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")

	want := make(map[string]bool, 5)
	for i := 0; i < 5; i++ {
		// Space the rows out so the `-created` order has no ties and a
		// page boundary can never fall inside one.
		time.Sleep(2 * time.Millisecond)
		rec := newFilterMemo(t, app, alice.Id, "memo", nil)
		want["memos/"+rec.Id] = true
	}

	var got []string
	token := ""
	pages := 0
	for {
		res, err := NewMemoServer(app).ListMemos(t.Context(), &v1.ListMemosRequest{
			PageSize:  2,
			PageToken: token,
		})
		require.NoError(t, err)
		pages++
		for _, m := range res.GetMemos() {
			got = append(got, m.GetName())
		}
		token = res.GetNextPageToken()
		if token == "" {
			break
		}
		require.Less(t, pages, 10, "pagination must terminate")
	}

	require.Equal(t, 3, pages, "5 memos at 2 per page is three pages")
	require.Len(t, got, len(want), "every memo must appear exactly once")
	seen := make(map[string]bool, len(got))
	for _, name := range got {
		require.False(t, seen[name], "memo %q appeared on two pages", name)
		seen[name] = true
	}
	assert.Equal(t, want, seen)
}

// A full page whose rows are all filtered out by the per-row access
// check still has successors: the token is derived from the query, not
// from the response length. Stopping there would silently truncate the
// feed for the caller who can see the later rows.
func TestListMemos_PageTokenContinuesPastFilteredRows(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")

	time.Sleep(2 * time.Millisecond)
	public := newFilterMemo(t, app, alice.Id, "public", map[string]any{"visibility": "PUBLIC"})
	// Newer rows, invisible to an anonymous caller: they occupy the
	// first pages.
	time.Sleep(2 * time.Millisecond)
	newFilterMemo(t, app, alice.Id, "private one", map[string]any{"visibility": "PRIVATE"})
	time.Sleep(2 * time.Millisecond)
	newFilterMemo(t, app, alice.Id, "private two", map[string]any{"visibility": "PRIVATE"})

	first, err := NewMemoServer(app).ListMemos(t.Context(), &v1.ListMemosRequest{PageSize: 1})
	require.NoError(t, err)
	assert.Empty(t, first.GetMemos(), "the newest row is private")
	require.NotEmpty(t, first.GetNextPageToken(), "a filtered-out page must still advance")

	var visible []string
	token := first.GetNextPageToken()
	for token != "" {
		res, err := NewMemoServer(app).ListMemos(t.Context(), &v1.ListMemosRequest{PageSize: 1, PageToken: token})
		require.NoError(t, err)
		for _, m := range res.GetMemos() {
			visible = append(visible, m.GetName())
		}
		token = res.GetNextPageToken()
	}
	require.Equal(t, []string{"memos/" + public.Id}, visible)
}

func TestListMemos_RejectsInvalidPageToken(t *testing.T) {
	app := newTestApp(t)
	_, err := NewMemoServer(app).ListMemos(t.Context(), &v1.ListMemosRequest{PageToken: "!!not-base64!!"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestListAttachments_PageToken(t *testing.T) {
	app := newTestApp(t)
	alice := newUser(t, app, "alice")
	memo := newFilterMemo(t, app, alice.Id, "with attachments", nil)

	want := make(map[string]bool, 3)
	for i := 0; i < 3; i++ {
		time.Sleep(2 * time.Millisecond)
		rec := newFilterAttachment(t, app, alice.Id, memo.Id, nil)
		want["attachments/"+rec.Id] = true
	}

	server := NewAttachmentServer(app)
	ctx := withViewer(t.Context(), alice)
	var got []string
	token := ""
	for {
		res, err := server.ListAttachments(ctx, &v1.ListAttachmentsRequest{PageSize: 2, PageToken: token})
		require.NoError(t, err)
		for _, a := range res.GetAttachments() {
			got = append(got, a.GetName())
		}
		token = res.GetNextPageToken()
		if token == "" {
			break
		}
	}
	require.Len(t, got, len(want))
	seen := make(map[string]bool, len(got))
	for _, name := range got {
		seen[name] = true
	}
	assert.Equal(t, want, seen)
}

func TestListMemoComments_PageToken(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	parent := newFilterMemo(t, app, alice.Id, "parent", nil)
	ctx := withViewer(t.Context(), alice)

	srv := NewMemoServer(app)
	want := make(map[string]bool, 3)
	for i := 0; i < 3; i++ {
		time.Sleep(2 * time.Millisecond)
		comment, err := srv.CreateMemoComment(ctx, &v1.CreateMemoCommentRequest{
			Name:    "memos/" + parent.Id,
			Comment: &v1.Memo{Content: "reply"},
		})
		require.NoError(t, err)
		want[comment.GetName()] = true
	}

	var got []string
	token := ""
	for {
		res, err := srv.ListMemoComments(ctx, &v1.ListMemoCommentsRequest{
			Name:      "memos/" + parent.Id,
			PageSize:  2,
			PageToken: token,
		})
		require.NoError(t, err)
		for _, m := range res.GetMemos() {
			got = append(got, m.GetName())
		}
		token = res.GetNextPageToken()
		if token == "" {
			break
		}
	}
	require.Len(t, got, len(want))
	seen := make(map[string]bool, len(got))
	for _, name := range got {
		seen[name] = true
	}
	assert.Equal(t, want, seen)
}

func TestListDevices_PageToken(t *testing.T) {
	app := newHookedApp(t)
	alice := newUser(t, app, "alice")
	for i := 0; i < 3; i++ {
		time.Sleep(2 * time.Millisecond)
		newPo33Device(t, app, alice.Id)
	}

	srv := NewDeviceServer(app)
	ctx := withViewer(t.Context(), alice)
	var got []string
	token := ""
	for {
		res, err := srv.ListDevices(ctx, &v1.ListDevicesRequest{PageSize: 2, PageToken: token})
		require.NoError(t, err)
		for _, d := range res.GetDevices() {
			got = append(got, d.GetName())
		}
		token = res.GetNextPageToken()
		if token == "" {
			break
		}
	}
	require.Len(t, got, 3)
	assert.Len(t, map[string]bool{got[0]: true, got[1]: true, got[2]: true}, 3)
}

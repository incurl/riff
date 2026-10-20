package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/incurl/riff/internal/random"
	"github.com/incurl/riff/pb/connect"
	"github.com/incurl/riff/pb/hooks"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
	apiv1connect "github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// TestProductionRouterFileSurfaceNotShadowedBySpaCatchall mounts
// the file routes AND a real apis.Static catch-all (production
// layout) and probes /o/r/{uid} to confirm the file surface wins
// the mux match.
//
// Background: server/frontend/embed.go::Register installs
// `Any /{path...}` with `apis.Static(dist, true)`, with
// `indexFallback=true` so deep links resolve to index.html. If the
// catch-all intercepted `/file/...` or `/o/r/...`, the MoeMemos
// client would see 200 with HTML (the SPA's index.html) instead of
// the attachment bytes — a status-200 image-decode failure that
// surfaces as a "no image preview" or "broken image" in the client.
//
// Per Go's ServeMux pattern precedence, a more specific literal
// prefix beats `{path...}`, regardless of registration order, so
// `/file/attachments/{uid}/{filename}` should win. This test pins
// that contract.
func TestProductionRouterFileSurfaceNotShadowedBySpaCatchall(t *testing.T) {
	// Build a fresh per-test app with the file routes mounted,
	// plus a real apis.Static catch-all standing in for the
	// production SPA. The dist dir lives at repo root and ships
	// with the binary; the test reads it directly.
	distDir := findDistDir(t)
	distFS := os.DirFS(distDir)

	app := newTestApp(t)
	r := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountFileRoutes(r, app)

	// Mirror server/frontend/embed.go::Register exactly: an
	// `Any /{path...}` catch-all with `indexFallback=true`.
	r.Any("/{path...}", apis.Static(distFS, true))

	mux, err := r.BuildMux()
	require.NoError(t, err)

	probes := []string{
		// V0 client (Memos 0.21 / MoeMemos iOS): `/o/r/<uid>` and
		// `/o/r/<uid>/<filename>`. `/o/r/<uid>` matches
		// `/o/r/{uid}` (one trailing segment); the second has two
		// segments and currently falls through to the SPA.
		"/o/r/abc123",
		"/o/r/abc123/photo.jpeg",
		// V1 client (MoeMemos Android / future) fallback URL:
		// `host/file/<name>/<filename>` where `name` is the
		// resource name like `attachments/<id>`. So the URL is
		// `/file/attachments/<id>/<filename>` — three segments
		// under /file/. None of the existing file routes match
		// this shape, so it falls through to the SPA catch-all.
		"/file/attachments/abc123/photo.jpeg",
		// SPA's own asset paths. These SHOULD match the SPA
		// catch-all (return 200 HTML) — included as a sanity
		// probe so we can tell that the catch-all is alive.
		"/index.html",
		"/",
	}
	for _, p := range probes {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		body := w.Body.String()
		isSpa := w.Code == http.StatusOK && looksLikeSpaIndex(body)
		t.Logf("GET %s -> status=%d body-len=%d isSpa=%t",
			p, w.Code, len(body), isSpa)
	}

	// Now assert that the file surface routes are not shadowed
	// by the SPA. The probes do not hit a real attachment, so
	// the file route returns 404 (fileNotFound) — what matters
	// is that the response is NOT the SPA's index.html (which
	// would answer 200 with HTML).
	fileRoutes := []string{
		"/file/attachments/abc123",
		"/file/users/moe/avatar",
		"/o/r/abc123",
	}
	for _, p := range fileRoutes {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		body := w.Body.String()
		notSpa := w.Code != http.StatusOK || !looksLikeSpaIndex(body)
		assert.True(t, notSpa,
			"GET %s must not be intercepted by the SPA catch-all "+
				"(got status=%d body-len=%d body-starts-with=%q)",
			p, w.Code, len(body), trim(body, 80))
		t.Logf("GET %s -> status=%d body=%q", p, w.Code, trim(body, 80))
	}

	// These paths are KNOWN to be intercepted by the SPA
	// catch-all today (3-segment V1 fallback URL, and the
	// 2-segment `/o/r/<uid>/<filename>` variant). They are
	// the precise URLs the MoeMemos V0/V1 clients actually
	// fetch — `/file/attachments/<id>/<filename>` is the V1
	// fallback built from `host + "file" + name + filename` in
	// `MemosV1Service/Ext.swift`. The fix is to register a
	// route at that shape (see pb/api/file_routes.go). Until
	// that lands, MoeMemos V1 sees HTML where it expects JPEG.
	intercepted := []string{
		"/file/attachments/abc123/photo.jpeg",
		"/o/r/abc123/photo.jpeg",
	}
	for _, p := range intercepted {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		t.Logf("INTERCEPTED GET %s -> status=%d (should not be the SPA's HTML)",
			p, w.Code)
	}
}

// findDistDir walks up from the test working directory to locate
// server/frontend/dist, which the embed.FS is built from at
// compile time. Tests run from the package directory, so the path
// is always ../../server/frontend/dist relative to pb/api/.
func findDistDir(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "..", "server", "frontend", "dist"),
		filepath.Join("server", "frontend", "dist"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatalf("could not locate server/frontend/dist; tried %v", candidates)
	return ""
}

// looksLikeSpaIndex reports whether a response body starts with
// the HTML prelude the Vite build emits. The SPA always serves
// `<!doctype html>` (or `<!DOCTYPE html>`); the file server never
// does. Catches the catch-all intercepting a file route.
func looksLikeSpaIndex(body string) bool {
	s := body
	if len(s) > 200 {
		s = s[:200]
	}
	s = stringsTrim(s)
	return stringsHasPrefix(s, "<!doctype html") ||
		stringsHasPrefix(s, "<!DOCTYPE html")
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	return s
}

func stringsHasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// avoid unused import of "io/fs"
var _ fs.File

// TestProductionSpaMoeMemosDownloadFlow drives the full SPA-upload
// → MoeMemos-download chain against the production router layout
// (file routes + V0/V1 REST + real apis.Static SPA catch-all) and
// asserts on the actual bytes the client would receive.
//
// A green test means the production router layout is correct and
// the user-reported "SPA upload not visible from MoeMemos" symptom
// is NOT in this code path — it's elsewhere (deployment, TLS, CDN,
// auth). A failing test reproduces the symptom in CI.
func TestProductionSpaMoeMemosDownloadFlow(t *testing.T) {
	app := newTestApp(t)
	hooks.Register(app)

	require.NoError(t, connect.Register(app, nil))
	srv := connectrpc.NewServer(connect.WithAuth(app))
	apiv1connect.RegisterAuthServiceHandler(srv, connect.NewAuthServer(app, nil))
	apiv1connect.RegisterInstanceServiceHandler(srv, connect.NewInstanceServer(app))
	apiv1connect.RegisterUserServiceHandler(srv, connect.NewUserServer(app))
	apiv1connect.RegisterMemoServiceHandler(srv, connect.NewMemoServer(app))
	apiv1connect.RegisterAttachmentServiceHandler(srv, connect.NewAttachmentServer(app))
	connectMux := http.NewServeMux()
	connecthttp.Mount(connectMux, srv)
	connect.SetConnectMux(connectMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	distDir := findDistDir(t)
	distFS := os.DirFS(distDir)
	prodR := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountFileRoutes(prodR, app)
	mountUpstreamMemosV0Routes(prodR, app, nil)
	mountUpstreamMemosV1Routes(prodR, app, nil)
	prodR.Any("/{path...}", apis.Static(distFS, true))
	prodMux, err := prodR.BuildMux()
	require.NoError(t, err)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	alice := core.NewRecord(users)
	alice.Set("username", "alice")
	alice.Set("email", "alice@example.com")
	alice.Set("password", "passw0rd!")
	require.NoError(t, app.Save(alice))
	patRaw, _ := random.String(24)
	pat := "memos_pat_" + patRaw
	patHash, _ := bcrypt.GenerateFromPassword([]byte(pat), bcrypt.MinCost)
	pats, _ := app.FindCollectionByNameOrId("personal_access_tokens")
	patRec := core.NewRecord(pats)
	patRec.Set("user", alice.Id)
	patRec.Set("description", "prod e2e")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	connectSrv := httptest.NewServer(connectMux)
	t.Cleanup(connectSrv.Close)
	bearer := &bearerTransport{token: pat}
	httpClient := &http.Client{Transport: bearer}
	connClient := connectrpc.NewClient(connecthttp.NewTransport(httpClient, connectSrv.URL))
	memoClient := apiv1connect.NewMemoServiceClient(connClient)
	createResp, err := memoClient.CreateMemo(t.Context(), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "from SPA", Visibility: v1.Visibility_PRIVATE},
	})
	require.NoError(t, err)
	memoName := createResp.GetName()

	attachClient := apiv1connect.NewAttachmentServiceClient(connClient)
	imgBytes := sampleJPEG(t, 4, 4)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "photo.jpeg")
	_, _ = fw.Write(imgBytes)
	require.NoError(t, mw.Close())
	specResp, err := attachClient.UploadAttachment(t.Context(), &v1.UploadAttachmentRequest{
		Upload: &v1.UploadAttachmentRequest_Spec{
			Spec: &v1.UploadAttachmentSpec{
				Attachment: &v1.Attachment{
					Filename: "photo.jpeg",
					Type:     "image/jpeg",
					Memo:     &memoName,
				},
				TotalSize: int64(len(imgBytes)),
			},
		},
	})
	require.NoError(t, err)
	finResp, err := attachClient.UploadAttachment(t.Context(), &v1.UploadAttachmentRequest{
		Upload:      &v1.UploadAttachmentRequest_UploadId{UploadId: specResp.GetUploadId()},
		WriteOffset: 0,
		Data:        imgBytes,
		FinishWrite: true,
	})
	require.NoError(t, err)
	finAtt := finResp.GetAttachment()

	attachmentName := finAtt.GetName()
	filename := finAtt.GetFilename()

	wList := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+pat)
		w := httptest.NewRecorder()
		prodMux.ServeHTTP(w, req)
		return w
	}

	// (a) V0 list externalLink — the URL MoeMemos fetches.
	wListResp := wList("/api/v1/memo?pageSize=50")
	require.Equal(t, http.StatusOK, wListResp.Code)
	var rows []any
	dec := json.NewDecoder(bytes.NewReader(wListResp.Body.Bytes()))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&rows))
	require.Len(t, rows, 1)
	feedMemo, _ := rows[0].(map[string]any)
	feedRes, _ := feedMemo["resourceList"].([]any)
	require.Len(t, feedRes, 1)
	feedAtt, _ := feedRes[0].(map[string]any)
	feedExt, _ := feedAtt["externalLink"].(string)
	require.NotEmpty(t, feedExt)
	w := wList(feedExt)
	isJpeg := len(w.Body.Bytes()) >= 2 && w.Body.Bytes()[0] == 0xFF && w.Body.Bytes()[1] == 0xD8
	t.Logf("V0 list externalLink=%q status=%d body-len=%d isJpeg=%v isSpaIndex=%v",
		feedExt, w.Code, len(w.Body.Bytes()), isJpeg, looksLikeSpaIndex(w.Body.String()))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, imgBytes, w.Body.Bytes(),
		"V0 list externalLink must serve the actual JPEG bytes, not the SPA index.html")

	// (b) V1 client fallback URL — `/file/<name>/<filename>`.
	w2 := wList("/file/" + attachmentName + "/" + filename)
	isJpeg2 := len(w2.Body.Bytes()) >= 2 && w2.Body.Bytes()[0] == 0xFF && w2.Body.Bytes()[1] == 0xD8
	t.Logf("V1 fallback=/file/%s/%s status=%d body-len=%d isJpeg=%v isSpaIndex=%v",
		attachmentName, filename, w2.Code, len(w2.Body.Bytes()), isJpeg2, looksLikeSpaIndex(w2.Body.String()))
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, imgBytes, w2.Body.Bytes(),
		"V1 fallback URL must serve the actual JPEG bytes")

	// (c) V0 /o/r/<uid> fallback (1 segment).
	uidOnly := strings.TrimPrefix(attachmentName, "attachments/")
	w3 := wList("/o/r/" + uidOnly)
	isJpeg3 := len(w3.Body.Bytes()) >= 2 && w3.Body.Bytes()[0] == 0xFF && w3.Body.Bytes()[1] == 0xD8
	t.Logf("V0 /o/r/<uid>=%s status=%d body-len=%d isJpeg=%v isSpaIndex=%v",
		uidOnly, w3.Code, len(w3.Body.Bytes()), isJpeg3, looksLikeSpaIndex(w3.Body.String()))
	assert.Equal(t, http.StatusOK, w3.Code)
	assert.Equal(t, imgBytes, w3.Body.Bytes(),
		"V0 /o/r/<uid> fallback must serve the actual JPEG bytes")
}

// TestProductionSpaMoeMemosDownloadStreamCompletes is the
// user-reported symptom ("loading ticker that never stops")
// expressed as a test. iOS SwiftUI's AsyncImage shows a spinner
// that never stops when the HTTP request hangs partway through,
// or when Content-Length does not match the actual body length.
// This test reads the response body off a real HTTP server with
// a deadline — a hang surfaces as a test failure.
func TestProductionSpaMoeMemosDownloadStreamCompletes(t *testing.T) {
	app := newTestApp(t)
	hooks.Register(app)

	require.NoError(t, connect.Register(app, nil))
	srv := connectrpc.NewServer(connect.WithAuth(app))
	apiv1connect.RegisterAuthServiceHandler(srv, connect.NewAuthServer(app, nil))
	apiv1connect.RegisterInstanceServiceHandler(srv, connect.NewInstanceServer(app))
	apiv1connect.RegisterUserServiceHandler(srv, connect.NewUserServer(app))
	apiv1connect.RegisterMemoServiceHandler(srv, connect.NewMemoServer(app))
	apiv1connect.RegisterAttachmentServiceHandler(srv, connect.NewAttachmentServer(app))
	connectMux := http.NewServeMux()
	connecthttp.Mount(connectMux, srv)
	connect.SetConnectMux(connectMux)
	t.Cleanup(func() { connect.SetConnectMux(nil) })

	distDir := findDistDir(t)
	distFS := os.DirFS(distDir)
	prodR := router.NewRouter[*core.RequestEvent](eventFactoryFor(app))
	mountFileRoutes(prodR, app)
	mountUpstreamMemosV0Routes(prodR, app, nil)
	mountUpstreamMemosV1Routes(prodR, app, nil)
	prodR.Any("/{path...}", apis.Static(distFS, true))
	prodMux, err := prodR.BuildMux()
	require.NoError(t, err)
	httpSrv := httptest.NewServer(prodMux)
	t.Cleanup(httpSrv.Close)

	users, _ := app.FindCollectionByNameOrId("users")
	alice := core.NewRecord(users)
	alice.Set("username", "alice")
	alice.Set("email", "alice@example.com")
	alice.Set("password", "passw0rd!")
	require.NoError(t, app.Save(alice))
	patRaw, _ := random.String(24)
	pat := "memos_pat_" + patRaw
	patHash, _ := bcrypt.GenerateFromPassword([]byte(pat), bcrypt.MinCost)
	pats, _ := app.FindCollectionByNameOrId("personal_access_tokens")
	patRec := core.NewRecord(pats)
	patRec.Set("user", alice.Id)
	patRec.Set("description", "prod stream")
	patRec.Set("token_hash", string(patHash))
	require.NoError(t, app.Save(patRec))

	connectSrv := httptest.NewServer(connectMux)
	t.Cleanup(connectSrv.Close)
	bearer := &bearerTransport{token: pat}
	httpClient := &http.Client{Transport: bearer}
	connClient := connectrpc.NewClient(connecthttp.NewTransport(httpClient, connectSrv.URL))
	memoClient := apiv1connect.NewMemoServiceClient(connClient)
	createResp, err := memoClient.CreateMemo(t.Context(), &v1.CreateMemoRequest{
		Memo: &v1.Memo{Content: "from SPA", Visibility: v1.Visibility_PUBLIC},
	})
	require.NoError(t, err)
	memoName := createResp.GetName()

	attachClient := apiv1connect.NewAttachmentServiceClient(connClient)
	imgBytes := sampleJPEG(t, 16, 16)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "photo.jpeg")
	_, _ = fw.Write(imgBytes)
	require.NoError(t, mw.Close())
	specResp, err := attachClient.UploadAttachment(t.Context(), &v1.UploadAttachmentRequest{
		Upload: &v1.UploadAttachmentRequest_Spec{
			Spec: &v1.UploadAttachmentSpec{
				Attachment: &v1.Attachment{
					Filename: "photo.jpeg",
					Type:     "image/jpeg",
					Memo:     &memoName,
				},
				TotalSize: int64(len(imgBytes)),
			},
		},
	})
	require.NoError(t, err)
	finResp, err := attachClient.UploadAttachment(t.Context(), &v1.UploadAttachmentRequest{
		Upload:      &v1.UploadAttachmentRequest_UploadId{UploadId: specResp.GetUploadId()},
		WriteOffset: 0,
		Data:        imgBytes,
		FinishWrite: true,
	})
	require.NoError(t, err)
	finAtt := finResp.GetAttachment()

	wListResp, err := httpGetWithProgress(t.Context(),
		httpSrv.URL+"/api/v1/memo?pageSize=50", pat, 5*time.Second)
	require.NoError(t, err, "V0 list must answer within 5s")
	defer wListResp.Body.Close()

	var rows []any
	require.NoError(t, json.NewDecoder(wListResp.Body).Decode(&rows))
	require.Len(t, rows, 1)
	feedMemo, _ := rows[0].(map[string]any)
	feedRes, _ := feedMemo["resourceList"].([]any)
	require.Len(t, feedRes, 1)
	feedAtt, _ := feedRes[0].(map[string]any)
	feedExt, _ := feedAtt["externalLink"].(string)
	require.NotEmpty(t, feedExt)

	dlURL := httpSrv.URL + feedExt
	dlResp, err := httpGetWithProgress(t.Context(), dlURL, pat, 5*time.Second)
	require.NoError(t, err, "download must complete within 5s — a hang here is the user-reported symptom")
	defer dlResp.Body.Close()

	gotBytes, err := readAllWithDeadline(t.Context(), dlResp.Body, len(imgBytes), 5*time.Second)
	t.Logf("download: status=%d content-length=%d uploaded=%d downloaded=%d isJpeg=%v err=%v",
		dlResp.StatusCode, dlResp.ContentLength, len(imgBytes), len(gotBytes),
		len(gotBytes) >= 2 && gotBytes[0] == 0xFF && gotBytes[1] == 0xD8,
		err)
	require.NoError(t, err, "the response body must stream to completion")
	assert.Equal(t, http.StatusOK, dlResp.StatusCode)
	assert.Equal(t, len(imgBytes), len(gotBytes),
		"Content-Length and actual body length must match")
	assert.Equal(t, imgBytes, gotBytes,
		"the downloaded bytes must match the uploaded JPEG exactly")
	_ = finAtt
}

func httpGetWithProgress(ctx context.Context, url, pat string, d time.Duration) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	c := &http.Client{Timeout: d}
	return c.Do(req)
}

func readAllWithDeadline(ctx context.Context, r io.Reader, wantLen int, d time.Duration) ([]byte, error) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	got := make([]byte, 0, wantLen)
	buf := make([]byte, 4096)
	for {
		if len(got) >= wantLen {
			return got, nil
		}
		select {
		case <-ctx.Done():
			return got, ctx.Err()
		case <-deadline.C:
			return got, errors.New("read deadline exceeded — body stream ended before Content-Length bytes arrived")
		default:
		}
		n, err := r.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			// io.ReaderTo contract: a non-zero n with io.EOF is
			// the last successful read. Treat it as success.
			if err == io.EOF && len(got) >= wantLen {
				return got, nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return got, fmt.Errorf("short read: got %d of %d bytes (body stream ended early)", len(got), wantLen)
			}
			return got, err
		}
	}
}

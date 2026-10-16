package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/internal/ed25519verify"
	"github.com/incurl/riff/pb/hooks"
)

// newAPIApp returns a freshly-migrated app, with
// riff's hooks registered. The auth record the
// tests need is created inline.
func newAPIApp(t *testing.T) core.App {
	t.Helper()
	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "riff_api_internal_test",
	})
	t.Cleanup(func() { _ = app.ClearBootstrap() })
	require.NoError(t, app.Bootstrap())
	require.NoError(t, app.RunAllMigrations())
	hooks.Register(app)
	return app
}

// newAPIUser inserts a `users` record and returns
// it. Used to set the auth context on a
// RequestEvent for the authenticated routes.
func newAPIUser(t *testing.T, app core.App, username string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("username", username)
	rec.Set("email", username+"@example.com")
	rec.Set("password", "passw0rd!")
	require.NoError(t, app.Save(rec))
	return rec
}

// newAPIRequest builds a core.RequestEvent bound
// to the supplied app. The `auth` is optional; a
// nil value means "no authenticated user" (the
// device-side /pair/finish and /sketches/download
// paths). The returned event is wrapped in an
// httptest.ResponseRecorder so the handler can
// write to it.
func newAPIRequest(app core.App, method, path string, body []byte, auth *core.Record) (*core.RequestEvent, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.1:0"
	e := &core.RequestEvent{
		App:  app,
		Auth: auth,
		Event: router.Event{
			Request:  r,
			Response: rec,
		},
	}
	return e, rec
}

// readJSON decodes a handler response body into
// dst. The httptest.ResponseRecorder stores the
// body as bytes, which readJSON copies before
// json.Unmarshal so the test does not have to.
func readJSON(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	b, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, dst), "body: %s", string(b))
}

// apiErrorStatus extracts the HTTP status from
// the value a handler returned. The convention
// in pb/api is to return *router.ApiError; the
// router converts it to a JSON response at
// serve-time. In the unit-test surface we
// inspect the returned value directly.
func apiErrorStatus(t *testing.T, err error) (int, bool) {
	t.Helper()
	if err == nil {
		return 0, false
	}
	if apiErr, ok := err.(*router.ApiError); ok {
		return apiErr.Status, true
	}
	return 0, false
}

// TestPairStartHandler_RequiresAuth pins the
// unauthenticated path: a missing viewer returns
// 401 and no row is created.
func TestPairStartHandler_RequiresAuth(t *testing.T) {
	app := newAPIApp(t)
	e, _ := newAPIRequest(app, http.MethodPost, "/api/po33/pair/start", nil, nil)
	err := pairStartHandler(e)
	status, ok := apiErrorStatus(t, err)
	require.True(t, ok, "expected ApiError, got %T %v", err, err)
	assert.Equal(t, http.StatusUnauthorized, status)

	// No row was created.
	rows, err := app.FindAllRecords("device_authorizations")
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestPairStartHandler_CreatesRow pins the
// happy path: an authenticated caller creates a
// row with a 6-character Crockford user code and
// a UUID-shaped authorization id.
func TestPairStartHandler_CreatesRow(t *testing.T) {
	app := newAPIApp(t)
	alice := newAPIUser(t, app, "alice")
	e, rec := newAPIRequest(app, http.MethodPost, "/api/po33/pair/start", nil, alice)
	require.NoError(t, pairStartHandler(e))
	assert.Equal(t, http.StatusOK, rec.Code)

	var out pairStartResponse
	readJSON(t, rec, &out)
	assert.Len(t, out.UserCode, 6, "Crockford user code must be 6 chars")
	assert.NotEmpty(t, out.AuthorizationID)
	assert.NotEmpty(t, out.PairingURL)
	assert.NotEmpty(t, out.ExpiresAt)
	// Pairing URL must include the user code.
	assert.Contains(t, out.PairingURL, out.UserCode)

	// Verify the row exists and is PENDING.
	rows, err := app.FindAllRecords("device_authorizations")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "PENDING", rows[0].GetString("status"))
	assert.Equal(t, alice.Id, rows[0].GetString("user"))
}

// TestPairQRCoreHandler_ReturnsPNG pins the QR
// happy path: the response is a valid PNG with
// the right Content-Type.
func TestPairQRCoreHandler_ReturnsPNG(t *testing.T) {
	app := newAPIApp(t)
	alice := newAPIUser(t, app, "alice")
	// Pre-create a row so the lookup is well-defined.
	col, err := app.FindCollectionByNameOrId("device_authorizations")
	require.NoError(t, err)
	rec := core.NewRecord(col)
	rec.Set("user", alice.Id)
	rec.Set("user_code", "ABCDEF")
	rec.Set("authorization_id", "auth-row-id")
	rec.Set("nonce", "deadbeef")
	rec.Set("public_key", "")
	rec.Set("issued_at", time.Now())
	rec.Set("expires_at", time.Now().Add(time.Hour))
	rec.Set("status", "PENDING")
	require.NoError(t, app.Save(rec))

	e, rr := newAPIRequest(app, http.MethodGet, "/api/po33/pair/ABCDEF/qrcode", nil, alice)
	e.Request.SetPathValue("code", "ABCDEF")
	require.NoError(t, pairQRCoreHandler(e))
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "image/png", rr.Header().Get("Content-Type"))
	body := rr.Body.Bytes()
	assert.NotEmpty(t, body)
	// PNG signature is 8 bytes: 89 50 4E 47 0D 0A 1A 0A.
	assert.True(t, bytes.HasPrefix(body, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}),
		"body must start with PNG signature")
}

// TestPairFinishHandler_HappyPath pins the
// full round-trip: /pair/start creates a row,
// /pair/finish signs the canonical request and
// the row is promoted to a po33_devices row.
func TestPairFinishHandler_HappyPath(t *testing.T) {
	app := newAPIApp(t)
	alice := newAPIUser(t, app, "alice")

	// 1. Create a row via /pair/start.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)

	e, rec := newAPIRequest(app, http.MethodPost, "/api/po33/pair/start", nil, alice)
	require.NoError(t, pairStartHandler(e))
	require.Equal(t, http.StatusOK, rec.Code)
	var startOut pairStartResponse
	readJSON(t, rec, &startOut)

	// 2. Build the canonical request and sign it.
	row, err := app.FindFirstRecordByFilter("device_authorizations",
		"user_code = {:code}",
		map[string]any{"code": startOut.UserCode})
	require.NoError(t, err)
	issued := row.GetDateTime("issued_at").Time()
	expires := row.GetDateTime("expires_at").Time()
	nonce := row.GetString("nonce")

	canonical, err := ed25519verify.CanonicalizeAuthorizationRequest(&ed25519verify.AuthorizationRequest{
		DeviceFingerprint: ed25519verify.FingerprintFull(pub),
		AuthorizationID:   startOut.AuthorizationID,
		UserCode:          startOut.UserCode,
		IssuedAtUnix:      issued.Unix(),
		ExpiresAtUnix:     expires.Unix(),
		Nonce:             nonce,
	})
	require.NoError(t, err)
	sig := ed25519.Sign(priv, canonical)

	body, err := json.Marshal(pairFinishRequest{
		UserCode:          startOut.UserCode,
		AuthorizationID:   startOut.AuthorizationID,
		DeviceFingerprint: ed25519verify.FingerprintFull(pub),
		IssuedAtUnix:      issued.Unix(),
		ExpiresAtUnix:     expires.Unix(),
		Nonce:             nonce,
		PublicKey:         pubHex,
		Signature:         hex.EncodeToString(sig),
	})
	require.NoError(t, err)

	// 3. POST /pair/finish. The handler is
	// noAuth — pass nil for the auth record.
	e, rec = newAPIRequest(app, http.MethodPost, "/api/po33/pair/finish", body, nil)
	require.NoError(t, pairFinishHandler(e))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var finOut pairFinishResponse
	readJSON(t, rec, &finOut)
	assert.True(t, strings.HasPrefix(finOut.DeviceID, "po33-devices/"))

	// 4. The device row exists and is ACTIVE.
	devRows, err := app.FindAllRecords("po33_devices")
	require.NoError(t, err)
	require.Len(t, devRows, 1)
	assert.Equal(t, "ACTIVE", devRows[0].GetString("status"))
	assert.Equal(t, alice.Id, devRows[0].GetString("user"))
	assert.Equal(t, ed25519verify.FingerprintFull(pub), devRows[0].GetString("public_key_fpr"))

	// 5. The authorization row is CONSUMED.
	reloaded, err := app.FindRecordById("device_authorizations", row.Id)
	require.NoError(t, err)
	assert.Equal(t, "CONSUMED", reloaded.GetString("status"))
}

// TestPairFinishHandler_BadSignatureRejected pins
// the bad-signature path: the row is marked
// FAILED and no device row is created.
func TestPairFinishHandler_BadSignatureRejected(t *testing.T) {
	app := newAPIApp(t)
	alice := newAPIUser(t, app, "alice")
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)

	// Create the row.
	e, rec := newAPIRequest(app, http.MethodPost, "/api/po33/pair/start", nil, alice)
	require.NoError(t, pairStartHandler(e))
	require.Equal(t, http.StatusOK, rec.Code)
	var startOut pairStartResponse
	readJSON(t, rec, &startOut)

	row, err := app.FindFirstRecordByFilter("device_authorizations",
		"user_code = {:code}",
		map[string]any{"code": startOut.UserCode})
	require.NoError(t, err)

	// Sign with a different key — the signature
	// will not validate.
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	canonical, err := ed25519verify.CanonicalizeAuthorizationRequest(&ed25519verify.AuthorizationRequest{
		DeviceFingerprint: ed25519verify.FingerprintFull(pub),
		AuthorizationID:   startOut.AuthorizationID,
		UserCode:          startOut.UserCode,
		IssuedAtUnix:      row.GetDateTime("issued_at").Time().Unix(),
		ExpiresAtUnix:     row.GetDateTime("expires_at").Time().Unix(),
		Nonce:             row.GetString("nonce"),
	})
	require.NoError(t, err)
	sig := ed25519.Sign(wrongPriv, canonical)

	body, _ := json.Marshal(pairFinishRequest{
		UserCode:          startOut.UserCode,
		AuthorizationID:   startOut.AuthorizationID,
		DeviceFingerprint: ed25519verify.FingerprintFull(pub),
		IssuedAtUnix:      row.GetDateTime("issued_at").Time().Unix(),
		ExpiresAtUnix:     row.GetDateTime("expires_at").Time().Unix(),
		Nonce:             row.GetString("nonce"),
		PublicKey:         pubHex,
		Signature:         hex.EncodeToString(sig),
	})

	e, _ = newAPIRequest(app, http.MethodPost, "/api/po33/pair/finish", body, nil)
	err = pairFinishHandler(e)
	status, ok := apiErrorStatus(t, err)
	require.True(t, ok)
	assert.Equal(t, http.StatusUnauthorized, status)

	// The row is FAILED; no device row exists.
	reloaded, err := app.FindRecordById("device_authorizations", row.Id)
	require.NoError(t, err)
	assert.Equal(t, "FAILED", reloaded.GetString("status"))
	devRows, err := app.FindAllRecords("po33_devices")
	require.NoError(t, err)
	assert.Empty(t, devRows)
}

// TestPairFinishHandler_UnknownCodeRejected pins
// the unknown-code path.
func TestPairFinishHandler_UnknownCodeRejected(t *testing.T) {
	app := newAPIApp(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	body, _ := json.Marshal(pairFinishRequest{
		UserCode:         "NOSUCH",
		AuthorizationID:  "x",
		DeviceFingerprint: "abc",
		IssuedAtUnix:     time.Now().Unix(),
		ExpiresAtUnix:    time.Now().Add(time.Hour).Unix(),
		Nonce:            "n",
		PublicKey:        hex.EncodeToString(pub),
		Signature:        hex.EncodeToString(ed25519.Sign(priv, []byte("anything"))),
	})
	e, _ := newAPIRequest(app, http.MethodPost, "/api/po33/pair/finish", body, nil)
	err = pairFinishHandler(e)
	status, ok := apiErrorStatus(t, err)
	require.True(t, ok)
	assert.Equal(t, http.StatusNotFound, status)
}

// TestPairFinishHandler_ExpiredCodeRejected pins
// the expired-row path: the row is marked
// EXPIRED and the handler returns 410 Gone.
func TestPairFinishHandler_ExpiredCodeRejected(t *testing.T) {
	app := newAPIApp(t)
	alice := newAPIUser(t, app, "alice")
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	col, err := app.FindCollectionByNameOrId("device_authorizations")
	require.NoError(t, err)
	row := core.NewRecord(col)
	row.Set("user", alice.Id)
	row.Set("user_code", "ABCDEF")
	row.Set("authorization_id", "auth-id")
	row.Set("nonce", "n")
	row.Set("public_key", hex.EncodeToString(pub))
	row.Set("issued_at", time.Now().Add(-time.Hour))
	row.Set("expires_at", time.Now().Add(-time.Minute))
	row.Set("status", "PENDING")
	require.NoError(t, app.Save(row))

	canonical, err := ed25519verify.CanonicalizeAuthorizationRequest(&ed25519verify.AuthorizationRequest{
		DeviceFingerprint: ed25519verify.FingerprintFull(pub),
		AuthorizationID:   "auth-id",
		UserCode:          "ABCDEF",
		IssuedAtUnix:      row.GetDateTime("issued_at").Time().Unix(),
		ExpiresAtUnix:     row.GetDateTime("expires_at").Time().Unix(),
		Nonce:             "n",
	})
	require.NoError(t, err)
	sig := ed25519.Sign(priv, canonical)

	body, _ := json.Marshal(pairFinishRequest{
		UserCode:          "ABCDEF",
		AuthorizationID:   "auth-id",
		DeviceFingerprint: ed25519verify.FingerprintFull(pub),
		IssuedAtUnix:      row.GetDateTime("issued_at").Time().Unix(),
		ExpiresAtUnix:     row.GetDateTime("expires_at").Time().Unix(),
		Nonce:             "n",
		PublicKey:         hex.EncodeToString(pub),
		Signature:         hex.EncodeToString(sig),
	})
	e, _ := newAPIRequest(app, http.MethodPost, "/api/po33/pair/finish", body, nil)
	err = pairFinishHandler(e)
	status, ok := apiErrorStatus(t, err)
	require.True(t, ok)
	assert.Equal(t, http.StatusGone, status)
}

// TestPairRevokeHandler_DeletesRow pins the
// happy path: the caller can delete their own
// in-flight authorization row.
func TestPairRevokeHandler_DeletesRow(t *testing.T) {
	app := newAPIApp(t)
	alice := newAPIUser(t, app, "alice")
	col, err := app.FindCollectionByNameOrId("device_authorizations")
	require.NoError(t, err)
	row := core.NewRecord(col)
	row.Set("user", alice.Id)
	row.Set("user_code", "ABCDEF")
	row.Set("authorization_id", "auth-id")
	row.Set("nonce", "n")
	// public_key can be empty: the device supplies
	// it at /pair/finish time. The column is
	// nullable in the migration.
	row.Set("public_key", "")
	row.Set("issued_at", time.Now())
	row.Set("expires_at", time.Now().Add(time.Hour))
	row.Set("status", "PENDING")
	require.NoError(t, app.Save(row))

	e, rec := newAPIRequest(app, http.MethodDelete,
		"/api/po33/pair/"+row.Id, nil, alice)
	e.Request.SetPathValue("id", row.Id)
	require.NoError(t, pairRevokeHandler(e))
	assert.Equal(t, http.StatusOK, rec.Code)

	// The row is gone.
	_, err = app.FindRecordById("device_authorizations", row.Id)
	assert.Error(t, err)
}

// TestPairRevokeHandler_NotOwnerForbidden pins
// the cross-tenant guard.
func TestPairRevokeHandler_NotOwnerForbidden(t *testing.T) {
	app := newAPIApp(t)
	alice := newAPIUser(t, app, "alice")
	bob := newAPIUser(t, app, "bob")
	col, err := app.FindCollectionByNameOrId("device_authorizations")
	require.NoError(t, err)
	row := core.NewRecord(col)
	row.Set("user", alice.Id)
	row.Set("user_code", "ABCDEF")
	row.Set("authorization_id", "auth-id")
	row.Set("nonce", "n")
	row.Set("public_key", "")
	row.Set("issued_at", time.Now())
	row.Set("expires_at", time.Now().Add(time.Hour))
	row.Set("status", "PENDING")
	require.NoError(t, app.Save(row))

	e, _ := newAPIRequest(app, http.MethodDelete,
		"/api/po33/pair/"+row.Id, nil, bob)
	e.Request.SetPathValue("id", row.Id)
	err = pairRevokeHandler(e)
	status, ok := apiErrorStatus(t, err)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, status)

	// The row is still there.
	_, err = app.FindRecordById("device_authorizations", row.Id)
	assert.NoError(t, err)
}

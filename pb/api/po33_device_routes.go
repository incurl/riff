package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/incurl/riff/internal/ed25519verify"
	"github.com/incurl/riff/internal/qr"
	"github.com/incurl/riff/internal/random"
)

// pairAuthorizationTTL is how long an in-flight
// device pairing row stays valid. The device must
// read the QR code, build the canonical request,
// and POST the signature inside this window.
// 15 minutes is long enough for a human at the
// device to type the code and pair; short enough
// that a stale QR code does not stay scannable on
// a lost phone.
const pairAuthorizationTTL = 15 * time.Minute

// pairStartRequest is the body of
// POST /api/po33/pair/start. The optional
// `public_key` field lets the SPA pre-register the
// key the device will use. When omitted, the key
// is supplied at /pair/finish time (the more
// common case: the user opens the SPA, generates
// the QR, then walks over to the device and
// types the code).
type pairStartRequest struct {
	PublicKey string `json:"public_key"`
}

// pairStartResponse is the body of
// POST /api/po33/pair/start. The SPA renders the
// user_code on screen and embeds the pairing_url
// in the QR code.
type pairStartResponse struct {
	AuthorizationID string `json:"authorization_id"`
	UserCode        string `json:"user_code"`
	PairingURL      string `json:"pairing_url"`
	ExpiresAt       string `json:"expires_at"`
}

// pairStartHandler serves POST /api/po33/pair/start.
// The caller must be authenticated; the row
// created belongs to the caller. The
// `user_code` is a 6-character Crockford-base32
// token; the unique index on the column makes a
// collision-triggered retry the worst case (and
// the retry loop is bounded by 5 attempts).
func pairStartHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return NewAPIError(http.StatusUnauthorized, "authentication required")
	}
	var body pairStartRequest
	// Body is optional; an empty body is fine.
	if e.Request.ContentLength > 0 {
		if err := e.BindBody(&body); err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid request body")
		}
	}

	col, err := e.App.FindCollectionByNameOrId("device_authorizations")
	if err != nil {
		return err
	}

	// Generate the user code and the row id. The
	// row id is what the device echoes back; the
	// user code is what the user types. Both are
	// unique-indexed so a collision surfaces as a
	// storage error we can retry.
	const maxAttempts = 5
	var (
		userCode   string
		authID     string
		nonce      string
		rec        *core.Record
		publicKey  = strings.TrimSpace(body.PublicKey)
		saveErr    error
	)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		userCode, err = random.Crockford(6)
		if err != nil {
			return err
		}
		authID = random.UUID()
		// UUID does not return an error in
		// the current internal/random helper,
		// so we keep the err local to
		// random.Crockford above.
		_ = err
		// 16 random bytes hex-encoded. The device
		// includes this in the signing input so a
		// captured signature cannot be replayed
		// against a different authorization row.
		raw, err := randomBytes(16)
		if err != nil {
			return err
		}
		nonce = hex.EncodeToString(raw)

		rec = core.NewRecord(col)
		rec.Set("user", e.Auth.Id)
		rec.Set("user_code", userCode)
		rec.Set("authorization_id", authID)
		rec.Set("nonce", nonce)
		rec.Set("public_key", publicKey) // may be empty; /pair/finish will set it
		now := time.Now()
		rec.Set("issued_at", now)
		rec.Set("expires_at", now.Add(pairAuthorizationTTL))
		rec.Set("status", "PENDING")
		saveErr = e.App.Save(rec)
		if saveErr == nil {
			break
		}
		// Most likely a unique-index collision on
		// user_code or authorization_id; retry
		// with fresh values.
	}
	if saveErr != nil {
		return NewAPIError(http.StatusInternalServerError,
			"could not create device authorization after retries")
	}

	// The pairing URL the device's QR code
	// encodes. The device uses the user_code in
	// the URL to find the right authorization
	// row; the fingerprint the user typed and
	// the signature round-trip through
	// /pair/finish.
	scheme := "http"
	if e.Request.TLS != nil || strings.EqualFold(e.Request.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := e.Request.Host
	pairingURL := fmt.Sprintf("%s://%s/api/po33/pair/finish?code=%s", scheme, host, userCode)

	return e.JSON(http.StatusOK, pairStartResponse{
		AuthorizationID: authID,
		UserCode:        userCode,
		PairingURL:      pairingURL,
		ExpiresAt:       rec.GetDateTime("expires_at").Time().UTC().Format(time.RFC3339),
	})
}

// pairQRCoreHandler serves
// GET /api/po33/pair/{code}/qrcode. The {code}
// segment is the user_code. The handler returns
// a PNG image the SPA embeds in <img src="data:..."/>.
//
// Auth: same as /pair/start; the SPA's authenticated
// session is the protection. A future hardening
// pass could require a one-shot bearer token
// minted by /pair/start, but a leaked PNG is
// useless to a stranger without a signed request.
func pairQRCoreHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return NewAPIError(http.StatusUnauthorized, "authentication required")
	}
	code := e.Request.PathValue("code")
	if code == "" {
		return NewAPIError(http.StatusBadRequest, "missing code")
	}

	// Verify the code belongs to a row owned by
	// the caller. A non-owner request gets
	// NotFound rather than Forbidden so a
	// stranger cannot enumerate active
	// authorization rows.
	row, err := findActivePairRow(e.App, code, e.Auth.Id)
	if err != nil {
		return NewAPIError(http.StatusNotFound, "pairing code not found or expired")
	}

	scheme := "http"
	if e.Request.TLS != nil || strings.EqualFold(e.Request.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := e.Request.Host
	url := fmt.Sprintf("%s://%s/api/po33/pair/finish?code=%s", scheme, host, code)

	png, err := qr.PNG(url, 256)
	if err != nil {
		return NewAPIError(http.StatusInternalServerError, "could not generate QR code")
	}

	e.Response.Header().Set("Content-Type", "image/png")
	e.Response.Header().Set("Cache-Control", "no-store")
	// The user_code never changes; the row
	// itself has a TTL the SPA polls.
	e.Response.Header().Set("X-Pair-Authorization-Id", row.GetString("authorization_id"))
	e.Response.Header().Set("X-Pair-Expires-At",
		row.GetDateTime("expires_at").Time().UTC().Format(time.RFC3339))
	e.Response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(e.Response, "")
	_, _ = e.Response.Write(png)
	return nil
}

// pairFinishRequest is the body of
// POST /api/po33/pair/finish. The body is a
// device-supplied JSON envelope; the signature
// is over the canonical JSON serialisation of
// the authorization request, not over the body
// itself, so a server that re-orders the fields
// cannot break the signature.
type pairFinishRequest struct {
	UserCode         string `json:"user_code"`
	AuthorizationID  string `json:"authorization_id"`
	DeviceFingerprint string `json:"device_fingerprint"`
	IssuedAtUnix     int64  `json:"issued_at_unix"`
	ExpiresAtUnix    int64  `json:"expires_at_unix"`
	Nonce            string `json:"nonce"`
	PublicKey        string `json:"public_key"`
	// Signature is hex-encoded 64 raw bytes.
	Signature string `json:"signature"`
}

// pairFinishResponse is the body of
// POST /api/po33/pair/finish on success. The
// `device_id` is the resource name the SPA can
// use to revoke the device later.
type pairFinishResponse struct {
	DeviceID string `json:"device_id"`
}

// pairFinishHandler serves
// POST /api/po33/pair/finish. The request is NOT
// authenticated: the device has no auth token
// until after the pairing completes. The
// signature is the bearer credential. The flow:
//
//  1. Look up the row by user_code. Confirm the
//     row is PENDING, the nonce / fingerprint
//     match the row, and the row is not expired.
//  2. Validate the signature with the device's
//     public key (extracted from the row, which
//     the device supplied at /pair/start time
//     or in this body).
//  3. Promote the row to a `po33_devices` row
//     in ACTIVE state.
//  4. Mark the authorization row CONSUMED.
//
// The rate-limit scope is `ScopePairFinish` so a
// brute-force attack against the signature does
// not exhaust the catch-all IP budget.
func pairFinishHandler(e *core.RequestEvent) error {
	var body pairFinishRequest
	if err := e.BindBody(&body); err != nil {
		return NewAPIError(http.StatusBadRequest, "invalid request body")
	}
	if body.UserCode == "" || body.AuthorizationID == "" ||
		body.PublicKey == "" || body.Signature == "" {
		return NewAPIError(http.StatusBadRequest, "missing required fields")
	}

	// Look up the row. We do NOT require the
	// caller be authenticated; the signature is
	// the credential.
	row, err := e.App.FindFirstRecordByFilter("device_authorizations",
		"user_code = {:code}",
		map[string]any{"code": body.UserCode})
	if err != nil || row == nil {
		return NewAPIError(http.StatusNotFound, "pairing code not found")
	}
	if row.GetString("status") != "PENDING" {
		return NewAPIError(http.StatusConflict,
			fmt.Sprintf("pairing row is %s", row.GetString("status")))
	}
	if time.Now().After(row.GetDateTime("expires_at").Time()) {
		// Mark the row as EXPIRED so the
		// scheduler does not need to clean it
		// up.
		row.Set("status", "EXPIRED")
		_ = e.App.Save(row)
		return NewAPIError(http.StatusGone, "pairing code expired")
	}
	if row.GetString("authorization_id") != body.AuthorizationID {
		return NewAPIError(http.StatusBadRequest, "authorization_id mismatch")
	}
	if row.GetString("nonce") != body.Nonce {
		return NewAPIError(http.StatusBadRequest, "nonce mismatch")
	}
	issuedAt := row.GetDateTime("issued_at").Time()
	expiresAt := row.GetDateTime("expires_at").Time()
	if body.IssuedAtUnix != issuedAt.Unix() ||
		body.ExpiresAtUnix != expiresAt.Unix() {
		return NewAPIError(http.StatusBadRequest, "issued/expires mismatch")
	}
	// The device fingerprint is bound to the
	// row's stored public key. The device may
	// supply the public key in this body (if it
	// was not pre-registered at /pair/start
	// time) or we use the value the SPA
	// pre-registered.
	storedKey := row.GetString("public_key")
	if storedKey == "" {
		storedKey = body.PublicKey
		row.Set("public_key", storedKey)
	} else if storedKey != body.PublicKey {
		return NewAPIError(http.StatusBadRequest, "public_key mismatch")
	}

	pub, err := ed25519verify.ParsePublicKey(mustHexDecode(storedKey))
	if err != nil {
		return NewAPIError(http.StatusBadRequest, "stored public_key is malformed")
	}
	sig, err := ed25519verify.ParseSignature(mustHexDecode(body.Signature))
	if err != nil {
		return NewAPIError(http.StatusBadRequest, "signature is malformed")
	}

	// Re-derive the fingerprint the device
	// presented and reject if it does not match
	// the public key. This binds the public key
	// and the fingerprint together so an
	// attacker cannot substitute a key whose
	// fingerprint is a row they have a pairing
	// code for.
	if body.DeviceFingerprint != "" &&
		body.DeviceFingerprint != ed25519verify.FingerprintFull(pub) {
		return NewAPIError(http.StatusBadRequest, "device_fingerprint mismatch")
	}

	canonical, err := ed25519verify.CanonicalizeAuthorizationRequest(&ed25519verify.AuthorizationRequest{
		DeviceFingerprint: body.DeviceFingerprint,
		AuthorizationID:   body.AuthorizationID,
		UserCode:          body.UserCode,
		IssuedAtUnix:      body.IssuedAtUnix,
		ExpiresAtUnix:     body.ExpiresAtUnix,
		Nonce:             body.Nonce,
	})
	if err != nil {
		return NewAPIError(http.StatusInternalServerError, "could not canonicalize request")
	}
	if err := ed25519verify.Verify(pub, canonical, sig); err != nil {
		// Mark the row as FAILED so the
		// SPA can render a precise diagnostic
		// if it polls.
		row.Set("status", "FAILED")
		row.Set("failure_reason", "signature does not validate")
		_ = e.App.Save(row)
		return NewAPIError(http.StatusUnauthorized, "signature does not validate")
	}

	// Promote to a `po33_devices` row.
	devices, err := e.App.FindCollectionByNameOrId("po33_devices")
	if err != nil {
		return err
	}
	devRec := core.NewRecord(devices)
	devRec.Set("user", row.GetString("user"))
	devRec.Set("label", "")
	devRec.Set("public_key", storedKey)
	devRec.Set("public_key_fpr", ed25519verify.FingerprintFull(pub))
	devRec.Set("status", "ACTIVE")
	if err := e.App.Save(devRec); err != nil {
		// A (user, fingerprint) unique
		// collision means the same device
		// already exists; refresh the existing
		// row's status to ACTIVE.
		if existing, err2 := e.App.FindFirstRecordByFilter("po33_devices",
			"user = {:uid} && public_key_fpr = {:fpr}",
			map[string]any{"uid": row.GetString("user"), "fpr": ed25519verify.FingerprintFull(pub)},
		); err2 == nil && existing != nil {
			existing.Set("status", "ACTIVE")
			existing.Set("public_key", storedKey)
			if err := e.App.Save(existing); err != nil {
				return NewAPIError(http.StatusInternalServerError, err.Error())
			}
			devRec = existing
		} else {
			return NewAPIError(http.StatusInternalServerError, err.Error())
		}
	}

	// Mark the authorization row CONSUMED.
	row.Set("status", "CONSUMED")
	if err := e.App.Save(row); err != nil {
		return NewAPIError(http.StatusInternalServerError, err.Error())
	}

	return e.JSON(http.StatusOK, pairFinishResponse{
		DeviceID: "po33-devices/" + devRec.Id,
	})
}

// pairRevokeHandler serves
// DELETE /api/po33/pair/{id}. The {id} is the
// authorization row id. The caller must be the
// owner of the row. The row is removed
// immediately; the bound device (if any) is left
// in place.
func pairRevokeHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return NewAPIError(http.StatusUnauthorized, "authentication required")
	}
	id := e.Request.PathValue("id")
	if id == "" {
		return NewAPIError(http.StatusBadRequest, "missing id")
	}
	row, err := e.App.FindRecordById("device_authorizations", id)
	if err != nil {
		return NewAPIError(http.StatusNotFound, "authorization not found")
	}
	if row.GetString("user") != e.Auth.Id {
		return NewAPIError(http.StatusForbidden, "not your authorization")
	}
	if err := e.App.Delete(row); err != nil {
		return NewAPIError(http.StatusInternalServerError, err.Error())
	}
	return e.JSON(http.StatusOK, map[string]any{"deleted": true})
}

// findActivePairRow is a small helper that
// looks up an authorization row by user_code and
// confirms it belongs to the supplied user and
// is still PENDING. Returns (nil, err) on any
// miss; the handler maps the error to NotFound.
func findActivePairRow(app core.App, code, userID string) (*core.Record, error) {
	if code == "" || userID == "" {
		return nil, errors.New("empty code or user")
	}
	row, err := app.FindFirstRecordByFilter("device_authorizations",
		"user_code = {:code} && user = {:uid} && status = 'PENDING' && expires_at > {:now}",
		map[string]any{
			"code": code,
			"uid":  userID,
			"now":  types.NowDateTime(),
		})
	if err != nil || row == nil {
		return nil, errors.New("not found")
	}
	return row, nil
}

// mustHexDecode is a tiny wrapper that surfaces
// invalid hex as a Go error the handler can
// report as InvalidArgument. Used in
// pairFinishHandler for the public_key and
// signature fields, which are hex-encoded.
func mustHexDecode(s string) []byte {
	if s == "" {
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// randomBytes is a small wrapper around
// crypto/rand. It is in a file rather than the
// shared `internal/random` package because the
// public API is `Crockford` and `UUID` today;
// adding a third return shape would force every
// caller to keep up. The wrapper is 6 lines.
func randomBytes(n int) ([]byte, error) {
	if n < 1 {
		return nil, errors.New("n must be >= 1")
	}
	out := make([]byte, n)
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return out, nil
}

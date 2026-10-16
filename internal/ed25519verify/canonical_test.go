package ed25519verify

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCanonicalizeAuthorizationRequest_KnownVector pins the
// canonical byte sequence for a known-good authorization
// request. The device firmware performs the same
// serialisation in C; the test guards against accidental
// drift in Go (e.g. someone changing the field order or
// letting encoding/json inject a space).
func TestCanonicalizeAuthorizationRequest_KnownVector(t *testing.T) {
	req := &AuthorizationRequest{
		DeviceFingerprint: "abc123",
		AuthorizationID:   "01HXYZABCDEFGHJ01234",
		UserCode:          "ABCDE",
		IssuedAtUnix:      1730000000,
		ExpiresAtUnix:     1730000900,
		Nonce:             "deadbeef",
	}
	got, err := CanonicalizeAuthorizationRequest(req)
	require.NoError(t, err)

	want := `{"authorization_id":"01HXYZABCDEFGHJ01234","device_fingerprint":"abc123","expires_at_unix":1730000900,"issued_at_unix":1730000000,"nonce":"deadbeef","user_code":"ABCDE"}`
	assert.Equal(t, want, string(got))
}

// TestCanonicalizeAuthorizationRequest_KeyOrderIndependent
// pins the property that the serialisation is the same
// regardless of how the caller populated the struct
// fields. The map-based dispatch re-sorts keys
// alphabetically.
func TestCanonicalizeAuthorizationRequest_KeyOrderIndependent(t *testing.T) {
	a := &AuthorizationRequest{
		DeviceFingerprint: "fp",
		AuthorizationID:   "aid",
		UserCode:          "uc",
		IssuedAtUnix:      1,
		ExpiresAtUnix:     2,
		Nonce:             "n",
	}
	b := &AuthorizationRequest{
		Nonce:             "n",
		ExpiresAtUnix:     2,
		IssuedAtUnix:      1,
		UserCode:          "uc",
		AuthorizationID:   "aid",
		DeviceFingerprint: "fp",
	}
	canonA, err := CanonicalizeAuthorizationRequest(a)
	require.NoError(t, err)
	canonB, err := CanonicalizeAuthorizationRequest(b)
	require.NoError(t, err)
	assert.Equal(t, string(canonA), string(canonB))
}

// TestCanonicalizeAuthorizationRequest_Nil covers the
// defensive nil check.
func TestCanonicalizeAuthorizationRequest_Nil(t *testing.T) {
	_, err := CanonicalizeAuthorizationRequest(nil)
	require.Error(t, err)
}

// TestParsePublicKey_AcceptsRawForm pins the 32-byte raw
// public key shape the device firmware sends.
func TestParsePublicKey_AcceptsRawForm(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	got, err := ParsePublicKey(pub)
	require.NoError(t, err)
	assert.Equal(t, pub, got)
}

// TestParsePublicKey_AcceptsConcatenatedForm pins the
// 64-byte (seed||key) form some libraries emit.
func TestParsePublicKey_AcceptsConcatenatedForm(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	concat := append(seed, pub...)
	got, err := ParsePublicKey(concat)
	require.NoError(t, err)
	assert.Equal(t, pub, got)
}

// TestParsePublicKey_RejectsOtherSizes pins the
// InvalidArgument path.
func TestParsePublicKey_RejectsOtherSizes(t *testing.T) {
	for _, n := range []int{0, 16, 33, 63, 100} {
		_, err := ParsePublicKey(make([]byte, n))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidPublicKey)
	}
}

// TestParseSignature_RejectsBadLength pins the
// InvalidArgument path on the signature.
func TestParseSignature_RejectsBadLength(t *testing.T) {
	for _, n := range []int{0, 32, 63, 65, 100} {
		_, err := ParseSignature(make([]byte, n))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidSignature)
	}
}

// TestVerify_RoundTrip is the happy path: a known message
// signed by a known key verifies.
func TestVerify_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	msg := []byte("hello, world")
	sig := ed25519.Sign(priv, msg)
	require.NoError(t, Verify(pub, msg, sig))
}

// TestVerify_RejectsBadSignature pins the
// Unauthenticated path: a well-formed signature that
// does not match the message is rejected.
func TestVerify_RejectsBadSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sig := make([]byte, ed25519.SignatureSize)
	err = Verify(pub, []byte("hello, world"), sig)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

// TestFingerprint_Deterministic pins the property that
// the same key always produces the same fingerprint.
func TestFingerprint_Deterministic(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	assert.Equal(t, Fingerprint(pub), Fingerprint(pub))
	assert.Equal(t, FingerprintFull(pub), FingerprintFull(pub))
}

// TestFingerprint_ShortAndFullDiffer pins the fact that
// the short form is the first 8 hex chars of the full
// form, so the UI can just truncate. The unit test fails
// loudly if a future refactor changes the truncation.
func TestFingerprint_ShortAndFullDiffer(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	short := Fingerprint(pub)
	full := FingerprintFull(pub)
	assert.Equal(t, short, full[:8])
	assert.Len(t, short, 8)
	assert.Len(t, full, 64)
}

// TestParseSignature_AcceptsRawForm is the happy path
// for ParseSignature.
func TestParseSignature_AcceptsRawForm(t *testing.T) {
	sig := make([]byte, ed25519.SignatureSize)
	_, err := rand.Read(sig)
	require.NoError(t, err)
	got, err := ParseSignature(sig)
	require.NoError(t, err)
	assert.Equal(t, sig, got)
}

// TestRoundTripAuthorizationRequest_WithFingerprint is the
// integration test that ties everything together: a
// device generates a key, builds a canonical request,
// signs it, and the server verifies. The test uses the
// Fingerprint helper to populate the request so the
// signing input matches what the device would send.
func TestRoundTripAuthorizationRequest_WithFingerprint(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	req := &AuthorizationRequest{
		DeviceFingerprint: FingerprintFull(pub),
		AuthorizationID:   "auth-row-id",
		UserCode:          "ABCDE",
		IssuedAtUnix:      1730000000,
		ExpiresAtUnix:     1730000900,
		Nonce:             hex.EncodeToString([]byte{0xde, 0xad, 0xbe, 0xef}),
	}
	canon, err := CanonicalizeAuthorizationRequest(req)
	require.NoError(t, err)
	sig := ed25519.Sign(priv, canon)

	require.NoError(t, Verify(pub, canon, sig))
}

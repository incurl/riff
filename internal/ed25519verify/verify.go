package ed25519verify

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// Sentinel errors. Handlers map these to precise Connect
// error codes; library consumers that do not care about the
// distinction can compare with errors.Is.
var (
	// ErrInvalidPublicKey is returned when the supplied
	// public key cannot be parsed (wrong length, not a
	// point on the curve). Mapped to InvalidArgument.
	ErrInvalidPublicKey = errors.New("ed25519verify: invalid public key")

	// ErrInvalidSignature is returned when the signature
	// is the wrong length or otherwise malformed.
	// Mapped to InvalidArgument.
	ErrInvalidSignature = errors.New("ed25519verify: invalid signature")

	// ErrVerificationFailed is returned when the
	// signature is well-formed but does not validate
	// against the public key over the canonical message.
	// Mapped to Unauthenticated.
	ErrVerificationFailed = errors.New("ed25519verify: signature does not validate")
)

// ParsePublicKey parses a 32-byte raw Ed25519 public key.
// The PO-33 firmware produces a 32-byte blob; we accept the
// 64-byte expanded form too for tests that round-trip through
// the stdlib.
func ParsePublicKey(raw []byte) (ed25519.PublicKey, error) {
	switch len(raw) {
	case ed25519.PublicKeySize:
		return ed25519.PublicKey(raw), nil
	case 2 * ed25519.PublicKeySize:
		// Some libraries emit the concatenated
		// (seed || key) form. Take the second half.
		return ed25519.PublicKey(raw[ed25519.PublicKeySize:]), nil
	default:
		return nil, fmt.Errorf("%w: got %d bytes, want %d or %d",
			ErrInvalidPublicKey, len(raw),
			ed25519.PublicKeySize, 2*ed25519.PublicKeySize)
	}
}

// ParseSignature parses a 64-byte raw Ed25519 signature.
func ParseSignature(raw []byte) ([]byte, error) {
	if len(raw) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: got %d bytes, want %d",
			ErrInvalidSignature, len(raw), ed25519.SignatureSize)
	}
	return raw, nil
}

// Verify validates sig over message under pub. Returns
// ErrVerificationFailed on a bad signature so the handler
// can map it to a precise Connect code without inspecting
// the underlying stdlib error.
func Verify(pub ed25519.PublicKey, message, sig []byte) error {
	if !ed25519.Verify(pub, message, sig) {
		return ErrVerificationFailed
	}
	return nil
}

// Fingerprint returns the short, hex-encoded SHA-256
// truncation of the public key the device's UI shows the
// user ("...pair with the device showing EF3A 91C2"). 8
// hex chars = 32 bits = a collision-resistant display
// surface; the full SHA-256 is the authoritative
// fingerprint we store alongside the row.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

// FingerprintFull is the full 64-hex-char SHA-256
// fingerprint stored on the row. The short form is for UI
// only; the full form is the value operators search by.
func FingerprintFull(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// formatInt renders a JSON-style integer with no
// exponent and no leading zeros; Go's strconv already
// produces that shape for int64, so this is just a
// re-export that keeps callers from importing strconv
// directly.
func formatInt(n int64) string {
	return strconv.FormatInt(n, 10)
}

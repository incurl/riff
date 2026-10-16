package connect

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
)

// ed25519Verify_FingerprintForTest computes the
// same SHA-256 hex fingerprint the production
// ed25519verify package uses. The function is
// kept in the test surface because pulling
// internal/ed25519verify into the production code
// for a one-line helper would be a circular
// dependency (the production handlers do not
// need this helper — only the tests do, to
// generate fixtures).
func ed25519Verify_FingerprintForTest(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

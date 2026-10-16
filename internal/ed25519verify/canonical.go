package ed25519verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// AuthorizationRequest is the shape the device signs in
// the binding flow. The fields are stable across firmware
// versions; any new field must be added at the end so
// existing signatures keep validating.
//
// The on-the-wire signing input is
// CanonicalizeAuthorizationRequest(req) — a deterministic
// JSON serialisation with sorted keys, no whitespace, and
// fixed number rendering. The device firmware performs the
// same serialisation in C; the unit test in
// canonical_test.go pins the exact byte shape so the two
// sides never drift.
type AuthorizationRequest struct {
	// DeviceFingerprint is the hex-encoded full SHA-256
	// of the device's Ed25519 public key. The server
	// re-derives it from the key it received, so the
	// two values MUST match or the signature is
	// rejected.
	DeviceFingerprint string `json:"device_fingerprint"`

	// AuthorizationID is the server-issued row id the
	// device echoes back. The server checks it matches
	// the in-flight authorization row.
	AuthorizationID string `json:"authorization_id"`

	// UserCode is the short Crockford-base32 token the
	// device is pairing to (e.g. "ABCDE"). Echoed back
	// so the user can verify what they typed.
	UserCode string `json:"user_code"`

	// IssuedAtUnix is the unix-second timestamp the
	// authorization row was minted at. Replayed
	// signatures older than the row's expires_at are
	// rejected.
	IssuedAtUnix int64 `json:"issued_at_unix"`

	// ExpiresAtUnix is the unix-second timestamp after
	// which the authorization row is invalid.
	ExpiresAtUnix int64 `json:"expires_at_unix"`

	// Nonce is a server-issued random 16-byte
	// hex-encoded value bound to the row. Prevents an
	// attacker from replaying a captured signature
	// against a different authorization row.
	Nonce string `json:"nonce"`
}

// CanonicalizeAuthorizationRequest serialises req in the
// deterministic shape both sides sign. Sorted keys, no
// whitespace, integers rendered as bare digits. Strings
// pass through encoding/json's standard string escaper
// (which is the same escaper both sides use).
func CanonicalizeAuthorizationRequest(req *AuthorizationRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("ed25519verify: nil authorization request")
	}
	// Build a key-sorted map so the on-wire form is
	// stable regardless of how callers populate the
	// struct. The map keys match the JSON tags 1:1; any
	// new field on the proto side must also be added
	// here.
	fields := map[string]string{
		"device_fingerprint": req.DeviceFingerprint,
		"authorization_id":   req.AuthorizationID,
		"user_code":          req.UserCode,
		"issued_at_unix":     formatInt(req.IssuedAtUnix),
		"expires_at_unix":    formatInt(req.ExpiresAtUnix),
		"nonce":              req.Nonce,
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		// Escape the key the same way encoding/json
		// does. The keys we accept are fixed ASCII
		// identifiers, so this is just a matter of
		// being defensive.
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		// Value can be string (most fields) or bare
		// integer (the two *_unix fields). We know
		// which is which at canonicalisation time, so
		// the dispatch is hard-coded rather than
		// re-typed via encoding/json.
		if k == "issued_at_unix" || k == "expires_at_unix" {
			// The integer is rendered as-is; formatInt
			// guarantees no exponent and no leading
			// zeros, matching Go's strconv output.
			n, err := strconv.ParseInt(fields[k], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("ed25519verify: bad integer %q for %s: %w", fields[k], k, err)
			}
			buf.WriteString(formatInt(n))
		} else {
			vb, err := json.Marshal(fields[k])
			if err != nil {
				return nil, fmt.Errorf("ed25519verify: marshal %s: %w", k, err)
			}
			buf.Write(vb)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

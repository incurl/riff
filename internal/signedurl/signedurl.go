// Package signedurl issues and verifies short-lived,
// HMAC-signed URLs for the device-pull sketch download
// path described in docs/design/po33-device-binding.md
// (2026-10-19 appendix).
//
// A signed URL has the shape:
//
//	<path>?exp=<unix-seconds>&mac=<hex-hmac-sha256>
//
// where `mac` is the HMAC-SHA256 of the exact bytes
// `<path>?exp=<unix-seconds>` keyed by a 32-byte
// per-install secret. The verifier checks the path
// prefix, the exp window, and the MAC. A URL is valid
// only when the verifier's key matches the issuer's
// key (so a URL issued before a key rotation is
// rejected).
//
// The package never logs the secret; the install
// bootstrap path is the only place a secret is read
// or written, and even that is silent. The internal
// /random package supplies the secret bytes so the
// same crypto/rand source backs every secret in
// the binary.
//
// The key is intentionally NOT persisted in code: a
// fresh install mints one on bootstrap. The
// caller passes the key into New(); the install
// bootstrap path in main.go owns the read/write of
// the key to instance_settings.download_signing_key.
package signedurl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ErrExpired is returned by Verify when the URL's
// exp timestamp is in the past.
var ErrExpired = errors.New("signed url expired")

// ErrBadMAC is returned by Verify when the URL's
// MAC does not match the recomputed MAC.
var ErrBadMAC = errors.New("signed url has bad mac")

// ErrMalformed is returned by Verify when the
// URL is missing the exp / mac query parameters
// or the exp value is not a base-10 integer.
var ErrMalformed = errors.New("signed url malformed")

// DefaultTTL is the default lifetime of a signed
// URL. 15 minutes is long enough for a human at
// the device to poll + download + verify the
// attachment, and short enough that a leaked URL
// from a packet capture expires before the next
// round of rotation.
const DefaultTTL = 15 * time.Minute

// Signer issues signed URLs. A Signer is bound to
// a single key; an install bootstrap path mints
// one Signer and passes it to every caller.
type Signer struct {
	now func() time.Time
	key []byte
}

// New returns a Signer that uses the given 32-byte
// key and time.Now. The key is copied so the caller
// can zero its buffer after the call.
func New(key []byte) *Signer {
	cp := make([]byte, len(key))
	copy(cp, key)
	return &Signer{now: time.Now, key: cp}
}

// WithClock returns a copy of the Signer whose time
// source is f. Used in tests so a Verify can pin
// "now" to a deterministic instant.
func (s *Signer) WithClock(f func() time.Time) *Signer {
	cp := *s
	cp.now = f
	return &cp
}

// Sign returns the signed URL string for the given
// path, using the default TTL. The path is appended
// verbatim; the caller is responsible for any prefix
// (e.g. "/api/po33/sketches/").
func (s *Signer) Sign(path string) string {
	return s.SignWithTTL(path, DefaultTTL)
}

// SignWithTTL is Sign with an explicit TTL. The
// returned string is the full URL — path + query
// string — with no scheme / host prefix.
func (s *Signer) SignWithTTL(path string, ttl time.Duration) string {
	exp := s.now().Add(ttl).Unix()
	base := path + "?exp=" + strconv.FormatInt(exp, 10)
	mac := s.mac([]byte(base))
	return base + "&mac=" + hex.EncodeToString(mac)
}

// Verify checks that the URL's path matches
// `expectPath`, that the URL has not expired, and
// that the MAC is correct for the current key.
//
// `rawQuery` is the request URL's RawQuery
// (e.g. "exp=1700000000&mac=deadbeef"). The
// signature is computed against the path + the
// `exp=...` segment; the `mac=...` segment is
// verified against the result.
//
// Returns the exp timestamp on success so the
// caller can surface "expires at" in audit rows.
func (s *Signer) Verify(expectPath, rawQuery string) (time.Time, error) {
	expStr, macHex, err := splitQuery(rawQuery)
	if err != nil {
		return time.Time{}, err
	}
	expN, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	macBytes, err := hex.DecodeString(macHex)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	base := expectPath + "?exp=" + strconv.FormatInt(expN, 10)
	want := s.mac([]byte(base))
	if !hmac.Equal(macBytes, want) {
		return time.Time{}, ErrBadMAC
	}
	exp := time.Unix(expN, 0)
	if s.now().After(exp) {
		return time.Time{}, ErrExpired
	}
	return exp, nil
}

// mac is the SHA-256 HMAC of msg with the Signer's
// key. The output is a 32-byte slice; the caller
// hex-encodes for the URL.
func (s *Signer) mac(msg []byte) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write(msg)
	return h.Sum(nil)
}

// splitQuery walks rawQuery looking for `exp=` and
// `mac=` in any order. The values are returned
// without further validation; the caller parses
// them.
//
// The function does NOT use url.ParseQuery because
// the rest of the riff HTTP layer needs the raw
// bytes for the MAC base. Parsing + re-serialising
// would change the encoding (e.g. "+" vs "%20"),
// which would break the signature.
func splitQuery(raw string) (exp, mac string, err error) {
	for raw != "" {
		var seg string
		seg, raw = nextSegment(raw)
		if seg == "" {
			continue
		}
		switch {
		case len(seg) > 4 && seg[:4] == "exp=":
			exp = seg[4:]
		case len(seg) > 4 && seg[:4] == "mac=":
			mac = seg[4:]
		}
	}
	if exp == "" || mac == "" {
		return "", "", ErrMalformed
	}
	return exp, mac, nil
}

// nextSegment returns the next `&`-separated segment
// of raw and the remainder. The remainder keeps its
// leading `&` so a loop invariant of "raw starts with
// a segment or is empty" holds.
func nextSegment(raw string) (seg, rest string) {
	for i := 0; i < len(raw); i++ {
		if raw[i] == '&' {
			return raw[:i], raw[i+1:]
		}
	}
	return raw, ""
}

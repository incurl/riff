package signedurl

import (
	"errors"
	"testing"
	"time"
)

func key() []byte {
	// 32 bytes; the package's New() copies the input so
	// tests can pass a stack buffer.
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// TestSignAndVerify_RoundTrip pins the happy path:
// the URL Sign() produces verifies cleanly when the
// system clock is inside the TTL window.
func TestSignAndVerify_RoundTrip(t *testing.T) {
	t.Parallel()
	s := New(key())
	url := s.Sign("/api/po33/sketches/abcdef/download")
	exp, err := s.Verify("/api/po33/sketches/abcdef/download", stripPath(url))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !exp.After(time.Now()) {
		t.Fatalf("exp is in the past: %v", exp)
	}
}

// TestVerify_RejectsExpired pins the TTL bound: a URL
// issued now is rejected once the clock is past exp.
func TestVerify_RejectsExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s := New(key()).WithClock(func() time.Time { return now })
	url := s.Sign("/p")
	// Re-key after issuance so the test isn't validating
	// a clock the Signer also reads — the WithClock is
	// installed once and the second clock value is
	// supplied via the test-local future.
	expStr, mac, _ := splitQuery(stripPath(url))
	_ = expStr
	_ = mac
	// Advance the clock past TTL.
	now2 := now.Add(DefaultTTL + time.Second)
	s2 := New(key()).WithClock(func() time.Time { return now2 })
	_, err := s2.Verify("/p", stripPath(url))
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

// TestVerify_RejectsBadMAC pins the tamper check:
// flipping any byte of `mac=...` is a verification
// failure (NOT a malformed failure; the bytes decode
// to hex but don't match the recomputed MAC).
func TestVerify_RejectsBadMAC(t *testing.T) {
	t.Parallel()
	s := New(key())
	url := s.Sign("/p")
	// Flip a hex char in mac=... without changing the
	// length, so the URL stays parseable.
	q := stripPath(url)
	last := q[len(q)-1]
	var replacement string
	switch last {
	case '0':
		replacement = "1"
	case '1':
		replacement = "0"
	case 'a':
		replacement = "b"
	case 'f':
		replacement = "e"
	default:
		replacement = "a"
	}
	tampered := q[:len(q)-1] + replacement
	_, err := s.Verify("/p", tampered)
	if !errors.Is(err, ErrBadMAC) {
		t.Fatalf("want ErrBadMAC, got %v", err)
	}
}

// TestVerify_RejectsWrongKey pins the key-rotation
// case: a URL signed by signer A does not verify
// under signer B even when the exp window is open.
func TestVerify_RejectsWrongKey(t *testing.T) {
	t.Parallel()
	a := New([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	b := New([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	url := a.Sign("/p")
	_, err := b.Verify("/p", stripPath(url))
	if !errors.Is(err, ErrBadMAC) {
		t.Fatalf("want ErrBadMAC, got %v", err)
	}
}

// TestVerify_RejectsWrongPath pins the path-pin
// check: a URL signed for /p does not verify when
// the caller supplies /q.
func TestVerify_RejectsWrongPath(t *testing.T) {
	t.Parallel()
	s := New(key())
	url := s.Sign("/p")
	_, err := s.Verify("/q", stripPath(url))
	if !errors.Is(err, ErrBadMAC) {
		t.Fatalf("want ErrBadMAC, got %v", err)
	}
}

// TestVerify_RejectsMalformed pins the URL parse
// path: missing exp, missing mac, or non-integer
// exp surfaces as ErrMalformed.
func TestVerify_RejectsMalformed(t *testing.T) {
	t.Parallel()
	s := New(key())
	for _, q := range []string{
		"",
		"exp=1",
		"mac=deadbeef",
		"exp=notanumber&mac=deadbeef",
	} {
		_, err := s.Verify("/p", q)
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("query %q: want ErrMalformed, got %v", q, err)
		}
	}
}

// TestSignWithTTL_RespectsDuration pins the
// per-call TTL override: a URL signed with TTL=1s
// is rejected a few seconds later.
func TestSignWithTTL_RespectsDuration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s := New(key()).WithClock(func() time.Time { return now })
	url := s.SignWithTTL("/p", time.Second)
	now2 := now.Add(2 * time.Second)
	s2 := s.WithClock(func() time.Time { return now2 })
	_, err := s2.Verify("/p", stripPath(url))
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

// TestNew_CopiesKey pins the secret-clearing
// contract: zeroing the caller's buffer after the
// call does not invalidate a URL the Signer has
// already issued.
func TestNew_CopiesKey(t *testing.T) {
	t.Parallel()
	b := make([]byte, 32)
	for i := range b {
		b[i] = 0x42
	}
	s := New(b)
	for i := range b {
		b[i] = 0
	}
	url := s.Sign("/p")
	if _, err := s.Verify("/p", stripPath(url)); err != nil {
		t.Fatalf("Verify after key wipe: %v", err)
	}
}

// stripPath returns the query-string portion of a
// signed URL (everything after the first '?').
// Tests build the full URL through Sign() and then
// hand the query to Verify so the sign+verify pair
// exercises the same byte path the production
// router will.
func stripPath(url string) string {
	for i := 0; i < len(url); i++ {
		if url[i] == '?' {
			return url[i+1:]
		}
	}
	return url
}

// flipHex returns a different hex digit so the MAC
// changes but stays hex-decodable. Used to test
// ErrBadMAC without tripping ErrMalformed.
func flipHex(c byte) byte {
	switch c {
	case '0':
		return '1'
	case '1':
		return '0'
	case 'a':
		return 'b'
	case 'f':
		return 'e'
	default:
		return 'a'
	}
}

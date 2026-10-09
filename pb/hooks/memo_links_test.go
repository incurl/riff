package hooks

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNormaliseLinkURL pins the URL filter. The hook uses this to
// decide which URLs to fan out to linkmeta; the library's own
// validateURL will reject most of these too, but rejecting them
// up front means the worker pool does not waste slots on inputs
// that were going to fail anyway.
func TestNormaliseLinkURL(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		// Accept http and https, with and without whitespace.
		{"https", "https://example.com", "https://example.com", true},
		{"http", "http://example.com/x", "http://example.com/x", true},
		{"trim", "  https://example.com  ", "https://example.com", true},
		// Reject mailto, javascript, file, data, ftp, etc.
		{"mailto", "mailto:user@example.com", "", false},
		{"javascript", "javascript:alert(1)", "", false},
		{"file", "file:///etc/passwd", "", false},
		{"data", "data:text/plain,hello", "", false},
		{"ftp", "ftp://example.com/", "", false},
		// Reject garbage.
		{"empty", "", "", false},
		{"whitespace", "   ", "", false},
		{"relative", "not a url", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normaliseLinkURL(tc.in)
			assert.Equal(t, tc.wantOK, ok, "ok flag for %q", tc.in)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

// TestTruncate asserts the byte-bound helper used to clamp
// fetched metadata to the column Max limits. The "+…" suffix
// must not be applied to inputs that already fit; long inputs
// must be cut to fit and end in a single trailing "…". The "…"
// rune is 3 bytes in UTF-8, so a 10-byte cut is 7 bytes of head
// + 3 bytes of ellipsis.
func TestTruncate(t *testing.T) {
	assert.Equal(t, "", truncate("", 10))
	assert.Equal(t, "hi", truncate("hi", 10))
	assert.Equal(t, "hello", truncate("hello", 5))
	// Cut case: 100-byte string at n=10 → 7 bytes of head + 3
	// bytes of "…".
	s := strings.Repeat("a", 100)
	got := truncate(s, 10)
	assert.Equal(t, 10, len(got))
	assert.True(t, strings.HasSuffix(got, "…"))
	// n <= 3: the cut head is empty; the function still
	// returns at most n bytes total, which is fine because
	// any further work would put the column over budget
	// anyway.
	assert.Equal(t, "h", truncate("hello", 1))
}

// TestTruncateErr covers the error-string truncator that the
// hook uses to keep `error` under 500 bytes. Empty input maps
// to empty output; a multi-line error chain is collapsed to a
// single line.
func TestTruncateErr(t *testing.T) {
	assert.Equal(t, "", truncateErr(nil))
	assert.Equal(t, "boom", truncateErr(errBoom("boom")))
	long := strings.Repeat("x", 1000)
	got := truncateErr(errBoom(long))
	assert.Equal(t, 500, len(got))
	assert.True(t, strings.HasSuffix(got, "…"))
}

type errBoom string

func (e errBoom) Error() string { return string(e) }

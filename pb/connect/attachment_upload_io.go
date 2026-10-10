// attachment_upload_io.go is the I/O plumbing for
// attachment_upload.go. The split exists so the
// import surface in the main file stays small: the
// random reader and the default-id generator do not
// share dependencies with the rest of the upload
// handler, and putting them in their own file lets
// goimports group the import blocks cleanly.
package connect

import (
	"crypto/rand"
	"io"

	"github.com/incurl/riff/internal/random"
)

// defaultRecordID returns a PocketBase-shaped default
// id. PocketBase's id format is 15 lowercase alnum
// characters; the internal helpers use
// `security.RandomStringByRegex("[a-z0-9]{15}")`. We
// avoid a PB dependency by reading crypto/rand bytes
// and indexing them into the lowercase alphabet. The
// probability of collision is negligible (15 chars
// from a 36-char alphabet = 36^15 ≈ 2×10^23), and
// the on-write hook stamps the actual storage key,
// so this is only an in-memory placeholder used to
// pick the file's filesystem path before the row
// exists.
func defaultRecordID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	const n = 15
	out := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, out); err != nil {
		// crypto/rand on Linux never returns an error
		// in practice, but fall back to a fresh random
		// alphanumeric string from internal/random so
		// the row is still created.
		s, _ := random.String(n)
		return s
	}
	for i, b := range out {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out)
}
package connect

import (
	"crypto/rand"
	"encoding/hex"
	"io"
)

// encodeHex returns the lowercase hex form of b.
func encodeHex(b []byte) string {
	return hex.EncodeToString(b)
}

// decodeHexInto writes the 2*len(out) hex chars
// in s into out. Returns the number of bytes
// written, or an error if s is not a valid
// 2*len(out)-char hex string.
func decodeHexInto(s string, out []byte) (int, error) {
	return hex.Decode(out, []byte(s))
}

// readFullRandom fills b with crypto/rand bytes.
// It is a thin wrapper so the call site reads
// as "mint 32 random bytes" rather than the
// io.ReadFull boilerplate.
func readFullRandom(b []byte) (int, error) {
	return io.ReadFull(rand.Reader, b)
}

// randomBytes32 returns 32 fresh random
// bytes from crypto/rand. The helper is
// used by the install bootstrap to mint
// a fresh per-install signing key. The
// output is uniformly random.
func randomBytes32() ([]byte, error) {
	out := make([]byte, 32)
	_, err := readFullRandom(out)
	return out, err
}

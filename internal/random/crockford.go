package random

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// Crockford returns a Crockford-base32 random string of
// length n. Crockford base32 is the human-friendly
// alphabet the PO-33 pairing code uses; it deliberately
// drops the letters I, L, O, and U to avoid visual
// ambiguity (I/L/1, O/0, V/U). The output is uppercase
// and ready to be typed off a screen without confusion.
//
// The function is intentionally a thin wrapper over
// crypto/rand so the underlying random source stays in
// one place. If the project ever migrates from
// crypto/rand to a hardware RNG, only this file
// changes.
func Crockford(n int) (string, error) {
	if n < 1 {
		return "", fmt.Errorf("crockford: length must be >= 1, got %d", n)
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	max := big.NewInt(int64(len(alphabet)))
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[idx.Int64()]
	}
	return string(out), nil
}

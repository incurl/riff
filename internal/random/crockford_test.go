package random

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCrockford_Length pins the output length: the
// caller asks for n characters and gets n back.
func TestCrockford_Length(t *testing.T) {
	for _, n := range []int{1, 5, 10, 20, 32} {
		out, err := Crockford(n)
		require.NoError(t, err)
		assert.Len(t, out, n)
	}
}

// TestCrockford_Alphabet pins the alphabet: only
// Crockford-base32 characters. The forbidden letters
// (I, L, O, U) and lowercase must never appear.
func TestCrockford_Alphabet(t *testing.T) {
	const allowed = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	out, err := Crockford(64)
	require.NoError(t, err)
	for _, r := range out {
		assert.True(t, strings.ContainsRune(allowed, r),
			"character %q not in Crockford alphabet", r)
	}
}

// TestCrockford_Uniqueness: two consecutive calls are
// highly unlikely to collide (the chance for n=5 is
// 32^5 = ~33M combinations; we just check a small
// batch).
func TestCrockford_Uniqueness(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		out, err := Crockford(5)
		require.NoError(t, err)
		_, dup := seen[out]
		assert.False(t, dup, "duplicate Crockford output %q on iteration %d", out, i)
		seen[out] = struct{}{}
	}
}

// TestCrockford_RejectsBadLength pins the guard: a
// non-positive length returns an error so callers can't
// accidentally request an empty token.
func TestCrockford_RejectsBadLength(t *testing.T) {
	_, err := Crockford(0)
	require.Error(t, err)
	_, err = Crockford(-1)
	require.Error(t, err)
}

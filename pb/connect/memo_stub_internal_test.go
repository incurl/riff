package connect

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/incurl/riff/pb/migrations"
)

// TestStubSentinelMatchesTheMigrationCopy pins the one duplicated value
// guarding the legacy-stub cleanup.
//
// `pb/migrations` cannot import `pb/connect` (the dependency runs the other
// way: `pb/connect` test helpers pull in `pb/migrations` to build a fully
// migrated app), so the sweep migration carries its own copy of the sentinel
// and cannot check it from inside its own test package either — `go test`
// applies the same cycle rules. This file is the only place both constants are
// importable.
//
// The drift this guards against is silent and bad in both directions: if the
// migration's copy drifted from this one, the sweep would stop matching the
// stubs it exists to delete while the list handlers in both packages kept
// hiding them — so the rows would be invisible *and* permanent, with no error
// anywhere to explain why.
func TestStubSentinelMatchesTheMigrationCopy(t *testing.T) {
	assert.Equal(t, StubMemoContent, migrations.LegacyStubMemoContent(),
		"pb/connect.StubMemoContent and pb/migrations' copy must not drift; "+
			"a mismatch would make the sweep silently stop matching the stubs it deletes")
}

// TestIsStubMemoContent pins the legacy-stub predicate itself.
//
// The sentinel is the zero-width space U+200B. A memo a user actually wrote
// never carries it — the composer requires a visible character — which is what
// made the content marker safe to filter on. Visibility is deliberately not
// part of the test: a stub that a later V0 edit flipped to PUBLIC is still a
// stub and must still be hidden.
func TestIsStubMemoContent(t *testing.T) {
	assert.True(t, IsStubMemoContent(StubMemoContent),
		"the sentinel itself is a stub")

	assert.False(t, IsStubMemoContent(""),
		"an empty memo is not a stub — it is a memo a user wrote")
	assert.False(t, IsStubMemoContent(" "),
		"a space is not the zero-width-space sentinel")
	assert.False(t, IsStubMemoContent("hello"),
		"ordinary content is not a stub")
	assert.False(t, IsStubMemoContent(StubMemoContent+"trailing"),
		"the sentinel must be the whole body, not a prefix")
}

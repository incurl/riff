package identifier

import (
	"regexp"
)

var (
	UIDMatcher = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,34}[a-zA-Z0-9])?$`)
)

// ToInt was removed when the V0 (Memos 0.21) client
// support was dropped; the V1 client uses resource-name
// identities (e.g. "users/alice", "memos/abc") and the
// int-id projection is no longer needed.

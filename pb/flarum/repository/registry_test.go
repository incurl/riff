package repository

import (
	"testing"
)

// TestExtensionID_MatchesUpstreamsNameToId pins the admin id.
//
// It is upstream's `Extension::nameToId()`, and the admin surface
// depends on the exact spelling: the id is a PATH SEGMENT in the admin
// app's extension URL, and the extension map is keyed by it.
//
// A plugin's raw id is `vendor/name` — it CONTAINS A SLASH — and the
// admin app's extension page lives at `/extension/:id`, a pattern with
// ONE segment. The raw id is therefore not a path segment, and the
// page is unreachable without the flattening.
func TestExtensionID_MatchesUpstreamsNameToId(t *testing.T) {
	cases := []struct {
		id       string
		expected string
	}{
		{"flarum/tags", "flarum-tags"},
		{"flarum/flarum-ext-suspend", "flarum-suspend"},
		{"some-vendor/foo", "some-vendor-foo"},
		{"riff/likes", "riff-likes"},
		{"flarum/mentions", "flarum-mentions"},
		{"fof/polls", "fof-polls"},
		{"flarum/core", "core"},
		{"", ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.id, func(t *testing.T) {
			got := ExtensionID(testCase.id)
			if got != testCase.expected {
				t.Fatalf("ExtensionID(%q) = %q, want %q", testCase.id, got, testCase.expected)
			}

			if got != "" && containsSlash(got) {
				t.Fatalf("ExtensionID(%q) = %q, which is not one path segment", testCase.id, got)
			}
		})
	}
}

// containsSlash reports whether a string holds a slash.
func containsSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}

// TestPluginSlug_IsNotTheAdminId pins that the two spellings are
// different things.
//
// The plugin slug names a COLLECTION, and a collection's name is not a
// path segment — so it keeps the `flarum-` prefix that the admin id
// strips.
func TestPluginSlug_IsNotTheAdminId(t *testing.T) {
	if got := PluginSlug("flarum/flarum-ext-suspend"); got != "flarum-flarum-ext-suspend" {
		t.Fatalf("PluginSlug = %q, want the collection spelling", got)
	}

	if got := ExtensionID("flarum/flarum-ext-suspend"); got != "flarum-suspend" {
		t.Fatalf("ExtensionID = %q, want the admin spelling", got)
	}
}

package api

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWrapMemosPathBody_Flat covers the flat request shape: the
// resource name is injected at the top level of the body and the
// caller's own fields survive.
func TestWrapMemosPathBody_Flat(t *testing.T) {
	t.Parallel()
	spec, ok := pathWrapSpecFor("GET", "/memos/api/memos/{name}")
	require.True(t, ok)

	out, err := wrapMemosPathBody(spec)([]byte(`{"visibility":"PUBLIC"}`), map[string]string{"name": "abc"}, url.Values{})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "memos/abc", got["name"])
	assert.Equal(t, "PUBLIC", got["visibility"])
}

// TestWrapMemosPathBody_Nested covers the nested request shape: the
// caller's body becomes the payload and the update mask is added.
func TestWrapMemosPathBody_Nested(t *testing.T) {
	t.Parallel()
	spec, ok := pathWrapSpecFor("PATCH", "/memos/api/memos/{name}")
	require.True(t, ok)

	out, err := wrapMemosPathBody(spec)([]byte(`{"content":"patched"}`), map[string]string{"name": "abc"}, url.Values{})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	// An absent mask is an EMPTY path list, which the Connect
	// handlers read as "every field in the body"
	// (pb/connect/translate.go::fieldMaskPaths). It must not be
	// the literal "*": protojson rejects that as a FieldMask path.
	mask, ok := got["update_mask"].(string)
	require.True(t, ok, "the update mask must be a protojson FieldMask string: %s", string(out))
	assert.Empty(t, mask, "an absent mask means every field in the body")

	memo, ok := got["memo"].(map[string]any)
	require.True(t, ok, "the payload must be nested under `memo`: %s", string(out))
	assert.Equal(t, "memos/abc", memo["name"])
	assert.Equal(t, "patched", memo["content"])
}

// TestWrapMemosPathBody_Append covers the two-segment Memos path:
// the second variable is appended to the resource name.
func TestWrapMemosPathBody_Append(t *testing.T) {
	t.Parallel()
	spec, ok := pathWrapSpecFor("DELETE", "/memos/api/spaces/{name}/members/{member_name}")
	require.True(t, ok)

	out, err := wrapMemosPathBody(spec)(nil, map[string]string{"name": "sp1", "member_name": "m2"}, url.Values{})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "spaces/sp1/m2", got["name"])
}

// TestPathWrapSpecFor_CoversEveryWrappedRoute pins that every route
// the table marks as connect_proxy_wrap has a spec — otherwise the
// factory falls back to unknownFlavorHandler and the route 500s.
func TestPathWrapSpecFor_CoversEveryWrappedRoute(t *testing.T) {
	t.Parallel()
	for _, entry := range upstreamMemosRoutes() {
		if entry.Flavor != "connect_proxy_wrap" {
			continue
		}
		_, ok := pathWrapSpecFor(entry.Method, entry.Path)
		assert.True(t, ok, "no path spec for %s %s", entry.Method, entry.Path)
	}
}

// TestPathWrapSpecFor_NoConnectProxyPathParamRoute pins the
// invariant that started this: a connect_proxy route whose path
// carries a parameter reaches the Connect handler with an empty
// resource name, because the plain proxy forwards the body
// verbatim. Every such route must use connect_proxy_wrap instead.
func TestPathWrapSpecFor_NoConnectProxyPathParamRoute(t *testing.T) {
	t.Parallel()
	for _, entry := range upstreamMemosRoutes() {
		if entry.Flavor != "connect_proxy" {
			continue
		}
		assert.NotContains(t, entry.Path, "{",
			"%s %s is a plain connect_proxy but its path carries a parameter, "+
				"so the Connect handler never sees the resource name",
			entry.Method, entry.Path)
	}
}

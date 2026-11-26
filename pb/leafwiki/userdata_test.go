package leafwiki

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The per-user metadata endpoints, and the trap they exist to close.
//
// **AN UNROUTED PATH IS NOT A 404 — IT IS THE SPA's `index.html` WITH A
// 200.** The SPA catch-all is the least specific route in the mux, so
// every path the wiki does not serve is answered with the shell. The
// SPA's request helper then does `await response.json()`, fails on the
// markup, swallows the failure and returns `null` — and the caller reads
// a field off it:
//
//	let t = await loadUserSettings();   // null
//	t.dateFormat ?? `locale`            // TypeError
//
// Both settings and favourites are loaded on EVERY mount, so the console
// showed two TypeErrors per page load before these routes existed.

// TestUserdata_DefaultsAreTheSpaFallbacks pins that a fresh account's
// settings are the values the SPA itself falls back to.
//
// The SPA reads every field with `??`, so an empty object would work —
// but a saved-and-reloaded round trip should not depend on the client's
// fallbacks, and the settings page renders these values as the CURRENT
// selection.
func TestUserdata_DefaultsAreTheSpaFallbacks(t *testing.T) {
	_, _, handler := newTestAPI(t)

	recorder := doJSON(t, handler, http.MethodGet, "/wiki/api/user-settings", nil, nil)
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())

	settings := decodeJSON[userSettings](t, recorder)
	assert.True(t, settings.AutoSave, "auto-save defaults on, as the SPA's store does")
	assert.Equal(t, "en", settings.Language)
	assert.Equal(t, "locale", settings.DateFormat)
	assert.Equal(t, "locale", settings.TimeFormat)
}

// TestUserdata_AnUnroutedPathWouldHaveAnsweredHtml pins the trap itself.
//
// It is the regression guard for the whole file: a path the wiki does not
// serve is answered by the SPA catch-all with a 200 and markup, which is
// exactly what made the SPA's JSON parse fail and return `null`.
func TestUserdata_AnUnroutedPathWouldHaveAnsweredHtml(t *testing.T) {
	_, _, handler := newTestAPI(t)

	recorder := doJSON(t, handler, http.MethodGet, "/wiki/api/definitely-not-a-route", nil, nil)
	require.Equal(t, http.StatusOK, recorder.Code,
		"the catch-all answers 200; that is why an unrouted API path is a silent null")
	assert.Contains(t, recorder.Body.String(), "<html",
		"and the body is the shell, not JSON")
}

// TestUserdata_SaveRoundTrips pins that a saved setting is read back.
func TestUserdata_SaveRoundTrips(t *testing.T) {
	server, app, handler := newTestAPI(t)
	user := testUser(t, app, "prefs")

	saved := doJSON(t, handler, http.MethodPut, "/wiki/api/user-settings", map[string]any{
		"autoSave":   false,
		"language":   "de",
		"dateFormat": "iso",
		"timeFormat": "24h",
	}, bearer(t, user))
	require.Equal(t, http.StatusOK, saved.Code, "body: %s", saved.Body.String())

	read := decodeJSON[userSettings](t, doJSON(t, handler, http.MethodGet, "/wiki/api/user-settings", nil, bearer(t, user)))
	assert.False(t, read.AutoSave)
	assert.Equal(t, "de", read.Language)
	assert.Equal(t, "iso", read.DateFormat)
	assert.Equal(t, "24h", read.TimeFormat)

	// And it survives a restart, because it is a file beside the tree.
	reopened := newUserdata(server.Tree().StorageDir())
	assert.Equal(t, "de", reopened.Settings(user.Id).Language)
}

// TestUserdata_AnOmittedFieldKeepsItsPreviousValue pins the merge.
//
// A partial save must not silently reset the reader's language to the
// default: the SPA sends the whole object, but an older build or a
// hand-written request may not.
func TestUserdata_AnOmittedFieldKeepsItsPreviousValue(t *testing.T) {
	server, app, handler := newTestAPI(t)
	user := testUser(t, app, "partial")

	first := doJSON(t, handler, http.MethodPut, "/wiki/api/user-settings", map[string]any{
		"autoSave": true,
		"language": "fr",
	}, bearer(t, user))
	require.Equal(t, http.StatusOK, first.Code, "body: %s", first.Body.String())

	second := doJSON(t, handler, http.MethodPut, "/wiki/api/user-settings", map[string]any{
		"autoSave": false,
	}, bearer(t, user))
	require.Equal(t, http.StatusOK, second.Code, "body: %s", second.Body.String())

	read := decodeJSON[userSettings](t, doJSON(t, handler, http.MethodGet, "/wiki/api/user-settings", nil, bearer(t, user)))
	assert.False(t, read.AutoSave, "the field that was sent is updated")
	assert.Equal(t, "fr", read.Language, "the field that was omitted keeps its value")

	// And the file really holds it.
	reopened := newUserdata(server.Tree().StorageDir())
	assert.Equal(t, "fr", reopened.Settings(user.Id).Language)
}

// TestUserdata_SavingNeedsAnAccount pins that an anonymous caller cannot
// write preferences.
func TestUserdata_SavingNeedsAnAccount(t *testing.T) {
	_, _, handler := newTestAPI(t)

	recorder := doJSON(t, handler, http.MethodPut, "/wiki/api/user-settings", map[string]any{
		"language": "de",
	}, nil)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

// TestUserdata_FavoritesRoundTrip pins the favourites surface.
func TestUserdata_FavoritesRoundTrip(t *testing.T) {
	_, app, handler := newTestAPI(t)
	user := testUser(t, app, "favouriter")
	auth := bearer(t, user)

	created := doJSON(t, handler, http.MethodPost, "/wiki/api/pages", map[string]any{
		"title": "Good Page",
		"slug":  "good-page",
		"kind":  "page",
	}, auth)
	require.Equal(t, http.StatusCreated, created.Code, "body: %s", created.Body.String())
	page := decodeJSON[Page](t, created)

	// Empty to start with: the SPA reads `.pages` off an object.
	empty := decodeJSON[favoritesResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/favorites", nil, bearer(t, user)))
	assert.Empty(t, empty.Pages)

	added := doJSON(t, handler, http.MethodPut, "/wiki/api/pages/"+page.ID+"/favorite", nil, auth)
	require.Equal(t, http.StatusNoContent, added.Code, "body: %s", added.Body.String())

	listed := decodeJSON[favoritesResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/favorites", nil, bearer(t, user)))
	require.Len(t, listed.Pages, 1)
	assert.Equal(t, page.ID, listed.Pages[0].ID)
	assert.Equal(t, "good-page", listed.Pages[0].Path)
	assert.Equal(t, "page", listed.Pages[0].Kind)

	// The toggle is idempotent: the SPA retries on a slow response.
	again := doJSON(t, handler, http.MethodPut, "/wiki/api/pages/"+page.ID+"/favorite", nil, auth)
	require.Equal(t, http.StatusNoContent, again.Code)
	listed = decodeJSON[favoritesResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/favorites", nil, bearer(t, user)))
	assert.Len(t, listed.Pages, 1, "favouriting twice is one entry")

	removed := doJSON(t, handler, http.MethodDelete, "/wiki/api/pages/"+page.ID+"/favorite", nil, auth)
	require.Equal(t, http.StatusNoContent, removed.Code, "body: %s", removed.Body.String())

	listed = decodeJSON[favoritesResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/favorites", nil, bearer(t, user)))
	assert.Empty(t, listed.Pages)
}

// TestUserdata_FavoritesArePerAccount pins that one reader's favourites do
// not leak into another's.
func TestUserdata_FavoritesArePerAccount(t *testing.T) {
	_, app, handler := newTestAPI(t)
	alice := testUser(t, app, "fav_alice")
	bob := testUser(t, app, "fav_bob")

	created := doJSON(t, handler, http.MethodPost, "/wiki/api/pages", map[string]any{
		"title": "Shared",
		"slug":  "shared",
		"kind":  "page",
	}, bearer(t, alice))
	require.Equal(t, http.StatusCreated, created.Code)
	page := decodeJSON[Page](t, created)

	added := doJSON(t, handler, http.MethodPut, "/wiki/api/pages/"+page.ID+"/favorite", nil, bearer(t, alice))
	require.Equal(t, http.StatusNoContent, added.Code)

	alices := decodeJSON[favoritesResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/favorites", nil, bearer(t, alice)))
	assert.Len(t, alices.Pages, 1)

	bobs := decodeJSON[favoritesResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/favorites", nil, bearer(t, bob)))
	assert.Empty(t, bobs.Pages, "bob's favourites are his own")
}

// TestUserdata_AnonymousFavoritesAreEmpty pins that a signed-out visitor
// gets an empty list rather than a 401 — the SPA loads the list on every
// mount, and a 401 would make it log the reader out before they did
// anything.
func TestUserdata_AnonymousFavoritesAreEmpty(t *testing.T) {
	_, _, handler := newTestAPI(t)

	recorder := doJSON(t, handler, http.MethodGet, "/wiki/api/favorites", nil, nil)
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())

	listed := decodeJSON[favoritesResponse](t, recorder)
	assert.Empty(t, listed.Pages)
}

// TestUserdata_AStaleFavoriteIsAbsent pins that a favourite whose page was
// deleted does not resurrect a phantom.
func TestUserdata_AStaleFavoriteIsAbsent(t *testing.T) {
	server, app, handler := newTestAPI(t)
	user := testUser(t, app, "stale")

	require.NoError(t, server.userdata.AddFavorite(user.Id, "a-page-that-never-existed"))

	listed := decodeJSON[favoritesResponse](t, doJSON(t, handler, http.MethodGet, "/wiki/api/favorites", nil, bearer(t, user)))
	assert.Empty(t, listed.Pages)
}

// TestUserdata_TheFileIsWrittenBesideTheTree pins the storage location.
//
// Pages are the source of truth and live under `root/`; this file is
// per-account metadata and sits at the top of the storage directory, so an
// upstream LeafWiki directory pointed at it keeps working.
func TestUserdata_TheFileIsWrittenBesideTheTree(t *testing.T) {
	server, app, handler := newTestAPI(t)
	user := testUser(t, app, "onsdisk")

	saved := doJSON(t, handler, http.MethodPut, "/wiki/api/user-settings", map[string]any{
		"language": "ja",
	}, bearer(t, user))
	require.Equal(t, http.StatusOK, saved.Code, "body: %s", saved.Body.String())

	path := filepath.Join(server.Tree().StorageDir(), userdataFilename)
	payload, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(payload), "ja")
}

// TestBranding_TheReadAnswersTheShellsSiteName pins that the branding
// store can load.
//
// Upstream's SPA reads `brandingConstraints` NESTED, so a flat response
// leaves every limit `undefined`. The site name matches what the shell
// substitutes for `{{__SITE_NAME__}}`, so the title and the name the SPA
// renders cannot drift.
func TestBranding_TheReadAnswersTheShellsSiteName(t *testing.T) {
	_, _, handler := newTestAPI(t)

	recorder := doJSON(t, handler, http.MethodGet, "/wiki/api/branding", nil, nil)
	require.Equal(t, http.StatusOK, recorder.Code, "body: %s", recorder.Body.String())

	branding := decodeJSON[brandingResponse](t, recorder)
	assert.Equal(t, "Wiki", branding.SiteName)
	assert.Empty(t, branding.LogoFile)
	assert.NotNil(t, branding.BrandingConstraints.LogoExts,
		"the constraint block must be an object, not null: the SPA reads brandingConstraints.logoExts")
}

// TestBranding_AWriteIsRefused pins that a branding save does not pretend
// to succeed.
//
// The port has no branding store, and a 200 to an upload it did not keep
// would tell the admin application the change landed.
func TestBranding_AWriteIsRefused(t *testing.T) {
	_, app, handler := newTestAPI(t)
	user := testUser(t, app, "brander")

	recorder := doJSON(t, handler, http.MethodPut, "/wiki/api/branding", map[string]any{
		"siteName": "Other",
	}, bearer(t, user))

	assert.Equal(t, http.StatusNotImplemented, recorder.Code)
}

package flarum

import (
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/incurl/riff/pb/flarum/api"
	"github.com/incurl/riff/pb/flarum/plugins/gamification"
	"github.com/incurl/riff/pb/flarum/plugins/likes"
	"github.com/incurl/riff/pb/flarum/plugins/tags"
	"github.com/incurl/riff/pb/flarum/plugins/uploads"
)

// TestForum_TheAdminPayloadCarriesThePluginSettings pins that every
// setting a plugin READS reaches the admin application with its
// DEFAULT value.
//
// The admin application reads `app.data.settings` to render the
// settings form. A setting that is absent from the payload renders as
// an EMPTY field — so an operator looking at a boolean sees it off,
// whatever the forum is actually doing. The forum's own reader falls
// back to the plugin's declared default, so the form and the forum
// would disagree about the very same setting.
//
// The keys are upstream's own, so an operator's saved setting carries
// over and means what it means upstream.
func TestForum_TheAdminPayloadCarriesThePluginSettings(t *testing.T) {
	app, handler, _ := newTestForumModule(t)

	admin := testSuperuser(t, app)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)

	for _, tc := range []struct {
		key   string
		value any
	}{
		{likes.SettingLikeOwnPost, "1"},
		{tags.SettingMinPrimaryTags, strconv.Itoa(tags.DefaultMinPrimaryTags)},
		{tags.SettingMaxPrimaryTags, strconv.Itoa(tags.DefaultMaxPrimaryTags)},
		{tags.SettingMinSecondaryTags, strconv.Itoa(tags.DefaultMinSecondaryTags)},
		{tags.SettingMaxSecondaryTags, strconv.Itoa(tags.DefaultMaxSecondaryTags)},
		{gamification.SettingAllowSelfVotes, "1"},
		{gamification.SettingDefaultMetric, gamification.MetricPosts},
		{gamification.SettingDefaultPeriod, gamification.PeriodYear},
	} {
		require.Contains(t, payload.Settings, tc.key,
			"the admin payload must carry %q, or the settings form shows it empty", tc.key)
		require.Equal(t, tc.value, payload.Settings[tc.key],
			"the admin payload's %q must carry the value the forum falls back to", tc.key)
	}
}

// TestForum_AStoredPluginSettingWinsOverTheDefault pins that a
// saved setting beats the declared default.
//
// The default is a fallback for a setting that is UNSET. Once an
// operator has saved one, the saved value is the answer — and the
// form must show what the forum will do, not what it did before the
// operator touched it.
func TestForum_AStoredPluginSettingWinsOverTheDefault(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	setForumSetting(t, app, likes.SettingLikeOwnPost, "0")

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)

	require.Contains(t, payload.Settings, likes.SettingLikeOwnPost)
	require.Equal(t, "0", payload.Settings[likes.SettingLikeOwnPost],
		"a stored setting must win over the declared default")
}

// TestForum_ThePluginSettingsAreTheOnesTheServerReads pins that the
// settings the admin portal offers are the settings the server READS.
//
// A setting nobody reads is a control that changes nothing, and one
// the admin portal shows as if it did. The test therefore compares the
// declared schema against the settings the server actually consults,
// and it fails when a plugin declares a setting it does not read —
// which is how the dead constants (the polls' `maxOptions`, the
// gamification's `firstPostOnly`) were found.
//
// The read side is the SOURCE of truth, so the test lists the keys
// each plugin's reader consults. A new reader must add its key here,
// and a removed reader must remove its key — otherwise the schema and
// the reader drift apart and one of them is lying.
func TestForum_ThePluginSettingsAreTheOnesTheServerReads(t *testing.T) {
	_, _, module := newTestForumModule(t)

	read := map[string]bool{
		likes.SettingLikeOwnPost:           true,
		tags.SettingMinPrimaryTags:         true,
		tags.SettingMaxPrimaryTags:         true,
		tags.SettingMinSecondaryTags:       true,
		tags.SettingMaxSecondaryTags:       true,
		gamification.SettingAllowSelfVotes: true,
		gamification.SettingDefaultMetric:  true,
		gamification.SettingDefaultPeriod:  true,
		// The uploads plugin's limit is read by `maxFileSizeBytes`
		// (`plugins/uploads/handlers.go`), which caps the effective size
		// at the collection's own field maximum.
		uploads.SettingMaxFileSize: true,
		// The sticky plugin's switch is read by `stickyPinned`
		// (`api/handlers_discussions.go`), which decides whether a listing
		// puts pinned discussions first.
		api.SettingPinStickyToTop: true,
	}

	for _, schema := range module.PluginSettingSchemas() {
		for _, field := range schema.Fields {
			require.True(t, read[field.Key],
				"the plugin declares the setting %q, but nothing reads it: a control that changes nothing",
				field.Key)
		}
	}
}

// TestForum_TheAdminSettingsAreStrings pins that the admin payload
// carries every setting as a STRING.
//
// Upstream's settings column is TEXT, so `Settings::all()` hands the
// admin application a map of strings, and the application types every
// setting as `Stream<string>` (`AdminPage.setting` does
// `Stream<string>(app.data.settings[key] ?? fallback)`).
//
// This port's settings column is a JSON field, so a stored value
// arrives as the JSON type it was written as — a number as `float64`,
// a boolean as `bool`, and in practice as raw JSON bytes
// (`types.JSONRaw`). A number in a stream typed as a string is saved
// back as a number, and a boolean switch reads `value !== '0'` against
// a value that is neither `"0"` nor `0`.
//
// The two keys the admin application PARSES as JSON stay documents:
// `extensions_enabled` is an array, and `JSON.parse` of a quoted
// document throws.
func TestForum_TheAdminSettingsAreStrings(t *testing.T) {
	app, handler := newTestForum(t)

	admin := testSuperuser(t, app)

	// A setting stored as a JSON string, a number and a boolean.
	setForumSetting(t, app, likes.SettingLikeOwnPost, "0")
	setForumSetting(t, app, tags.SettingMaxPrimaryTags, "3")
	setForumSetting(t, app, gamification.SettingDefaultMetric, gamification.MetricVotes)

	recorder := do(t, handler, http.MethodGet, "/forum/admin", "", sessionCookies(admin[0].Value, admin[1].Value))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	payload := bootPayloadOf(t, recorder)

	for _, key := range []string{
		likes.SettingLikeOwnPost,
		tags.SettingMaxPrimaryTags,
		gamification.SettingDefaultMetric,
	} {
		require.IsType(t, "", payload.Settings[key],
			"the setting %q must be a string, because the admin application types every setting as one", key)
	}

	require.Equal(t, "0", payload.Settings[likes.SettingLikeOwnPost])
	require.Equal(t, "3", payload.Settings[tags.SettingMaxPrimaryTags])
	require.Equal(t, gamification.MetricVotes, payload.Settings[gamification.SettingDefaultMetric])

	// The JSON documents stay documents: `isExtensionEnabled` parses
	// this one, and `JSON.parse` of a quoted document throws.
	enabled, ok := payload.Settings["extensions_enabled"].(string)
	require.True(t, ok, "extensions_enabled must be a string")
	require.Contains(t, enabled, "[", "extensions_enabled must stay a JSON document: %s", enabled)
}

// TestForum_TheAdminBundlesRegisterTheUpstreamSettings pins that each
// plugin's ADMIN bundle registers the settings it declares.
//
// The two halves of the contract are separate: the Go side declares
// the settings the server reads, and the frontend bundle registers the
// form that edits them. The bundle is the only place the form can
// live — the admin application renders a setting that a bundle
// registered, and NOTHING else.
//
// The check reads the bundle's SOURCE, because the contract is the
// key the bundle registers, and a key that survives minification is
// still the key.
func TestForum_TheAdminBundlesRegisterTheUpstreamSettings(t *testing.T) {
	for _, tc := range []struct {
		plugin   string
		settings []string
		// page names the file the settings live in when the plugin
		// registers a CUSTOM page. The admin application renders a
		// custom page INSTEAD of the plain settings list, so the
		// form is in the page and not in the bundle.
		page string
	}{
		{plugin: "likes", settings: []string{likes.SettingLikeOwnPost}},
		{
			plugin: "tags",
			settings: []string{
				tags.SettingMinPrimaryTags,
				tags.SettingMaxPrimaryTags,
				tags.SettingMinSecondaryTags,
				tags.SettingMaxSecondaryTags,
			},
			page: "admin/TagsPage.js",
		},
		{plugin: "gamification", settings: []string{
			gamification.SettingAllowSelfVotes,
			gamification.SettingDefaultMetric,
			gamification.SettingDefaultPeriod,
		}},
	} {
		settingsFile := "js/admin.js"
		if tc.page != "" {
			settingsFile = "js/" + tc.page
		}

		source := readVendoredFrontendFile(t,
			filepath.Join("pb/flarum/frontend/packages/plugins", tc.plugin, settingsFile))

		for _, key := range tc.settings {
			require.Contains(t, source, key,
				"the %s admin form must register the setting %q", tc.plugin, key)
		}

		// The bundle must use the admin application's own vocabulary,
		// or the form renders the wrong control.
		bundle := readVendoredFrontendFile(t,
			filepath.Join("pb/flarum/frontend/packages/plugins", tc.plugin, "js/admin.js"))

		require.Contains(t, bundle, "Extend.Admin()",
			"the %s admin bundle must register through Extend.Admin()", tc.plugin)
	}
}

// TestForum_TheAdminBundlesCarryNoDeadSettings pins that a plugin's
// admin bundle registers no setting the server does not read.
//
// This is the other half of the previous test: the bundle is the form,
// and a form field for a setting nobody reads is a control that
// changes nothing — the operator turns it and nothing happens, which
// is worse than the control being absent.
func TestForum_TheAdminBundlesCarryNoDeadSettings(t *testing.T) {
	read := map[string]bool{
		likes.SettingLikeOwnPost:           true,
		tags.SettingMinPrimaryTags:         true,
		tags.SettingMaxPrimaryTags:         true,
		tags.SettingMinSecondaryTags:       true,
		tags.SettingMaxSecondaryTags:       true,
		gamification.SettingAllowSelfVotes: true,
		gamification.SettingDefaultMetric:  true,
		gamification.SettingDefaultPeriod:  true,
		// The tags extension's `allow_tag_change` is upstream's FLAT key, not a
		// `flarum-tags.`-prefixed one, because the server reads it by that name
		// (`api.AllowTagChangeSetting`) and the admin payload publishes it there.
		// Naming the CONSTANT rather than spelling the string means this entry
		// tracks the reader: change the reader's key and this map changes with it.
		api.AllowTagChangeSetting: true,
	}

	// The setting key as it appears in a bundle: a quoted string.
	//
	// Two shapes: the registry's `setting: 'key'` (the plain settings
	// list) and a custom page's `this.setting('key', ...)` (the page's
	// own settings streams).
	keyPattern := regexp.MustCompile(`(?:setting:\s*|\.setting\()'([^']+)'`)

	// The forms each plugin registers: the admin bundle, and the
	// custom page a plugin may register INSTEAD of the plain settings
	// list.
	forms := map[string][]string{
		"likes":        {"js/admin.js"},
		"gamification": {"js/admin.js"},
		// The tags plugin registers the tag TREE page, and the count
		// settings live on its second tab.
		"tags": {"js/admin.js", "js/admin/TagsPage.js"},
	}

	for plugin, files := range forms {
		for _, file := range files {
			source := readVendoredFrontendFile(t,
				filepath.Join("pb/flarum/frontend/packages/plugins", plugin, file))

			for _, match := range keyPattern.FindAllStringSubmatch(source, -1) {
				require.True(t, read[match[1]],
					"the %s admin form registers the setting %q, but nothing reads it", plugin, match[1])
			}
		}
	}
}

// TestForum_TheSettingDefaultsMatchTheReaders pins that a plugin's
// declared default is the value its own reader falls back to.
//
// A default that disagrees with the reader is a setting the admin
// portal shows one value for and the forum uses another for — and the
// operator has no way to tell which is which.
func TestForum_TheSettingDefaultsMatchTheReaders(t *testing.T) {
	_, _, module := newTestForumModule(t)

	for _, schema := range module.PluginSettingSchemas() {
		for _, field := range schema.Fields {
			require.NotNil(t, field.Default,
				"the setting %q has no default, so the admin form shows an empty field", field.Key)
			require.NotEmpty(t, field.Label,
				"the setting %q has no label", field.Key)
			require.Contains(t, []string{"bool", "number", "text", "select"}, field.Type,
				"the setting %q has the type %q, which the admin application does not render", field.Key, field.Type)
		}
	}
}

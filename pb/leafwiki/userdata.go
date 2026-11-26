package leafwiki

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/pkg/errors"
)

// The per-user metadata the wiki's own SPA stores: a reader's settings
// and their favourites.
//
// **THEY BELONG TO AN ACCOUNT, NOT TO A PAGE.** LeafWiki keeps them in
// `users.db`; this port keeps the Markdown tree as the source of truth
// for PAGES and has no database of its own, so they live in one small
// JSON file beside the tree.
//
// The file is ADDITIVE to upstream's layout: an upstream LeafWiki
// binary pointed at the same directory ignores it, and this port
// recreates it empty when it is absent. A data directory therefore
// round-trips, minus these two pieces of per-user state — the same kind
// of deviation as the in-memory index.
const (
	// userdataFilename is the per-user metadata file.
	userdataFilename = "userdata.json"

	// The defaults the SPA falls back to when a field is absent
	// (`ui/leafwiki-ui`'s `account/preferences` store: `Ig = 'en'`,
	// `Lg = 'locale'`, `Rg = 'locale'`, and `autoSave` defaults true).
	defaultSettingsLanguage   = "en"
	defaultSettingsDateFormat = "locale"
	defaultSettingsTimeFormat = "locale"
)

// userSettings is one account's wiki preferences.
//
// The field names are the SPA's own (`autoSave`, `language`,
// `dateFormat`, `timeFormat`); a name that disagrees is a setting the UI
// writes and never reads back.
type userSettings struct {
	// AutoSave saves an edit without an explicit Save press.
	AutoSave bool `json:"autoSave"`
	// Language is the UI language code.
	Language string `json:"language"`
	// DateFormat is the SPA's date-format token (`locale` for the
	// browser's own).
	DateFormat string `json:"dateFormat"`
	// TimeFormat is the SPA's time-format token.
	TimeFormat string `json:"timeFormat"`
}

// defaultUserSettings returns the settings an account that never saved
// any gets.
//
// The values are the SPA's own fallbacks, so an account that never
// opened the settings page behaves exactly as if it had and accepted
// the defaults.
func defaultUserSettings() userSettings {
	return userSettings{
		AutoSave:   true,
		Language:   defaultSettingsLanguage,
		DateFormat: defaultSettingsDateFormat,
		TimeFormat: defaultSettingsTimeFormat,
	}
}

// normalize fills in a field the client left out.
//
// A PUT carries the whole settings object, but an older SPA build or a
// hand-written request may omit a field; keeping the previous value for
// an omitted one is what stops a partial save from silently resetting
// the reader's language to English.
func (s userSettings) normalize(fallback userSettings) userSettings {
	if s.Language == "" {
		s.Language = fallback.Language
	}
	if s.DateFormat == "" {
		s.DateFormat = fallback.DateFormat
	}
	if s.TimeFormat == "" {
		s.TimeFormat = fallback.TimeFormat
	}
	return s
}

// userdataDoc is the file's on-disk shape.
type userdataDoc struct {
	// Settings is every account's preferences, keyed by user id.
	Settings map[string]userSettings `json:"settings"`
	// Favorites is every account's favourite page ids, keyed by user id.
	Favorites map[string][]string `json:"favorites"`
}

// userdata is the per-user metadata store.
//
// It is read once on startup and rewritten on every change. The file is
// small (a few fields per account) and only ever written by a signed-in
// reader changing their own preferences, so a rewrite is cheaper than a
// dependency on a database that the rest of the wiki does not need.
//
// The mutex guards BOTH the maps and the file: two readers saving at the
// same moment would otherwise interleave their JSON encodes and leave a
// truncated file behind — the wiki would then load with no favourites at
// all, which reads as "the feature is broken", not as a write fault.
type userdata struct {
	mu   sync.Mutex
	path string
	doc  userdataDoc
}

// newUserdata loads the per-user metadata beside the tree.
//
// A MISSING or UNREADABLE file is not an error that stops the wiki: it
// starts empty, and the first save writes a fresh one. A wiki whose
// settings file was lost should still serve its pages.
func newUserdata(storageDir string) *userdata {
	store := &userdata{
		path: filepath.Join(storageDir, userdataFilename),
		doc: userdataDoc{
			Settings:  map[string]userSettings{},
			Favorites: map[string][]string{},
		},
	}

	payload, err := os.ReadFile(store.path)
	if err != nil {
		return store
	}

	decoded := userdataDoc{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return store
	}

	if decoded.Settings != nil {
		store.doc.Settings = decoded.Settings
	}
	if decoded.Favorites != nil {
		store.doc.Favorites = decoded.Favorites
	}

	return store
}

// Settings returns an account's preferences.
//
// An account that never saved any gets the defaults rather than an empty
// object: the SPA reads every field with a `??` fallback, but a saved-
// and-reloaded round trip should not depend on the client's fallbacks.
func (u *userdata) Settings(userID string) userSettings {
	u.mu.Lock()
	defer u.mu.Unlock()

	settings, ok := u.doc.Settings[userID]
	if !ok {
		return defaultUserSettings()
	}

	return settings.normalize(defaultUserSettings())
}

// SaveSettings stores an account's preferences and writes the file.
func (u *userdata) SaveSettings(userID string, settings userSettings) (userSettings, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	current := defaultUserSettings()
	if existing, ok := u.doc.Settings[userID]; ok {
		current = existing.normalize(current)
	}

	merged := settings.normalize(current)
	u.doc.Settings[userID] = merged

	if err := u.writeLocked(); err != nil {
		return merged, err
	}

	return merged, nil
}

// Favorites returns an account's favourite page ids.
func (u *userdata) Favorites(userID string) []string {
	u.mu.Lock()
	defer u.mu.Unlock()

	ids := u.doc.Favorites[userID]
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// AddFavorite marks a page as one of an account's favourites.
//
// It is IDEMPOTENT: favouriting twice succeeds without a second entry,
// because the SPA's toggle is optimistic and may retry.
func (u *userdata) AddFavorite(userID, pageID string) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	for _, id := range u.doc.Favorites[userID] {
		if id == pageID {
			return nil
		}
	}

	u.doc.Favorites[userID] = append(u.doc.Favorites[userID], pageID)

	return u.writeLocked()
}

// RemoveFavorite drops a page from an account's favourites.
//
// Removing a page that is not a favourite succeeds: the SPA's toggle is
// optimistic, so "already gone" is the state the caller wanted.
func (u *userdata) RemoveFavorite(userID, pageID string) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	ids := u.doc.Favorites[userID]
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != pageID {
			kept = append(kept, id)
		}
	}

	if len(kept) == len(ids) {
		return nil
	}

	if len(kept) == 0 {
		delete(u.doc.Favorites, userID)
	} else {
		u.doc.Favorites[userID] = kept
	}

	return u.writeLocked()
}

// writeLocked writes the file.
//
// The caller holds the mutex. The write goes to a temporary file in the
// same directory and is renamed into place, so a crash halfway through
// leaves the PREVIOUS file intact rather than a truncated one — the
// difference between losing one save and losing every account's
// favourites.
func (u *userdata) writeLocked() error {
	payload, err := json.MarshalIndent(u.doc, "", "  ")
	if err != nil {
		return errors.Wrap(err, "leafwiki: could not encode the user data")
	}

	temp := u.path + ".tmp"
	if err := os.WriteFile(temp, payload, 0o640); err != nil {
		return errors.Wrap(err, "leafwiki: could not write the user data")
	}
	if err := os.Rename(temp, u.path); err != nil {
		return errors.Wrap(err, "leafwiki: could not replace the user data")
	}

	return nil
}

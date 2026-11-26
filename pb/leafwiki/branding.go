package leafwiki

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/pkg/errors"
)

// The wiki's branding: the site name, the logo and the favicon.
//
// Upstream LeafWiki keeps them in `<storageDir>/branding.json` and the two
// image files in `<storageDir>/branding/`, and its SPA reads them on boot from
// `GET /api/branding` — the site name for the header and every `{{siteName}}`
// translation parameter, the favicon through a `<link rel="icon">` it rewrites
// itself.
//
// **THIS PORT USED TO REFUSE THE WRITES**, and the operator's experience was
// the one thing a refusal must never be: a SAVE THAT LOOKS LIKE IT WORKED. The
// page reads the updated configuration out of the response
// (`updateBranding` does `updated.faviconFile`), and an answer without a JSON
// body makes `fetchWithAuth` resolve to `null` — so the operator got "Cannot
// read properties of null (reading 'faviconFile')" and the name never changed.
// The store below is the real thing, at upstream's paths and with upstream's
// defaults, so a LeafWiki data directory still round-trips.

const (
	// brandingFilename is the configuration file's name, beside the pages.
	brandingFilename = "branding.json"

	// brandingAssetsDirname is the directory the uploaded images live in.
	brandingAssetsDirname = "branding"

	// maxSiteNameLength is upstream's `MaxSiteNameLength` default.
	maxSiteNameLength = 100

	// maxBrandingImageBytes is upstream's cap for both images (1 MiB).
	maxBrandingImageBytes = 1 * 1024 * 1024
)

// defaultBrandingSiteName is the name the wiki advertises until an
// administrator sets one.
//
// It is upstream's own default (`DefaultBrandingConfig().SiteName` is
// "LeafWiki"), and it is one value for the shell, for `GET /api/branding` and
// for the SPA's header, so the three cannot disagree.
const defaultBrandingSiteName = "LeafWiki"

// brandingConfig is the persisted configuration.
//
// The JSON names are upstream's (`siteName`, `logoFile`, `faviconFile`), so a
// `branding.json` written by LeafWiki itself is read unchanged.
type brandingConfig struct {
	// SiteName is the wiki's name.
	SiteName string `json:"siteName"`
	// LogoFile is the uploaded logo's file name, empty when none.
	LogoFile string `json:"logoFile"`
	// FaviconFile is the uploaded favicon's file name, empty when none.
	FaviconFile string `json:"faviconFile"`
}

// defaultBrandingConfig returns the configuration before anything is saved.
func defaultBrandingConfig() brandingConfig {
	return brandingConfig{SiteName: defaultBrandingSiteName}
}

// allowedLogoExts and allowedFaviconExts are upstream's extension lists.
var (
	allowedLogoExts    = []string{".jpeg", ".jpg", ".png", ".webp"}
	allowedFaviconExts = []string{".gif", ".ico", ".png", ".webp"}
)

// brandingConstraints is what the SPA validates an upload against.
//
// It is upstream's, value for value: an image is accepted only when its
// extension is on the list and its size under the cap, and the client checks
// both BEFORE it uploads. The values are not persisted — they are the server's
// rules, and a `branding.json` carrying a different cap would be a file
// overriding the code that enforces it.
type brandingConstraints struct {
	// LogoExts are the accepted logo extensions, sorted.
	LogoExts []string `json:"logoExts"`
	// FaviconExts are the accepted favicon extensions, sorted.
	FaviconExts []string `json:"faviconExts"`
	// MaxLogoSize is the logo upload cap, in bytes.
	MaxLogoSize int64 `json:"maxLogoSize"`
	// MaxFaviconSize is the favicon upload cap, in bytes.
	MaxFaviconSize int64 `json:"maxFaviconSize"`
	// MaxSiteNameLength is the site name's character cap.
	MaxSiteNameLength int `json:"maxSiteNameLength"`
}

// brandingLimits returns the constraints the SPA is told about.
func brandingLimits() brandingConstraints {
	return brandingConstraints{
		LogoExts:          append([]string{}, allowedLogoExts...),
		FaviconExts:       append([]string{}, allowedFaviconExts...),
		MaxLogoSize:       maxBrandingImageBytes,
		MaxFaviconSize:    maxBrandingImageBytes,
		MaxSiteNameLength: maxSiteNameLength,
	}
}

// brandingStore is the branding configuration and the two images.
//
// It is read once when the server starts and rewritten on every change, the
// same way the per-user metadata is (see userdata.go): the file is tiny, and
// the rewrite is atomic, so a crash halfway through leaves the PREVIOUS
// configuration rather than a truncated one.
type brandingStore struct {
	// dir is the wiki's storage directory.
	dir string
	// mu guards doc.
	mu sync.RWMutex
	// doc is the current configuration.
	doc brandingConfig
}

// newBrandingStore loads the configuration beside the pages.
//
// A missing file is the normal first run. A CORRUPT file is not fatal either:
// the defaults are used, because refusing to start the wiki over a bad
// branding file would take the pages down with it.
func newBrandingStore(storageDir string) *brandingStore {
	store := &brandingStore{dir: storageDir, doc: defaultBrandingConfig()}

	raw, err := os.ReadFile(store.path())
	if err != nil {
		return store
	}

	var decoded brandingConfig
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return store
	}

	if name := strings.TrimSpace(decoded.SiteName); name != "" {
		store.doc.SiteName = name
	}
	store.doc.LogoFile = safeBrandingFilename(decoded.LogoFile)
	store.doc.FaviconFile = safeBrandingFilename(decoded.FaviconFile)

	return store
}

// path returns the configuration file's path.
func (b *brandingStore) path() string {
	return filepath.Join(b.dir, brandingFilename)
}

// assetsDir returns the directory the images live in.
func (b *brandingStore) assetsDir() string {
	return filepath.Join(b.dir, brandingAssetsDirname)
}

// snapshot returns the current configuration.
//
// A nil store answers the defaults: a `Server` assembled by a unit test (the
// shell's rendering is one) has no data directory to read a file from, and the
// shell must still render.
func (b *brandingStore) snapshot() brandingConfig {
	if b == nil {
		return defaultBrandingConfig()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.doc
}

// siteName returns the name the shell and the SPA show.
func (b *brandingStore) siteName() string {
	name := strings.TrimSpace(b.snapshot().SiteName)
	if name == "" {
		return defaultBrandingSiteName
	}
	return name
}

// faviconFile returns the uploaded favicon's file name, or empty.
func (b *brandingStore) faviconFile() string {
	return b.snapshot().FaviconFile
}

// safeBrandingFilename keeps a file name that is a plain name.
//
// A `branding.json` is data, not a capability: a value carrying a path
// separator or `..` is dropped rather than joined onto the assets directory.
func safeBrandingFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name != filepath.Base(name) || strings.Contains(name, "..") {
		return ""
	}
	return name
}

// writeLocked persists the configuration.
//
// The caller holds the write lock. The write goes to a temporary file in the
// same directory and is renamed into place, so a crash halfway through leaves
// the previous file intact.
func (b *brandingStore) writeLocked() error {
	payload, err := json.MarshalIndent(b.doc, "", "  ")
	if err != nil {
		return errors.Wrap(err, "leafwiki: could not encode the branding")
	}

	if err := os.MkdirAll(b.dir, 0o750); err != nil {
		return errors.Wrap(err, "leafwiki: could not create the data directory")
	}

	temp := b.path() + ".tmp"
	if err := os.WriteFile(temp, payload, 0o640); err != nil {
		return errors.Wrap(err, "leafwiki: could not write the branding")
	}
	if err := os.Rename(temp, b.path()); err != nil {
		return errors.Wrap(err, "leafwiki: could not replace the branding")
	}

	return nil
}

// ErrBrandingImageTooLarge reports an upload past the size cap.
//
// It is a sentinel rather than an API error because the store is also used by
// the tests; the handler maps it onto 413, which is what the SPA's uploader
// expects for "file too large".
var ErrBrandingImageTooLarge = errors.New("leafwiki: the branding image is too large")

// UpdateSiteName validates and saves the wiki's name.
//
// The rules are upstream's (`BrandingService.UpdateBranding`): a name that is
// empty after trimming, longer than the cap, or carrying control characters is
// a VALIDATION error — the SPA shows the message against the field, so it must
// be a 400 with the server's own wording rather than a generic failure.
func (b *brandingStore) UpdateSiteName(name string) (brandingConfig, error) {
	name = strings.TrimSpace(name)

	switch {
	case name == "":
		return brandingConfig{}, errors.Wrap(ErrInvalidArgument, "the site name must not be empty")
	case len(name) > maxSiteNameLength:
		return brandingConfig{}, errors.Wrapf(ErrInvalidArgument, "the site name must not exceed %d characters", maxSiteNameLength)
	case containsControlCharacters(name):
		return brandingConfig{}, errors.Wrap(ErrInvalidArgument, "the site name contains invalid control characters")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.doc.SiteName = name
	if err := b.writeLocked(); err != nil {
		return brandingConfig{}, err
	}

	return b.doc, nil
}

// containsControlCharacters reports whether a string carries control
// characters that would break a layout.
//
// Upstream blocks every `unicode.IsControl` rune except tab, newline and
// carriage return, so ordinary multi-line text still fits.
func containsControlCharacters(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return true
		}
	}
	return false
}

// brandingImageKind names one of the two images.
type brandingImageKind string

const (
	// brandingLogo is the logo shown in the header.
	brandingLogo brandingImageKind = "logo"
	// brandingFavicon is the icon the browser tab shows.
	brandingFavicon brandingImageKind = "favicon"
)

// exts returns the extensions the kind accepts.
func (k brandingImageKind) exts() []string {
	if k == brandingFavicon {
		return allowedFaviconExts
	}
	return allowedLogoExts
}

// allowedExt reports whether a file extension is accepted for the kind.
func (k brandingImageKind) allowedExt(ext string) bool {
	ext = strings.ToLower(ext)
	for _, allowed := range k.exts() {
		if ext == allowed {
			return true
		}
	}
	return false
}

// uploadImage stores a logo or a favicon.
//
// The file is written to `<storageDir>/branding/<kind><ext>` — upstream's
// naming, so a LeafWiki upload directory drops in — and every OTHER file of
// the same kind is removed, which is what stops an old `.png` shadowing the
// new `.webp` after an operator switches formats.
func (b *brandingStore) uploadImage(kind brandingImageKind, filename string, reader io.Reader) (brandingConfig, error) {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	if !kind.allowedExt(ext) {
		return brandingConfig{}, errors.Wrapf(ErrInvalidArgument,
			"invalid %s file type %q (allowed: %s)", kind, ext, strings.Join(kind.exts(), ", "))
	}

	if err := os.MkdirAll(b.assetsDir(), 0o750); err != nil {
		return brandingConfig{}, errors.Wrap(err, "leafwiki: could not create the branding directory")
	}

	target := filepath.Join(b.assetsDir(), string(kind)+ext)
	written, err := writeStreamAtomic(target, reader, maxBrandingImageBytes)
	if err != nil {
		return brandingConfig{}, err
	}
	_ = written

	// Remove the previous image of the same kind, so only one file answers
	// for it.
	b.removeOtherImages(kind, target)

	b.mu.Lock()
	defer b.mu.Unlock()

	if kind == brandingFavicon {
		b.doc.FaviconFile = filepath.Base(target)
	} else {
		b.doc.LogoFile = filepath.Base(target)
	}

	if err := b.writeLocked(); err != nil {
		return brandingConfig{}, err
	}

	return b.doc, nil
}

// removeOtherImages deletes the kind's images other than keep.
func (b *brandingStore) removeOtherImages(kind brandingImageKind, keep string) {
	matches, err := filepath.Glob(filepath.Join(b.assetsDir(), string(kind)+".*"))
	if err != nil {
		return
	}
	for _, match := range matches {
		if match == keep {
			continue
		}
		_ = os.Remove(match)
	}
}

// DeleteImage removes a kind's image and forgets it.
//
// Deleting an image that is not there is a NO-OP rather than an error: the
// SPA's delete control is only drawn when there is something to delete, and a
// retry after a failed response must not fail on the second attempt.
func (b *brandingStore) DeleteImage(kind brandingImageKind) (brandingConfig, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	name := b.doc.LogoFile
	if kind == brandingFavicon {
		name = b.doc.FaviconFile
	}

	if name != "" {
		_ = os.Remove(filepath.Join(b.assetsDir(), safeBrandingFilename(name)))
	}

	if kind == brandingFavicon {
		b.doc.FaviconFile = ""
	} else {
		b.doc.LogoFile = ""
	}

	if err := b.writeLocked(); err != nil {
		return brandingConfig{}, err
	}

	return b.doc, nil
}

// AssetPath returns the path of a configured image by its file name.
//
// It answers only for the names the configuration currently holds, which is
// what makes path traversal impossible: the request names a file, and the
// STORE decides whether that name is one of its own.
func (b *brandingStore) AssetPath(filename string) (string, bool) {
	if b == nil {
		return "", false
	}

	filename = safeBrandingFilename(filename)
	if filename == "" {
		return "", false
	}

	doc := b.snapshot()
	if filename != doc.LogoFile && filename != doc.FaviconFile {
		return "", false
	}

	return filepath.Join(b.assetsDir(), filename), true
}

// writeStreamAtomic writes a reader to a path, refusing anything over max.
//
// The bytes go to a temporary file in the same directory and are renamed into
// place, so a failed or oversized upload leaves the PREVIOUS image intact —
// the difference between a rejected file and a broken one. The size check is
// streaming: `max+1` bytes are read, and a reader that produced more is
// refused, which is what stops a large body being buffered first.
func writeStreamAtomic(path string, source io.Reader, max int64) (int64, error) {
	temp := path + ".tmp"

	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, errors.Wrap(err, "leafwiki: could not create the branding image")
	}

	written, copyErr := io.Copy(file, io.LimitReader(source, max+1))
	closeErr := file.Close()

	if copyErr != nil {
		_ = os.Remove(temp)
		return 0, errors.Wrap(copyErr, "leafwiki: could not write the branding image")
	}
	if closeErr != nil {
		_ = os.Remove(temp)
		return 0, errors.Wrap(closeErr, "leafwiki: could not write the branding image")
	}
	if written > max {
		_ = os.Remove(temp)
		return 0, ErrBrandingImageTooLarge
	}

	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return 0, errors.Wrap(err, "leafwiki: could not replace the branding image")
	}

	return written, nil
}

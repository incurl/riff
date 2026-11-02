package leafwiki

import (
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// DefaultMaxAssetUploadSizeBytes is LeafWiki's default upload cap.
const DefaultMaxAssetUploadSizeBytes int64 = 50 * 1024 * 1024

// assetURLPrefix is the route prefix the SPA rewrites through its own base
// path (see ui/leafwiki-ui's normalizeAssetSrc). Returning "/assets/…" rather
// than a prefixed path is what makes the attachment panel work behind /wiki.
const assetURLPrefix = "/assets/"

// Assets stores per-page uploads under <storage>/assets/<pageID>/.
//
// LeafWiki keeps uploads out of the Markdown tree on purpose: an asset must
// not be mistaken for a page, and renaming a page must not move its files.
type Assets struct {
	store     *store
	maxUpload int64
}

// newAssets returns the asset service.
func newAssets(st *store, maxUpload int64) *Assets {
	if maxUpload <= 0 {
		maxUpload = DefaultMaxAssetUploadSizeBytes
	}
	return &Assets{store: st, maxUpload: maxUpload}
}

// assetURL builds the public URL of an attachment.
func assetURL(pageID, filename string) string {
	return assetURLPrefix + url.PathEscape(pageID) + "/" + url.PathEscape(filename)
}

// List returns the attachment URLs of a page.
func (a *Assets) List(pageID string) ([]string, error) {
	dir, err := a.store.pageAssetDir(pageID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, errors.Wrap(err, "leafwiki: could not read the asset directory")
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		files = append(files, assetURL(pageID, entry.Name()))
	}
	return files, nil
}

// Path resolves an attachment to a path inside the asset directory. It refuses
// anything that would escape that directory.
func (a *Assets) Path(pageID, filename string) (string, error) {
	if err := safeFilename(pageID); err != nil {
		return "", errors.Wrap(err, "leafwiki: invalid page id")
	}
	if err := safeFilename(filename); err != nil {
		return "", errors.Wrap(err, "leafwiki: invalid asset name")
	}
	dir, err := a.store.pageAssetDir(pageID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filename), nil
}

// Save stores an uploaded attachment and returns its URL.
//
// The filename is normalised (LeafWiki validates it as a slug) and made
// unique, so an upload can never overwrite a sibling.
func (a *Assets) Save(pageID, filename string, content io.Reader) (string, error) {
	dir, err := a.store.pageAssetDir(pageID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", errors.Wrap(err, "leafwiki: could not create the asset directory")
	}

	name, err := uniqueAssetName(dir, filename)
	if err != nil {
		return "", err
	}

	target := filepath.Join(dir, name)
	file, err := os.CreateTemp(dir, ".leafwiki-upload-*")
	if err != nil {
		return "", errors.Wrap(err, "leafwiki: could not create the asset file")
	}
	tmpName := file.Name()

	written, copyErr := io.Copy(file, io.LimitReader(content, a.maxUpload+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmpName)
		return "", errors.Wrap(copyErr, "leafwiki: could not write the asset")
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return "", errors.Wrap(closeErr, "leafwiki: could not write the asset")
	}
	if written > a.maxUpload {
		_ = os.Remove(tmpName)
		return "", errors.Wrap(ErrInvalidArgument, "asset exceeds the upload size limit")
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		_ = os.Remove(tmpName)
		return "", errors.Wrap(err, "leafwiki: could not set asset permissions")
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return "", errors.Wrap(err, "leafwiki: could not store the asset")
	}
	return assetURL(pageID, name), nil
}

// uniqueAssetName normalises a filename and appends "-N" until it does not
// collide with an existing file.
func uniqueAssetName(dir, filename string) (string, error) {
	base := path.Base(strings.ReplaceAll(filename, `\`, "/"))
	if err := safeFilename(base); err != nil {
		return "", errors.Wrap(ErrInvalidArgument, "asset name is not usable")
	}

	extension := strings.ToLower(path.Ext(base))
	stem := strings.TrimSuffix(base, path.Ext(base))
	normalised := normaliseSlug(stem)
	if normalised == "" {
		normalised = "asset"
	}

	name := normalised + extension
	for index := 1; ; index++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			return name, nil
		}
		name = normalised + "-" + strconv.Itoa(index) + extension
	}
}

// Rename changes an attachment's filename, keeping its extension.
func (a *Assets) Rename(pageID, oldName, newName string) (string, error) {
	oldPath, err := a.Path(pageID, oldName)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(oldPath); statErr != nil {
		return "", errors.Wrap(ErrNotFound, "asset "+oldName)
	}

	base := path.Base(strings.ReplaceAll(newName, `\`, "/"))
	if err := safeFilename(base); err != nil {
		return "", errors.Wrap(ErrInvalidArgument, "asset name is not usable")
	}

	extension := strings.ToLower(path.Ext(base))
	oldExtension := strings.ToLower(path.Ext(oldName))
	if extension != oldExtension {
		return "", errors.Wrap(ErrInvalidArgument, "the asset extension must not change")
	}
	stem := strings.TrimSuffix(base, path.Ext(base))
	normalised := normaliseSlug(stem)
	if normalised == "" {
		return "", errors.Wrap(ErrInvalidArgument, "asset name is not usable")
	}

	target := normalised + extension
	if target == oldName {
		return assetURL(pageID, oldName), nil
	}

	newPath, err := a.Path(pageID, target)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(newPath); statErr == nil {
		return "", errors.Wrap(ErrAlreadyExists, "asset "+target)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return "", errors.Wrap(err, "leafwiki: could not rename the asset")
	}
	return assetURL(pageID, target), nil
}

// Delete removes an attachment.
func (a *Assets) Delete(pageID, filename string) error {
	target, err := a.Path(pageID, filename)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			return errors.Wrap(ErrNotFound, "asset "+filename)
		}
		return errors.Wrap(err, "leafwiki: could not delete the asset")
	}
	return nil
}

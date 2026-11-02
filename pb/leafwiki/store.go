package leafwiki

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
)

const (
	// pagesDirName holds the Markdown tree. LeafWiki's own layout puts the
	// pages under <storage>/root, and a data directory is only portable
	// between the two if that is preserved.
	pagesDirName = "root"

	// assetsDirName holds per-page uploads, one directory per page id.
	assetsDirName = "assets"

	// indexFilename is the section body file.
	indexFilename = "index.md"

	// orderFilename holds the explicit child order of a directory.
	orderFilename = ".order.json"

	// schemaFilename records the on-disk layout version.
	schemaFilename = "schema.json"

	// schemaVersion is LeafWiki's current layout version. Bumping it is a
	// migration concern; this port only writes the current value.
	schemaVersion = 5

	systemUserID = "system"
)

// store is the filesystem half of the wiki. It owns the Markdown tree and the
// per-page asset directories and knows nothing about the HTTP surface or the
// index.
type store struct {
	storageDir string
	rootDir    string
	assetsDir  string
}

// newStore prepares the storage directories and returns the store.
func newStore(storageDir string) (*store, error) {
	s := &store{
		storageDir: storageDir,
		rootDir:    filepath.Join(storageDir, pagesDirName),
		assetsDir:  filepath.Join(storageDir, assetsDirName),
	}
	for _, dir := range []string{s.storageDir, s.rootDir, s.assetsDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.Wrap(err, "leafwiki: could not create storage directory")
		}
	}
	if err := s.ensureSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

// ensureSchema writes the layout version marker when it is missing. The
// marker is informational for this port — it exists so an upstream LeafWiki
// binary pointed at the same directory recognises the layout.
func (s *store) ensureSchema() error {
	path := filepath.Join(s.storageDir, schemaFilename)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	payload, err := json.MarshalIndent(struct {
		Version int `json:"version"`
	}{Version: schemaVersion}, "", "  ")
	if err != nil {
		return errors.Wrap(err, "leafwiki: could not encode schema marker")
	}
	if err := os.WriteFile(path, payload, 0o640); err != nil {
		return errors.Wrap(err, "leafwiki: could not write schema marker")
	}
	return nil
}

// dirForNode returns the directory that holds the node's files.
func (s *store) dirForNode(node *PageNode) (string, error) {
	if node == nil || node.ID == rootID {
		return s.rootDir, nil
	}
	if node.Kind == NodeKindSection {
		parentDir, err := s.dirForNode(node.Parent)
		if err != nil {
			return "", err
		}
		return filepath.Join(parentDir, node.Slug), nil
	}
	return s.dirForNode(node.Parent)
}

// contentPath returns the Markdown file backing the node.
func (s *store) contentPath(node *PageNode) (string, error) {
	dir, err := s.dirForNode(node)
	if err != nil {
		return "", err
	}
	if node != nil && node.Kind == NodeKindSection {
		return filepath.Join(dir, indexFilename), nil
	}
	return filepath.Join(dir, node.Slug+".md"), nil
}

// fileExists reports whether path exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// writeFileAtomic writes content to path through a temporary file in the same
// directory, so a crash mid-write cannot leave a half-written page.
func writeFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return errors.Wrap(err, "leafwiki: could not create directory")
	}

	tmp, err := os.CreateTemp(dir, ".leafwiki-tmp-*")
	if err != nil {
		return errors.Wrap(err, "leafwiki: could not create temporary file")
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return errors.Wrap(err, "leafwiki: could not write temporary file")
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return errors.Wrap(err, "leafwiki: could not close temporary file")
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		_ = os.Remove(tmpName)
		return errors.Wrap(err, "leafwiki: could not set file permissions")
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return errors.Wrap(err, "leafwiki: could not replace file")
	}
	return nil
}

// generateID returns a new page identifier. The format is opaque to clients;
// it only has to be unique and URL-safe.
func generateID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.Wrap(err, "leafwiki: could not generate page id")
	}
	return hex.EncodeToString(buf), nil
}

// parseMetadataTime parses a leafwiki_*_at frontmatter value.
func parseMetadataTime(value string) time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

// formatMetadataTime renders a timestamp for the frontmatter.
func formatMetadataTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}

// childOrder is the on-disk representation of an explicit child order.
type childOrder struct {
	OrderedIDs []string `json:"ordered_ids"`
}

// readChildOrder returns the explicit child order of a directory, or nil when
// no order file exists or it cannot be parsed.
func (*store) readChildOrder(dir string) []string {
	raw, err := os.ReadFile(filepath.Join(dir, orderFilename))
	if err != nil {
		return nil
	}
	var order childOrder
	if err := json.Unmarshal(raw, &order); err != nil {
		return nil
	}
	return order.OrderedIDs
}

// writeChildOrder persists the explicit child order of a directory. An empty
// order removes the file, which restores alphabetical order.
func (*store) writeChildOrder(dir string, ids []string) error {
	path := filepath.Join(dir, orderFilename)
	if len(ids) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return errors.Wrap(err, "leafwiki: could not remove order file")
		}
		return nil
	}
	payload, err := json.MarshalIndent(childOrder{OrderedIDs: ids}, "", "  ")
	if err != nil {
		return errors.Wrap(err, "leafwiki: could not encode order file")
	}
	return writeFileAtomic(path, payload)
}

// readFile returns the raw contents of a page file.
func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrap(err, "leafwiki: could not read page file")
	}
	return string(raw), nil
}

// writePageFile serialises a page to disk.
func writePageFile(path string, frontmatter Frontmatter, body string) error {
	raw, err := BuildMarkdown(frontmatter, body)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(raw))
}

// writePage writes the node's file, creating parent directories as needed.
func (s *store) writePage(node *PageNode, frontmatter Frontmatter, body string) (string, error) {
	path, err := s.contentPath(node)
	if err != nil {
		return "", err
	}
	if err := writePageFile(path, frontmatter, body); err != nil {
		return "", err
	}
	return path, nil
}

// readPage returns the parsed file of the node.
func (s *store) readPage(node *PageNode) (Frontmatter, string, error) {
	path, err := s.contentPath(node)
	if err != nil {
		return Frontmatter{}, "", err
	}
	raw, err := readFile(path)
	if err != nil {
		return Frontmatter{}, "", err
	}
	return ParseMarkdown(raw)
}

// pageAssetDir returns the asset directory of a page.
func (s *store) pageAssetDir(pageID string) (string, error) {
	if err := safeFilename(pageID); err != nil {
		return "", errors.Wrap(err, "leafwiki: invalid page id")
	}
	return filepath.Join(s.assetsDir, pageID), nil
}

// reconstruct walks the Markdown tree and returns the root node. Missing
// managed frontmatter (id, timestamps, title) is written back so identifiers
// stay stable across restarts.
func (s *store) reconstruct(now time.Time) (*PageNode, error) {
	root := &PageNode{
		ID:       rootID,
		Slug:     rootID,
		Title:    rootID,
		Kind:     NodeKindSection,
		Children: []*PageNode{},
		Metadata: PageMetadata{
			CreatedAt:    now,
			UpdatedAt:    now,
			CreatorID:    systemUserID,
			LastAuthorID: systemUserID,
		},
	}
	seenIDs := map[string]string{rootID: s.rootDir}

	if !dirExists(s.rootDir) {
		return root, nil
	}
	if err := s.loadDir(s.rootDir, root, now, seenIDs); err != nil {
		return nil, err
	}
	return root, nil
}

// readOptionalPage reads a page file that may not exist yet.
func (*store) readOptionalPage(path string) (Frontmatter, string, bool) {
	if !fileExists(path) {
		return Frontmatter{Extra: map[string]any{}}, "", false
	}
	raw, err := readFile(path)
	if err != nil {
		return Frontmatter{Extra: map[string]any{}}, "", false
	}
	fm, body, err := ParseMarkdown(raw)
	if err != nil {
		return Frontmatter{Extra: map[string]any{}}, "", false
	}
	return fm, body, true
}

// metadataFromFrontmatter builds a PageMetadata block, falling back to the
// file's modification time (and then to now) for missing timestamps.
func metadataFromFrontmatter(fm Frontmatter, fallback time.Time) PageMetadata {
	created := parseMetadataTime(fm.CreatedAt)
	updated := parseMetadataTime(fm.UpdatedAt)
	if created.IsZero() {
		created = fallback
	}
	if updated.IsZero() {
		updated = created
	}
	return PageMetadata{
		CreatedAt:    created,
		UpdatedAt:    updated,
		CreatorID:    firstNonEmpty(fm.CreatorID, systemUserID),
		LastAuthorID: firstNonEmpty(fm.LastAuthorID, systemUserID),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// pageTitle derives the display title of a page file.
func pageTitle(fm Frontmatter, body, fallback string) string {
	if title := strings.TrimSpace(fm.Title); title != "" {
		return title
	}
	if title := TitleFromBody(body); title != "" {
		return title
	}
	return fallback
}

// applyOrder sorts a node's children according to its `.order.json` file and
// assigns the final positions.
func (s *store) applyOrder(parent *PageNode, dir string) {
	if parent == nil {
		return
	}
	order := s.readChildOrder(dir)
	if len(order) > 0 {
		positions := make(map[string]int, len(order))
		for index, id := range order {
			if _, exists := positions[id]; exists {
				continue
			}
			positions[id] = index
		}
		rank := func(node *PageNode) (int, bool) {
			position, ok := positions[node.ID]
			return position, ok
		}
		slices.SortStableFunc(parent.Children, func(a, b *PageNode) int {
			leftRank, leftKnown := rank(a)
			rightRank, rightKnown := rank(b)
			switch {
			case leftKnown && rightKnown:
				return cmp.Compare(leftRank, rightRank)
			case leftKnown:
				return -1
			case rightKnown:
				return 1
			default:
				return 0
			}
		})
	}
	for index, child := range parent.Children {
		child.Position = index
	}
}

// writeBackFrontmatter persists the managed metadata of a reconstructed node
// while keeping the file's modification time, so an externally edited file
// does not look modified to the user after a load.
func (*store) writeBackFrontmatter(path string, fm Frontmatter, body string, node *PageNode) {
	var originalModTime time.Time
	if info, err := os.Stat(path); err == nil {
		originalModTime = info.ModTime()
	}

	fm.ID = node.ID
	fm.Title = node.Title
	fm.CreatedAt = formatMetadataTime(node.Metadata.CreatedAt)
	fm.UpdatedAt = formatMetadataTime(node.Metadata.UpdatedAt)
	fm.CreatorID = node.Metadata.CreatorID
	fm.LastAuthorID = node.Metadata.LastAuthorID
	fm.Pinned = node.Pinned

	if err := writePageFile(path, fm, body); err != nil {
		// Best effort: this runs while reconstructing the tree, and a failure
		// here must not stop the wiki from loading the page it could read.
		return //nolint:nilerr // see above
	}
	if !originalModTime.IsZero() {
		_ = os.Chtimes(path, originalModTime, originalModTime)
	}
}

// loadDir reconstructs the children of parent from dir.
func (s *store) loadDir(dir string, parent *PageNode, now time.Time, seenIDs map[string]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.Wrap(err, "leafwiki: could not read pages directory")
	}

	seenSlugs := map[string]string{}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			// Hidden files (including the order file) are not pages.
			continue
		}

		fullPath := filepath.Join(dir, name)

		if entry.IsDir() {
			if err := validateSlug(name); err != nil {
				continue
			}
			if err := s.loadSection(fullPath, name, parent, now, seenIDs, seenSlugs); err != nil {
				return err
			}
			continue
		}

		if err := s.loadPageFile(fullPath, name, parent, now, seenIDs, seenSlugs); err != nil {
			return err
		}
	}

	s.applyOrder(parent, dir)
	return nil
}

// loadSection reconstructs a directory node and its subtree.
func (s *store) loadSection(fullPath, name string, parent *PageNode, now time.Time, seenIDs, seenSlugs map[string]string) error {
	indexPath := filepath.Join(fullPath, indexFilename)
	fm, body, existed := s.readOptionalPage(indexPath)

	fallback := now
	if info, err := os.Stat(indexPath); err == nil {
		fallback = info.ModTime().UTC()
	}

	id := strings.TrimSpace(fm.ID)
	if id == "" {
		generated, err := generateID()
		if err != nil {
			return err
		}
		id = generated
	}

	node := &PageNode{
		ID:       id,
		Slug:     name,
		Title:    pageTitle(fm, body, name),
		Kind:     NodeKindSection,
		Children: []*PageNode{},
		Pinned:   fm.Pinned,
		Metadata: metadataFromFrontmatter(fm, fallback),
		Parent:   parent,
	}

	if err := checkUnique(node, fullPath, seenIDs, seenSlugs); err != nil {
		return err
	}
	parent.Children = append(parent.Children, node)

	if err := s.loadDir(fullPath, node, now, seenIDs); err != nil {
		return err
	}

	if !existed || strings.TrimSpace(fm.ID) == "" || fm.CreatedAt == "" || fm.UpdatedAt == "" || strings.TrimSpace(fm.Title) == "" {
		s.writeBackFrontmatter(indexPath, fm, body, node)
	}
	return nil
}

// loadPageFile reconstructs a Markdown file node.
func (s *store) loadPageFile(fullPath, name string, parent *PageNode, now time.Time, seenIDs, seenSlugs map[string]string) error {
	extension := filepath.Ext(name)
	if !strings.EqualFold(extension, ".md") {
		return nil
	}
	slug := strings.TrimSuffix(name, extension)
	if strings.EqualFold(slug, "index") {
		// The section body is handled by loadSection.
		return nil
	}
	if err := validateSlug(slug); err != nil {
		// A file whose name is not a route-safe slug is not a page. Skip it
		// rather than fail the whole load: one stray file must not take the
		// wiki down.
		return nil //nolint:nilerr // see above
	}

	fm, body, existed := s.readOptionalPage(fullPath)
	if !existed {
		return nil
	}

	fallback := now
	if info, err := os.Stat(fullPath); err == nil {
		fallback = info.ModTime().UTC()
	}

	id := strings.TrimSpace(fm.ID)
	if id == "" {
		generated, err := generateID()
		if err != nil {
			return err
		}
		id = generated
	}

	node := &PageNode{
		ID:       id,
		Slug:     slug,
		Title:    pageTitle(fm, body, slug),
		Kind:     NodeKindPage,
		Children: nil,
		Pinned:   fm.Pinned,
		Metadata: metadataFromFrontmatter(fm, fallback),
		Parent:   parent,
	}

	if err := checkUnique(node, fullPath, seenIDs, seenSlugs); err != nil {
		return err
	}
	parent.Children = append(parent.Children, node)

	if strings.TrimSpace(fm.ID) == "" || fm.CreatedAt == "" || fm.UpdatedAt == "" || strings.TrimSpace(fm.Title) == "" {
		s.writeBackFrontmatter(fullPath, fm, body, node)
	}
	return nil
}

// checkUnique rejects a node whose id or slug is already taken among its
// siblings. A duplicate id is skipped rather than fatal so one stray file
// cannot take the wiki down; a duplicate slug is fatal, because the tree
// would be ambiguous.
func checkUnique(node *PageNode, path string, seenIDs, seenSlugs map[string]string) error {
	slugKey := strings.ToLower(strings.TrimSpace(node.Slug))
	if existing, exists := seenSlugs[slugKey]; exists {
		return errors.New("leafwiki: duplicate slug " + node.Slug + " at " + path + " (already at " + existing + ")")
	}
	seenSlugs[slugKey] = path

	if existing, exists := seenIDs[node.ID]; exists {
		return errors.New("leafwiki: duplicate page id " + node.ID + " at " + path + " (already at " + existing + ")")
	}
	seenIDs[node.ID] = path
	return nil
}

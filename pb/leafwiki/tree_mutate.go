package leafwiki

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// checkVersion enforces the optimistic-lock token. An empty expectation is
// always accepted: older clients do not send one.
func checkVersion(node *PageNode, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return nil
	}
	if expected != node.Version {
		return errors.Wrap(ErrVersionConflict, "page "+node.ID)
	}
	return nil
}

// resolveMutableLocked resolves an id to a node that may be changed: the tree
// root is not mutable.
func (t *Tree) resolveMutableLocked(id string) (*PageNode, error) {
	node, err := t.nodeLocked(id)
	if err != nil {
		return nil, err
	}
	if node.ID == rootID {
		return nil, errors.Wrap(ErrInvalidArgument, "the tree root cannot be modified")
	}
	return node, nil
}

// ensureSectionLocked converts a page parent into a section so a child can be
// created below it, matching LeafWiki's behaviour.
func (t *Tree) ensureSectionLocked(node *PageNode, actor Actor) error {
	if node.Kind == NodeKindSection {
		return nil
	}
	return t.convertLocked(node, NodeKindSection, actor)
}

// convertLocked changes a node between page and section on disk.
func (t *Tree) convertLocked(node *PageNode, target NodeKind, actor Actor) error {
	if node.Kind == target {
		return nil
	}

	if target == NodeKindPage && len(node.Children) > 0 {
		return errors.Wrap(ErrInvalidArgument, "a section with children cannot become a page")
	}

	oldPath, err := t.store.contentPath(node)
	if err != nil {
		return err
	}
	fm, body, err := t.store.readPage(node)
	if err != nil {
		fm = Frontmatter{Extra: map[string]any{}}
		body = ""
	}

	node.Kind = target
	newPath, err := t.store.contentPath(node)
	if err != nil {
		node.Kind = otherKind(target)
		return err
	}

	fm.ID = firstNonEmpty(fm.ID, node.ID)
	fm.Title = firstNonEmpty(fm.Title, node.Title)
	fm.UpdatedAt = formatMetadataTime(t.now().UTC())
	fm.LastAuthorID = firstNonEmpty(actor.ID, fm.LastAuthorID, systemUserID)
	if fm.CreatedAt == "" {
		fm.CreatedAt = fm.UpdatedAt
	}

	if err := writePageFile(newPath, fm, body); err != nil {
		node.Kind = otherKind(target)
		return err
	}
	if oldPath != newPath {
		if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
			return errors.Wrap(err, "leafwiki: could not remove the old page file")
		}
	}
	if target == NodeKindPage {
		// The now-empty section directory must go, otherwise the loader
		// would resurrect it as a section.
		_ = os.Remove(filepath.Dir(newPath))
	}
	return nil
}

// otherKind returns the counterpart of a node kind.
func otherKind(kind NodeKind) NodeKind {
	if kind == NodeKindPage {
		return NodeKindSection
	}
	return NodeKindPage
}

// dirLocked returns the directory of a node without touching the disk.
func (t *Tree) dirLocked(node *PageNode) string {
	dir, err := t.store.dirForNode(node)
	if err != nil {
		return t.store.rootDir
	}
	return dir
}

// childOrderLocked returns the current child ids of a node in display order.
func (*Tree) childOrderLocked(node *PageNode) []string {
	ids := make([]string, 0, len(node.Children))
	for _, child := range node.Children {
		ids = append(ids, child.ID)
	}
	return ids
}

// pageLockedByIDLocked builds the Page projection of an id that is known to
// exist in the freshly loaded tree.
func (t *Tree) pageLockedByIDLocked(id string) (*Page, error) {
	node, err := t.nodeLocked(id)
	if err != nil {
		return nil, err
	}
	return t.pageLocked(node)
}

// CreatePage creates a page or section under parentID.
func (t *Tree) CreatePage(actor Actor, parentID, title, slug string, kind NodeKind) (*Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.Wrap(ErrInvalidArgument, "title must not be empty")
	}
	if kind != NodeKindPage && kind != NodeKindSection {
		return nil, errors.Wrap(ErrInvalidArgument, "kind must be either 'page' or 'section'")
	}

	parent, err := t.nodeLocked(parentID)
	if err != nil {
		return nil, err
	}
	if err := t.ensureSectionLocked(parent, actor); err != nil {
		return nil, err
	}
	// ensureSectionLocked may have changed the parent's kind on disk; reload
	// so the in-memory node matches before a child is attached.
	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	if parent, err = t.nodeLocked(parentID); err != nil {
		return nil, err
	}

	desired := strings.TrimSpace(slug)
	if desired == "" {
		desired = title
	}
	childSlug := uniqueChildSlug(parent, "", desired)

	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := t.now().UTC()
	node := &PageNode{
		ID:       id,
		Title:    title,
		Slug:     childSlug,
		Kind:     kind,
		Parent:   parent,
		Metadata: PageMetadata{CreatedAt: now, UpdatedAt: now, CreatorID: actor.ID, LastAuthorID: actor.ID},
	}
	if kind == NodeKindSection {
		node.Children = []*PageNode{}
	}

	order := t.childOrderLocked(parent)
	order = append(order, id)
	if err := t.store.writeChildOrder(t.dirLocked(parent), order); err != nil {
		return nil, err
	}

	fm := Frontmatter{
		ID:           id,
		Title:        title,
		CreatedAt:    formatMetadataTime(now),
		UpdatedAt:    formatMetadataTime(now),
		CreatorID:    actor.ID,
		LastAuthorID: actor.ID,
		Extra:        map[string]any{},
	}
	if _, err := t.store.writePage(node, fm, ""); err != nil {
		return nil, err
	}

	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	return t.pageLockedByIDLocked(id)
}

// EnsurePath creates every missing segment of a route path.
func (t *Tree) EnsurePath(actor Actor, path, title string) (*Page, error) {
	normalised, err := normalisePathSlugs(path)
	if err != nil {
		return nil, errors.Wrap(ErrInvalidArgument, err.Error())
	}
	segments := pathSegments(normalised)
	if len(segments) == 0 {
		return nil, errors.Wrap(ErrInvalidArgument, "path must not be empty")
	}

	parentID := rootID
	for index, segment := range segments {
		prefix := strings.Join(segments[:index+1], "/")
		if existing, lookupErr := t.PageByPath(prefix); lookupErr == nil {
			parentID = existing.ID
			continue
		}

		kind := NodeKindSection
		segmentTitle := segment
		if index == len(segments)-1 {
			kind = NodeKindPage
			if strings.TrimSpace(title) != "" {
				segmentTitle = strings.TrimSpace(title)
			}
		}

		created, createErr := t.CreatePage(actor, parentID, segmentTitle, segment, kind)
		if createErr != nil {
			return nil, createErr
		}
		parentID = created.ID
	}

	return t.Page(parentID)
}

// UpdatePage saves a page's content and/or metadata.
//
// content, tags and properties are optional: nil means "leave unchanged".
// That distinction matters for the SPA's autosave, which sends the body
// without touching the tag list.
func (t *Tree) UpdatePage(actor Actor, id, version, title, slug string, content *string, tags *[]string, properties *map[string]string) (*Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	node, err := t.resolveMutableLocked(id)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(node, version); err != nil {
		return nil, err
	}

	newSlug := node.Slug
	if desired := strings.TrimSpace(slug); desired != "" && desired != node.Slug {
		if err := validateSlug(desired); err != nil {
			return nil, errors.Wrap(ErrInvalidArgument, err.Error())
		}
		if existing := findChildBySlug(node.Parent, desired); existing != nil && existing.ID != node.ID {
			return nil, errors.Wrap(ErrAlreadyExists, "slug "+desired)
		}
		newSlug = desired
	}

	fm, body, err := t.store.readPage(node)
	if err != nil {
		fm = Frontmatter{Extra: map[string]any{}}
		body = ""
	}

	if content != nil {
		body = *content
	}
	if trimmed := strings.TrimSpace(title); trimmed != "" {
		fm.Title = trimmed
	}
	if tags != nil {
		applyTags(&fm, *tags)
	}
	if properties != nil {
		applyProperties(&fm, *properties)
	}

	now := t.now().UTC()
	fm.ID = firstNonEmpty(fm.ID, node.ID)
	fm.Title = firstNonEmpty(fm.Title, node.Title)
	fm.CreatedAt = firstNonEmpty(fm.CreatedAt, formatMetadataTime(node.Metadata.CreatedAt), formatMetadataTime(now))
	fm.UpdatedAt = formatMetadataTime(now)
	fm.LastAuthorID = firstNonEmpty(actor.ID, systemUserID)

	if newSlug != node.Slug {
		if err := t.renameNodeFileLocked(node, newSlug); err != nil {
			return nil, err
		}
	}
	if _, err := t.store.writePage(node, fm, body); err != nil {
		return nil, err
	}

	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	return t.pageLockedByIDLocked(id)
}

// renameNodeFileLocked renames the node's file or directory on disk and its
// entry in the parent's order file.
func (t *Tree) renameNodeFileLocked(node *PageNode, newSlug string) error {
	oldDir := t.dirLocked(node)
	oldPath, err := t.nodeDiskPathLocked(node)
	if err != nil {
		return err
	}

	node.Slug = newSlug
	newPath, err := t.nodeDiskPathLocked(node)
	if err != nil {
		return err
	}
	if oldPath == newPath {
		return nil
	}
	if _, statErr := os.Stat(newPath); statErr == nil {
		return errors.Wrap(ErrAlreadyExists, "path "+newPath)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o750); err != nil {
		return errors.Wrap(err, "leafwiki: could not create directory")
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return errors.Wrap(err, "leafwiki: could not rename page")
	}
	_ = oldDir
	return nil
}

// nodeDiskPathLocked returns the file (page) or directory (section) of a node.
func (t *Tree) nodeDiskPathLocked(node *PageNode) (string, error) {
	if node.Kind == NodeKindSection {
		return t.store.dirForNode(node)
	}
	return t.store.contentPath(node)
}

// applyTags writes the tag list into the frontmatter.
func applyTags(fm *Frontmatter, tags []string) {
	if fm.Extra == nil {
		fm.Extra = map[string]any{}
	}
	seen := map[string]struct{}{}
	normalised := make([]string, 0, len(tags))
	for _, tag := range tags {
		value := strings.ToLower(strings.TrimSpace(tag))
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		normalised = append(normalised, value)
	}
	if len(normalised) == 0 {
		delete(fm.Extra, keyTags)
		return
	}
	fm.Extra[keyTags] = normalised
}

// applyProperties replaces the scalar property keys of the frontmatter while
// preserving managed keys and the tag list.
func applyProperties(fm *Frontmatter, properties map[string]string) {
	if fm.Extra == nil {
		fm.Extra = map[string]any{}
	}
	for key := range fm.Extra {
		trimmed := strings.TrimSpace(key)
		if _, managed := managedFrontmatterKeys[trimmed]; managed {
			continue
		}
		if _, system := systemPropertyKeys[strings.ToLower(trimmed)]; system {
			continue
		}
		delete(fm.Extra, key)
	}
	for key, value := range properties {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		if _, managed := managedFrontmatterKeys[trimmed]; managed {
			continue
		}
		if _, system := systemPropertyKeys[strings.ToLower(trimmed)]; system {
			continue
		}
		fm.Extra[trimmed] = value
	}
}

// PinPage toggles the pinned flag of a page.
func (t *Tree) PinPage(actor Actor, id, version string, pinned bool) (*Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	node, err := t.resolveMutableLocked(id)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(node, version); err != nil {
		return nil, err
	}

	fm, body, readErr := t.store.readPage(node)
	if readErr != nil {
		fm = Frontmatter{Extra: map[string]any{}}
		body = ""
	}
	now := t.now().UTC()
	fm.ID = firstNonEmpty(fm.ID, node.ID)
	fm.Title = firstNonEmpty(fm.Title, node.Title)
	fm.CreatedAt = firstNonEmpty(fm.CreatedAt, formatMetadataTime(node.Metadata.CreatedAt), formatMetadataTime(now))
	fm.UpdatedAt = formatMetadataTime(now)
	fm.LastAuthorID = firstNonEmpty(actor.ID, systemUserID)
	fm.Pinned = pinned

	if _, err := t.store.writePage(node, fm, body); err != nil {
		return nil, err
	}
	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	return t.pageLockedByIDLocked(id)
}

// DeletePage removes a page or a whole section subtree.
func (t *Tree) DeletePage(_ Actor, id string, recursive bool, version string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	node, err := t.resolveMutableLocked(id)
	if err != nil {
		return err
	}
	if err := checkVersion(node, version); err != nil {
		return err
	}
	if node.HasChildren() && !recursive {
		return errors.Wrap(ErrInvalidArgument, "page has children; a recursive delete is required")
	}

	ids := collectSubtreeIDs(node)
	parentDir := t.dirLocked(node.Parent)
	diskPath, pathErr := t.nodeDiskPathLocked(node)

	if pathErr == nil {
		if node.Kind == NodeKindSection {
			if err := os.RemoveAll(diskPath); err != nil {
				return errors.Wrap(err, "leafwiki: could not delete section")
			}
		} else if err := os.Remove(diskPath); err != nil && !os.IsNotExist(err) {
			return errors.Wrap(err, "leafwiki: could not delete page")
		}
	}

	order := removeID(t.store.readChildOrder(parentDir), node.ID)
	if err := t.store.writeChildOrder(parentDir, order); err != nil {
		return err
	}

	for _, pageID := range ids {
		if dir, assetErr := t.store.pageAssetDir(pageID); assetErr == nil {
			_ = os.RemoveAll(dir)
		}
	}

	return t.loadLocked()
}

// collectSubtreeIDs returns the ids of a node and all of its descendants.
func collectSubtreeIDs(node *PageNode) []string {
	ids := []string{node.ID}
	for _, child := range node.Children {
		ids = append(ids, collectSubtreeIDs(child)...)
	}
	return ids
}

// removeID returns ids without the given entry.
func removeID(ids []string, target string) []string {
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != target {
			kept = append(kept, id)
		}
	}
	return kept
}

// insertIDAt inserts target at position, clamping the position to the slice.
func insertIDAt(ids []string, target string, position int) []string {
	if position < 0 || position > len(ids) {
		position = len(ids)
	}
	result := make([]string, 0, len(ids)+1)
	result = append(result, ids[:position]...)
	result = append(result, target)
	result = append(result, ids[position:]...)
	return result
}

// isDescendantOf reports whether candidate sits below ancestor.
func isDescendantOf(candidate, ancestor *PageNode) bool {
	for current := candidate; current != nil; current = current.Parent {
		if current.ID == ancestor.ID {
			return true
		}
	}
	return false
}

// MovePage moves a page or section below another parent.
func (t *Tree) MovePage(actor Actor, id, version, parentID string, position *int) (*Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	node, err := t.resolveMutableLocked(id)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(node, version); err != nil {
		return nil, err
	}

	newParent, err := t.nodeLocked(parentID)
	if err != nil {
		return nil, err
	}
	if newParent.ID == node.ID || isDescendantOf(newParent, node) {
		return nil, errors.Wrap(ErrInvalidArgument, "a page cannot be moved below itself")
	}
	if err := t.ensureSectionLocked(newParent, actor); err != nil {
		return nil, err
	}
	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	if node, err = t.resolveMutableLocked(id); err != nil {
		return nil, err
	}
	if newParent, err = t.nodeLocked(parentID); err != nil {
		return nil, err
	}

	if existing := findChildBySlug(newParent, node.Slug); existing != nil && existing.ID != node.ID {
		return nil, errors.Wrap(ErrAlreadyExists, "slug "+node.Slug)
	}

	fm, body, readErr := t.store.readPage(node)
	if readErr != nil {
		fm = Frontmatter{Extra: map[string]any{}}
		body = ""
	}

	oldDir := t.dirLocked(node)
	oldPath, err := t.nodeDiskPathLocked(node)
	if err != nil {
		return nil, err
	}

	node.Parent = newParent
	newDir := t.dirLocked(node)
	newPath, err := t.nodeDiskPathLocked(node)
	if err != nil {
		return nil, err
	}

	if oldPath != newPath {
		if err := os.MkdirAll(newDir, 0o750); err != nil {
			return nil, errors.Wrap(err, "leafwiki: could not create directory")
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			return nil, errors.Wrap(err, "leafwiki: could not move page")
		}
	}

	now := t.now().UTC()
	fm.ID = firstNonEmpty(fm.ID, node.ID)
	fm.Title = firstNonEmpty(fm.Title, node.Title)
	fm.CreatedAt = firstNonEmpty(fm.CreatedAt, formatMetadataTime(node.Metadata.CreatedAt), formatMetadataTime(now))
	fm.UpdatedAt = formatMetadataTime(now)
	fm.LastAuthorID = firstNonEmpty(actor.ID, systemUserID)
	if _, err := t.store.writePage(node, fm, body); err != nil {
		return nil, err
	}

	if oldDir != newDir {
		if err := t.store.writeChildOrder(oldDir, removeID(t.store.readChildOrder(oldDir), node.ID)); err != nil {
			return nil, err
		}
	}
	targetPosition := len(newParent.Children)
	if position != nil {
		targetPosition = *position
	}
	newOrder := removeID(t.store.readChildOrder(newDir), node.ID)
	if len(newOrder) == 0 {
		// No explicit order yet: derive one from the current children so the
		// requested position is honoured.
		newOrder = make([]string, 0, len(newParent.Children))
		for _, child := range newParent.Children {
			if child.ID != node.ID {
				newOrder = append(newOrder, child.ID)
			}
		}
	}
	if err := t.store.writeChildOrder(newDir, insertIDAt(newOrder, node.ID, targetPosition)); err != nil {
		return nil, err
	}

	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	return t.pageLockedByIDLocked(id)
}

// SortChildren persists an explicit child order for a parent.
func (t *Tree) SortChildren(_ Actor, parentID string, orderedIDs []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	parent, err := t.nodeLocked(parentID)
	if err != nil {
		return err
	}
	if parent.Kind != NodeKindSection {
		return errors.Wrap(ErrInvalidArgument, "only a section can order children")
	}

	known := map[string]struct{}{}
	for _, child := range parent.Children {
		known[child.ID] = struct{}{}
	}
	ordered := make([]string, 0, len(parent.Children))
	seen := map[string]struct{}{}
	for _, id := range orderedIDs {
		if _, ok := known[id]; !ok {
			return errors.Wrap(ErrInvalidArgument, "unknown child id "+id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	// Children the caller did not mention keep their relative order at the
	// end, so a partial order does not drop pages from the sidebar.
	for _, child := range parent.Children {
		if _, mentioned := seen[child.ID]; !mentioned {
			ordered = append(ordered, child.ID)
		}
	}

	if err := t.store.writeChildOrder(t.dirLocked(parent), ordered); err != nil {
		return err
	}
	return t.loadLocked()
}

// ConvertPage changes a page into a section or a section into a page.
func (t *Tree) ConvertPage(actor Actor, id, version string, target NodeKind) (*Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if target != NodeKindPage && target != NodeKindSection {
		return nil, errors.Wrap(ErrInvalidArgument, "targetKind must be either 'page' or 'section'")
	}

	node, err := t.resolveMutableLocked(id)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(node, version); err != nil {
		return nil, err
	}
	if err := t.convertLocked(node, target, actor); err != nil {
		return nil, err
	}
	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	return t.pageLockedByIDLocked(id)
}

// CopyPage copies a page — or a whole section subtree — below targetParentID.
func (t *Tree) CopyPage(actor Actor, id, targetParentID, title, slug string) (*Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	source, err := t.nodeLocked(id)
	if err != nil {
		return nil, err
	}
	if source.ID == rootID {
		return nil, errors.Wrap(ErrInvalidArgument, "the tree root cannot be copied")
	}

	parent, err := t.nodeLocked(targetParentID)
	if err != nil {
		return nil, err
	}
	if err := t.ensureSectionLocked(parent, actor); err != nil {
		return nil, err
	}
	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	if source, err = t.nodeLocked(id); err != nil {
		return nil, err
	}
	if parent, err = t.nodeLocked(targetParentID); err != nil {
		return nil, err
	}

	newID, err := t.copyNodeLocked(actor, source, parent, title, slug)
	if err != nil {
		return nil, err
	}
	if err := t.loadLocked(); err != nil {
		return nil, err
	}
	return t.pageLockedByIDLocked(newID)
}

// copyNodeLocked writes a copy of source below parent and returns its id.
func (t *Tree) copyNodeLocked(actor Actor, source, parent *PageNode, title, slug string) (string, error) {
	sourceFrontmatter, body, readErr := t.store.readPage(source)
	if readErr != nil {
		sourceFrontmatter = Frontmatter{Extra: map[string]any{}}
		body = ""
	}

	newTitle := strings.TrimSpace(title)
	if newTitle == "" {
		newTitle = source.Title
	}
	desiredSlug := strings.TrimSpace(slug)
	if desiredSlug == "" {
		// The copy dialog usually supplies a slug; when it does not, derive
		// one from the new title, which is what the user typed.
		desiredSlug = newTitle
	}
	childSlug := uniqueChildSlug(parent, "", desiredSlug)

	id, err := generateID()
	if err != nil {
		return "", err
	}

	now := t.now().UTC()
	node := &PageNode{
		ID:       id,
		Title:    newTitle,
		Slug:     childSlug,
		Kind:     source.Kind,
		Parent:   parent,
		Pinned:   source.Pinned,
		Metadata: PageMetadata{CreatedAt: now, UpdatedAt: now, CreatorID: actor.ID, LastAuthorID: actor.ID},
	}
	if source.Kind == NodeKindSection {
		node.Children = []*PageNode{}
	}

	frontmatter := Frontmatter{
		ID:           id,
		Title:        newTitle,
		CreatedAt:    formatMetadataTime(now),
		UpdatedAt:    formatMetadataTime(now),
		CreatorID:    actor.ID,
		LastAuthorID: actor.ID,
		Pinned:       source.Pinned,
		Extra:        copyExtra(sourceFrontmatter.Extra),
	}
	if _, err := t.store.writePage(node, frontmatter, body); err != nil {
		return "", err
	}

	order := append(t.childOrderLocked(parent), id)
	if err := t.store.writeChildOrder(t.dirLocked(parent), order); err != nil {
		return "", err
	}

	if err := t.copyAssetDir(source.ID, id); err != nil {
		return "", err
	}

	for _, child := range source.Children {
		if _, err := t.copyNodeLocked(actor, child, node, child.Title, child.Slug); err != nil {
			return "", err
		}
	}
	return id, nil
}

// copyExtra returns a shallow copy of a frontmatter extra-field map.
func copyExtra(extra map[string]any) map[string]any {
	clone := make(map[string]any, len(extra))
	for key, value := range extra {
		clone[key] = value
	}
	return clone
}

// copyAssetDir copies every attachment of one page to another.
func (t *Tree) copyAssetDir(sourceID, targetID string) error {
	sourceDir, err := t.store.pageAssetDir(sourceID)
	if err != nil {
		return err
	}
	if !dirExists(sourceDir) {
		return nil
	}
	targetDir, err := t.store.pageAssetDir(targetID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return errors.Wrap(err, "leafwiki: could not create asset directory")
	}

	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return errors.Wrap(err, "leafwiki: could not read asset directory")
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(sourceDir, entry.Name()))
		if readErr != nil {
			return errors.Wrap(readErr, "leafwiki: could not read asset")
		}
		if err := writeFileAtomic(filepath.Join(targetDir, entry.Name()), content); err != nil {
			return err
		}
	}
	return nil
}

// LookupPath reports which segments of a route path already exist.
func (t *Tree) LookupPath(path string) *PathLookup {
	t.mu.RLock()
	defer t.mu.RUnlock()

	segments := pathSegments(path)
	lookup := &PathLookup{Segments: make([]PathSegment, 0, len(segments))}

	node := t.root
	allExist := true
	slugs := make([]string, 0, len(segments))

	for _, segment := range segments {
		normalised := normaliseSlug(segment)
		if normalised == "" {
			normalised = segment
		}
		slugs = append(slugs, normalised)

		child := findChildBySlug(node, segment)
		if child == nil {
			allExist = false
			lookup.Segments = append(lookup.Segments, PathSegment{Slug: normalised})
			node = nil
			continue
		}
		lookup.Segments = append(lookup.Segments, PathSegment{Slug: child.Slug, ID: child.ID, Exists: true})
		node = child
	}

	lookup.Path = strings.Join(slugs, "/")
	lookup.Exists = allExist
	lookup.CanCreate = len(segments) > 0 && !allExist
	return lookup
}

// SuggestSlug returns the slug a new page would get under parentID.
func (t *Tree) SuggestSlug(parentID, title, currentID string) (string, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	parent, err := t.nodeLocked(parentID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(title) == "" {
		return "", nil
	}
	return uniqueChildSlug(parent, currentID, title), nil
}

// Permalink resolves a stable page id to its current route path.
func (t *Tree) Permalink(id string) (*PermalinkTarget, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	node, err := t.nodeLocked(id)
	if err != nil {
		return nil, err
	}
	return &PermalinkTarget{ID: node.ID, Slug: node.Slug, Path: node.Path}, nil
}

package leafwiki

import (
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// Sentinel errors. The HTTP layer maps them to status codes; everything else
// is a 500.
var (
	// ErrNotFound reports a page, parent or asset that does not exist.
	ErrNotFound = errors.New("leafwiki: not found")

	// ErrAlreadyExists reports a slug collision.
	ErrAlreadyExists = errors.New("leafwiki: already exists")

	// ErrVersionConflict reports a stale optimistic-lock token.
	ErrVersionConflict = errors.New("leafwiki: version conflict")

	// ErrInvalidArgument reports a request the tree cannot honour.
	ErrInvalidArgument = errors.New("leafwiki: invalid argument")
)

// Actor is the identity behind a mutation. ID is a PocketBase record id, or
// "" for an anonymous caller on an instance that allows public writes.
type Actor struct {
	ID string
}

// systemActor is used when a mutation has no authenticated author.
func systemActor() Actor {
	return Actor{ID: systemUserID}
}

// Tree is the in-memory view of the Markdown tree plus the filesystem
// mutations behind it.
//
// LeafWiki keeps the same shape: the tree is reconstructed from disk on load,
// mutations write to disk and then reload. That is what makes the Markdown
// files the source of truth and the index a cache.
type Tree struct {
	mu    sync.RWMutex
	store *store
	root  *PageNode
	byID  map[string]*PageNode
	index *index

	// now is injectable so tests can produce deterministic timestamps.
	now func() time.Time
}

// newTree loads the Markdown tree under storageDir. index may be nil, in
// which case the wiki works without a search index (useful in unit tests).
func newTree(storageDir string, index *index) (*Tree, error) {
	st, err := newStore(storageDir)
	if err != nil {
		return nil, err
	}

	t := &Tree{
		store: st,
		byID:  map[string]*PageNode{},
		index: index,
		now:   time.Now,
	}
	if err := t.Load(); err != nil {
		return nil, err
	}
	return t, nil
}

// StorageDir returns the directory the Markdown tree lives in.
func (t *Tree) StorageDir() string {
	return t.store.storageDir
}

// Load (re)builds the tree from disk and reconciles the index.
func (t *Tree) Load() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.loadLocked()
}

func (t *Tree) loadLocked() error {
	root, err := t.store.reconstruct(t.now().UTC())
	if err != nil {
		return err
	}
	t.root = root
	t.reindexLocked()
	t.syncIndexLocked()
	return nil
}

// reindexLocked recomputes the derived node fields (parent pointers, paths,
// versions, positions) and rebuilds the id lookup.
func (t *Tree) reindexLocked() {
	t.byID = map[string]*PageNode{}

	var walk func(node *PageNode)
	walk = func(node *PageNode) {
		node.Path = node.CalculatePath()
		node.Version = ""
		if !node.Metadata.UpdatedAt.IsZero() {
			node.Version = node.Metadata.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		t.byID[node.ID] = node
		for position, child := range node.Children {
			child.Parent = node
			child.Position = position
			walk(child)
		}
	}
	if t.root != nil {
		t.root.Parent = nil
		walk(t.root)
	}
}

// syncIndexLocked pushes the current tree into the PocketBase index. A
// failure is logged, never returned: the index is a cache, and a wiki that
// refuses to serve a page because a search row could not be written would be
// worse than a stale index.
func (t *Tree) syncIndexLocked() {
	if t.index == nil {
		return
	}
	t.index.sync(t.pagesLocked())
}

// pagesLocked reads every page file and returns the pages with their derived
// metadata. It is the only place that reads the whole tree at once.
func (t *Tree) pagesLocked() []*Page {
	pages := make([]*Page, 0, len(t.byID))
	if t.root == nil {
		return pages
	}

	var walk func(node *PageNode)
	walk = func(node *PageNode) {
		if node.ID != rootID {
			if page, err := t.pageLocked(node); err == nil {
				pages = append(pages, page)
			}
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(t.root)
	return pages
}

// pageLocked builds the Page projection of a node.
func (t *Tree) pageLocked(node *PageNode) (*Page, error) {
	fm, body, err := t.store.readPage(node)
	if err != nil {
		return nil, err
	}
	return &Page{
		PageNode:   cloneNode(node, false),
		Content:    body,
		Tags:       ExtractTags(fm),
		Properties: ExtractProperties(fm),
	}, nil
}

// cloneNode returns a deep copy of the node and — optionally — its subtree.
// Copies are what the HTTP layer serialises, so a later mutation cannot
// change a response that is being written.
func cloneNode(node *PageNode, withChildren bool) *PageNode {
	if node == nil {
		return nil
	}
	clone := &PageNode{
		ID:       node.ID,
		Title:    node.Title,
		Slug:     node.Slug,
		Path:     node.Path,
		Version:  node.Version,
		Position: node.Position,
		Kind:     node.Kind,
		Pinned:   node.Pinned,
		Metadata: node.Metadata,
	}
	if withChildren && node.Children != nil {
		clone.Children = make([]*PageNode, 0, len(node.Children))
		for _, child := range node.Children {
			childClone := cloneNode(child, true)
			childClone.Parent = clone
			clone.Children = append(clone.Children, childClone)
		}
	}
	return clone
}

// Root returns a deep copy of the tree root, including its children.
func (t *Tree) Root() *PageNode {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return cloneNode(t.root, true)
}

// Node returns a deep copy of a node and its subtree. The empty string and
// "root" resolve to the tree root.
func (t *Tree) Node(id string) (*PageNode, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	node, err := t.nodeLocked(id)
	if err != nil {
		return nil, err
	}
	return cloneNode(node, true), nil
}

// nodeLocked resolves an id (or the root alias) to a live node.
func (t *Tree) nodeLocked(id string) (*PageNode, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" || trimmed == rootID {
		if t.root == nil {
			return nil, errors.Wrap(ErrNotFound, "tree is not loaded")
		}
		return t.root, nil
	}
	node, ok := t.byID[trimmed]
	if !ok {
		return nil, errors.Wrap(ErrNotFound, "page "+trimmed)
	}
	return node, nil
}

// Page returns the page projection (node + content + derived metadata).
func (t *Tree) Page(id string) (*Page, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	node, err := t.nodeLocked(id)
	if err != nil {
		return nil, err
	}
	return t.pageLocked(node)
}

// PageByPath resolves a route path ("guides/setup") to a page.
func (t *Tree) PageByPath(path string) (*Page, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	node, err := t.nodeByPathLocked(path)
	if err != nil {
		return nil, err
	}
	return t.pageLocked(node)
}

// nodeByPathLocked walks the tree along the given route path.
func (t *Tree) nodeByPathLocked(path string) (*PageNode, error) {
	segments := pathSegments(path)
	node := t.root
	if node == nil {
		return nil, errors.Wrap(ErrNotFound, "tree is not loaded")
	}
	for _, segment := range segments {
		next := findChildBySlug(node, segment)
		if next == nil {
			return nil, errors.Wrap(ErrNotFound, "path "+path)
		}
		node = next
	}
	return node, nil
}

// findChildBySlug resolves a child case-insensitively, the way the SPA's
// route matching does.
func findChildBySlug(parent *PageNode, slug string) *PageNode {
	if parent == nil {
		return nil
	}
	for _, child := range parent.Children {
		if strings.EqualFold(child.Slug, slug) {
			return child
		}
	}
	return nil
}

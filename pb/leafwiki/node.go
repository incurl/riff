package leafwiki

import (
	"time"
)

// NodeKind distinguishes a page (a Markdown file) from a section (a
// directory holding an index.md and further children).
type NodeKind string

const (
	// NodeKindPage is a Markdown page: <slug>.md on disk.
	NodeKindPage NodeKind = "page"

	// NodeKindSection is a directory with an index.md on disk.
	NodeKindSection NodeKind = "section"

	// rootID is the identifier of the virtual tree root. A page can never
	// have this id; the SPA sends "root" (and "" ) for "top level".
	rootID = "root"
)

// PageMetadata is the authorship/timestamp block the API exposes for every
// node. The values mirror the leafwiki_* frontmatter keys of the page file.
type PageMetadata struct {
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	CreatorID    string    `json:"creatorId"`
	LastAuthorID string    `json:"lastAuthorId"`

	// Creator and LastAuthor are display labels resolved from the `users`
	// collection. They are omitted when the id is unknown (for example a
	// page whose author record has since been deleted).
	Creator    *UserLabel `json:"creator,omitempty"`
	LastAuthor *UserLabel `json:"lastAuthor,omitempty"`
}

// UserLabel is the {id, username} pair the SPA renders next to a page.
type UserLabel struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// PageNode is a single node of the navigation tree.
//
// Parent is deliberately not serialised: the tree is sent top-down and a
// parent pointer would either duplicate the whole tree on every child or
// produce a cycle.
type PageNode struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Slug     string       `json:"slug"`
	Path     string       `json:"path"`
	Version  string       `json:"version"`
	Position int          `json:"position"`
	Kind     NodeKind     `json:"kind"`
	Children []*PageNode  `json:"children"`
	Pinned   bool         `json:"pinned,omitempty"`
	Metadata PageMetadata `json:"metadata"`

	Parent *PageNode `json:"-"`
}

// Page is a node plus the page body and the derived tags/properties. The
// embedded node is flattened on the wire, matching LeafWiki's Page DTO.
type Page struct {
	*PageNode
	Content    string            `json:"content"`
	Tags       []string          `json:"tags"`
	Properties map[string]string `json:"properties"`
}

// IsSection reports whether the node is a section.
func (n *PageNode) IsSection() bool {
	return n != nil && n.Kind == NodeKindSection
}

// HasChildren reports whether the node has children.
func (n *PageNode) HasChildren() bool {
	return n != nil && len(n.Children) > 0
}

// CalculatePath returns the slash-separated route path of the node. The root
// node's path is empty; a top-level page's path is its slug.
func (n *PageNode) CalculatePath() string {
	if n == nil || n.Parent == nil {
		if n == nil || n.Slug == "" || n.Slug == rootID {
			return ""
		}
		return n.Slug
	}
	parentPath := n.Parent.CalculatePath()
	if parentPath == "" {
		return n.Slug
	}
	return parentPath + "/" + n.Slug
}

// VersionToken returns the optimistic-lock token of the node: the RFC3339Nano
// representation of its last update. Two saves racing from the same version
// are rejected with 409. An empty version means "no expectation" and is always
// accepted (older clients).
//
// The token is stored in the Version field so responses never have to compute
// it; this method exists for callers that only have a node.
func (n *PageNode) VersionToken() string {
	return n.Version
}

// PermalinkTarget is the stable-id to route-path projection the SPA's
// permalink route resolves before navigating.
type PermalinkTarget struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Path string `json:"path"`
}

// PathSegment is one segment of a path-lookup response.
type PathSegment struct {
	Slug   string `json:"slug"`
	ID     string `json:"id,omitempty"`
	Exists bool   `json:"exists"`
}

// PathLookup is the response of GET /api/pages/lookup: it tells the SPA
// which prefix of the requested path already exists, so it can offer to
// create the remainder.
type PathLookup struct {
	Path      string        `json:"path"`
	Exists    bool          `json:"exists"`
	CanCreate bool          `json:"canCreate"`
	Segments  []PathSegment `json:"segments"`
}

package leafwiki

import (
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// createPageRequest is the POST /api/pages body.
type createPageRequest struct {
	Title    string  `json:"title"`
	Slug     string  `json:"slug"`
	ParentID *string `json:"parentId"`
	Kind     string  `json:"kind"`
}

// updatePageRequest is the PUT /api/pages/{id} body. The pointer fields are
// optional: the SPA's autosave sends only content, while the editor's save
// sends everything.
type updatePageRequest struct {
	Version    string             `json:"version"`
	Title      string             `json:"title"`
	Slug       string             `json:"slug"`
	Content    *string            `json:"content"`
	Tags       *[]string          `json:"tags"`
	Properties *map[string]string `json:"properties"`
}

// movePageRequest is the PUT /api/pages/{id}/move body.
type movePageRequest struct {
	Version  string  `json:"version"`
	ParentID *string `json:"parentId"`
	Position *int    `json:"position"`
}

// pinPageRequest is the PUT /api/pages/{id}/pin body.
type pinPageRequest struct {
	Version string `json:"version"`
	Pinned  bool   `json:"pinned"`
}

// sortPagesRequest is the PUT /api/pages/{id}/sort body.
type sortPagesRequest struct {
	OrderedIDs []string `json:"orderedIDs"`
}

// convertPageRequest is the POST /api/pages/{id}/convert body.
type convertPageRequest struct {
	TargetKind string `json:"targetKind"`
	Version    string `json:"version"`
}

// copyPageRequest is the POST /api/pages/copy/{id} body.
type copyPageRequest struct {
	TargetParentID *string `json:"targetParentId"`
	Title          string  `json:"title"`
	Slug           string  `json:"slug"`
}

// ensurePathRequest is the POST /api/pages/ensure body.
type ensurePathRequest struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// refactorRequest is the body of the refactor preview/apply pair.
type refactorRequest struct {
	Kind         string  `json:"kind"`
	Version      string  `json:"version"`
	Title        string  `json:"title"`
	Slug         string  `json:"slug"`
	Content      *string `json:"content"`
	ParentID     *string `json:"parentId"`
	Position     *int    `json:"position"`
	RewriteLinks bool    `json:"rewriteLinks"`
}

// parentIDOf normalises the "no parent" spellings the SPA uses.
func parentIDOf(parentID *string) string {
	if parentID == nil {
		return rootID
	}
	trimmed := strings.TrimSpace(*parentID)
	if trimmed == "" || trimmed == rootID {
		return rootID
	}
	return trimmed
}

// ptr returns a pointer to a string, for APIs that distinguish nil from "".
func ptr(value string) *string {
	return &value
}

// invalidArg wraps a message into ErrInvalidArgument.
func invalidArg(message string) error {
	return errors.Wrap(ErrInvalidArgument, message)
}

// treeHandler serves the navigation tree.
func (s *Server) treeHandler(e *core.RequestEvent) error {
	root := s.tree.Root()
	s.decorateNode(root)
	return e.JSON(http.StatusOK, root)
}

// pageHandler serves a page with its content and children.
func (s *Server) pageHandler(e *core.RequestEvent) error {
	page, err := s.tree.Page(pathValue(e, "id"))
	if err != nil {
		return s.fail(e, err)
	}
	s.decoratePage(page)
	return e.JSON(http.StatusOK, page)
}

// pageByPathHandler serves a page by its route path.
func (s *Server) pageByPathHandler(e *core.RequestEvent) error {
	page, err := s.tree.PageByPath(e.Request.URL.Query().Get("path"))
	if err != nil {
		return s.fail(e, err)
	}
	s.decoratePage(page)
	return e.JSON(http.StatusOK, page)
}

// permalinkHandler resolves a stable id to the page's current path.
func (s *Server) permalinkHandler(e *core.RequestEvent) error {
	target, err := s.tree.Permalink(pathValue(e, "id"))
	if err != nil {
		return s.fail(e, err)
	}
	return e.JSON(http.StatusOK, target)
}

// slugSuggestionHandler returns the slug a new page would get.
func (s *Server) slugSuggestionHandler(e *core.RequestEvent) error {
	query := e.Request.URL.Query()
	slug, err := s.tree.SuggestSlug(
		parentIDOf(ptr(query.Get("parentId"))),
		query.Get("title"),
		query.Get("currentId"),
	)
	if err != nil {
		return s.fail(e, err)
	}
	return e.JSON(http.StatusOK, map[string]string{"slug": slug})
}

// lookupPathHandler reports which segments of a path exist.
func (s *Server) lookupPathHandler(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, s.tree.LookupPath(e.Request.URL.Query().Get("path")))
}

// createPageHandler creates a page or section.
func (s *Server) createPageHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	var request createPageRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}

	kind := NodeKind(strings.TrimSpace(request.Kind))
	if kind == "" {
		kind = NodeKindPage
	}

	page, err := s.tree.CreatePage(actor, parentIDOf(request.ParentID), request.Title, request.Slug, kind)
	if err != nil {
		return s.fail(e, err)
	}
	s.decoratePage(page)
	return e.JSON(http.StatusCreated, page)
}

// updatePageHandler saves content and/or metadata.
func (s *Server) updatePageHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	var request updatePageRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}

	page, err := s.tree.UpdatePage(
		actor,
		pathValue(e, "id"),
		request.Version,
		request.Title,
		request.Slug,
		request.Content,
		request.Tags,
		request.Properties,
	)
	if err != nil {
		return s.fail(e, err)
	}
	s.decoratePage(page)
	return e.JSON(http.StatusOK, page)
}

// deletePageHandler removes a page or a whole subtree.
func (s *Server) deletePageHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	query := e.Request.URL.Query()
	recursive := query.Get("recursive") == "true"
	if err := s.tree.DeletePage(actor, pathValue(e, "id"), recursive, query.Get("version")); err != nil {
		return s.fail(e, err)
	}
	return e.JSON(http.StatusOK, map[string]string{"message": "deleted"})
}

// movePageHandler moves a page below another parent.
func (s *Server) movePageHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	var request movePageRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}

	page, err := s.tree.MovePage(actor, pathValue(e, "id"), request.Version, parentIDOf(request.ParentID), request.Position)
	if err != nil {
		return s.fail(e, err)
	}
	s.decoratePage(page)
	return e.JSON(http.StatusOK, page)
}

// pinPageHandler toggles the pinned flag.
func (s *Server) pinPageHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	var request pinPageRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}

	page, err := s.tree.PinPage(actor, pathValue(e, "id"), request.Version, request.Pinned)
	if err != nil {
		return s.fail(e, err)
	}
	s.decoratePage(page)
	return e.JSON(http.StatusOK, page)
}

// sortPagesHandler persists an explicit child order.
func (s *Server) sortPagesHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	var request sortPagesRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}
	if err := s.tree.SortChildren(actor, parentIDOf(ptr(pathValue(e, "id"))), request.OrderedIDs); err != nil {
		return s.fail(e, err)
	}
	return e.JSON(http.StatusOK, map[string]string{"message": "sorted"})
}

// convertPageHandler changes a page into a section or back.
func (s *Server) convertPageHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	var request convertPageRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}

	page, err := s.tree.ConvertPage(actor, pathValue(e, "id"), request.Version, NodeKind(strings.TrimSpace(request.TargetKind)))
	if err != nil {
		return s.fail(e, err)
	}
	s.decoratePage(page)
	return e.JSON(http.StatusOK, page)
}

// copyPageHandler duplicates a page or section.
func (s *Server) copyPageHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	var request copyPageRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}

	page, err := s.tree.CopyPage(actor, pathValue(e, "id"), parentIDOf(request.TargetParentID), request.Title, request.Slug)
	if err != nil {
		return s.fail(e, err)
	}
	s.decoratePage(page)
	return e.JSON(http.StatusCreated, page)
}

// ensurePathHandler creates every missing segment of a path.
func (s *Server) ensurePathHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	var request ensurePathRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}
	if strings.TrimSpace(request.Path) == "" {
		return s.fail(e, invalidArg("path must not be empty"))
	}

	page, err := s.tree.EnsurePath(actor, request.Path, request.Title)
	if err != nil {
		return s.fail(e, err)
	}
	s.decoratePage(page)
	return e.JSON(http.StatusOK, page)
}

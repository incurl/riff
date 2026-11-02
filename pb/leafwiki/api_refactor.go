package leafwiki

import (
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// refactorAffectedPage describes one page whose content links to the moved
// page.
type refactorAffectedPage struct {
	FromPageID   string   `json:"fromPageId"`
	FromTitle    string   `json:"fromTitle"`
	FromPath     string   `json:"fromPath"`
	MatchedPaths []string `json:"matchedPaths"`
	Warnings     []string `json:"warnings"`
}

// refactorCounts is the summary block of a preview.
type refactorCounts struct {
	AffectedPages int `json:"affectedPages"`
	MatchedLinks  int `json:"matchedLinks"`
}

// refactorPreview is the payload of the refactor preview endpoint.
type refactorPreview struct {
	Kind          string                 `json:"kind"`
	PageID        string                 `json:"pageId"`
	OldPath       string                 `json:"oldPath"`
	NewPath       string                 `json:"newPath"`
	AffectedPages []refactorAffectedPage `json:"affectedPages"`
	Counts        refactorCounts         `json:"counts"`
	Warnings      []string               `json:"warnings"`
}

// refactorTargetPath computes the path a rename or move would produce.
func (s *Server) refactorTargetPath(node *PageNode, request refactorRequest) (string, error) {
	switch strings.TrimSpace(request.Kind) {
	case "rename":
		slug := strings.TrimSpace(request.Slug)
		if slug == "" {
			slug = normaliseSlug(request.Title)
		}
		if err := validateSlug(slug); err != nil {
			return "", invalidArg(err.Error())
		}
		parentPath := ""
		if node.Parent != nil {
			parentPath = node.Parent.Path
		}
		if parentPath == "" {
			return slug, nil
		}
		return parentPath + "/" + slug, nil

	case "move":
		parent, err := s.tree.Node(parentIDOf(request.ParentID))
		if err != nil {
			return "", err
		}
		if parent.Path == "" {
			return node.Slug, nil
		}
		return parent.Path + "/" + node.Slug, nil

	default:
		return "", invalidArg("kind must be either 'rename' or 'move'")
	}
}

// affectedPagesFor finds the pages that link to oldPath (or, for a section, to
// anything below it).
func (s *Server) affectedPagesFor(node *PageNode, oldPath string) []refactorAffectedPage {
	affected := make([]refactorAffectedPage, 0, 4)
	if oldPath == "" {
		return affected
	}

	for _, entry := range s.index.allPages() {
		if entry.PageID == node.ID {
			continue
		}
		matched := make([]string, 0, 2)
		for _, target := range ExtractLinks(entry.Content) {
			if target.Path == "" {
				continue
			}
			if target.Path == oldPath || strings.HasPrefix(target.Path, oldPath+"/") {
				matched = append(matched, target.Path)
			}
		}
		if len(matched) == 0 {
			continue
		}
		affected = append(affected, refactorAffectedPage{
			FromPageID:   entry.PageID,
			FromTitle:    entry.Title,
			FromPath:     entry.Path,
			MatchedPaths: matched,
			Warnings:     []string{},
		})
	}
	return affected
}

// refactorPreviewHandler reports what a rename or move would break.
func (s *Server) refactorPreviewHandler(e *core.RequestEvent) error {
	var request refactorRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}

	node, err := s.tree.Node(pathValue(e, "id"))
	if err != nil {
		return s.fail(e, err)
	}
	if node.ID == rootID {
		return s.fail(e, invalidArg("the tree root cannot be refactored"))
	}

	newPath, targetErr := s.refactorTargetPath(node, request)
	if targetErr != nil {
		return s.fail(e, targetErr)
	}

	affected := s.affectedPagesFor(node, node.Path)
	matchedLinks := 0
	for _, page := range affected {
		matchedLinks += len(page.MatchedPaths)
	}

	return e.JSON(http.StatusOK, refactorPreview{
		Kind:          strings.TrimSpace(request.Kind),
		PageID:        node.ID,
		OldPath:       node.Path,
		NewPath:       newPath,
		AffectedPages: affected,
		Counts:        refactorCounts{AffectedPages: len(affected), MatchedLinks: matchedLinks},
		Warnings:      []string{},
	})
}

// refactorApplyHandler performs a rename or move, rewriting inbound links when
// the client asked for it.
func (s *Server) refactorApplyHandler(e *core.RequestEvent) error {
	actor, authErr := s.requireWriter(e)
	if authErr != nil {
		return authErr
	}

	var request refactorRequest
	if err := decodeBody(e, &request); err != nil {
		return s.fail(e, err)
	}

	id := pathValue(e, "id")
	node, err := s.tree.Node(id)
	if err != nil {
		return s.fail(e, err)
	}
	if node.ID == rootID {
		return s.fail(e, invalidArg("the tree root cannot be refactored"))
	}

	newPath, targetErr := s.refactorTargetPath(node, request)
	if targetErr != nil {
		return s.fail(e, targetErr)
	}

	affected := s.affectedPagesFor(node, node.Path)

	var page *Page
	switch strings.TrimSpace(request.Kind) {
	case "rename":
		page, err = s.tree.UpdatePage(actor, id, request.Version, request.Title, request.Slug, request.Content, nil, nil)
	case "move":
		page, err = s.tree.MovePage(actor, id, request.Version, parentIDOf(request.ParentID), request.Position)
	default:
		err = invalidArg("kind must be either 'rename' or 'move'")
	}
	if err != nil {
		return s.fail(e, err)
	}

	if request.RewriteLinks {
		for _, affectedPage := range affected {
			if rewriteErr := s.rewritePageLinks(actor, affectedPage.FromPageID, node.Path, newPath); rewriteErr != nil {
				return s.fail(e, rewriteErr)
			}
		}
	}

	s.decoratePage(page)
	return e.JSON(http.StatusOK, page)
}

// rewritePageLinks replaces inbound link destinations in a single page.
func (s *Server) rewritePageLinks(actor Actor, pageID, oldPath, newPath string) error {
	page, err := s.tree.Page(pageID)
	if err != nil {
		// The page may have been part of a deleted subtree; there is nothing
		// left to rewrite, and failing the rename the user already made
		// would be worse than leaving one stale link.
		return nil //nolint:nilerr // see above
	}

	rewritten := rewriteLinkDestinations(page.Content, oldPath, newPath)
	if rewritten == page.Content {
		return nil
	}
	_, err = s.tree.UpdatePage(actor, pageID, "", "", "", &rewritten, nil, nil)
	return err
}

// rewriteLinkDestinations rewrites `/oldPath` and `oldPath` link destinations
// inside a Markdown body. Only whole-segment matches are rewritten, so renaming
// `guides` does not touch `guides-old`.
func rewriteLinkDestinations(content, oldPath, newPath string) string {
	if oldPath == "" || oldPath == newPath {
		return content
	}
	replacements := []struct{ from, to string }{
		{"](/" + oldPath + ")", "](/" + newPath + ")"},
		{"](/" + oldPath + "#", "](/" + newPath + "#"},
		{"](/" + oldPath + "?", "](/" + newPath + "?"},
		{"(" + oldPath + ")", "(" + newPath + ")"},
		{"(" + oldPath + "#", "(" + newPath + "#"},
		{"(" + oldPath + "?", "(" + newPath + "?"},
		{"](/" + oldPath + "/", "](/" + newPath + "/"},
		{"(" + oldPath + "/", "(" + newPath + "/"},
	}
	rewritten := content
	for _, replacement := range replacements {
		rewritten = strings.ReplaceAll(rewritten, replacement.from, replacement.to)
	}
	return rewritten
}

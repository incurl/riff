package leafwiki

import (
	"net/http"
	"os"

	"github.com/pocketbase/pocketbase/core"
)

// assetListResponse is the payload of GET /api/pages/{id}/assets.
type assetListResponse struct {
	Files []string `json:"files"`
}

// assetWriteResponse is the payload of an upload or rename.
type assetWriteResponse struct {
	File string `json:"file"`
}

// renameAssetRequest is the PUT /api/pages/{id}/assets/rename body.
type renameAssetRequest struct {
	OldFilename string `json:"old_filename"`
	NewFilename string `json:"new_filename"`
}

// requirePage verifies that a page exists before its attachments are touched,
// so an upload against a stale id answers 404 instead of silently creating an
// orphan asset directory.
func (s *Server) requirePage(e *core.RequestEvent) (string, error) {
	page, err := s.tree.Page(pathValue(e, "id"))
	if err != nil {
		return "", err
	}
	return page.ID, nil
}

// listAssetsHandler lists a page's attachments.
func (s *Server) listAssetsHandler(e *core.RequestEvent) error {
	pageID, err := s.requirePage(e)
	if err != nil {
		return s.fail(e, err)
	}
	files, listErr := s.assets.List(pageID)
	if listErr != nil {
		return s.fail(e, listErr)
	}
	return e.JSON(http.StatusOK, assetListResponse{Files: files})
}

// uploadAssetHandler stores a multipart upload for a page.
func (s *Server) uploadAssetHandler(e *core.RequestEvent) error {
	if _, authErr := s.requireWriter(e); authErr != nil {
		return authErr
	}
	pageID, err := s.requirePage(e)
	if err != nil {
		return s.fail(e, err)
	}

	file, header, formErr := e.Request.FormFile("file")
	if formErr != nil {
		return s.fail(e, invalidArg("a multipart 'file' field is required"))
	}
	defer func() { _ = file.Close() }()

	url, saveErr := s.assets.Save(pageID, header.Filename, file)
	if saveErr != nil {
		return s.fail(e, saveErr)
	}
	return e.JSON(http.StatusCreated, assetWriteResponse{File: url})
}

// renameAssetHandler renames an attachment.
func (s *Server) renameAssetHandler(e *core.RequestEvent) error {
	if _, authErr := s.requireWriter(e); authErr != nil {
		return authErr
	}
	pageID, err := s.requirePage(e)
	if err != nil {
		return s.fail(e, err)
	}

	var request renameAssetRequest
	if decodeErr := decodeBody(e, &request); decodeErr != nil {
		return s.fail(e, decodeErr)
	}
	url, renameErr := s.assets.Rename(pageID, request.OldFilename, request.NewFilename)
	if renameErr != nil {
		return s.fail(e, renameErr)
	}
	return e.JSON(http.StatusOK, assetWriteResponse{File: url})
}

// deleteAssetHandler removes an attachment.
func (s *Server) deleteAssetHandler(e *core.RequestEvent) error {
	if _, authErr := s.requireWriter(e); authErr != nil {
		return authErr
	}
	pageID, err := s.requirePage(e)
	if err != nil {
		return s.fail(e, err)
	}
	if deleteErr := s.assets.Delete(pageID, pathValue(e, "filename")); deleteErr != nil {
		return s.fail(e, deleteErr)
	}
	return e.JSON(http.StatusOK, map[string]string{"message": "deleted"})
}

// serveAssetHandler serves an attachment file.
//
// The blob is served from the page's own directory through fs.FS, so a crafted
// page id or filename cannot escape it; Assets.Path validates both before the
// directory is opened.
func (s *Server) serveAssetHandler(e *core.RequestEvent) error {
	pageID := pathValue(e, "pageId")
	filename := pathValue(e, "filename")

	target, err := s.assets.Path(pageID, filename)
	if err != nil {
		return e.NotFoundError("asset not found", nil)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		return e.NotFoundError("asset not found", nil)
	}

	http.ServeFile(e.Response, e.Request, target)
	return nil
}

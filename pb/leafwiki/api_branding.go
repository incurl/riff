package leafwiki

import (
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// The wiki's branding endpoints, at upstream LeafWiki's paths and shapes.
//
// Upstream's contract (`internal/wiki/branding/routes.go`):
//
//	GET    /api/branding            public   → the configuration
//	PUT    /api/branding            admin    → {siteName} → the configuration
//	POST   /api/branding/logo       admin    → {path, branding}
//	POST   /api/branding/favicon    admin    → {path, branding}
//	DELETE /api/branding/logo       admin    → the configuration
//	DELETE /api/branding/favicon    admin    → the configuration
//	GET    /branding/{filename}     public   → the image bytes
//	GET    /favicon.ico             public   → the current favicon
//
// **EVERY ONE OF THEM MUST ANSWER WITH A JSON BODY.** The SPA's
// `updateBranding` reads `updated.faviconFile` off the response, and
// `fetchWithAuth` resolves to `null` for an `ok` response that carries no JSON
// — so a 200 with an empty body (or the SPA shell, from an unrouted PUT) is the
// "Cannot read properties of null (reading 'faviconFile')" an operator sees
// instead of an error. The refusals this port used to send were 501s, which do
// throw properly — but the save they refused simply never happened, and the
// site name never applied.

// brandingResponse is the payload of every branding endpoint.
//
// The field names are upstream's `BrandingConfigResponse`, and
// `brandingConstraints` is NESTED because the SPA reads
// `brandingConstraints.logoExts`: a flat response leaves every limit
// `undefined` and the uploader cannot validate a file before sending it.
type brandingResponse struct {
	SiteName            string              `json:"siteName"`
	LogoFile            string              `json:"logoFile"`
	FaviconFile         string              `json:"faviconFile"`
	BrandingConstraints brandingConstraints `json:"brandingConstraints"`
}

// brandingResponseFor renders the configuration the SPA consumes.
func brandingResponseFor(cfg brandingConfig) brandingResponse {
	return brandingResponse{
		SiteName:            cfg.SiteName,
		LogoFile:            cfg.LogoFile,
		FaviconFile:         cfg.FaviconFile,
		BrandingConstraints: brandingLimits(),
	}
}

// brandingHandler serves GET /api/branding.
//
// It is PUBLIC: the SPA loads it on every mount, before it knows whether the
// reader can sign in, and the site name and the favicon are shown to guests.
func (s *Server) brandingHandler(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, brandingResponseFor(s.branding.snapshot()))
}

// brandingUpdateHandler serves PUT /api/branding.
//
// The body is `{siteName}` — partial by design, because the page sends only
// the field the operator edited (upstream's `UpdateBrandingInput`). A body that
// omits it keeps the stored name.
func (s *Server) brandingUpdateHandler(e *core.RequestEvent) error {
	if _, err := s.requireAdmin(e); err != nil {
		return err
	}

	var body struct {
		SiteName *string `json:"siteName"`
	}
	if err := decodeBody(e, &body); err != nil {
		return e.BadRequestError("invalid payload", nil)
	}

	if body.SiteName == nil {
		return e.JSON(http.StatusOK, brandingResponseFor(s.branding.snapshot()))
	}

	if _, err := s.branding.UpdateSiteName(*body.SiteName); err != nil {
		return s.fail(e, err)
	}

	return e.JSON(http.StatusOK, brandingResponseFor(s.branding.snapshot()))
}

// brandingUploadHandler serves POST /api/branding/{logo,favicon}.
//
// The multipart part is `file`, which is what the SPA posts. The upload is
// validated against the SAME limits `GET /api/branding` advertises, so the
// client-side check and the server-side one cannot disagree.
func (s *Server) brandingUploadHandler(kind brandingImageKind) func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if _, err := s.requireAdmin(e); err != nil {
			return err
		}

		header, err := brandingUploadPart(e)
		if err != nil {
			return e.BadRequestError("a file is required", nil)
		}

		file, err := header.Open()
		if err != nil {
			return e.BadRequestError("the uploaded file could not be read", nil)
		}
		defer func() { _ = file.Close() }()

		cfg, err := s.branding.uploadImage(kind, header.Filename, file)
		if err != nil {
			if errors.Is(err, ErrBrandingImageTooLarge) {
				return e.Error(http.StatusRequestEntityTooLarge, "file too large", nil)
			}
			return s.fail(e, err)
		}

		name := cfg.LogoFile
		if kind == brandingFavicon {
			name = cfg.FaviconFile
		}

		// Upstream's shape: the stored path AND the whole configuration, so
		// the SPA updates its store from one answer.
		return e.JSON(http.StatusOK, map[string]any{
			"path":     s.opts.Prefix + "/" + brandingAssetsDirname + "/" + name,
			"branding": brandingResponseFor(cfg),
		})
	}
}

// brandingDeleteHandler serves DELETE /api/branding/{logo,favicon}.
func (s *Server) brandingDeleteHandler(kind brandingImageKind) func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if _, err := s.requireAdmin(e); err != nil {
			return err
		}

		cfg, err := s.branding.DeleteImage(kind)
		if err != nil {
			return s.fail(e, err)
		}

		return e.JSON(http.StatusOK, brandingResponseFor(cfg))
	}
}

// brandingUploadPart returns the uploaded file.
//
// The SPA posts it as `file`; falling back to the first file part under any
// name keeps a hand-written curl working, the same way the avatar uploader
// does.
func brandingUploadPart(e *core.RequestEvent) (*multipart.FileHeader, error) {
	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, maxBrandingImageBytes+multipartOverheadBytes)

	if _, header, err := e.Request.FormFile("file"); err == nil {
		return header, nil
	}

	if err := e.Request.ParseMultipartForm(maxBrandingImageBytes + multipartOverheadBytes); err == nil {
		for _, headers := range e.Request.MultipartForm.File {
			if len(headers) > 0 {
				return headers[0], nil
			}
		}
	}

	return nil, errors.New("leafwiki: no branding file part")
}

// brandingAssetHandler serves GET <prefix>/branding/{filename}.
//
// Only the two names the configuration currently holds are served; every other
// request is a 404, so no path a caller invents can reach the filesystem.
//
// The bytes go through this route rather than being linked at a file-system
// URL, for the same reason the avatars do: every URL the SPA builds is
// relative to the wiki's mount point, and a reverse proxy that owns the
// prefix decides what a root-relative URL means.
func (s *Server) brandingAssetHandler(e *core.RequestEvent) error {
	path, ok := s.branding.AssetPath(pathValue(e, "filename"))
	if !ok {
		return e.NotFoundError("image not found", nil)
	}

	return s.serveBrandingImage(e, path)
}

// faviconICOHandler serves GET <prefix>/favicon.ico.
//
// Browsers ask for `/favicon.ico` on their own, before the SPA rewrites the
// `<link rel="icon">`. It answers with the uploaded favicon when there is one
// and 404s otherwise, which is what keeps the shell's `/favicon.svg` in place
// until an administrator uploads something.
func (s *Server) faviconICOHandler(e *core.RequestEvent) error {
	path, ok := s.branding.AssetPath(s.branding.faviconFile())
	if !ok {
		return e.NotFoundError("favicon not found", nil)
	}

	return s.serveBrandingImage(e, path)
}

// serveBrandingImage writes an image with its content type.
//
// The SPA appends `?v=<timestamp>` after every upload or delete
// (`stores/branding.ts`), so the URL changes whenever the bytes do and the
// response can be cached hard.
func (*Server) serveBrandingImage(e *core.RequestEvent, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return e.NotFoundError("image not found", nil)
	}

	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	e.Response.Header().Set("Content-Type", contentType)
	e.Response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	return e.Blob(http.StatusOK, contentType, raw)
}

// requireAdmin authorises a branding change.
//
// Upstream gates these routes with `RequireAdmin`, and LeafWiki's `admin` role
// is a PocketBase superuser in this port (see `userFromRecord` in auth.go). A
// wiki EDITOR may write pages and may not rename the wiki: the name is the
// site's identity and it is shown to every reader.
//
// It is deliberately not `requireWriter`: that one lets
// `RIFF_WIKI_PUBLIC_WRITE` through for an anonymous caller, which is right for
// pages and wrong for the site's own identity.
func (s *Server) requireAdmin(e *core.RequestEvent) (*core.Record, error) {
	record := s.currentUser(e)
	if record == nil {
		return nil, e.UnauthorizedError("authentication is required to change the wiki's branding", nil)
	}
	if record.Collection().Name != core.CollectionNameSuperusers {
		return nil, e.ForbiddenError("only an administrator may change the wiki's branding", nil)
	}
	if !verifyCSRF(e) {
		return nil, e.ForbiddenError("missing or invalid CSRF token", nil)
	}
	return record, nil
}

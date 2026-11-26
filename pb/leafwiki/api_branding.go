package leafwiki

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// The wiki's branding endpoint.
//
// Upstream LeafWiki lets an administrator upload a logo and a favicon and
// rename the site; the SPA reads the result on boot from
// `GET /api/branding` and applies the favicon itself.
//
// **THIS PORT HAS NO BRANDING STORE**, and the SPA's failure mode is not
// a crash but a silent one: `loadBranding` uses a RAW `fetch`, checks
// `ok`, and then `await response.json()`. The unrouted path is answered
// with the SPA shell and a 200, so `ok` is true and the JSON parse is
// what throws — the branding store keeps its defaults and logs nothing
// visible. The site name in the shell would then disagree with the name
// the SPA renders.
//
// The read is therefore served: the port reports its OWN identity, which
// is what the shell substitutes (`{{__SITE_NAME__}}`), so the two agree.
// The WRITE endpoints are not implemented, and they refuse with a clear
// 501 rather than answering 200 to something they did not do — a save
// that appears to succeed and is forgotten is the worse failure.

// brandingConstraints is the upload limits block.
//
// The SPA reads it NESTED (`brandingConstraints.logoExts`), not at the top
// level, so a flat response leaves every constraint `undefined` and the
// admin page renders an uploader with no stated limits.
type brandingConstraints struct {
	// LogoExts are the accepted logo file extensions.
	LogoExts []string `json:"logoExts"`
	// MaxLogoSize is the logo upload cap, in bytes.
	MaxLogoSize int64 `json:"maxLogoSize"`
	// FaviconExts are the accepted favicon file extensions.
	FaviconExts []string `json:"faviconExts"`
	// MaxFaviconSize is the favicon upload cap, in bytes.
	MaxFaviconSize int64 `json:"maxFaviconSize"`
}

// brandingResponse is the payload of GET /api/branding.
type brandingResponse struct {
	SiteName            string              `json:"siteName"`
	LogoFile            string              `json:"logoFile"`
	FaviconFile         string              `json:"faviconFile"`
	LogoVersion         int                 `json:"logoVersion"`
	FaviconVersion      int                 `json:"faviconVersion"`
	BrandingConstraints brandingConstraints `json:"brandingConstraints"`
}

// brandingSiteName is the name the wiki advertises.
//
// It is the same literal the SPA shell substitutes for
// `{{__SITE_NAME__}}` (api_spa.go), so the document title and the name
// the SPA renders cannot drift.
const brandingSiteName = "Wiki"

// brandingHandler serves GET /api/branding.
func (*Server) brandingHandler(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, brandingResponse{
		SiteName:    brandingSiteName,
		LogoFile:    "",
		FaviconFile: "",
		// A version of zero tells the SPA there is nothing to
		// cache-bust, and it falls back to its own default favicon.
		LogoVersion:    0,
		FaviconVersion: 0,
		BrandingConstraints: brandingConstraints{
			LogoExts:       []string{},
			MaxLogoSize:    0,
			FaviconExts:    []string{},
			MaxFaviconSize: 0,
		},
	})
}

// brandingWriteHandler refuses a branding change.
//
// It answers 501 Not Implemented rather than 404 or 200: the endpoint
// exists in LeafWiki's contract, this port does not implement it, and a
// 200 would tell the admin application the upload succeeded.
func (*Server) brandingWriteHandler(e *core.RequestEvent) error {
	return e.Error(http.StatusNotImplemented, "branding is not configurable in this port", nil)
}

package leafwiki

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// reservedSlugs are route segments the SPA owns. A page may not take one,
// otherwise /w/<slug> would shadow an application route.
var reservedSlugs = map[string]bool{
	"e":        true,
	"edit":     true,
	"api":      true,
	"assets":   true,
	"branding": true,
	"index":    true,
	"users":    true,
	"user":     true,
	"login":    true,
	"history":  true,
	"settings": true,
}

// defaultSlug is used when a title yields no usable characters.
const defaultSlug = "page"

var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`)

// errInvalidSlug is returned by validateSlug for a slug that cannot be a
// route segment.
var errInvalidSlug = errors.New("invalid slug")

// validateSlug reports whether slug is usable as a route segment: non-empty,
// not reserved, letters/digits/hyphens only, no leading or trailing hyphen.
func validateSlug(slug string) error {
	if slug == "" {
		return errors.Wrap(errInvalidSlug, "slug must not be empty")
	}
	if reservedSlugs[strings.ToLower(slug)] {
		return errors.Wrapf(errInvalidSlug, "slug %q is reserved", slug)
	}
	if !slugPattern.MatchString(slug) {
		return errors.Wrap(errInvalidSlug, "slug must contain only letters, numbers and hyphens")
	}
	if strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return errors.Wrap(errInvalidSlug, "slug must not start or end with a hyphen")
	}
	return nil
}

// normaliseSlug turns a desired value into a valid slug, appending "-N" until
// the result passes validateSlug.
func normaliseSlug(desired string) string {
	slug := slugifyTitle(desired)
	if slug == "" {
		return ""
	}
	original := slug
	for i := 1; validateSlug(slug) != nil; i++ {
		slug = original + "-" + strconv.Itoa(i)
	}
	return slug
}

// uniqueChildSlug returns a slug that is valid and does not collide with any
// sibling of parent. currentID is excluded from the collision check so a
// page can be re-saved under its own slug.
func uniqueChildSlug(parent *PageNode, currentID, desired string) string {
	slug := normaliseSlug(desired)
	if slug == "" {
		slug = defaultSlug
	}
	original := slug

	for i := 1; ; i++ {
		conflict := false
		if parent != nil {
			for _, child := range parent.Children {
				if child == nil {
					continue
				}
				if strings.EqualFold(child.Slug, slug) && child.ID != currentID {
					conflict = true
					break
				}
			}
		}
		if !conflict && validateSlug(slug) == nil {
			return slug
		}
		slug = original + "-" + strconv.Itoa(i)
	}
}

// normalisePathSlugs normalises every segment of a slash-separated path and
// validates the result. An empty segment is skipped.
func normalisePathSlugs(path string) (string, error) {
	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(path, "/") {
		if strings.TrimSpace(segment) == "" {
			continue
		}
		slug := normaliseSlug(segment)
		if slug == "" {
			return "", errors.Wrapf(errInvalidSlug, "segment %q yields an empty slug", segment)
		}
		segments = append(segments, slug)
	}
	return strings.Join(segments, "/"), nil
}

// pathSegments splits a route path into its non-empty segments.
func pathSegments(path string) []string {
	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(path, "/") {
		if trimmed := strings.TrimSpace(segment); trimmed != "" {
			segments = append(segments, trimmed)
		}
	}
	return segments
}

// safeFilename rejects a filename that could escape its directory.
func safeFilename(name string) error {
	if name == "" {
		return errors.New("filename must not be empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return errors.New("filename must not contain path separators")
	}
	if name == "." || name == ".." {
		return errors.New("filename must not be a dot component")
	}
	return nil
}

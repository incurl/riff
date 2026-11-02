package leafwiki

import (
	"bytes"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	yaml "go.yaml.in/yaml/v3"
)

// Frontmatter is the managed metadata block of a page file.
//
// The key names are LeafWiki's: a data directory written by upstream
// LeafWiki loads here unchanged, and a directory written here loads in
// upstream LeafWiki. Extra (unmanaged) keys — `tags`, `owner`, … — are
// preserved verbatim in Extra and re-emitted in a deterministic order.
type Frontmatter struct {
	ID           string
	Title        string
	CreatedAt    string
	UpdatedAt    string
	CreatorID    string
	LastAuthorID string
	Pinned       bool
	Extra        map[string]any
}

const (
	frontmatterDelimiter = "---"

	keyID           = "leafwiki_id"
	keyTitle        = "leafwiki_title"
	keyCreatedAt    = "leafwiki_created_at"
	keyUpdatedAt    = "leafwiki_updated_at"
	keyCreatorID    = "leafwiki_creator_id"
	keyLastAuthorID = "leafwiki_last_author_id"
	keyPinned       = "leafwiki_pinned"
	keyTags         = "tags"

	// maxExcerptLength is the size of the excerpt stored in the index and
	// shown in search/tag results.
	maxExcerptLength = 240
)

// managedFrontmatterKeys are the keys this module owns. They are never
// exposed as page properties, and they are rewritten (not duplicated) on
// save.
var managedFrontmatterKeys = map[string]struct{}{
	keyID:           {},
	keyTitle:        {},
	keyCreatedAt:    {},
	keyUpdatedAt:    {},
	keyCreatorID:    {},
	keyLastAuthorID: {},
	keyPinned:       {},
	"title":         {},
}

// systemPropertyKeys are frontmatter keys that never surface as page
// properties: `tags` has its own facet UI.
var systemPropertyKeys = map[string]struct{}{
	keyTags: {},
}

// ParseMarkdown splits a page file into its frontmatter and body.
//
// A file without a leading `---` line has no frontmatter: the whole input is
// the body. A malformed YAML block is reported as an error and the whole
// input is returned as the body, so a broken file is never silently
// truncated.
func ParseMarkdown(raw string) (Frontmatter, string, error) {
	fm := Frontmatter{Extra: map[string]any{}}

	trimmed := strings.TrimLeftFunc(raw, func(r rune) bool { return r == '\uFEFF' })
	if !hasFrontmatterOpen(trimmed) {
		return fm, raw, nil
	}

	rest := trimmed[len(frontmatterDelimiter):]
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")

	bounds := findFrontmatterClose(rest)
	if bounds.start < 0 {
		// An unterminated block is not frontmatter; treat the file as body.
		return fm, raw, nil
	}

	yamlPart := rest[:bounds.start]
	body := rest[bounds.end:]

	decoded := map[string]any{}
	if err := yaml.Unmarshal([]byte(yamlPart), &decoded); err != nil {
		return Frontmatter{Extra: map[string]any{}}, raw, errors.New("leafwiki: malformed frontmatter: " + err.Error())
	}

	for key, value := range decoded {
		switch key {
		case keyID:
			fm.ID = stringValue(value)
		case keyTitle:
			fm.Title = stringValue(value)
		case keyCreatedAt:
			fm.CreatedAt = stringValue(value)
		case keyUpdatedAt:
			fm.UpdatedAt = stringValue(value)
		case keyCreatorID:
			fm.CreatorID = stringValue(value)
		case keyLastAuthorID:
			fm.LastAuthorID = stringValue(value)
		case keyPinned:
			fm.Pinned = boolValue(value)
		case "title":
			// LeafWiki accepts a plain `title` key as a title fallback for
			// files written by other tools; keep it in Extra so it survives
			// a rewrite, but use it when leafwiki_title is absent.
			fm.Extra[key] = value
			if fm.Title == "" {
				fm.Title = stringValue(value)
			}
		default:
			fm.Extra[key] = value
		}
	}

	return fm, body, nil
}

// frontmatterBounds is the byte range of a frontmatter block within the
// input that follows the opening delimiter.
type frontmatterBounds struct {
	start int
	end   int
}

// findFrontmatterClose locates the closing `---` line of a frontmatter
// block. It returns start == -1 when there is no closing delimiter.
func findFrontmatterClose(s string) frontmatterBounds {
	offset := 0
	for offset <= len(s) {
		lineEnd := strings.IndexByte(s[offset:], '\n')

		var line string
		if lineEnd < 0 {
			line = s[offset:]
		} else {
			line = s[offset : offset+lineEnd]
		}

		trimmedLine := strings.TrimRight(line, "\r")
		if trimmedLine == frontmatterDelimiter || trimmedLine == "..." {
			end := len(s)
			if lineEnd >= 0 {
				end = offset + lineEnd + 1
			}
			return frontmatterBounds{start: offset, end: end}
		}

		if lineEnd < 0 {
			break
		}
		offset += lineEnd + 1
	}
	return frontmatterBounds{start: -1, end: -1}
}

func hasFrontmatterOpen(s string) bool {
	if !strings.HasPrefix(s, frontmatterDelimiter) {
		return false
	}
	// The delimiter must be the whole first line: `---foo` is not a fence.
	rest := s[len(frontmatterDelimiter):]
	return rest == "" || strings.HasPrefix(rest, "\n") || strings.HasPrefix(rest, "\r\n")
}

// BuildMarkdown assembles a page file from frontmatter and body. Extra keys
// are emitted first (sorted), then the managed block, so a rewrite does not
// churn the user's own metadata ordering.
func BuildMarkdown(fm Frontmatter, body string) (string, error) {
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	extraKeys := make([]string, 0, len(fm.Extra))
	for key := range fm.Extra {
		if _, managed := managedFrontmatterKeys[key]; managed {
			continue
		}
		extraKeys = append(extraKeys, key)
	}
	slices.Sort(extraKeys)

	for _, key := range extraKeys {
		valueNode, err := yamlNodeFor(fm.Extra[key])
		if err != nil {
			return "", err
		}
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			valueNode,
		)
	}

	appendScalar := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
		)
	}
	appendScalar(keyID, strings.TrimSpace(fm.ID))
	appendScalar(keyTitle, strings.TrimSpace(fm.Title))
	appendScalar(keyCreatedAt, strings.TrimSpace(fm.CreatedAt))
	appendScalar(keyUpdatedAt, strings.TrimSpace(fm.UpdatedAt))
	appendScalar(keyCreatorID, strings.TrimSpace(fm.CreatorID))
	appendScalar(keyLastAuthorID, strings.TrimSpace(fm.LastAuthorID))
	if fm.Pinned {
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: keyPinned},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
		)
	}

	if len(mapping.Content) == 0 {
		return body, nil
	}

	encoded, err := yaml.Marshal(mapping)
	if err != nil {
		return "", errors.New("leafwiki: could not encode frontmatter: " + err.Error())
	}

	var out bytes.Buffer
	out.WriteString(frontmatterDelimiter)
	out.WriteByte('\n')
	out.Write(encoded)
	out.WriteString(frontmatterDelimiter)
	out.WriteByte('\n')
	out.WriteString(body)
	return out.String(), nil
}

// yamlNodeFor converts a decoded YAML value back into a node. Composite
// values are round-tripped through Marshal/Unmarshal because building them
// by hand duplicates the encoder's type dispatch for no gain.
func yamlNodeFor(value any) (*yaml.Node, error) {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return nil, errors.New("leafwiki: could not encode frontmatter value: " + err.Error())
	}
	var node yaml.Node
	if err := yaml.Unmarshal(encoded, &node); err != nil {
		return nil, errors.New("leafwiki: could not decode frontmatter value: " + err.Error())
	}
	if len(node.Content) == 0 {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	return node.Content[0], nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case nil:
		return ""
	default:
		encoded, err := yaml.Marshal(typed)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(encoded))
	}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

var headingPattern = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)

// TitleFromBody returns the first level-one heading of a Markdown body, or
// "" when the body has none.
func TitleFromBody(body string) string {
	if match := headingPattern.FindStringSubmatch(body); match != nil {
		return strings.TrimSpace(match[1])
	}
	return ""
}

// ExtractTags returns the normalised tag list of a page: the `tags`
// frontmatter key, lower-cased, trimmed, de-duplicated, order-preserving.
func ExtractTags(fm Frontmatter) []string {
	value, ok := fm.Extra[keyTags]
	if !ok {
		for key, candidate := range fm.Extra {
			if strings.EqualFold(strings.TrimSpace(key), keyTags) {
				value, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return []string{}
	}

	items, ok := value.([]any)
	if !ok {
		list, listOK := value.([]string)
		if !listOK {
			return []string{}
		}
		items = make([]any, len(list))
		for i, item := range list {
			items[i] = item
		}
	}

	seen := make(map[string]struct{}, len(items))
	tags := make([]string, 0, len(items))
	for _, item := range items {
		tag, ok := item.(string)
		if !ok {
			continue
		}
		normalised := strings.ToLower(strings.TrimSpace(tag))
		if normalised == "" {
			continue
		}
		if _, duplicate := seen[normalised]; duplicate {
			continue
		}
		seen[normalised] = struct{}{}
		tags = append(tags, normalised)
	}
	return tags
}

// ExtractProperties returns the scalar frontmatter keys as page properties.
// System keys (leafwiki_*, title, tags) are skipped; nested maps are flattened
// with dot notation; lists and nulls are ignored.
func ExtractProperties(fm Frontmatter) map[string]string {
	properties := map[string]string{}
	for key, value := range fm.Extra {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		if _, managed := managedFrontmatterKeys[trimmedKey]; managed {
			continue
		}
		if _, system := systemPropertyKeys[strings.ToLower(trimmedKey)]; system {
			continue
		}
		flattenProperty(trimmedKey, value, properties)
	}
	return properties
}

func flattenProperty(key string, value any, out map[string]string) {
	switch typed := value.(type) {
	case nil:
		return
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			out[key] = trimmed
		}
	case bool:
		if typed {
			out[key] = "true"
		} else {
			out[key] = "false"
		}
	case int, int64, float64, uint64:
		out[key] = stringValue(typed)
	case time.Time:
		out[key] = typed.UTC().Format(time.RFC3339)
	case map[string]any:
		for nestedKey, nestedValue := range typed {
			flattenProperty(key+"."+strings.TrimSpace(nestedKey), nestedValue, out)
		}
	case map[any]any:
		for nestedKey, nestedValue := range typed {
			flattenProperty(key+"."+strings.TrimSpace(stringValue(nestedKey)), nestedValue, out)
		}
	default:
		// Lists and other composites are not page properties; the SPA
		// renders a property as a single string.
	}
}

// Excerpt returns a plain-text preview of a Markdown body for search results
// and tag listings.
func Excerpt(body string) string {
	plain := stripMarkdown(body)
	plain = strings.Join(strings.Fields(plain), " ")
	runes := []rune(plain)
	if len(runes) <= maxExcerptLength {
		return plain
	}
	return strings.TrimSpace(string(runes[:maxExcerptLength])) + "…"
}

var (
	codeFencePattern  = regexp.MustCompile("(?s)```.*?```")
	inlineCodePattern = regexp.MustCompile("`[^`]*`")
	imagePattern      = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	linkPattern       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	emphasisPattern   = regexp.MustCompile(`[*_~]+`)
	htmlTagPattern    = regexp.MustCompile(`<[^>]+>`)
)

// stripMarkdown removes the Markdown syntax that would be noise in a
// one-line excerpt. It is intentionally lossy: the excerpt is a preview, not
// a rendering.
func stripMarkdown(body string) string {
	plain := codeFencePattern.ReplaceAllString(body, " ")
	plain = inlineCodePattern.ReplaceAllString(plain, " ")
	plain = imagePattern.ReplaceAllString(plain, " ")
	plain = linkPattern.ReplaceAllString(plain, "$1")
	plain = htmlTagPattern.ReplaceAllString(plain, " ")
	plain = emphasisPattern.ReplaceAllString(plain, "")
	plain = strings.ReplaceAll(plain, "[[", "")
	plain = strings.ReplaceAll(plain, "]]", "")

	lines := strings.Split(plain, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "#>+-")
		if trimmed == "" {
			continue
		}
		kept = append(kept, strings.TrimSpace(trimmed))
	}
	return strings.Join(kept, " ")
}

var markdownParser = goldmark.New()

// wikiLinkPattern matches [[Target]] and [[Target|Alias]]; group 1 is the
// target.
var wikiLinkPattern = regexp.MustCompile(`\[\[(\S[^\]|\n]*?)(?:\|[^\]\n]+?)?\]\]`)

// LinkTarget is one extracted outgoing link of a page.
type LinkTarget struct {
	// Path is the internal route path the link points at (no leading
	// slash, no fragment).
	Path string
	// Title is the wiki-link target when the link uses [[...]] syntax.
	Title string
}

// ExtractLinks returns the internal links of a Markdown body: Markdown link
// destinations that are not external, not anchors and not asset paths, plus
// [[WikiLinks]]. This mirrors LeafWiki's link indexing closely enough for
// backlinks and the broken-link audit.
func ExtractLinks(body string) []LinkTarget {
	targets := make([]LinkTarget, 0, 8)
	seen := map[string]struct{}{}

	reader := text.NewReader([]byte(body))
	doc := markdownParser.Parser().Parse(reader)
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		link, ok := node.(*ast.Link)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		destination := string(link.Destination)
		lowered := strings.ToLower(destination)
		if strings.HasPrefix(lowered, "http://") || strings.HasPrefix(lowered, "https://") ||
			strings.HasPrefix(lowered, "mailto:") || strings.HasPrefix(lowered, "#") ||
			strings.HasPrefix(lowered, "tel:") {
			return ast.WalkContinue, nil
		}
		target := normaliseLinkPath(destination)
		if target == "" {
			return ast.WalkContinue, nil
		}
		if _, duplicate := seen[target]; duplicate {
			return ast.WalkContinue, nil
		}
		seen[target] = struct{}{}
		targets = append(targets, LinkTarget{Path: target})
		return ast.WalkContinue, nil
	})

	for _, match := range wikiLinkPattern.FindAllStringSubmatch(body, -1) {
		title := strings.TrimSpace(match[1])
		if index := strings.IndexByte(title, '#'); index >= 0 {
			title = strings.TrimSpace(title[:index])
		}
		if title == "" {
			continue
		}
		key := "wiki:" + strings.ToLower(title)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, LinkTarget{Title: title})
	}

	return targets
}

// normaliseLinkPath turns a Markdown link destination into a wiki route path.
func normaliseLinkPath(destination string) string {
	path := strings.TrimSpace(destination)
	path = strings.TrimPrefix(path, "<")
	path = strings.TrimSuffix(path, ">")
	if index := strings.IndexByte(path, '#'); index >= 0 {
		path = path[:index]
	}
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "assets/") {
		return ""
	}
	// A relative link is still an internal link; strip the ./ noise.
	path = strings.TrimPrefix(path, "./")
	path = strings.Trim(path, "/")
	return path
}

// slugifyTitle is the fallback used to derive a slug from a title.
func slugifyTitle(title string) string {
	var builder strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(strings.ReplaceAll(title, "_", "-")) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingDash && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			pendingDash = false
			builder.WriteRune(r)
		default:
			// Non-ASCII letters have no route-safe representation without a
			// transliteration table; treat them as separators so the slug
			// stays valid instead of being rejected.
			pendingDash = true
		}
	}
	return builder.String()
}

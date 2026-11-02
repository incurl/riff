package leafwiki

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// configResponse is the payload of GET /api/config. The field set mirrors
// LeafWiki's client Config type: the SPA reads every field, and a missing one
// degrades a feature (an undefined publicAccess, for instance, hides the
// settings toggle in a way that looks like a server bug).
type configResponse struct {
	PublicAccess            bool     `json:"publicAccess"`
	PublicAccessEnvManaged  bool     `json:"publicAccessEnvManaged"`
	AlwaysShowToc           bool     `json:"alwaysShowToc"`
	EditorLimit             int      `json:"editorLimit"`
	HideLinkMetadataSection bool     `json:"hideLinkMetadataSection"`
	AuthDisabled            bool     `json:"authDisabled"`
	MaxAssetUploadSizeBytes int64    `json:"maxAssetUploadSizeBytes"`
	MaxAvatarUploadSizeB    int64    `json:"maxAvatarUploadSizeBytes"`
	AvatarAllowedExts       []string `json:"avatarAllowedExts"`
	EnableRevision          bool     `json:"enableRevision"`
	EnableLinkRefactor      bool     `json:"enableLinkRefactor"`
	EnableAPIKeyManagement  bool     `json:"enableApiKeyManagement"`
	GitBackupEnabled        bool     `json:"gitBackupEnabled"`
	GitBackupEnvManaged     bool     `json:"gitBackupEnvManaged"`
	GitBackupConfigured     bool     `json:"gitBackupConfigured"`
	SnapshotEnabled         bool     `json:"snapshotEnabled"`
	SMTPEnabled             bool     `json:"smtpEnabled"`
	TOTPAvailable           bool     `json:"totpAvailable"`
	HTTPRemoteUserEnabled   bool     `json:"httpRemoteUserEnabled"`
	LoginURL                string   `json:"loginUrl"`
	LogoutURL               string   `json:"logoutUrl"`
	UserManagementURL       string   `json:"userManagementUrl"`
	DefaultLanguage         string   `json:"defaultLanguage"`
}

// configHandler serves the client bootstrap config.
//
// Reads are always public (a wiki is readable), writes require an account
// unless Options.PublicWrite is set, and LeafWiki's own TOTP / SMTP / git
// backup / revision features are not ported, so they are reported as
// disabled instead of pretending to exist.
func (s *Server) configHandler(e *core.RequestEvent) error {
	continueURL := s.opts.Prefix + "/"
	return e.JSON(http.StatusOK, configResponse{
		PublicAccess:            !s.opts.PublicWrite,
		PublicAccessEnvManaged:  false,
		AlwaysShowToc:           false,
		EditorLimit:             0,
		HideLinkMetadataSection: false,
		AuthDisabled:            s.opts.PublicWrite,
		MaxAssetUploadSizeBytes: s.assets.maxUpload,
		MaxAvatarUploadSizeB:    5 * 1024 * 1024,
		AvatarAllowedExts:       []string{".gif", ".jpeg", ".jpg", ".png", ".webp"},
		EnableRevision:          false,
		EnableLinkRefactor:      true,
		EnableAPIKeyManagement:  false,
		GitBackupEnabled:        false,
		GitBackupEnvManaged:     false,
		GitBackupConfigured:     false,
		SnapshotEnabled:         false,
		SMTPEnabled:             false,
		TOTPAvailable:           false,
		HTTPRemoteUserEnabled:   false,
		LoginURL:                continueURL + "login",
		LogoutURL:               continueURL,
		UserManagementURL:       "",
		DefaultLanguage:         "en",
	})
}

// healthResponse is the payload of GET /api/health.
type healthResponse struct {
	Status         string `json:"status"`
	Version        string `json:"version"`
	StorageDir     string `json:"storageDir"`
	Pages          int    `json:"pages"`
	IndexAvailable bool   `json:"indexAvailable"`
}

// healthHandler reports liveness plus the size of the tree and the state of
// the index. It is safe to expose: it carries no secrets and no page titles.
func (s *Server) healthHandler(e *core.RequestEvent) error {
	root := s.tree.Root()
	return e.JSON(http.StatusOK, healthResponse{
		Status:         "ok",
		Version:        s.opts.Version,
		StorageDir:     s.tree.StorageDir(),
		Pages:          countPages(root),
		IndexAvailable: s.index.available(),
	})
}

// countPages counts the non-root nodes of a tree.
func countPages(node *PageNode) int {
	if node == nil {
		return 0
	}
	total := 0
	if node.ID != rootID {
		total = 1
	}
	for _, child := range node.Children {
		total += countPages(child)
	}
	return total
}

// searchStatus is the payload of GET /api/search/status.
type searchStatus struct {
	Active     bool   `json:"active"`
	Indexed    int    `json:"indexed"`
	Failed     int    `json:"failed"`
	FinishedAt string `json:"finished_at"`
}

// searchStatusHandler reports the state of the index. The PocketBase index is
// synchronous, so a search never observes a partially built index; "active"
// therefore describes whether indexing is available at all.
func (s *Server) searchStatusHandler(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, searchStatus{
		Active:     s.index.available(),
		Indexed:    len(s.index.allPages()),
		Failed:     0,
		FinishedAt: "",
	})
}

// userLabels resolves the {id, username} labels of a set of user ids in one
// query. It is called once per response, never per page: a tree with a few
// hundred nodes would otherwise turn into a few hundred queries.
func (s *Server) userLabels(ids []string) map[string]UserLabel {
	labels := map[string]UserLabel{}
	if len(ids) == 0 {
		return labels
	}

	unique := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || trimmed == systemUserID {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}
	if len(unique) == 0 {
		return labels
	}

	clauses := make([]string, 0, len(unique))
	params := make(map[string]any, len(unique))
	for index, id := range unique {
		key := fmt.Sprintf("u%d", index)
		clauses = append(clauses, "id = {:"+key+"}")
		params[key] = id
	}

	records, err := s.app.FindRecordsByFilter("users", strings.Join(clauses, " || "), "", 0, 0, params)
	if err != nil {
		return labels
	}
	for _, record := range records {
		label := UserLabel{ID: record.Id, Username: record.GetString("username")}
		if label.Username == "" {
			label.Username = record.Email()
		}
		labels[record.Id] = label
	}
	return labels
}

// decorateNode fills the creator/lastAuthor labels of a node and its children.
func (s *Server) decorateNode(node *PageNode) {
	if node == nil {
		return
	}
	ids := make([]string, 0, 8)
	collectAuthorIDs(node, &ids)
	applyAuthorLabels(node, s.userLabels(ids))
}

// decoratePage fills the labels of a page projection.
func (s *Server) decoratePage(page *Page) {
	if page == nil || page.PageNode == nil {
		return
	}
	applyAuthorLabels(page.PageNode, s.userLabels([]string{page.Metadata.CreatorID, page.Metadata.LastAuthorID}))
}

// collectAuthorIDs gathers the author ids of a subtree.
func collectAuthorIDs(node *PageNode, ids *[]string) {
	*ids = append(*ids, node.Metadata.CreatorID, node.Metadata.LastAuthorID)
	for _, child := range node.Children {
		collectAuthorIDs(child, ids)
	}
}

// applyAuthorLabels sets the resolved labels on a subtree.
func applyAuthorLabels(node *PageNode, labels map[string]UserLabel) {
	if label, ok := labels[node.Metadata.CreatorID]; ok {
		node.Metadata.Creator = &UserLabel{ID: label.ID, Username: label.Username}
	}
	if label, ok := labels[node.Metadata.LastAuthorID]; ok {
		node.Metadata.LastAuthor = &UserLabel{ID: label.ID, Username: label.Username}
	}
	for _, child := range node.Children {
		applyAuthorLabels(child, labels)
	}
}

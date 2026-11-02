package leafwiki

import (
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
)

// Options configures the wiki.
//
// The zero value is valid and gives the defaults used by riff: the pages live
// in <DataDir>/leafwiki, the API and SPA are mounted at /wiki, and only an
// authenticated caller may write.
type Options struct {
	// StorageDir is the root of the Markdown tree. Defaults to
	// <DataDir>/leafwiki, which is the same directory name upstream
	// LeafWiki uses, so an existing wiki can be dropped in.
	StorageDir string

	// Prefix is the URL prefix the wiki is mounted at. Defaults to "/wiki".
	// A trailing slash is ignored.
	Prefix string

	// MaxUploadBytes caps an attachment upload. Defaults to 50 MiB
	// (LeafWiki's default).
	MaxUploadBytes int64

	// PublicWrite allows anonymous callers to create and edit pages. It is
	// the equivalent of LeafWiki's "open editing without login" operating
	// mode and is off by default.
	PublicWrite bool

	// Version is reported by /api/health. Defaults to "dev".
	Version string
}

// defaultPrefix is the mount point riff uses.
const defaultPrefix = "/wiki"

// Server is the wiki's HTTP surface and its services.
type Server struct {
	app    core.App
	opts   Options
	tree   *Tree
	index  *index
	assets *Assets
	ui     *spaAssets
}

// Register mounts the wiki on the given app.
//
// Everything is built from the OnServe hook rather than from here: the app is
// not bootstrapped yet when this runs, so the data directory does not exist,
// the collections may not be migrated, and a collection lookup would panic.
// OnServe runs after the bootstrap and after the migrations, which is exactly
// when the storage directory and the index collections are guaranteed to be
// there — the same reason pb/bark ensures its collection from OnServe.
func Register(app core.App, opts Options) error {
	if app == nil {
		return errors.New("leafwiki: nil app")
	}
	if opts.Prefix == "" {
		opts.Prefix = defaultPrefix
	}
	opts.Prefix = "/" + strings.Trim(opts.Prefix, "/")
	if opts.Version == "" {
		opts.Version = "dev"
	}

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		server, err := newServer(app, opts)
		if err != nil {
			return err
		}
		server.mount(e.Router)
		return e.Next()
	})
	return nil
}

// newServer builds the services and loads the tree.
func newServer(app core.App, opts Options) (*Server, error) {
	storageDir := strings.TrimSpace(opts.StorageDir)
	if storageDir == "" {
		storageDir = filepath.Join(app.DataDir(), "leafwiki")
	}

	index := newIndex(app)
	tree, err := newTree(storageDir, index)
	if err != nil {
		return nil, errors.Wrap(err, "leafwiki: could not load the Markdown tree")
	}

	ui, err := loadSPA()
	if err != nil {
		return nil, err
	}

	server := &Server{
		app:    app,
		opts:   opts,
		tree:   tree,
		index:  index,
		assets: newAssets(tree.store, opts.MaxUploadBytes),
		ui:     ui,
	}
	return server, nil
}

// Tree exposes the tree service (used by tests).
func (s *Server) Tree() *Tree {
	return s.tree
}

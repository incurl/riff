package frontend_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/incurl/riff/server/frontend"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// TestRegister_ServesIndexOnRoot is the integration check
// that the static handler is actually mounted and the SPA
// reaches the client. It builds a fresh PocketBase app,
// registers frontend.Register, and asks the router for its
// mux; a GET / against that mux should return the embedded
// index.html (id="root"). A deep link like /m/123 should
// also fall through to index.html because we wired
// indexFallback=true.
//
// This is the "SPA boots" test the audit flagged as missing.
func TestRegister_ServesIndexOnRoot(t *testing.T) {
	app := pocketbase.New()
	frontend.Register(app)
	handler := mustBuildMux(t, app)

	t.Run("root", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /: want 200, got %d (body=%q)", rec.Code, truncate(rec.Body.String(), 200))
		}
		body := rec.Body.String()
		if !strings.Contains(body, `id="root"`) {
			t.Fatalf("GET /: response missing id=\"root\": %q", truncate(body, 200))
		}
	})

	t.Run("deep link", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/m/abc-123", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /m/abc-123: want 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Fatal("GET /m/abc-123: index.html fallback did not fire")
		}
	})

	t.Run("static asset", func(t *testing.T) {
		asset := firstJSAsset(t)
		req := httptest.NewRequest(http.MethodGet, "/assets/"+asset, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /assets/%s: want 200, got %d", asset, rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "application/javascript") && !strings.HasPrefix(ct, "text/javascript") {
			t.Fatalf("GET /assets/%s: content-type = %q, want javascript", asset, ct)
		}
	})
}

// mustBuildMux builds a real PocketBase router (the same way
// apis.Serve does internally), triggers the OnServe chain
// against a ServeEvent that has the router pre-populated,
// and returns the assembled http.Handler. No real listener
// is bound; BuildMux is pure and does not need a port.
func mustBuildMux(t *testing.T, app core.App) http.Handler {
	t.Helper()

	pbRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("apis.NewRouter: %v", err)
	}

	e := &core.ServeEvent{
		App:    app,
		Router: pbRouter,
		Server: &http.Server{},
	}

	// Run the OnServe chain. Register has already bound a
	// BindFunc that calls e.Router.GET, so this mounts the
	// static handler on the router.
	if err := app.OnServe().Trigger(e); err != nil {
		t.Fatalf("OnServe.Trigger: %v", err)
	}

	handler, err := pbRouter.BuildMux()
	if err != nil {
		t.Fatalf("BuildMux: %v", err)
	}
	return handler
}

// firstJSAsset returns the basename of the first .js file
// in the embedded assets/ directory. The SPA build always
// emits a hashed index-*.js entry-point bundle, so this is
// stable across machines and re-builds.
func firstJSAsset(t *testing.T) string {
	t.Helper()
	fsys := frontend.FS()
	entries, err := fs.ReadDir(fsys, "assets")
	if err != nil {
		t.Fatalf("read assets/: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".js") {
			return e.Name()
		}
	}
	t.Fatal("no .js files in assets/")
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

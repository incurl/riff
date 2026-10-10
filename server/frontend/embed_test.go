package frontend

import (
	"io"
	"testing"
)

// TestFS_ContainsIndex guards against a regression where the
// //go:embed directive silently fails to match (for example
// because the dist/ tree was deleted or moved) and a fresh
// clone ends up with a binary that 500s on every request. The
// static handler falls back to index.html, so an empty
// embed is the most likely failure mode: the binary boots,
// every request returns 404, and the operator has nothing
// actionable to look at.
//
// If the tree is empty, the panic from FS() at startup
// already covers us; this test is the belt-and-suspenders
// check that the embed actually has the file we expect.
func TestFS_ContainsIndex(t *testing.T) {
	fsys := FS()
	f, err := fsys.Open("index.html")
	if err != nil {
		t.Fatalf("server/frontend: index.html missing from embed: %v", err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("server/frontend: read index.html: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("server/frontend: index.html is empty; the SPA build did not run")
	}
}

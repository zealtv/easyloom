package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zealtv/easyloom/web"
)

// served builds a shell over a frontend, which is the embedded one unless the
// test wants to know exactly what it is looking at.
func served(t *testing.T, assets fs.FS) http.Handler {
	t.Helper()
	registry := NewRegistry()
	registry.Add(stub("/home/someone/easyloom"))
	return NewRouter(RouterOptions{Registry: registry, Assets: assets})
}

func TestEmbeddedFrontendIsComplete(t *testing.T) {
	assets := web.FS()
	for _, name := range []string{
		web.Index, "app.css", "app.js", "stitches.js", "tree.js", "queue.js", "stitch.js",
		"markdown.js", "events.js", "unblock.js", "impact.js", "graph.js", "graphview.js", "graphinteraction.js",
		// Vendored code and its licence ship inside the binary together.
		"vendor/dagre.esm.js", "vendor/dagre.LICENSE",
	} {
		body, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Errorf("%s is not embedded: %v", name, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("%s is embedded but empty", name)
		}
	}
}

// The binary is self-contained: the sources are the artefact, and there is no
// dist directory or build step to go looking for.
func TestFrontendSourcesAreWhatIsEmbedded(t *testing.T) {
	embedded, err := fs.ReadFile(web.FS(), web.Index)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(filepath.Join("..", "..", web.Dir, web.Index))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(onDisk) {
		t.Error("the embedded index does not match web/index.html")
	}
}

func TestStaticFilesAreServedFromRoot(t *testing.T) {
	handler := served(t, web.FS())

	for _, tc := range []struct{ path, contentType string }{
		{"/app.css", "text/css"},
		{"/app.js", "text/javascript"},
	} {
		rec := request(t, handler, tc.path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", tc.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.contentType) {
			t.Errorf("GET %s: Content-Type %q, want %s", tc.path, got, tc.contentType)
		}
	}
}

// Deep links survive a refresh: the browser asking the server for a view gets
// the document that knows how to render it.
func TestViewsServeTheIndexDocument(t *testing.T) {
	handler := served(t, web.FS())
	index, err := fs.ReadFile(web.FS(), web.Index)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/l/easyloom/frontier",
		"/l/easyloom/threads",
		"/l/easyloom/queue",
		"/l/easyloom/graph",
		"/l/easyloom/stitch/http-server",
	} {
		rec := request(t, handler, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", path, rec.Code)
			continue
		}
		if rec.Body.String() != string(index) {
			t.Errorf("GET %s did not serve index.html", path)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("GET %s: Content-Type %q, want HTML", path, got)
		}
	}
}

// An unknown path that is not a file is a deep link into the frontend. An
// unknown path that looks like a file is a missing file, and saying so beats
// serving it a page it cannot parse.
func TestUnknownPathsFallBackOnlyWhenTheyAreNotFiles(t *testing.T) {
	handler := served(t, web.FS())

	if rec := request(t, handler, "/somewhere/else"); rec.Code != http.StatusOK {
		t.Errorf("status %d for an unknown view path, want the index", rec.Code)
	}
	if rec := request(t, handler, "/missing.css"); rec.Code != http.StatusNotFound {
		t.Errorf("status %d for a missing stylesheet, want 404", rec.Code)
	}
}

// The API answers as an API even when the path is wrong. A fetch should never
// be handed the HTML shell and left to parse it as JSON.
func TestUnknownAPIPathsDoNotFallBackToTheIndex(t *testing.T) {
	handler := served(t, web.FS())

	rec := request(t, handler, "/api/nothing-here")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type %q, want JSON", got)
	}
}

// The index must not be cached: it changes whenever the binary does, and a
// stale shell after an upgrade is a mystery to debug.
func TestTheIndexIsNotCached(t *testing.T) {
	handler := served(t, web.FS())
	rec := request(t, handler, "/l/easyloom/frontier")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control %q, want no-cache", got)
	}
}

// Whatever the frontend is, the shell serves that one — which is what --dev
// swaps out.
func TestTheShellServesTheFrontendItWasGiven(t *testing.T) {
	handler := served(t, fstest.MapFS{
		web.Index: &fstest.MapFile{Data: []byte("<!doctype html><title>from disk</title>")},
	})

	rec := request(t, handler, "/l/easyloom/frontier")
	if !strings.Contains(rec.Body.String(), "from disk") {
		t.Errorf("body %q is not the supplied frontend", rec.Body.String())
	}
}

// --dev serves from the working directory, and says where it looked when the
// sources are not there.
func TestAssetsDevRequiresTheSources(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := Assets(true); err == nil {
		t.Fatal("--dev succeeded with no web/ directory")
	} else if !strings.Contains(err.Error(), web.Dir) {
		t.Errorf("error does not say what was missing: %v", err)
	}

	if _, err := Assets(false); err != nil {
		t.Errorf("the embedded frontend needs no working directory: %v", err)
	}
}

func TestAssetsDevServesFromDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, web.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, web.Dir, web.Index), []byte("<!doctype html><title>iterating</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	assets, err := Assets(true)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	served(t, assets).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/l/easyloom/frontier", nil))
	if !strings.Contains(rec.Body.String(), "iterating") {
		t.Errorf("body %q was not served from disk", rec.Body.String())
	}
}

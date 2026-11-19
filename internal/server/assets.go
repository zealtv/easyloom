package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/zealtv/easyloom/web"
)

// Assets is the frontend the shell serves at /.
//
// The embedded copy is the default and the shipped one. A --dev run serves the
// same files from disk instead, so the frontend can be iterated with a reload
// rather than a Go rebuild — with no build step, that is all iterating is.
func Assets(dev bool) (fs.FS, error) {
	if !dev {
		return web.FS(), nil
	}
	dir := os.DirFS(web.Dir)
	if _, err := fs.Stat(dir, web.Index); err != nil {
		cwd, _ := os.Getwd()
		return nil, fmt.Errorf("--dev: no %s in %s: run from the repository root", path.Join(web.Dir, web.Index), cwd)
	}
	return dir, nil
}

// serveAssets serves a file out of the frontend, falling back to index.html.
//
// The fallback is what makes deep links survive a refresh: /l/bopos/graph is a
// place in the frontend, not a file, and the browser asking the server for it
// must get the document that knows how to render it. Only paths that look like
// files 404, so a mistyped stylesheet does not silently return HTML.
func (r *Router) serveAssets(w http.ResponseWriter, req *http.Request) {
	name := strings.TrimPrefix(path.Clean(req.URL.Path), "/")
	if name == "" || name == "." {
		name = web.Index
	}

	file, err := r.assets.Open(name)
	if err == nil {
		defer file.Close()
		if info, statErr := file.Stat(); statErr == nil && !info.IsDir() {
			http.ServeFileFS(w, req, r.assets, name)
			return
		}
	}

	// A request for something with an extension is a request for a file. It is
	// missing, and saying so beats serving it a page it cannot parse.
	if path.Ext(name) != "" {
		http.NotFound(w, req)
		return
	}
	r.serveIndex(w, req)
}

// serveIndex serves the single document every view is rendered from.
func (r *Router) serveIndex(w http.ResponseWriter, req *http.Request) {
	// The document is small and changes whenever the binary does; caching it
	// only ever means serving a stale shell after an upgrade.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, req, r.assets, web.Index)
}

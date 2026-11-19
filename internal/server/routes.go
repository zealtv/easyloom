package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

// View is one of the shell's views. The view is in the URL alongside the loom,
// so a reload lands where it left off and a link can name a place.
type View string

const (
	// ViewFrontier is the landing view: the ready work.
	ViewFrontier View = "frontier"
	// ViewThreads is the decomposition tree, one root per goal stitch.
	ViewThreads View = "threads"
	// ViewQueue is .loom/queue in the order the loom will read it.
	ViewQueue View = "queue"
	// ViewGraph is the decomposition and dependency graph.
	ViewGraph View = "graph"
	// ViewImpact ranks stitches by what resolving them would make ready.
	ViewImpact View = "impact"
	// ViewStitch is one stitch in detail.
	ViewStitch View = "stitch"
)

// Request is a resolved request: which loom, which view, and which stitch when
// the view names one.
type Request struct {
	Entry *Entry
	View  View
	// StitchID is empty for any view but a stitch view.
	StitchID string
}

// ViewHandler renders one resolved view. It is called only after the slug has
// been resolved to a registered loom, so it never has to decide which loom it
// is looking at.
type ViewHandler func(http.ResponseWriter, *http.Request, Request)

// Router builds the shell's URL space over a registry.
//
// The routes are:
//
//	/l/<slug>/frontier
//	/l/<slug>/threads
//	/l/<slug>/queue
//	/l/<slug>/graph
//	/l/<slug>/impact
//	/l/<slug>/stitch/<stitch-id>
//
// Nothing about which loom is being viewed is held on the server. Two tabs on
// two looms are two URLs, and that is the whole mechanism.
type Router struct {
	registry *Registry
	assets   fs.FS
	view     ViewHandler
}

// RouterOptions configure the shell's URL space.
type RouterOptions struct {
	// Registry is the looms this server can serve. It is required.
	Registry *Registry
	// Assets is the frontend served at /. A nil frontend serves no static
	// files, which is only useful in a test.
	Assets fs.FS
	// View renders a resolved view. Nil serves the frontend's index document,
	// which is what a single-page frontend wants for every view.
	View ViewHandler
}

// NewRouter returns the mux serving the shell.
//
// With no assets and no view handler it falls back to a plain description of
// what a URL resolved to, so routing can be exercised on its own.
func NewRouter(opts RouterOptions) http.Handler {
	r := &Router{registry: opts.Registry, assets: opts.Assets, view: opts.View}
	if r.view == nil {
		r.view = r.defaultView()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", r.root)
	mux.HandleFunc("GET /l/{slug}/{$}", r.redirectToFrontier)
	mux.HandleFunc("GET /l/{slug}/frontier", r.handle(ViewFrontier))
	mux.HandleFunc("GET /l/{slug}/threads", r.handle(ViewThreads))
	mux.HandleFunc("GET /l/{slug}/queue", r.handle(ViewQueue))
	mux.HandleFunc("GET /l/{slug}/graph", r.handle(ViewGraph))
	mux.HandleFunc("GET /l/{slug}/impact", r.handle(ViewImpact))
	mux.HandleFunc("GET /l/{slug}/stitch/{id}", r.handle(ViewStitch))
	r.registerAPI(mux)
	if r.assets != nil {
		// Everything not matched above is a static file or a deep link into
		// the frontend. /api/ is registered explicitly so an unknown endpoint
		// answers as an endpoint rather than being handed the HTML shell.
		mux.HandleFunc("GET /", r.serveAssets)
		unknownEndpoint := func(w http.ResponseWriter, req *http.Request) {
			writeAPIError(w, http.StatusNotFound, "unknown-endpoint",
				fmt.Sprintf("no API endpoint at %s", req.URL.Path))
		}
		mux.HandleFunc("GET /api/", unknownEndpoint)
		mux.HandleFunc("POST /api/", unknownEndpoint)
	}
	return mux
}

// defaultView renders every view from the frontend's index document — the
// single page decides what to draw from the URL it was served at. Without a
// frontend it describes the resolved URL instead.
func (r *Router) defaultView() ViewHandler {
	if r.assets == nil {
		return describeView
	}
	return func(w http.ResponseWriter, req *http.Request, _ Request) {
		r.serveIndex(w, req)
	}
}

// FrontierPath is where a loom's landing view lives.
func FrontierPath(slug string) string { return "/l/" + slug + "/frontier" }

// StitchPath is where one stitch's detail view lives.
func StitchPath(slug, stitchID string) string {
	return "/l/" + slug + "/stitch/" + stitchID
}

// root sends a bare request to the loom this process was pointed at. That loom
// is fixed at startup: the redirect is a convenience, not a current-loom
// pointer something later in the session can move.
func (r *Router) root(w http.ResponseWriter, req *http.Request) {
	entry, ok := r.registry.Primary()
	if !ok {
		http.Error(w, "no loom is registered", http.StatusNotFound)
		return
	}
	http.Redirect(w, req, FrontierPath(entry.Slug), http.StatusFound)
}

func (r *Router) redirectToFrontier(w http.ResponseWriter, req *http.Request) {
	entry, ok := r.resolve(w, req)
	if !ok {
		return
	}
	http.Redirect(w, req, FrontierPath(entry.Slug), http.StatusFound)
}

func (r *Router) handle(view View) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		entry, ok := r.resolve(w, req)
		if !ok {
			return
		}
		resolved := Request{Entry: entry, View: view}
		if view == ViewStitch {
			resolved.StitchID = req.PathValue("id")
		}
		r.view(w, req, resolved)
	}
}

// resolve turns the slug in the path into a registered loom, or answers 404
// saying which slugs exist — a stale bookmark should say what happened rather
// than render an empty page.
func (r *Router) resolve(w http.ResponseWriter, req *http.Request) (*Entry, bool) {
	slug := req.PathValue("slug")
	if entry, ok := r.registry.Lookup(slug); ok {
		return entry, true
	}
	http.Error(w, fmt.Sprintf("no loom %q is registered; this server has %s",
		slug, strings.Join(knownSlugs(r.registry), ", ")), http.StatusNotFound)
	return nil, false
}

func knownSlugs(registry *Registry) []string {
	entries := registry.List()
	if len(entries) == 0 {
		return []string{"none"}
	}
	slugs := make([]string, 0, len(entries))
	for _, entry := range entries {
		slugs = append(slugs, entry.Slug)
	}
	sort.Strings(slugs)
	return slugs
}

// describeView is the placeholder view. It states exactly what the URL
// resolved to, which is the whole of what routing can demonstrate before the
// views exist.
func describeView(w http.ResponseWriter, _ *http.Request, resolved Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "loom  %s (%s)\n", resolved.Entry.Display, resolved.Entry.Slug)
	fmt.Fprintf(w, "root  %s\n", resolved.Entry.Target.Root)
	fmt.Fprintf(w, "view  %s\n", resolved.View)
	if resolved.StitchID != "" {
		fmt.Fprintf(w, "stitch %s\n", resolved.StitchID)
	}
	fmt.Fprintln(w, "\nThe views are not built yet.")
}

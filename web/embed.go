// Package web is the frontend, embedded into the binary.
//
// There is no build step. This directory holds hand-written HTML, CSS and ES
// modules that the browser loads natively, and embed.FS embeds them as they
// stand — the sources are the artefact, so a clean checkout builds with
// nothing beyond Go. The reasoning, and what would justify revisiting it, is
// on the loom stitch embedded-assets.
package web

import (
	"embed"
	"io/fs"
)

// The patterns cover the file types the frontend is written in. A new .html,
// .css or .js file is embedded automatically; anything else — a font, an image
// — has to be added here, and will not load until it is.
//
// vendor/ carries third-party modules as published, with their licences. The
// licence files are embedded alongside the code deliberately: they ship inside
// the binary because the code they cover does.
//
//go:embed *.html *.css *.js vendor/*.js vendor/*.LICENSE
var files embed.FS

// FS returns the embedded frontend.
func FS() fs.FS { return files }

// Dir is the directory the sources live in, relative to the repository root.
// The --dev flag serves from there instead of from the embedded copy, so the
// frontend can be iterated with a reload rather than a Go rebuild.
const Dir = "web"

// Index is the document every view is served from. Deep links survive a
// refresh because an unknown path serves this rather than 404ing.
const Index = "index.html"

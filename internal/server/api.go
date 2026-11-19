package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// The JSON API. Three reads and the commands:
//
//	GET  /api/looms                    which looms this server knows
//	GET  /api/l/<slug>/snapshot        the cached projection and its age
//	GET  /api/l/<slug>/stitch/<id>     one stitch, with its instructions
//	GET  /api/l/<slug>/events          server-sent events; see events.go
//	POST /api/l/<slug>/refresh         ask for a new projection, return at once
//	POST /api/l/<slug>/queue/...       change the queue; see queue.go
//
// Nothing here ever runs map synchronously. A snapshot can cost seconds —
// nearly 8s on a large loom under loom.sh v2.1.0 — so a request that waited on
// one would be a request that times out. Reads serve the cache as it stands and say how
// old it is; refresh is a request, not a result.

// LoomListing is one loom in the registry listing.
type LoomListing struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Root is the loom directory, and Script the deployed loom.sh inside it.
	Root   string `json:"root"`
	Script string `json:"script"`
	// Tier is what this build can do with the loom: v2, v1 or unsupported.
	Tier          string `json:"tier"`
	FormatVersion int    `json:"format_version"`
	SchemaVersion int    `json:"schema_version"`
}

// LoomList is the response of GET /api/looms.
type LoomList struct {
	Looms []LoomListing `json:"looms"`
	// Primary is the slug of the loom this process was pointed at.
	Primary string `json:"primary"`
}

// SnapshotResponse wraps the projection with everything the UI needs to say
// how much to trust it: which generation it is, when it was taken, whether the
// last refresh failed, and what went wrong if it did.
//
// Snapshot is null only before the first successful refresh. A failed refresh
// never blanks it: a broken loom shows the last good projection behind an
// error, never nothing.
type SnapshotResponse struct {
	Generation uint64 `json:"generation"`
	// CapturedAt is when the projection finished, or null when there is none.
	CapturedAt *time.Time `json:"captured_at"`
	// AgeMS is how old the projection is in milliseconds, so a UI can render
	// an age without trusting its own clock against the server's.
	AgeMS int64 `json:"age_ms"`
	// Stale reports that the most recent refresh failed, so the projection
	// predates it.
	Stale bool `json:"stale"`
	// Error is the most recent refresh error, or empty. With Stale false it is
	// schema drift on a projection that is otherwise usable.
	Error    string             `json:"error"`
	Snapshot *snapshot.Snapshot `json:"snapshot"`
}

// StitchResponse is one stitch in detail: its record from the projection, plus
// the instructions.md body, which the projection does not carry.
type StitchResponse struct {
	Generation uint64          `json:"generation"`
	Stitch     snapshot.Stitch `json:"stitch"`
	// Instructions is the instructions.md body, empty when the stitch has none.
	Instructions string `json:"instructions"`
	// InstructionsPath is where that body was read from, relative to the loom
	// directory, so a UI can say which file it is showing.
	InstructionsPath string `json:"instructions_path"`
	// InstructionsError explains why the body is empty when reading it failed.
	// A missing or unreadable file is not a failed request: the stitch record
	// is still worth rendering.
	InstructionsError string `json:"instructions_error,omitempty"`
	// Artifacts are the supporting files in the stitch directory: notes, logs,
	// decisions, and reason.md on a dropped stitch. The protocol puts them
	// there and they travel with the record, so they are part of what a stitch
	// entails.
	Artifacts []Artifact `json:"artifacts"`
	// ArtifactsError explains why the list is empty when the directory could
	// not be read.
	ArtifactsError string `json:"artifacts_error,omitempty"`
}

// Artifact is one supporting file or directory beside a stitch's instructions.
type Artifact struct {
	Name string `json:"name"`
	// Dir reports a directory. Its contents are not listed: a viewer offers
	// the name, not a file browser.
	Dir bool `json:"dir"`
	// Size is the file size in bytes, and 0 for a directory.
	Size int64 `json:"size"`
}

// RefreshResponse acknowledges a refresh request. It is an acknowledgement and
// nothing more: the new projection arrives later, over the live-updates
// stream, and until then the snapshot endpoint keeps serving the old one.
type RefreshResponse struct {
	Accepted bool `json:"accepted"`
	// Generation is the projection current at the moment of the request, so a
	// caller can tell when the refresh has actually landed.
	Generation uint64 `json:"generation"`
}

// apiError is the body of any failed API request.
type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// The rest are filled in only when a mutation was refused by the loom
	// itself. LoomCode is the loom's own code — an open set, so a consumer
	// must tolerate one it does not recognise — and Stderr is its words,
	// verbatim, because a refusal caused by an unrelated stale queue record is
	// otherwise inexplicable.
	LoomCode  string   `json:"loom_code,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	StitchIDs []string `json:"stitch_ids,omitempty"`
	ExitCode  int      `json:"exit_code,omitempty"`
}

// The protocol's own files inside a stitch directory. None of them are
// artifacts: instructions.md is the brief and is served as text, needs/ is the
// dependency list the projection already resolves with status and reason, and
// completed-at is bookkeeping the projection carries as a field. Listing them
// as files would show protocol state twice in two vocabularies.
const (
	instructionsName = "instructions.md"
	needsDirName     = "needs"
	completedAtName  = "completed-at"
)

// registerAPI adds the JSON API to the shell's mux.
func (r *Router) registerAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/looms", r.apiLooms)
	mux.HandleFunc("GET /api/l/{slug}/snapshot", r.apiEntry(r.apiSnapshot))
	mux.HandleFunc("GET /api/l/{slug}/stitch/{id}", r.apiEntry(r.apiStitch))
	mux.HandleFunc("POST /api/l/{slug}/refresh", r.apiEntry(r.apiRefresh))
	r.registerQueue(mux)
	r.registerEvents(mux)
}

func (r *Router) apiLooms(w http.ResponseWriter, _ *http.Request) {
	list := LoomList{Looms: []LoomListing{}}
	if primary, ok := r.registry.Primary(); ok {
		list.Primary = primary.Slug
	}
	for _, entry := range r.registry.List() {
		list.Looms = append(list.Looms, LoomListing{
			Slug:          entry.Slug,
			Name:          entry.Display,
			Root:          entry.Target.Root,
			Script:        entry.Target.Script,
			Tier:          string(entry.Version.Tier),
			FormatVersion: entry.Version.FormatVersion,
			SchemaVersion: entry.Version.SchemaVersion,
		})
	}
	writeJSON(w, http.StatusOK, list)
}

// apiEntry resolves the slug for an API route, answering JSON rather than the
// HTML router's plain-text 404.
func (r *Router) apiEntry(next func(http.ResponseWriter, *http.Request, *Entry)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		slug := req.PathValue("slug")
		entry, ok := r.registry.Lookup(slug)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "unknown-loom",
				fmt.Sprintf("no loom %q is registered; this server has %s",
					slug, strings.Join(knownSlugs(r.registry), ", ")))
			return
		}
		next(w, req, entry)
	}
}

func (r *Router) apiSnapshot(w http.ResponseWriter, _ *http.Request, entry *Entry) {
	writeJSON(w, http.StatusOK, snapshotResponse(entry))
}

func snapshotResponse(entry *Entry) SnapshotResponse {
	if entry.Cache == nil {
		return SnapshotResponse{Stale: true, Error: "this loom has no snapshot cache"}
	}
	state := entry.Cache.Current()
	body := SnapshotResponse{
		Generation: state.Generation,
		Stale:      state.Stale,
		Snapshot:   state.Snapshot,
	}
	if state.Err != nil {
		body.Error = state.Err.Error()
	}
	if !state.CompletedAt.IsZero() {
		captured := state.CompletedAt
		body.CapturedAt = &captured
		body.AgeMS = state.Age().Milliseconds()
	}
	return body
}

// apiStitch serves one stitch out of the cached projection, with its
// instructions.md body attached.
func (r *Router) apiStitch(w http.ResponseWriter, req *http.Request, entry *Entry) {
	id := req.PathValue("id")
	if entry.Cache == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "no-cache", "this loom has no snapshot cache")
		return
	}
	state := entry.Cache.Current()
	if state.Snapshot == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "no-snapshot",
			"this loom has no projection yet; ask for a refresh")
		return
	}
	found, ok := state.Snapshot.Index()[id]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "unknown-stitch",
			fmt.Sprintf("generation %d has no stitch %q", state.Generation, id))
		return
	}

	body := StitchResponse{Generation: state.Generation, Stitch: *found}
	instructions, path, err := readInstructions(entry.Target.Root, found.Path)
	body.Instructions, body.InstructionsPath = instructions, path
	if err != nil {
		body.InstructionsError = err.Error()
	}

	artifacts, err := readArtifacts(entry.Target.Root, found.Path)
	body.Artifacts = artifacts
	if err != nil {
		body.ArtifactsError = err.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

// apiRefresh asks the cache for a new projection and returns immediately. The
// cache collapses a burst of requests into one extra run, so this is safe to
// call at any rate.
func (r *Router) apiRefresh(w http.ResponseWriter, _ *http.Request, entry *Entry) {
	if entry.Cache == nil {
		writeAPIError(w, http.StatusConflict, "no-cache", "this loom has no snapshot cache")
		return
	}
	generation := entry.Cache.Current().Generation
	entry.Cache.Refresh()
	writeJSON(w, http.StatusAccepted, RefreshResponse{Accepted: true, Generation: generation})
}

// readInstructions reads a stitch's instructions.md.
//
// This is the one thing EasyLoom reads off a loom's disk, and it is artifact
// content rather than protocol state: the path comes from the projection, and
// the body is what the projection deliberately does not carry. Nothing here
// infers state from the file system.
func readInstructions(loomRoot, stitchPath string) (string, string, error) {
	relative := filepath.Join(filepath.FromSlash(stitchPath), instructionsName)
	full := filepath.Join(loomRoot, relative)

	// The path came from the projection, but joining it with a root is exactly
	// the operation that must never escape one.
	if !within(loomRoot, full) {
		return "", filepath.ToSlash(relative), fmt.Errorf("path escapes the loom directory")
	}
	body, err := os.ReadFile(full)
	if err != nil {
		return "", filepath.ToSlash(relative), err
	}
	return string(body), filepath.ToSlash(relative), nil
}

// readArtifacts lists the supporting material in a stitch directory.
//
// The protocol's own files are left out — see the names above — and so is a
// subdirectory holding its own instructions.md, which is a decomposition child
// the projection already carries as a child edge.
func readArtifacts(loomRoot, stitchPath string) ([]Artifact, error) {
	artifacts := []Artifact{}
	relative := filepath.FromSlash(stitchPath)
	dir := filepath.Join(loomRoot, relative)
	if !within(loomRoot, dir) {
		return artifacts, fmt.Errorf("path escapes the loom directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return artifacts, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == instructionsName || name == needsDirName || name == completedAtName {
			continue
		}
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, name, instructionsName)); err == nil {
				continue // a decomposition child, not an artifact
			}
			artifacts = append(artifacts, Artifact{Name: name, Dir: true})
			continue
		}
		artifact := Artifact{Name: name}
		if info, err := entry.Info(); err == nil {
			artifact.Size = info.Size()
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// A local inspector's data is never cacheable: the whole point is that it
	// changes under the reader.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(body)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Error: apiErrorBody{Code: code, Message: message}})
}

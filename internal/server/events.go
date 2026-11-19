package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// Live updates:
//
//	GET /api/l/<slug>/events        server-sent events
//
// One way is all this needs. The browser never tells the server anything over
// this stream — it POSTs a refresh like any other request — so a websocket
// would be two directions of machinery for one direction of traffic, and SSE
// reconnects on its own where a socket does not.
//
// What is pushed is the generation, never the projection. A snapshot is 162 KB
// on a large loom; sending it down every open stream on every change would
// make a refresh cost more the more people were looking. The event says
// something moved and the client GETs /snapshot, which is served from cache.

// eventHeartbeat is how often a comment is written down an idle stream.
//
// It is not a keepalive for the browser's sake — EventSource reconnects by
// itself. It is how the server finds out that a tab was closed, since a write
// to a dead connection is the only thing that reports one.
const eventHeartbeat = 25 * time.Second

// SnapshotEvent is the payload of every event on the stream.
//
// It carries what a UI needs to decide whether to fetch and what to say in the
// meantime, and nothing else.
//
// There is one event type rather than two. A run starting is a change to the
// same state a new generation is a change to, and the client wants both, so
// splitting them would only give it two facts to reconcile that the cache had
// already reconciled — and a client that reconnected between them would see
// half the story.
type SnapshotEvent struct {
	Generation uint64 `json:"generation"`
	// CapturedAt is when that projection finished, or null when there is none.
	CapturedAt *time.Time `json:"captured_at"`
	// Stale reports that the most recent refresh failed, so the projection
	// predates it.
	Stale bool `json:"stale"`
	// Error is the most recent refresh error, or empty.
	Error string `json:"error"`
	// Refreshing reports a snapshot running right now, which is what lets the
	// UI show progress through a multi-second run rather than looking frozen.
	Refreshing bool `json:"refreshing"`
}

// registerEvents adds the live-update stream to the API mux.
func (r *Router) registerEvents(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/l/{slug}/events", r.apiEntry(r.apiEvents))
}

// apiEvents streams a snapshot event whenever the cache's published state
// changes.
//
// The filesystem watcher is deliberately not the trigger. It is noisy and
// internal — a burst of events for one mutation, and events for changes that
// turn out to project to the same thing — while the generation number is the
// loom's own answer to "is this different". Pushing off the watcher would mean
// pushing a change before there was one to fetch.
func (r *Router) apiEvents(w http.ResponseWriter, req *http.Request, entry *Entry) {
	if entry.Cache == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "no-cache", "this loom has no snapshot cache")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "no-streaming",
			"this server cannot stream: the response writer does not flush")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// A proxy that buffers turns a live stream into a slow one. There is not
	// meant to be a proxy in front of a loopback server, but saying so costs
	// one header.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Retry is the client's reconnect floor. app.js backs off further on top
	// of this; a browser with no script of its own still gets a sane one.
	fmt.Fprintf(w, "retry: %d\n\n", 2000)
	flusher.Flush()

	ctx := req.Context()
	// The first event is the state as it stands. A client that connects after
	// a change would otherwise sit on whatever it loaded until the next one.
	changed := entry.Cache.Changed()
	if !writeEvent(w, flusher, snapshotEvent(entry.Cache.Current())) {
		return
	}

	heartbeat := time.NewTicker(eventHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-changed:
			// Take the next channel before reading the state, so a change
			// landing between the two is still waiting on the next pass.
			next := entry.Cache.Changed()
			state := entry.Cache.Current()
			changed = next
			if !writeEvent(w, flusher, snapshotEvent(state)) {
				return
			}

		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": still here\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func snapshotEvent(state snapshot.CacheState) SnapshotEvent {
	event := SnapshotEvent{
		Generation: state.Generation,
		Stale:      state.Stale,
		Refreshing: state.Refreshing,
	}
	if state.Err != nil {
		event.Error = state.Err.Error()
	}
	if !state.CompletedAt.IsZero() {
		captured := state.CompletedAt
		event.CapturedAt = &captured
	}
	return event
}

// writeEvent sends one named event and reports whether the connection is still
// there. A write failure is a closed tab, which is not an error worth logging.
func writeEvent(w http.ResponseWriter, flusher http.Flusher, event SnapshotEvent) bool {
	body, err := json.Marshal(event)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", body); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

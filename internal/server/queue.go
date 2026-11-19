package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	loomcmd "github.com/zealtv/easyloom/internal/loom"
	"github.com/zealtv/easyloom/internal/snapshot"
)

// The queue's write half.
//
//	POST /api/l/<slug>/queue/append    {id}
//	POST /api/l/<slug>/queue/first     {id}
//	POST /api/l/<slug>/queue/before    {id, anchor}
//	POST /api/l/<slug>/queue/after     {id, anchor}
//	POST /api/l/<slug>/queue/unqueue   {id}
//
// Every one of them shells out to the deployed loom.sh through internal/loom.
// This package never edits .loom/queue: the file is protocol state, and the
// commands are the only supported way to change it.
//
// These are the first endpoints that block on a subprocess. That is not the
// rule the read endpoints break — a mutation is a rename and a queue rewrite,
// milliseconds against a map run's seconds-per-hundred-stitches — but it can
// still wait behind another writer holding the queue lock, which internal/loom
// bounds.
//
// # Why the browser gets a POST per command rather than a queue document
//
// The protocol's queue commands are relative: first, before, after. Accepting a
// whole queue file would mean this package deciding what the new order is, and
// two writers doing that concurrently is a lost update with nothing to detect
// it. One command per drop is what the loom already makes atomic.

// maxQueueBody bounds a request body. These carry two stitch IDs; anything
// larger is a mistake or an attack, and reading it to find out is the mistake.
const maxQueueBody = 4 << 10

// QueueRequest is the body of every queue endpoint. Anchor is used only by
// before and after.
type QueueRequest struct {
	ID     string `json:"id"`
	Anchor string `json:"anchor"`
}

// QueueResponse is what one queue mutation did, from the loom's own result
// rather than from a re-derived projection.
//
// A refresh has been asked for by the time this is written, but it has not
// landed: the new projection arrives over the events stream. Generation is the
// one the mutation was made against, so a caller can tell when it has been
// overtaken.
type QueueResponse struct {
	Command string `json:"command"`
	ID      string `json:"id"`
	// Changed is false for an accepted no-op — a queue write that left the file
	// byte-identical. It is not a failure, and a UI that treats it as one will
	// flap on every repeated drop.
	Changed bool `json:"changed"`
	// QueuePosition is the one-based position afterwards, or null when the
	// stitch is not queued.
	QueuePosition *int           `json:"queue_position"`
	State         snapshot.State `json:"state"`
	Path          string         `json:"path"`
	Tray          snapshot.Tray  `json:"tray"`
	// Structured is false when the deployed loom.sh predates --json, in which
	// case the fields above are empty and only the loom's own words are known.
	Structured bool   `json:"structured"`
	Generation uint64 `json:"generation"`
	// Stdout is the loom's own words about what it did.
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
}

// queueCommand is one endpoint: the loom command behind it, and whether it
// takes an anchor.
type queueCommand struct {
	name    string
	anchor  bool
	invoke  func(*loomcmd.Runner, *http.Request, QueueRequest) (*loomcmd.Result, error)
	command loomcmd.Command
}

var queueCommands = []queueCommand{
	{name: "append", command: loomcmd.Queue, invoke: func(r *loomcmd.Runner, req *http.Request, body QueueRequest) (*loomcmd.Result, error) {
		return r.Queue(req.Context(), body.ID)
	}},
	{name: "first", command: loomcmd.First, invoke: func(r *loomcmd.Runner, req *http.Request, body QueueRequest) (*loomcmd.Result, error) {
		return r.First(req.Context(), body.ID)
	}},
	{name: "before", command: loomcmd.Before, anchor: true, invoke: func(r *loomcmd.Runner, req *http.Request, body QueueRequest) (*loomcmd.Result, error) {
		return r.Before(req.Context(), body.ID, body.Anchor)
	}},
	{name: "after", command: loomcmd.After, anchor: true, invoke: func(r *loomcmd.Runner, req *http.Request, body QueueRequest) (*loomcmd.Result, error) {
		return r.After(req.Context(), body.ID, body.Anchor)
	}},
	{name: "unqueue", command: loomcmd.Unqueue, invoke: func(r *loomcmd.Runner, req *http.Request, body QueueRequest) (*loomcmd.Result, error) {
		return r.Unqueue(req.Context(), body.ID)
	}},
}

func (r *Router) registerQueue(mux *http.ServeMux) {
	for _, cmd := range queueCommands {
		mux.HandleFunc("POST /api/l/{slug}/queue/"+cmd.name, r.apiEntry(r.queueHandler(cmd)))
	}
}

func (r *Router) queueHandler(cmd queueCommand) func(http.ResponseWriter, *http.Request, *Entry) {
	return func(w http.ResponseWriter, req *http.Request, entry *Entry) {
		if entry.Mutator == nil {
			writeAPIError(w, http.StatusConflict, "read-only",
				"this loom is open read-only; no mutation was attempted")
			return
		}

		body, ok := decodeQueueRequest(w, req)
		if !ok {
			return
		}

		state := entry.Cache.Current()
		if state.Snapshot == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "no-snapshot",
				"this loom has no projection yet, so no stitch ID can be checked against it")
			return
		}
		if !validateQueueRequest(w, cmd, body, state.Snapshot) {
			return
		}

		result, err := cmd.invoke(entry.Mutator, req, body)
		if err != nil {
			writeQueueError(w, err)
			return
		}

		// A queue write can change stitches this result says nothing about —
		// every position after the moved one — and a tie or drop elsewhere
		// evicts IDs without anyone touching the queue. So the projection is
		// always re-taken, and the browser learns the new one over the events
		// stream rather than from this response.
		entry.Cache.Refresh()

		writeJSON(w, http.StatusOK, QueueResponse{
			Command:       string(cmd.command),
			ID:            result.ID,
			Changed:       result.Changed,
			QueuePosition: result.QueuePosition,
			State:         result.State,
			Path:          result.Path,
			Tray:          result.Tray,
			Structured:    result.Structured,
			Generation:    state.Generation,
			Stdout:        result.Stdout,
			Stderr:        result.Stderr,
		})
	}
}

func decodeQueueRequest(w http.ResponseWriter, req *http.Request) (QueueRequest, bool) {
	var body QueueRequest
	decoder := json.NewDecoder(io.LimitReader(req.Body, maxQueueBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad-request",
			fmt.Sprintf("the request body is not a queue command: %v", err))
		return body, false
	}
	return body, true
}

// validateQueueRequest refuses what the loom would refuse, before running
// anything.
//
// internal/loom checks the ID too and has the last word, but a browser drag
// that lands somewhere impossible should read as "that anchor is not on the
// queue" rather than as a subprocess failure.
func validateQueueRequest(w http.ResponseWriter, cmd queueCommand, body QueueRequest, snap *snapshot.Snapshot) bool {
	index := snap.Index()

	if body.ID == "" {
		writeAPIError(w, http.StatusBadRequest, "bad-request", "no stitch id was given")
		return false
	}
	// unqueue is the repair path: the protocol accepts any syntactically valid
	// ID for it, precisely so a record naming a stitch that is not there can be
	// removed. Every other command needs a stitch that exists.
	if _, ok := index[body.ID]; !ok && cmd.command != loomcmd.Unqueue {
		writeAPIError(w, http.StatusNotFound, "unknown-stitch",
			fmt.Sprintf("this loom's projection has no stitch %q", body.ID))
		return false
	}

	if !cmd.anchor {
		if body.Anchor != "" {
			writeAPIError(w, http.StatusBadRequest, "bad-request",
				fmt.Sprintf("%s takes no anchor", cmd.name))
			return false
		}
		return true
	}

	// The protocol's two rules for before and after, enforced here so a drag
	// that cannot mean anything says why rather than surfacing a raw refusal.
	if body.Anchor == "" {
		writeAPIError(w, http.StatusBadRequest, "bad-request",
			fmt.Sprintf("%s needs an anchor to move %q against", cmd.name, body.ID))
		return false
	}
	if body.Anchor == body.ID {
		writeAPIError(w, http.StatusConflict, "queue-anchor",
			fmt.Sprintf("%q cannot be moved against itself", body.ID))
		return false
	}
	anchor, ok := index[body.Anchor]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "unknown-stitch",
			fmt.Sprintf("there is no stitch %q to anchor against", body.Anchor))
		return false
	}
	if anchor.QueuePosition == nil {
		writeAPIError(w, http.StatusConflict, "queue-anchor",
			fmt.Sprintf("%q is not on the queue, so nothing can be placed relative to it",
				body.Anchor))
		return false
	}
	return true
}

// writeQueueError turns a refusal into the loom's own words.
//
// Stderr travels verbatim and separately from the message. An unrelated stale
// queue record makes an unrelated mutation fail *without changing the file*,
// and only the loom's own output explains that — a paraphrase would turn it
// into an inexplicable refusal.
func writeQueueError(w http.ResponseWriter, err error) {
	var idErr *loomcmd.IDError
	if errors.As(err, &idErr) {
		writeAPIError(w, http.StatusBadRequest, "invalid-stitch", idErr.Error())
		return
	}

	var refused *loomcmd.Error
	if errors.As(err, &refused) {
		body := apiErrorBody{
			Code:      "loom-refused",
			Message:   refused.Message,
			LoomCode:  string(refused.Code),
			StitchIDs: refused.StitchIDs,
			ExitCode:  refused.ExitCode,
		}
		if refused.Result != nil {
			body.Stderr = refused.Result.Stderr
		}
		if body.Message == "" {
			body.Message = "the loom refused the mutation and said nothing about why"
		}
		// The queue lock is held by another writer: a normal outcome, and the
		// one case where retrying the identical request is the right thing.
		status := http.StatusConflict
		if refused.Code == loomcmd.CodeQueueLocked {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, apiError{Error: body})
		return
	}

	writeAPIError(w, http.StatusBadGateway, "loom-failed", err.Error())
}

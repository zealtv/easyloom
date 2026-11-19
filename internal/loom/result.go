package loom

import (
	"fmt"
	"strings"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// SupportedSchemaVersion is the mutation result schema this model
// transcribes. It is independent of the map projection's: a mutation result
// carries its own version, and the two move separately.
const SupportedSchemaVersion = 1

// Result is what one mutation did.
//
// The typed fields come from the loom's own --json result, which is
// authoritative for the affected stitch: it is what the command did, not a
// re-derived projection. A caller may apply them directly rather than paying
// for a whole map run. It still has to re-snapshot eventually, because a tie
// or drop evicts newly-inactive IDs from the queue and that changes stitches
// this result says nothing about.
type Result struct {
	// Args is the argv that ran, script path first, for display and for
	// error messages.
	Args []string
	// Structured is false when the deployed script predates --json, in which
	// case only Args, ExitCode, Stdout and Stderr are filled in. A loom's own
	// loom.sh is its protocol version, so both are in the wild.
	Structured bool
	ExitCode   int
	// Stdout and Stderr are the command's output, trimmed. Stderr is kept
	// verbatim because an unrelated stale queue record makes a mutation fail
	// without changing anything, and only the loom's own words explain that.
	Stdout string
	Stderr string

	SchemaVersion int
	FormatVersion int
	// Command is the command name as the loom saw it.
	Command Command
	// Changed is false for an accepted no-op: claiming a claimed stitch,
	// tying a tied one, a queue write that leaves the file byte-identical.
	Changed bool
	// ID is the affected stitch.
	ID string
	// State, Path and Tray are the stitch afterwards, in the same vocabulary
	// as a projection stitch. They are empty only where the ID is not a
	// stitch on disk, which just unqueue's repair path reaches.
	State snapshot.State
	Path  string
	Tray  snapshot.Tray
	// QueuePosition is the one-based position afterwards, or nil when the
	// stitch is not queued — including when a tie or drop evicted it.
	QueuePosition *int
	// CompletedAt is set by a tie or drop, and empty otherwise.
	CompletedAt string

	// Drift reports that the result schema moved. The result is still usable:
	// the fields above decoded, and the loom's own output is on Stdout.
	Drift *DriftError

	// The failure fields travel inside the result the loom emitted, and reach
	// the caller as an *Error rather than as fields to be checked.
	errCode      ErrorCode
	errMessage   string
	errStitchIDs []string
}

// Queued reports whether the stitch is on the queue after the mutation.
func (r *Result) Queued() bool { return r.QueuePosition != nil }

// ErrorCode is a mutation failure's code. The set is open — a consumer must
// tolerate an unrecognised code rather than treat it as a parse failure — so
// these are documentation, not an enum to switch exhaustively over.
type ErrorCode string

const (
	CodeUsage              ErrorCode = "usage"
	CodeFormat             ErrorCode = "format"
	CodeInvalidID          ErrorCode = "invalid_id"
	CodeNotFound           ErrorCode = "not_found"
	CodeAmbiguous          ErrorCode = "ambiguous"
	CodeNotUnderThreads    ErrorCode = "not_under_threads"
	CodeTerminal           ErrorCode = "terminal"
	CodeWaiting            ErrorCode = "waiting"
	CodeTended             ErrorCode = "tended"
	CodeNotTended          ErrorCode = "not_tended"
	CodeNotWaiting         ErrorCode = "not_waiting"
	CodeNotReady           ErrorCode = "not_ready"
	CodeNotLooseEnd        ErrorCode = "not_loose_end"
	CodeNotChildBearing    ErrorCode = "not_child_bearing"
	CodeUnresolvedChildren ErrorCode = "unresolved_children"
	CodeClaimedDescendants ErrorCode = "claimed_descendants"
	CodeDestinationExists  ErrorCode = "destination_exists"
	CodeQueueAnchor        ErrorCode = "queue_anchor"
	CodeQueueLocked        ErrorCode = "queue_locked"
	CodeQueueRecords       ErrorCode = "queue_records"
	CodeWriteFailed        ErrorCode = "write_failed"
	CodeFailed             ErrorCode = "failed"
)

// Error reports a mutation the loom refused. Nothing changed on disk: a
// mutation that fails fails before its atomic rename.
type Error struct {
	Args []string
	// Code is empty when the deployed script predates --json and could only
	// say so in prose.
	Code     ErrorCode
	ExitCode int
	// Message is the loom's own words, verbatim.
	Message string
	// StitchIDs names the other stitches implicated: the unresolved children,
	// the claimed descendants, the blocking waiting ancestor.
	StitchIDs []string
	// Result is the full result, present whenever the command ran.
	Result *Result
}

func (e *Error) Error() string {
	head := strings.Join(e.Args, " ")
	switch {
	case e.Message != "" && e.Code != "":
		return fmt.Sprintf("%s: %s: %s", head, e.Code, e.Message)
	case e.Message != "":
		return fmt.Sprintf("%s: %s", head, e.Message)
	default:
		return fmt.Sprintf("%s: exit status %d", head, e.ExitCode)
	}
}

// DriftError reports that the mutation result did not match the schema this
// package transcribes. It means the schema moved, not that the data is
// unusable — an unrecognised error code is explicitly not drift.
type DriftError struct {
	Issues []string
}

func (e *DriftError) Error() string {
	return fmt.Sprintf("mutation result schema drift: %s", strings.Join(e.Issues, "; "))
}

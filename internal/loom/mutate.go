package loom

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zealtv/easyloom/internal/proc"
	"github.com/zealtv/easyloom/internal/snapshot"
)

// DefaultTimeout bounds one mutation. A mutation is a rename and a queue
// rewrite rather than a whole-loom walk, so it is quick — but it takes the
// queue lock, and waiting behind another writer is a normal outcome the loom
// reports as queue_locked. This is generous enough to let that resolve.
const DefaultTimeout = 30 * time.Second

// Command is a mutating loom command.
type Command string

// The lifecycle and queue commands. Every one of them accepts --json.
const (
	Claim   Command = "claim"
	Tend    Command = "tend"
	Release Command = "release"
	Wait    Command = "wait"
	Resume  Command = "resume"
	Tie     Command = "tie"
	Drop    Command = "drop"
	Queue   Command = "queue"
	First   Command = "first"
	Before  Command = "before"
	After   Command = "after"
	Unqueue Command = "unqueue"
)

// Runner invokes mutating commands against one loom.
//
// It holds no protocol state. What it knows about the loom is the script to
// run and a way to see the current projection, which it uses only to refuse an
// ID the loom does not have.
type Runner struct {
	// Script is the loom's address: the deployed script inside the .loom it
	// mutates, invoked by that absolute path and never resolved from $PATH,
	// because each deployment is the protocol version for its own loom.
	Script string
	// Dir is the working directory for the command.
	Dir string
	// Timeout bounds one mutation. Zero means DefaultTimeout.
	Timeout time.Duration
	// Snapshot returns the projection to validate IDs against, or nil when
	// there is none yet. It is a func because the caller's snapshot is
	// replaced wholesale by every refresh.
	Snapshot func() *snapshot.Snapshot

	// json remembers what the deployed script turned out to support: 0
	// unknown, 1 structured, -1 prose only. One refusal is enough to stop
	// paying for the extra run on every later mutation.
	json atomic.Int32
}

// New returns a Runner for one loom. The Runner remembers what its script
// supports, so callers should keep and share one rather than making them per
// mutation.
func New(script, dir string, current func() *snapshot.Snapshot) *Runner {
	return &Runner{Script: script, Dir: dir, Snapshot: current}
}

// The lifecycle commands.

func (r *Runner) Claim(ctx context.Context, id string) (*Result, error) {
	return r.run(ctx, Claim, []string{id})
}

func (r *Runner) Tend(ctx context.Context, id string) (*Result, error) {
	return r.run(ctx, Tend, []string{id})
}

func (r *Runner) Release(ctx context.Context, id string) (*Result, error) {
	return r.run(ctx, Release, []string{id})
}

func (r *Runner) Wait(ctx context.Context, id string) (*Result, error) {
	return r.run(ctx, Wait, []string{id})
}

func (r *Runner) Resume(ctx context.Context, id string) (*Result, error) {
	return r.run(ctx, Resume, []string{id})
}

func (r *Runner) Tie(ctx context.Context, id string) (*Result, error) {
	return r.run(ctx, Tie, []string{id})
}

// Drop drops a stitch with a reason. The protocol wants one — a dropped stitch
// contains reason.md — so the reason is a parameter here rather than an
// option, but an empty one is still passed through to the loom's own handling
// of it rather than being second-guessed.
//
// The reason is one argv element however many words it contains, so nothing in
// it can be read as an option or a second stitch ID.
func (r *Runner) Drop(ctx context.Context, id, reason string) (*Result, error) {
	if strings.TrimSpace(reason) == "" {
		return r.run(ctx, Drop, []string{id})
	}
	return r.run(ctx, Drop, []string{id}, reason)
}

// The queue commands.

func (r *Runner) Queue(ctx context.Context, id string) (*Result, error) {
	return r.run(ctx, Queue, []string{id})
}

func (r *Runner) First(ctx context.Context, id string) (*Result, error) {
	return r.run(ctx, First, []string{id})
}

// Before moves id to just before anchor in the queue. The moved ID comes
// first and the anchor second — getting that backwards silently reorders the
// wrong stitch, which is why neither caller is allowed to build this argv.
func (r *Runner) Before(ctx context.Context, id, anchor string) (*Result, error) {
	return r.run(ctx, Before, []string{id, anchor})
}

// After moves id to just after anchor. The moved ID comes first, as in Before.
func (r *Runner) After(ctx context.Context, id, anchor string) (*Result, error) {
	return r.run(ctx, After, []string{id, anchor})
}

func (r *Runner) Unqueue(ctx context.Context, id string) (*Result, error) {
	return r.run(ctx, Unqueue, []string{id})
}

// idPattern is the loom's own stitch ID grammar. Validating against it here
// turns an argv the loom would have to reject into a refusal that names the
// caller's mistake.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// IDError reports a stitch ID refused before anything ran.
type IDError struct {
	ID     string
	Reason string
}

func (e *IDError) Error() string { return fmt.Sprintf("stitch %q: %s", e.ID, e.Reason) }

// checkID refuses an ID the loom cannot have. A loom is a directory tree
// somebody else can write to, so an ID reaching this package is input even
// when it came from a projection.
//
// membership is false for the one command that must reach an ID the projection
// does not have: see run.
func (r *Runner) checkID(id string, membership bool) error {
	if !idPattern.MatchString(id) {
		return &IDError{ID: id, Reason: "not a stitch id (letters, numbers, ., _ and -)"}
	}
	// argv keeps a leading dash out of the shell, but not out of the loom's
	// own option parsing.
	if strings.HasPrefix(id, "-") {
		return &IDError{ID: id, Reason: "a stitch id may not start with a dash"}
	}
	if !membership {
		return nil
	}
	// No projection yet is not a reason to refuse: the check is here to turn a
	// confusing loom error into a clear one, and the loom still has the last
	// word either way.
	snap := r.current()
	if snap == nil {
		return nil
	}
	if _, ok := snap.Index()[id]; !ok {
		return &IDError{ID: id, Reason: "not in this loom"}
	}
	return nil
}

func (r *Runner) current() *snapshot.Snapshot {
	if r.Snapshot == nil {
		return nil
	}
	return r.Snapshot()
}

// run validates the stitch IDs, then invokes one command.
//
// ids are validated; rest is passed through untouched, which is what makes a
// drop reason literal. Everything is an argv element: this package never
// builds a shell string, never runs sh -c, and never interpolates an ID into a
// command.
func (r *Runner) run(ctx context.Context, cmd Command, ids []string, rest ...string) (*Result, error) {
	// unqueue is the repair path. The protocol accepts any syntactically valid
	// ID for it precisely so a record naming a stitch that is not there can be
	// removed, and a stale record makes every *other* queue mutation fail
	// without changing the file — so refusing it here would leave the one
	// command that fixes that unreachable. The shape check still applies.
	membership := cmd != Unqueue
	for _, id := range ids {
		if err := r.checkID(id, membership); err != nil {
			return nil, err
		}
	}
	args := make([]string, 0, len(ids)+len(rest))
	args = append(args, ids...)
	args = append(args, rest...)

	if r.json.Load() < 0 {
		return r.prose(ctx, cmd, args)
	}

	// --json is recognised only before the first positional argument.
	argv := append([]string{string(cmd), "--json"}, args...)
	stdout, stderr, runErr := r.exec(ctx, argv)
	if !proc.Started(runErr) {
		return nil, r.failed(argv, "", stderr, runErr)
	}

	res, ok, drift := decodeResult(stdout)
	if res == nil {
		if proc.ExitCode(runErr) == 0 {
			// It worked; there is just no typed result to read. Never run it
			// again to find out why — it already did whatever it did.
			return r.proseResult(cmd, argv, args, stdout, stderr, runErr), nil
		}
		// A script that predates --json read the flag as a stitch ID and
		// refused before changing anything, so running it again without the
		// flag is safe. Only once: after that the answer is known.
		if r.json.Load() == 0 {
			r.json.Store(-1)
			return r.prose(ctx, cmd, args)
		}
		return nil, r.failed(argv, stdout, stderr, runErr)
	}
	r.json.Store(1)

	res.Args = r.argv(argv)
	res.Structured = true
	res.ExitCode = proc.ExitCode(runErr)
	res.Stdout, res.Stderr = stdout, stderr
	res.Drift = drift

	if !ok {
		err := &Error{
			Args:      res.Args,
			Code:      res.errCode,
			ExitCode:  res.ExitCode,
			Message:   res.errMessage,
			StitchIDs: res.errStitchIDs,
			Result:    res,
		}
		if err.Message == "" {
			err.Message = stderr
		}
		return res, err
	}
	return res, nil
}

// prose runs a command against a deployed script with no --json, and reports
// what it can: the exit status and the loom's own words. The contract is "the
// mutation succeeded, or here is exactly what the loom said", which is honest
// without the flag. The caller re-snapshots to learn the new state.
func (r *Runner) prose(ctx context.Context, cmd Command, args []string) (*Result, error) {
	argv := append([]string{string(cmd)}, args...)
	stdout, stderr, runErr := r.exec(ctx, argv)

	res := r.proseResult(cmd, argv, args, stdout, stderr, runErr)
	if runErr != nil {
		return res, r.failed(argv, stdout, stderr, runErr)
	}
	return res, nil
}

// proseResult is everything knowable about a run with no typed result: the
// command, its output, and which stitch it was aimed at.
func (r *Runner) proseResult(cmd Command, argv, args []string, stdout, stderr string, runErr error) *Result {
	res := &Result{
		Args:     r.argv(argv),
		ExitCode: proc.ExitCode(runErr),
		Stdout:   stdout,
		Stderr:   stderr,
		Command:  cmd,
		// Prose cannot distinguish an accepted no-op from a change, and
		// claiming it did nothing would be the worse guess.
		Changed: true,
	}
	if len(args) > 0 {
		res.ID = args[0]
	}
	return res
}

// failed builds the error for a command that did not succeed and said nothing
// structured about why.
func (r *Runner) failed(argv []string, stdout, stderr string, runErr error) *Error {
	message := stderr
	if message == "" {
		message = stdout
	}
	if message == "" && runErr != nil {
		message = runErr.Error()
	}
	return &Error{
		Args:     r.argv(argv),
		ExitCode: proc.ExitCode(runErr),
		Message:  message,
	}
}

// argv is the command as it should be shown: the script's own path first, so
// there is never any doubt about which loom was mutated.
func (r *Runner) argv(args []string) []string {
	return append([]string{r.Script}, args...)
}

// exec runs the script and returns its output. It reuses the snapshot side's
// process handling: absolute script path, Dir at the loom root, a bounded
// context, separate buffers, and a kill that reaches the whole process group
// so a cancelled mutation leaves nothing behind.
func (r *Runner) exec(ctx context.Context, args []string) (stdout, stderr string, err error) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.Script, args...)
	cmd.Dir = r.Dir
	cmd.WaitDelay = proc.WaitDelay
	proc.KillGroup(cmd)

	var out, errs bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errs

	err = cmd.Run()
	return strings.TrimSpace(out.String()), strings.TrimSpace(errs.String()), err
}

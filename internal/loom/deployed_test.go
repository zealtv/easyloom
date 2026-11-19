package loom

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// The stub tests fix what EasyLoom sends and what it does with the answer.
// These run the real deployed loom.sh, because the one thing a stub cannot
// check is whether this package and the protocol agree — and a queue move with
// its arguments the wrong way round would pass every stub test in the file
// while reordering the wrong stitch.

// pinnedLoom is upstream loom's script at a pinned commit (see its README).
// The deployed-loom tests run it, so they need neither a network nor a loom
// installed, and a release tree tests exactly what this checkout does.
const pinnedLoom = "testdata/upstream-641e0da/loom.sh"

// deployedLoom builds a throwaway loom from the pinned upstream script and
// returns a Runner over it. The script is the loom's protocol version, so
// testing against a real one is the point.
func deployedLoom(t *testing.T, ids ...string) *Runner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("loom.sh needs a POSIX shell")
	}
	body, err := os.ReadFile(pinnedLoom)
	if err != nil {
		t.Fatalf("the pinned loom.sh is missing: %v", err)
	}

	root := filepath.Join(t.TempDir(), ".loom")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "loom.sh")
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(script, args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("loom.sh %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	for _, id := range ids {
		run("new", id)
	}

	runner := New(script, root, nil)
	runner.Snapshot = func() *snapshot.Snapshot {
		snap, err := (snapshot.Runner{Script: script, Dir: root}).Run(context.Background())
		if err != nil && snap == nil {
			t.Errorf("map --json: %v", err)
		}
		return snap
	}
	return runner
}

func TestDeployedLoomClaimAndTie(t *testing.T) {
	runner := deployedLoom(t, "alpha")
	ctx := context.Background()

	claimed, err := runner.Claim(ctx, "alpha")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !claimed.Structured {
		t.Fatal("the deployed script did not produce a structured result")
	}
	if claimed.State != snapshot.StateStitching {
		t.Errorf("state = %q, want stitching", claimed.State)
	}
	if claimed.Path != "threads/alpha.stitching" {
		t.Errorf("path = %q", claimed.Path)
	}
	if claimed.Tray != snapshot.TrayThreads {
		t.Errorf("tray = %q", claimed.Tray)
	}
	if !claimed.Changed {
		t.Error("changed = false on a real claim")
	}

	// Claiming a claimed stitch is an accepted no-op, not a failure.
	again, err := runner.Claim(ctx, "alpha")
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if again.Changed {
		t.Error("changed = true for an idempotent claim")
	}

	tied, err := runner.Tie(ctx, "alpha")
	if err != nil {
		t.Fatalf("Tie: %v", err)
	}
	// A goal stitch tie archives the whole tree, and completed-at is written
	// before the terminal move.
	if tied.State != snapshot.StateTied {
		t.Errorf("state = %q, want tied", tied.State)
	}
	if tied.CompletedAt == "" {
		t.Error("a tie reported no completed_at")
	}
}

// The argument order, against the loom itself.
func TestDeployedLoomQueueMoves(t *testing.T) {
	runner := deployedLoom(t, "alpha", "beta", "gamma")
	ctx := context.Background()

	for _, id := range []string{"alpha", "beta", "gamma"} {
		res, err := runner.Queue(ctx, id)
		if err != nil {
			t.Fatalf("Queue %s: %v", id, err)
		}
		if !res.Queued() {
			t.Fatalf("Queue %s reported no position", id)
		}
	}

	// gamma moves to just before alpha, which means gamma ends up first and
	// alpha is pushed down. Backwards, this would move alpha instead.
	moved, err := runner.Before(ctx, "gamma", "alpha")
	if err != nil {
		t.Fatalf("Before: %v", err)
	}
	if moved.ID != "gamma" {
		t.Fatalf("moved %q, want gamma", moved.ID)
	}
	if !moved.Queued() || *moved.QueuePosition != 1 {
		t.Errorf("gamma at %v, want 1", moved.QueuePosition)
	}
	if got := queuePosition(t, runner, "alpha"); got != 2 {
		t.Errorf("alpha at %d, want 2", got)
	}

	// And the same the other way: alpha after beta.
	if _, err := runner.After(ctx, "alpha", "beta"); err != nil {
		t.Fatalf("After: %v", err)
	}
	if got := queuePosition(t, runner, "alpha"); got != 3 {
		t.Errorf("alpha at %d, want 3 (after beta)", got)
	}

	unqueued, err := runner.Unqueue(ctx, "alpha")
	if err != nil {
		t.Fatalf("Unqueue: %v", err)
	}
	if unqueued.Queued() {
		t.Errorf("alpha still queued at %v", unqueued.QueuePosition)
	}
}

// An anchor that is not on the queue is the loom's refusal to make, not ours.
func TestDeployedLoomRefusesAnUnqueuedAnchor(t *testing.T) {
	runner := deployedLoom(t, "alpha", "beta")
	ctx := context.Background()

	if _, err := runner.Queue(ctx, "alpha"); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	_, err := runner.Before(ctx, "alpha", "beta")
	var mutErr *Error
	if !errors.As(err, &mutErr) {
		t.Fatalf("got %v, want *Error", err)
	}
	if mutErr.Code != CodeQueueAnchor {
		t.Errorf("code = %q, want queue_anchor", mutErr.Code)
	}
	if mutErr.Message == "" {
		t.Error("the loom's own message did not survive")
	}
}

// A drop writes reason.md, so the reason has to arrive as one literal
// argument rather than as extra words the loom has to guess at.
func TestDeployedLoomDropKeepsTheReason(t *testing.T) {
	runner := deployedLoom(t, "alpha")

	reason := "superseded by --json, twice over"
	res, err := runner.Drop(context.Background(), "alpha", reason)
	if err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if res.State != snapshot.StateDropped {
		t.Errorf("state = %q, want dropped", res.State)
	}
	written, err := os.ReadFile(filepath.Join(runner.Dir, res.Path, "reason.md"))
	if err != nil {
		t.Fatalf("reason.md: %v", err)
	}
	// The loom writes its own heading above the reason; what matters is that
	// the reason arrived whole rather than as words the loom had to reassemble.
	if !strings.Contains(string(written), reason) {
		t.Errorf("reason.md = %q, want it to contain %q", written, reason)
	}
}

func queuePosition(t *testing.T, runner *Runner, id string) int {
	t.Helper()
	snap := runner.Snapshot()
	if snap == nil {
		t.Fatal("no projection")
	}
	st, ok := snap.Index()[id]
	if !ok {
		t.Fatalf("%s is not in the projection", id)
	}
	if st.QueuePosition == nil {
		return 0
	}
	return *st.QueuePosition
}

// The repair path, against the loom itself. A stale queue record — an ID with
// no stitch behind it — makes every other queue mutation fail without changing
// the file, and unqueue is the only way out. The protocol says it accepts any
// syntactically valid ID for exactly that reason; this is that claim tested
// rather than trusted.
func TestDeployedLoomUnqueueRepairsAStaleRecord(t *testing.T) {
	runner := deployedLoom(t, "alpha")
	ctx := context.Background()

	if _, err := runner.Queue(ctx, "alpha"); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	// Write the stale record the way a person would: by hand, into the file.
	queue := filepath.Join(runner.Dir, "queue")
	body, err := os.ReadFile(queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queue, append(body, []byte("ghost-stitch\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	// It poisons an unrelated mutation, which is what makes it worth repairing.
	if _, err := runner.First(ctx, "alpha"); err == nil {
		t.Error("a stale record did not make an unrelated queue move fail")
	}

	if _, err := runner.Unqueue(ctx, "ghost-stitch"); err != nil {
		t.Fatalf("Unqueue could not repair the stale record: %v", err)
	}
	repaired, err := os.ReadFile(queue)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(repaired), "ghost-stitch") {
		t.Errorf("the record survived: %q", repaired)
	}
	if _, err := runner.First(ctx, "alpha"); err != nil {
		t.Errorf("the queue is still poisoned after the repair: %v", err)
	}
}

// A checkout that has its own deployed loom (a development checkout) must run
// the same script the tests pin, or the tests check a protocol this repository
// no longer speaks. A tree without one, such as a release, has nothing to drift.
func TestPinnedLoomMatchesTheDeployedOne(t *testing.T) {
	deployed, err := os.ReadFile(filepath.Join("..", "..", ".loom", "loom.sh"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("no deployed loom in this tree")
	}
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := os.ReadFile(pinnedLoom)
	if err != nil {
		t.Fatal(err)
	}
	if string(deployed) != string(pinned) {
		t.Errorf("the deployed .loom/loom.sh differs from %s: re-pin it (see that directory's README) after upgrading loom", pinnedLoom)
	}
}

package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/zealtv/easyloom/internal/loom"
	"github.com/zealtv/easyloom/internal/snapshot"
)

// threadProjection is the standard projection plus one unqueued thread with
// work beneath it:
//
//	web
//	├── a      needs b, which is not done yet
//	│   └── a1
//	├── b
//	└── c      tied, so nothing to queue
func threadProjection(root string) *snapshot.Snapshot {
	snap := projection(root)
	parent := func(id string) *string { return &id }
	snap.Stitches = append(snap.Stitches,
		snapshot.Stitch{ID: "web", RootID: "web", Path: "threads/web",
			Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Children: []string{"a", "b", "c"}},
		snapshot.Stitch{ID: "a", RootID: "web", ParentID: parent("web"), Path: "threads/web/a",
			Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Children: []string{"a1"},
			Dependencies: []snapshot.Dependency{{From: "a", To: "b", Status: snapshot.DependencyBlocked}}},
		snapshot.Stitch{ID: "a1", RootID: "web", ParentID: parent("a"), Path: "threads/web/a/a1",
			Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true},
		snapshot.Stitch{ID: "b", RootID: "web", ParentID: parent("web"), Path: "threads/web/b",
			Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true},
		snapshot.Stitch{ID: "c", RootID: "web", ParentID: parent("web"), Path: "threads/web/c",
			Tray: snapshot.TrayThreads, State: snapshot.StateTied},
	)
	return snap
}

// Children come before their parent and dependencies before what needs them,
// and finished work is left off.
func TestSubtreeOrderIsAnOrderTheWorkCanHappenIn(t *testing.T) {
	got := subtreeOrder(threadProjection(t.TempDir()), "web")
	if strings.Join(got, ",") != "a1,b,a,web" {
		t.Fatalf("subtreeOrder = %v, want a1,b,a,web", got)
	}
}

// A cycle cannot be ordered, but it must not lose anybody either.
func TestSubtreeOrderSurvivesACycle(t *testing.T) {
	snap := threadProjection(t.TempDir())
	index := snap.Index()
	index["b"].Dependencies = []snapshot.Dependency{{From: "b", To: "a", Status: snapshot.DependencyBlocked}}
	got := subtreeOrder(snap, "web")
	if len(got) != 4 || got[0] != "a1" {
		t.Fatalf("subtreeOrder = %v, want all four with a1 first", got)
	}
}

func threadModel(t *testing.T, snap *snapshot.Snapshot, body string) (Model, func() []string) {
	t.Helper()
	m, argv := mutableModelOver(t, snap, body)
	m.view = viewTree
	return moveToID(t, m, "web"), argv
}

// Queueing a thread queues the work beneath it first, one command per stitch,
// and the thread last.
func TestQueueingAThreadQueuesItsChildren(t *testing.T) {
	m, argv := threadModel(t, threadProjection(t.TempDir()), okJSON("queue"))
	if !offers(m, "queue with children") {
		t.Fatalf("palette offers %v", names(m))
	}
	m = act(t, m, "Q")

	want := []string{"queue --json a1 ", "queue --json b ", "queue --json a ", "queue --json web "}
	if got := argv(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if !strings.Contains(m.View(), "queued web and 3 children at the end") {
		t.Errorf("nothing said what happened:\n%s", m.View())
	}
}

// A child somebody already queued keeps its place.
func TestQueueingAThreadLeavesQueuedChildrenAlone(t *testing.T) {
	snap := threadProjection(t.TempDir())
	snap.Index()["b"].QueuePosition = position(3)
	m, argv := threadModel(t, snap, okJSON("queue"))
	m = act(t, m, "Q")

	want := []string{"queue --json a1 ", "queue --json a ", "queue --json web "}
	if got := argv(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// A thread that is already queued but has new children unqueued queues them
// and moves itself behind them, rather than coming off the queue.
func TestQueuedThreadPicksUpNewChildren(t *testing.T) {
	snap := threadProjection(t.TempDir())
	snap.Index()["web"].QueuePosition = position(3)
	m, argv := threadModel(t, snap, okJSON("queue"))
	m = act(t, m, "Q")

	want := []string{"queue --json a1 ", "queue --json b ", "queue --json a ", "queue --json web "}
	if got := argv(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// With everything beneath it queued, the thread toggles like any stitch.
func TestFullyQueuedThreadUnqueuesAlone(t *testing.T) {
	snap := threadProjection(t.TempDir())
	index := snap.Index()
	for i, id := range []string{"a1", "b", "a", "web"} {
		index[id].QueuePosition = position(3 + i)
	}
	m, argv := threadModel(t, snap, okJSON("unqueue"))
	if !offers(m, "unqueue") {
		t.Fatalf("palette offers %v", names(m))
	}
	m = act(t, m, "Q")
	if got := argv(); len(got) != 1 || got[0] != "unqueue --json web " {
		t.Fatalf("argv = %q, want unqueue web alone", got)
	}
}

// Undo takes the whole batch back off, not just the thread.
func TestUndoTakesTheChildrenBackOff(t *testing.T) {
	m, argv := threadModel(t, threadProjection(t.TempDir()), okJSON("queue"))
	m = act(t, m, "Q")
	m = act(t, m, "u")

	got := argv()[4:]
	want := []string{"unqueue --json a1 ", "unqueue --json b ", "unqueue --json a ", "unqueue --json web "}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("undo ran %q, want %q", got, want)
	}
}

// A batch refused partway has still changed the queue, and says how far it got.
func TestBatchRefusalSaysHowFarItGot(t *testing.T) {
	msg := mutationMsg{
		mutation: mutation{command: loom.Queue, id: "web", steps: []mutation{
			{command: loom.Queue, id: "a1"}, {command: loom.Queue, id: "b"}, {command: loom.Queue, id: "web"},
		}},
		err:  errors.New("queue_locked"),
		done: 1,
	}
	if got := refusal(msg); !strings.HasPrefix(got, "queue b failed after 1 of 3") {
		t.Fatalf("refusal = %q", got)
	}
}

package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
)

func broken(reason snapshot.BrokenReason) *snapshot.BrokenReason { return &reason }

// queueLoom is a loom whose queue says everything a queue can say: a head that
// is skipped, the row the loom would actually take, a record that can never
// become ready, one queued above its own dependency, a finished record and one
// naming a stitch that is not there at all.
func queueLoom(root string) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		SchemaVersion: snapshot.SupportedSchemaVersion,
		FormatVersion: snapshot.FormatVersionV2,
		LoomRoot:      root,
		Frontier:      []string{"http-server", "snapshot-refresh"},
		Diagnostics: []snapshot.Diagnostic{
			{Severity: snapshot.SeverityError, Code: "queue_error",
				Message: "terminal queue entry 'shell-skeleton' at position 5"},
			{Severity: snapshot.SeverityError, Code: "queue_error",
				Message: "unknown queue entry 'ghost-stitch' at position 6"},
		},
		Stitches: []snapshot.Stitch{
			{
				ID: "embedded-assets", RootID: "web-shell", Path: "threads/web-shell/embedded-assets",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, QueuePosition: position(1),
				Dependencies: []snapshot.Dependency{
					{From: "embedded-assets", To: "http-server", Status: snapshot.DependencyBlocked},
				},
			},
			{
				ID: "loom-url-routing", RootID: "web-shell", Path: "threads/web-shell/loom-url-routing.waiting",
				Tray: snapshot.TrayThreads, State: snapshot.StateWaiting, QueuePosition: position(2),
				Dependencies: []snapshot.Dependency{
					{From: "loom-url-routing", To: "snapshot-refresh", Status: snapshot.DependencyBlocked},
				},
			},
			{
				ID: "http-server", RootID: "web-shell", Path: "threads/web-shell/http-server",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true,
				QueuePosition: position(3),
			},
			{
				ID: "graph-layout", RootID: "graph-view", Path: "threads/graph-view/graph-layout",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, QueuePosition: position(4),
				Dependencies: []snapshot.Dependency{
					{From: "graph-layout", To: "gone", Status: snapshot.DependencyBroken,
						Reason: broken(snapshot.BrokenMissing)},
					{From: "graph-layout", To: "http-server", Status: snapshot.DependencyBlocked},
				},
			},
			{
				ID: "shell-skeleton", RootID: "web-shell", Path: "threads/web-shell/shell-skeleton.tied",
				Tray: snapshot.TrayThreads, State: snapshot.StateTied, QueuePosition: position(5),
				CompletedAt: text("2026-08-01T09:00:00+10:00"),
			},
			{
				ID: "snapshot-refresh", RootID: "snapshot-model", Path: "threads/snapshot-model/snapshot-refresh",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true,
			},
		},
	}
}

// queueModel is a model over a loom, already switched to the queue, with a
// mutator wired in.
func queueModel(t *testing.T, snap *snapshot.Snapshot, body string) (Model, func() []string) {
	t.Helper()
	cache := snapshot.NewCache(neverFinishes())
	cache.Seed(snap)
	t.Cleanup(cache.Close)
	mutator, argv := stubMutator(t, snap, body)
	m := New(Options{
		Target:  target.Target{Root: snap.LoomRoot, Script: mutator.Script},
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Mutator: mutator,
		Width:   120,
		Height:  40,
	})
	return m.switchView(), argv
}

func queueIDs(rows []queueRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	return ids
}

func rowFor(t *testing.T, m Model, id string) queueRow {
	t.Helper()
	for _, r := range m.queueRows() {
		if r.id == id {
			return r
		}
	}
	t.Fatalf("no queue row for %q in %v", id, queueIDs(m.queueRows()))
	return queueRow{}
}

// The queue is drawn in queue order, which is the projection's, and the record
// the loom has no stitch for keeps its place in it.
func TestQueueKeepsTheRecordOrder(t *testing.T) {
	m, _ := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))

	want := []string{
		"embedded-assets", "loom-url-routing", "http-server",
		"graph-layout", "shell-skeleton", "ghost-stitch",
	}
	if got := queueIDs(m.queueRows()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queue = %v\nwant %v", got, want)
	}
}

// The head of this queue is blocked, so the stitch the loom would actually
// hand over is three rows down. Marking row 1 would be a lie about what
// happens next.
func TestNextIsTheRowTheLoomWouldTake(t *testing.T) {
	m, _ := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))

	for _, r := range m.queueRows() {
		if r.next != (r.id == "http-server") {
			t.Errorf("%s: next = %v", r.id, r.next)
		}
	}
	if !strings.Contains(m.View(), "next") {
		t.Errorf("the row is not marked:\n%s", m.View())
	}
}

// A queue whose head is skipped is a queue that is not saying what it looks
// like it is saying, so it says how many it will pass over.
func TestQueueCountsWhatIsSkipped(t *testing.T) {
	m, _ := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))

	if _, c := m.queueRowsAndCounts(); c.queued != 6 || c.skipped != 5 {
		t.Fatalf("counts = %+v, want 6 queued and 5 skipped", c)
	}
	if !strings.Contains(m.View(), "6 queued · 5 skipped") {
		t.Errorf("the counts are not shown:\n%s", m.View())
	}
}

// A record naming a stitch the projection does not have is what makes an
// unrelated queue mutation fail without changing the file. The loom reports it;
// so does this.
func TestStaleRecordIsShownWithTheLoomsWords(t *testing.T) {
	m, _ := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))

	stale := rowFor(t, m, "ghost-stitch")
	if stale.stitch != nil {
		t.Fatal("the stale record was matched to a stitch")
	}
	if stale.position != 6 {
		t.Errorf("position = %d, want 6", stale.position)
	}
	if !strings.Contains(m.View(), "unknown queue entry 'ghost-stitch'") {
		t.Errorf("the diagnostic is not shown:\n%s", m.View())
	}

	// A diagnostic about a record the projection does have belongs on that
	// record's row rather than a second one.
	if got := rowFor(t, m, "shell-skeleton").problem; !strings.Contains(got, "terminal queue entry") {
		t.Errorf("problem = %q", got)
	}
}

// Not yet and never are different things. A broken dependency cannot resolve
// itself, so a queue holding one is a plan that cannot run.
func TestImpossibleRowSaysNever(t *testing.T) {
	m, _ := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))

	if got := rowFor(t, m, "graph-layout").impossible; !strings.Contains(got, "never") ||
		!strings.Contains(got, "gone is missing") {
		t.Fatalf("impossible = %q", got)
	}
	if got := rowFor(t, m, "embedded-assets").impossible; got != "" {
		t.Errorf("an ordinary blocked row says %q", got)
	}
}

// A dependency queued below its dependent, or not queued at all, is not
// impossible — but the queue is then no longer the order the work will happen
// in, which is the only thing the queue is for.
func TestInvertedRowsSayWhichWay(t *testing.T) {
	m, _ := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))

	if got := rowFor(t, m, "embedded-assets").inverted; !strings.Contains(got, "http-server is queued below") {
		t.Errorf("inverted = %q", got)
	}
	if got := rowFor(t, m, "loom-url-routing").inverted; !strings.Contains(got, "snapshot-refresh is not queued") {
		t.Errorf("inverted = %q", got)
	}
	if got := rowFor(t, m, "http-server").inverted; got != "" {
		t.Errorf("a row with nothing below it says %q", got)
	}
}

// The queue draining is a milestone finishing. This repo's own loom reaches
// that state, and an empty box would say nothing about it.
func TestEmptyQueueSaysTheMilestoneIsDone(t *testing.T) {
	m := modelOver(t, threads(t.TempDir())).switchView()

	if rows := m.queueRows(); len(rows) != 0 {
		t.Fatalf("queue = %v", queueIDs(rows))
	}
	if !strings.Contains(m.View(), "milestone") {
		t.Errorf("nothing said the queue had drained:\n%s", m.View())
	}
}

// In the queue every row is a record, so a move is against the row above or
// below — including one that is nowhere near the frontier, which is what the
// frontier's nearest-queued-neighbour could never do.
func TestQueueMovesAgainstTheNeighbouringRow(t *testing.T) {
	m, argv := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))
	if m.selectedID != "embedded-assets" {
		t.Fatalf("selected %q", m.selectedID)
	}

	m = act(t, m, "J")
	if got := argv(); len(got) != 1 || got[0] != "after --json embedded-assets loom-url-routing " {
		t.Fatalf("argv = %q, want the blocked neighbour as the anchor", got)
	}
	want := "loom-url-routing,embedded-assets,http-server"
	if got := strings.Join(queueIDs(m.queueRows())[:3], ","); got != want {
		t.Fatalf("the queue did not reorder on the keystroke: %v", got)
	}
}

// Nothing can go before the first record, so the top of the queue is first
// rather than before.
func TestMovingToTheTopIsFirst(t *testing.T) {
	m, argv := queueModel(t, queueLoom(t.TempDir()), okJSON("first"))
	m = m.moveTo(1)
	m = act(t, m, "K")

	if got := argv(); len(got) != 1 || got[0] != "first --json loom-url-routing " {
		t.Fatalf("argv = %q, want first", got)
	}
}

// A mutation nobody meant must not be silent. Every move says what it did,
// including the ones made from the frontier, where the anchor is not the row
// the eye is on.
func TestEveryMoveAnnouncesItself(t *testing.T) {
	m, _ := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))
	m = act(t, m, "J")
	if want := "moved embedded-assets after loom-url-routing"; !strings.Contains(m.notice, want) {
		t.Errorf("notice = %q, want %q", m.notice, want)
	}

	// The same move made from the frontier, where nothing on screen said
	// anything had happened before.
	f, _ := mutableModel(t, okJSON("after"))
	f = act(t, f, "J")
	want := "moved http-server after ci-workflow"
	if !strings.Contains(f.notice, want) {
		t.Errorf("notice = %q, want %q", f.notice, want)
	}
	if !strings.Contains(f.View(), want) {
		t.Errorf("the move is not on screen:\n%s", f.View())
	}
}

// The move that has already cost this repo: a stray J that put a stitch behind
// something that needs it. Announce it — never refuse it. Parking an item
// below its dependency on purpose is legitimate, and u is right there.
func TestAMoveThatInvertsTheQueueSaysSo(t *testing.T) {
	m, argv := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))
	m = m.moveTo(2)
	if m.selectedID != "http-server" {
		t.Fatalf("selected %q", m.selectedID)
	}

	m = act(t, m, "J")
	if got := argv(); len(got) != 1 {
		t.Fatalf("the move was refused locally: %q", got)
	}
	if want := "moved http-server after graph-layout — which needs it"; m.notice != want {
		t.Fatalf("notice = %q, want %q", m.notice, want)
	}
}

// A move that leaves an already-inverted pair inverted has invented nothing to
// report.
func TestAMoveOnlySaysWhatItChanged(t *testing.T) {
	m, _ := queueModel(t, queueLoom(t.TempDir()), okJSON("before"))
	m = m.moveTo(2)
	m = act(t, m, "K")

	if want := "moved http-server before loom-url-routing"; m.notice != want {
		t.Fatalf("notice = %q, want %q", m.notice, want)
	}
}

// u puts the last move back, by inverting it against the neighbour the stitch
// came from: one command, the same path as any other move.
func TestUndoPutsTheMoveBack(t *testing.T) {
	m, argv := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))
	m = m.moveTo(2)
	m = act(t, m, "J")
	m = act(t, m, "u")

	got := argv()
	if len(got) != 2 || got[1] != "after --json http-server loom-url-routing " {
		t.Fatalf("argv = %q, want the move put back after its old predecessor", got)
	}
	if ids := queueIDs(m.queueRows()); ids[2] != "http-server" {
		t.Errorf("the queue did not come back: %v", ids)
	}

	// One level is enough: this is a keystroke guard, not a history.
	m = act(t, m, "u")
	if got := argv(); len(got) != 2 {
		t.Fatalf("a second undo ran %q", got)
	}
	if !strings.Contains(m.notice, "nothing to undo") {
		t.Errorf("notice = %q", m.notice)
	}
}

// Undoing a move off the head of the queue is first, because that is where it
// came from.
func TestUndoOfAMoveOffTheHeadIsFirst(t *testing.T) {
	m, argv := queueModel(t, queueLoom(t.TempDir()), okJSON("after"))
	m = act(t, m, "J")
	m = act(t, m, "u")

	got := argv()
	if len(got) != 2 || got[1] != "first --json embedded-assets " {
		t.Fatalf("argv = %q, want first", got)
	}
}

// An undo is a mutation like any other. If the loom refuses it, show the
// refusal and leave the queue alone.
func TestRefusedUndoLeavesTheQueueAlone(t *testing.T) {
	refusal := "cat <<'JSON'\n" +
		`{"schema_version":1,"format_version":2,"command":"after","ok":false,"changed":false,` +
		`"id":"http-server","error":{"code":"queue_records",` +
		`"message":"unrelated queue record 'ghost-stitch' is stale"}}` + "\nJSON\nexit 1\n"
	m, _ := queueModel(t, queueLoom(t.TempDir()), refusal)
	before := queueIDs(m.queueRows())

	m = act(t, m, "J")
	if got := queueIDs(m.queueRows()); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("a refused move changed the queue: %v", got)
	}
	if !strings.Contains(m.View(), "unrelated queue record") {
		t.Errorf("the loom's words are not shown:\n%s", m.View())
	}
	// A move that did not happen is not a move to undo.
	if m.lastMove != nil {
		t.Error("a refused move was recorded as undoable")
	}
}

// A refusal rolls back to what was on screen before that move — not to the
// projection, which is several moves behind by then. Two moves land between
// snapshots easily: a refresh takes seconds on a real loom.
func TestARefusalRollsBackOneMoveNotAll(t *testing.T) {
	body := "case \"$1\" in\nafter)\n" + okJSON("after") + ";;\n*)\n" +
		"cat <<'JSON'\n" +
		`{"schema_version":1,"format_version":2,"command":"before","ok":false,` +
		`"changed":false,"id":"http-server","error":{"code":"queue_records",` +
		`"message":"unrelated queue record is stale"}}` + "\nJSON\nexit 1\n;;\nesac\n"

	m, _ := queueModel(t, queueLoom(t.TempDir()), body)
	m = m.moveTo(2)
	m = act(t, m, "J")
	moved := strings.Join(queueIDs(m.queueRows()), ",")

	m = m.moveTo(2)
	if m.selectedID != "graph-layout" {
		t.Fatalf("selected %q", m.selectedID)
	}
	m = act(t, m, "K")

	if got := strings.Join(queueIDs(m.queueRows()), ","); got != moved {
		t.Fatalf("the refusal rolled back too far:\n got %s\nwant %s", got, moved)
	}
}

// A tie or a drop evicts an ID from the queue without anyone touching the
// file, so the queue must not be assumed unchanged after one. A claim does
// not: the record stays where it is, and only stops being available.
func TestTieEvictsFromTheQueueAndAClaimDoesNot(t *testing.T) {
	m, _ := queueModel(t, queueLoom(t.TempDir()), okJSON("tie"))
	m = m.moveTo(2)
	m = act(t, m, "t")

	for _, r := range m.queueRows() {
		if r.id == "http-server" {
			t.Fatalf("a tied stitch is still queued: %v", queueIDs(m.queueRows()))
		}
	}

	m, _ = queueModel(t, queueLoom(t.TempDir()), okJSON("claim"))
	m = m.moveTo(2)
	m = act(t, m, "c")

	row := rowFor(t, m, "http-server")
	if !row.skipped || row.next {
		t.Errorf("a claimed stitch is still offered as next: %+v", row)
	}
}

// There is nothing to act on for a record with no stitch behind it, and
// silence would look like a broken key.
func TestKeysOnAStaleRecordExplainThemselves(t *testing.T) {
	m, argv := queueModel(t, queueLoom(t.TempDir()), okJSON("queue"))
	m = m.moveTo(5)
	if m.selectedID != "ghost-stitch" {
		t.Fatalf("selected %q", m.selectedID)
	}

	// No stitch, so nothing to act on: the palette offers no stitch
	// commands, and the detail pane says how to repair the record.
	if offers(m, "queue") || offers(m, "unqueue") || offers(m, "claim") {
		t.Errorf("a stale record was offered stitch commands: %v", names(m))
	}
	if got := argv(); got != nil {
		t.Fatalf("a stale record ran %q", got)
	}
	if !strings.Contains(m.View(), "unqueue") {
		t.Errorf("nothing said how to repair it:\n%s", m.View())
	}
}

// The queue pane builds no argv and never edits .loom/queue — not even to
// reorder it, which is exactly the temptation here. Every move is one loom
// command.
func TestQueueMovesAreOneLoomCommand(t *testing.T) {
	root := t.TempDir()
	m, argv := queueModel(t, queueLoom(root), okJSON("after"))
	m = act(t, m, "J")
	m = act(t, m, "u")

	for _, line := range argv() {
		if !strings.HasPrefix(line, "after ") && !strings.HasPrefix(line, "first ") {
			t.Errorf("unexpected command: %q", line)
		}
	}
	// Nothing under the loom root was touched: the queue changes because the
	// loom changed it, and for no other reason.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the pane wrote into the loom: %v", entries)
	}
}

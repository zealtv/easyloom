package tui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zealtv/easyloom/internal/loom"
	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
)

// stubMutator returns a Runner over a stand-in loom script that records its
// argv and answers with the given shell body. The TUI's whole write path ends
// in one of these argv lines, so recording them is how a keystroke is checked
// against the command it is supposed to be.
func stubMutator(t *testing.T, snap *snapshot.Snapshot, body string) (*loom.Runner, func() []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub loom script needs a POSIX shell")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "loom.sh")
	argv := filepath.Join(dir, "argv")
	preamble := "#!/usr/bin/env bash\nprintf '%s ' \"$@\" >> " + argv + "\nprintf '\\n' >> " + argv + "\n"
	if err := os.WriteFile(script, []byte(preamble+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return loom.New(script, dir, func() *snapshot.Snapshot { return snap }),
		func() []string {
			data, err := os.ReadFile(argv)
			if err != nil {
				return nil
			}
			return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		}
}

func okJSON(command string) string {
	return "cat <<'JSON'\n" +
		`{"schema_version":1,"format_version":2,"command":"` + command + `","ok":true,` +
		`"changed":true,"id":"x","state":"stitching","path":"threads/x","tray":"threads",` +
		`"queue_position":null,"completed_at":null}` + "\nJSON\n"
}

// neverFinishes is a snapshot source that never finishes. Every mutation asks for a
// refresh, so these tests need a source that can be asked — but one that
// publishes would replace the optimistic overlay they are about to inspect,
// which is the reconciliation TestNewGenerationDiscardsTheOverlay covers on
// its own.
func neverFinishes() snapshot.Source {
	return func(ctx context.Context) (*snapshot.Snapshot, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

// mutableModel is a model over the standard projection with a mutator wired in.
func mutableModel(t *testing.T, body string) (Model, func() []string) {
	t.Helper()
	snap := projection(t.TempDir())
	return mutableModelOver(t, snap, body)
}

func mutableModelOver(t *testing.T, snap *snapshot.Snapshot, body string) (Model, func() []string) {
	t.Helper()
	cache := snapshot.NewCache(neverFinishes())
	cache.Seed(snap)
	t.Cleanup(cache.Close)
	mutator, argv := stubMutator(t, snap, body)
	return New(Options{
		Target:  target.Target{Root: snap.LoomRoot, Script: mutator.Script},
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Mutator: mutator,
		Width:   100,
		Height:  40,
	}), argv
}

func moveToID(t *testing.T, m Model, id string) Model {
	t.Helper()
	for i, candidate := range m.ids() {
		if candidate == id {
			return m.moveTo(i)
		}
	}
	t.Fatalf("%s is not in %v", id, m.ids())
	return m
}

// press sends one key and runs whatever command it returned, so a mutation
// started in the background comes back the way the program would receive it.
func press(t *testing.T, m Model, key string) Model {
	t.Helper()
	next, cmd := m.Update(keyMsg(key))
	m = next.(Model)
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		if _, isTick := msg.(tickMsg); isTick {
			break
		}
		next, cmd = m.Update(msg)
		m = next.(Model)
	}
	return m
}

// waitFor blocks until the condition holds, which is how a background refresh
// is waited on without guessing at a sleep.
func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the refresh")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

// Each lifecycle key is one loom command against the selected stitch, and the
// TUI builds no argv of its own to get there.
func TestLifecycleKeysInvokeTheirCommand(t *testing.T) {
	for _, tc := range []struct{ key, command string }{
		{"c", "claim"},
		{"t", "tie"},
		{"w", "wait"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			m, argv := mutableModel(t, okJSON(tc.command))
			m = act(t, m, tc.key)

			want := tc.command + " --json http-server "
			if got := argv(); len(got) != 1 || got[0] != want {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

func TestTreeLifecycleKeysToggleDirectState(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		key        string
		command    string
		optimistic bool
		prepare    func(*snapshot.Snapshot)
	}{
		{name: "wait", id: "tui", key: "w", command: "wait", optimistic: true},
		{name: "unwait", id: "backlog", key: "w", command: "resume"},
		{name: "inherited wait stays wait", id: "backlog-timeline", key: "w", command: "wait", optimistic: true},
		{
			name: "direct and inherited wait unwaits", id: "backlog-timeline", key: "w", command: "resume",
			prepare: func(snap *snapshot.Snapshot) {
				st := snap.Index()["backlog-timeline"]
				st.Path = "threads/backlog.waiting/backlog-timeline.waiting"
				st.State = snapshot.StateWaiting
			},
		},
		{name: "tend", id: "tui", key: "T", command: "tend", optimistic: true},
		{
			name: "untend", id: "tui", key: "T", command: "release",
			prepare: func(snap *snapshot.Snapshot) {
				st := snap.Index()["tui"]
				st.Path = "threads/tui.tending"
				st.State = snapshot.StateTending
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := threads(t.TempDir())
			if tc.prepare != nil {
				tc.prepare(snap)
			}
			m, argv := mutableModelOver(t, snap, okJSON(tc.command))
			m = m.switchView().switchView()
			m = moveToID(t, m, tc.id)
			m = act(t, m, tc.key)

			want := tc.command + " --json " + tc.id + " "
			if got := argv(); len(got) != 1 || got[0] != want {
				t.Fatalf("argv = %q, want %q", got, want)
			}
			if got := !m.over.empty(); got != tc.optimistic {
				t.Errorf("optimistic overlay = %v, want %v", got, tc.optimistic)
			}
		})
	}
}

func TestLifecycleToggleHintsFollowTreeSelection(t *testing.T) {
	snap := threads(t.TempDir())
	m, _ := mutableModelOver(t, snap, okJSON("wait"))
	m = m.switchView().switchView()
	m = moveToID(t, m, "backlog")
	if !offers(m, "unpark") || !offers(m, "tend") || offers(m, "park") {
		t.Errorf("directly waiting tree stitch has the wrong toggles: %v", names(m))
	}
	m = moveToID(t, m, "backlog-timeline")
	if !offers(m, "park") {
		t.Errorf("inherited wait was offered resume: %v", names(m))
	}

	st := snap.Index()["tui"]
	st.Path = "threads/tui.tending"
	st.State = snapshot.StateTending
	m = moveToID(t, m, "tui")
	if !offers(m, "release") {
		t.Errorf("tended stitch was not offered release: %v", names(m))
	}
}

// Tend is the loom's to refuse, not the pane's: the palette offers it on
// whatever is selected, wherever.
func TestTendRunsFromAnyView(t *testing.T) {
	m, argv := mutableModel(t, okJSON("tend"))
	m = act(t, m, "T")
	if got := argv(); len(got) != 1 || !strings.HasPrefix(got[0], "tend ") {
		t.Fatalf("frontier tend ran %q", got)
	}
}

// The list has to respond on the keystroke: a claimed stitch is no longer a
// loose end, and waiting seconds for the snapshot to say so is what the
// optimistic overlay exists to avoid.
func TestClaimLeavesTheFrontierImmediately(t *testing.T) {
	m, _ := mutableModel(t, okJSON("claim"))
	if !strings.Contains(m.View(), "http-server") {
		t.Fatal("http-server is not on the frontier to begin with")
	}

	m = act(t, m, "c")

	rows := m.rows()
	for _, r := range rows {
		if r.id == "http-server" {
			t.Fatalf("http-server is still on the frontier: %v", rows)
		}
	}
	if _, c := m.rowsAndCounts(); c.ready != 3 {
		t.Errorf("ready = %d, want 3", c.ready)
	}
}

// A refusal rolls the optimistic change back and shows the loom's own words.
// An unrelated stale queue record makes a mutation fail without changing
// anything, and nothing else can explain that.
func TestRefusalRollsBackAndShowsTheLoomsWords(t *testing.T) {
	refusal := "cat <<'JSON'\n" +
		`{"schema_version":1,"format_version":2,"command":"tie","ok":false,"changed":false,` +
		`"id":"http-server","error":{"code":"not_loose_end",` +
		`"message":"http-server has unresolved children: docs","stitch_ids":["docs"]}}` +
		"\nJSON\nexit 1\n"
	m, _ := mutableModel(t, refusal)

	m = act(t, m, "t")

	var found bool
	for _, r := range m.rows() {
		found = found || r.id == "http-server"
	}
	if !found {
		t.Error("the refused stitch did not come back to the frontier")
	}
	view := m.View()
	if !strings.Contains(view, "http-server has unresolved children: docs") {
		t.Errorf("the loom's message is not shown:\n%s", view)
	}
	if !strings.Contains(view, "not_loose_end") {
		t.Errorf("the refusal code is not shown:\n%s", view)
	}
}

// Only loose ends can be claimed, and a tie on a stitch with unresolved
// children will be refused — by the loom. Keys are never greyed out on our own
// reading of state, because that reading would be the second state model the
// boundary forbids.
func TestKeysAreOfferedOnStitchesTheLoomMayRefuse(t *testing.T) {
	m, argv := mutableModel(t, okJSON("wait")+"exit 0\n")
	m = act(t, m, "w")
	if got := argv(); len(got) != 1 {
		t.Fatalf("the key was suppressed locally: argv = %q", got)
	}
	if !offers(m, "claim") {
		t.Error("the palette hides actions the loom might refuse")
	}
}

// A drop needs a reason — the protocol requires one — so it is the action that
// asks before it acts.
func TestDropPromptsForAReason(t *testing.T) {
	m, argv := mutableModel(t, okJSON("drop"))

	m = act(t, m, "d")
	if m.prompt == nil {
		t.Fatal("d did not open the prompt")
	}
	if got := argv(); got != nil {
		t.Fatalf("d ran a command before the reason: %q", got)
	}
	if !strings.Contains(m.View(), "drop http-server") {
		t.Errorf("the prompt is not shown:\n%s", m.View())
	}

	// The keys that are otherwise commands are just text while typing.
	for _, key := range []string{"w", "o", "n", "t", "backspace", "c", "o", "d", "e"} {
		m = press(t, m, key)
	}
	if m.prompt.reason != "woncode" {
		t.Fatalf("reason = %q", m.prompt.reason)
	}

	m = press(t, m, "enter")
	want := "drop --json http-server woncode "
	if got := argv(); len(got) != 1 || got[0] != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// A reason is prose, and it reaches reason.md as typed. Driving the built
// binary under a pty is what caught this: the space key arrives with its own
// rune, and adding another put a double space in a durable record.
func TestDropReasonKeepsItsSpacing(t *testing.T) {
	m, argv := mutableModel(t, okJSON("drop"))
	m = act(t, m, "d")
	for _, key := range []string{"n", "o", "t", " ", "w", "a", "n", "t", "e", "d"} {
		m = press(t, m, key)
	}
	if m.prompt.reason != "not wanted" {
		t.Fatalf("reason = %q", m.prompt.reason)
	}

	m = press(t, m, "enter")
	want := "drop --json http-server not wanted "
	if got := argv(); len(got) != 1 || got[0] != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestDropPromptCancels(t *testing.T) {
	m, argv := mutableModel(t, okJSON("drop"))
	m = act(t, m, "d")
	m = press(t, m, "x")
	m = press(t, m, "esc")

	if m.prompt != nil {
		t.Error("esc left the prompt open")
	}
	if got := argv(); got != nil {
		t.Fatalf("a cancelled drop ran %q", got)
	}
}

func TestDropWithoutAReasonDoesNotRun(t *testing.T) {
	m, argv := mutableModel(t, okJSON("drop"))
	m = act(t, m, "d")
	m = press(t, m, "enter")

	if got := argv(); got != nil {
		t.Fatalf("an empty reason ran %q", got)
	}
	if !strings.Contains(m.View(), "needs a reason") {
		t.Errorf("nothing explained why:\n%s", m.View())
	}
}

// Q is a toggle: a queued stitch comes off, an unqueued one goes on.
func TestQueueKeyToggles(t *testing.T) {
	m, argv := mutableModel(t, okJSON("unqueue"))
	m = act(t, m, "Q")
	if got := argv(); len(got) != 1 || got[0] != "unqueue --json http-server " {
		t.Fatalf("argv = %q, want unqueue", got)
	}

	// snapshot-refresh is on the frontier without a queue position.
	m, argv = mutableModel(t, okJSON("queue"))
	m = m.moveTo(2)
	if m.selectedID != "snapshot-refresh" {
		t.Fatalf("selected %q", m.selectedID)
	}
	m = act(t, m, "Q")
	if got := argv(); len(got) != 1 || got[0] != "queue --json snapshot-refresh " {
		t.Fatalf("argv = %q, want queue", got)
	}
}

// A move is one command, not a rewrite of the queue file. Moving up past the
// stitch at the top is first; moving down past a queued neighbour is after.
func TestQueueMovesAreOneCommand(t *testing.T) {
	m, argv := mutableModel(t, okJSON("after"))
	m = act(t, m, "J")
	if got := argv(); len(got) != 1 || got[0] != "after --json http-server ci-workflow " {
		t.Fatalf("argv = %q, want after with the anchor second", got)
	}

	m, argv = mutableModel(t, okJSON("first"))
	m = m.moveTo(1)
	m = act(t, m, "K")
	if got := argv(); len(got) != 1 || got[0] != "first --json ci-workflow " {
		t.Fatalf("argv = %q, want first", got)
	}
}

// The list reorders on the keystroke, before the loom has answered.
func TestQueueMoveReordersImmediately(t *testing.T) {
	m, _ := mutableModel(t, okJSON("after"))
	m = act(t, m, "J")

	rows := m.rows()
	if rows[0].id != "ci-workflow" || rows[1].id != "http-server" {
		t.Fatalf("rows = %v, want ci-workflow above http-server", rows)
	}
}

// The queue is sparse: unqueued ready stitches share the list but have no
// position, so a reorder key on one queues it rather than erroring.
func TestReorderQueuesAnUnqueuedStitch(t *testing.T) {
	m, argv := mutableModel(t, okJSON("queue"))
	m = m.moveTo(3)
	if m.selectedID != "docs" {
		t.Fatalf("selected %q, want the unqueued docs", m.selectedID)
	}
	m = act(t, m, "K")
	if got := argv(); len(got) != 1 || got[0] != "queue --json docs " {
		t.Fatalf("argv = %q, want queue", got)
	}
}

// At the top of the queue there is no anchor to move past. Say so rather than
// sending a command that cannot mean anything.
func TestReorderAtTheEdgeSaysSo(t *testing.T) {
	m, argv := mutableModel(t, okJSON("first"))
	m = act(t, m, "K")

	if got := argv(); got != nil {
		t.Fatalf("a move with no anchor ran %q", got)
	}
	if !strings.Contains(m.View(), "already at the top") {
		t.Errorf("nothing explained why:\n%s", m.View())
	}
}

// A mutation plus a refresh is a multi-second round trip on a big loom, and
// the UI must not look frozen while it happens.
func TestPendingMutationIsVisible(t *testing.T) {
	m, _ := mutableModel(t, okJSON("claim"))
	m = press(t, m, paletteKey)
	for _, r := range "claim" {
		m = press(t, m, string(r))
	}
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(Model)

	if m.pending == nil {
		t.Fatal("nothing recorded as running")
	}
	if !strings.Contains(m.View(), "claiming http-server") {
		t.Errorf("the running mutation is not shown:\n%s", m.View())
	}

	// A second mutation while one is in flight is refused rather than racing
	// it for the queue lock.
	if second, _ := m.mutate("t"); second.pending.command != m.pending.command {
		t.Error("a second mutation displaced the one in flight")
	}

	if cmd != nil {
		next, _ = m.Update(cmd())
		m = next.(Model)
	}
	if m.pending != nil {
		t.Error("the mutation is still shown as running")
	}
}

// A mutation makes the snapshot stale immediately — a tie or drop can evict
// IDs from the queue without anyone touching it — so one is asked for as soon
// as the command returns.
func TestMutationTriggersARefresh(t *testing.T) {
	snap := projection(t.TempDir())
	refreshed := make(chan struct{}, 1)
	cache := snapshot.NewCache(func(ctx context.Context) (*snapshot.Snapshot, error) {
		refreshed <- struct{}{}
		return snap, nil
	})
	cache.Seed(snap)
	t.Cleanup(cache.Close)
	mutator, _ := stubMutator(t, snap, okJSON("claim"))
	m := New(Options{
		Target:  target.Target{Root: snap.LoomRoot, Script: mutator.Script},
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Mutator: mutator,
		Width:   100,
		Height:  40,
	})

	m = act(t, m, "c")
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("no refresh followed the mutation")
	}
}

// The projection is authoritative. When a newer one lands it replaces
// everything the overlay believed, silently, agreeing or not.
func TestNewGenerationDiscardsTheOverlay(t *testing.T) {
	snap := projection(t.TempDir())
	cache := snapshot.NewCache(func(context.Context) (*snapshot.Snapshot, error) {
		return snap, nil
	})
	cache.Seed(snap)
	t.Cleanup(cache.Close)
	mutator, _ := stubMutator(t, snap, okJSON("claim"))
	m := New(Options{
		Target:  target.Target{Root: snap.LoomRoot, Script: mutator.Script},
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Mutator: mutator,
		Width:   100,
		Height:  40,
	})

	m = act(t, m, "c")
	if len(m.rows()) != 3 {
		t.Fatalf("the overlay did not apply: %v", m.rows())
	}

	// The refresh the mutation asked for lands, and says http-server is a
	// loose end after all. The projection wins.
	waitFor(t, func() bool { return cache.Current().Generation > 1 })
	next, _ := m.Update(tickMsg(time.Now()))
	m = next.(Model)

	if len(m.rows()) != 4 {
		t.Fatalf("the overlay outlived its generation: %v", m.rows())
	}
	if !m.over.empty() {
		t.Error("the overlay was kept")
	}
}

// A loom opened without a mutator says so rather than looking broken.
func TestReadOnlySessionSaysSo(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	m = act(t, m, "c")
	if !strings.Contains(m.View(), "read-only") {
		t.Errorf("nothing explained why the key did nothing:\n%s", m.View())
	}
}

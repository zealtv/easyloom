package tui

import (
	"fmt"
	"math"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
)

func parent(id string) *string { return &id }

// threads is a loom with shape: a thread carrying tied work inline, a blocked
// stitch, a ready one, a parked branch, a decomposition cycle that must not
// hang the walk, and archived and legacy records to be counted rather than
// drawn.
func threads(root string) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		SchemaVersion: snapshot.SupportedSchemaVersion,
		FormatVersion: snapshot.FormatVersionV2,
		LoomRoot:      root,
		Frontier:      []string{"tui-thread-tree"},
		DecompositionEdges: []snapshot.Decomposition{
			{Parent: "tui", Child: "tui-shell"},
			{Parent: "tui", Child: "tui-frontier-pane"},
			{Parent: "tui", Child: "tui-lifecycle-keys"},
			{Parent: "tui", Child: "tui-thread-tree"},
			{Parent: "tui-frontier-pane", Child: "pane-notes"},
			{Parent: "pane-notes", Child: "pane-detail"},
			{Parent: "backlog", Child: "backlog-timeline"},
			{Parent: "loop", Child: "loop-child"},
			{Parent: "loop-child", Child: "loop"},
		},
		Stitches: []snapshot.Stitch{
			{
				ID: "tui", RootID: "tui", Path: "threads/tui", Tray: snapshot.TrayThreads,
				State: snapshot.StatePlain,
				Children: []string{"tui-shell", "tui-frontier-pane",
					"tui-lifecycle-keys", "tui-thread-tree"},
			},
			{
				ID: "tui-shell", RootID: "tui", ParentID: parent("tui"),
				Path: "threads/tui/tui-shell.tied", Tray: snapshot.TrayThreads,
				State: snapshot.StateTied, CompletedAt: text("2026-07-30T09:00:00+10:00"),
			},
			{
				ID: "tui-frontier-pane", RootID: "tui", ParentID: parent("tui"),
				Path: "threads/tui/tui-frontier-pane.tied", Tray: snapshot.TrayThreads,
				State: snapshot.StateTied, Children: []string{"pane-notes"},
			},
			{
				ID: "pane-notes", RootID: "tui", ParentID: parent("tui-frontier-pane"),
				Path: "threads/tui/tui-frontier-pane.tied/pane-notes.tied",
				Tray: snapshot.TrayThreads, State: snapshot.StateTied,
				Children: []string{"pane-detail"},
			},
			{
				ID: "pane-detail", RootID: "tui", ParentID: parent("pane-notes"),
				Path: "threads/tui/tui-frontier-pane.tied/pane-notes.tied/pane-detail.tied",
				Tray: snapshot.TrayThreads, State: snapshot.StateTied,
			},
			{
				ID: "tui-lifecycle-keys", RootID: "tui", ParentID: parent("tui"),
				Path: "threads/tui/tui-lifecycle-keys", Tray: snapshot.TrayThreads,
				State: snapshot.StatePlain,
				Dependencies: []snapshot.Dependency{
					{From: "tui-lifecycle-keys", To: "mutation-exec", Status: snapshot.DependencyBlocked},
				},
			},
			{
				ID: "tui-thread-tree", RootID: "tui", ParentID: parent("tui"),
				Path: "threads/tui/tui-thread-tree", Tray: snapshot.TrayThreads,
				State: snapshot.StatePlain, Ready: true,
			},
			{
				ID: "backlog", RootID: "backlog", Path: "threads/backlog.waiting",
				Tray: snapshot.TrayThreads, State: snapshot.StateWaiting,
				Children: []string{"backlog-timeline", "backlog-tui"},
			},
			{
				ID: "backlog-tui", RootID: "backlog", ParentID: parent("backlog"),
				Path: "threads/backlog.waiting/backlog-tui.dropped", Tray: snapshot.TrayThreads,
				State: snapshot.StateDropped, WaitingInherited: true,
			},
			{
				ID: "backlog-timeline", RootID: "backlog", ParentID: parent("backlog"),
				Path: "threads/backlog.waiting/backlog-timeline", Tray: snapshot.TrayThreads,
				State: snapshot.StatePlain, WaitingInherited: true,
			},
			// Decomposition is a tree by construction, but a projection is a
			// document and a document can say anything.
			{
				ID: "loop", RootID: "loop", Path: "threads/loop", Tray: snapshot.TrayThreads,
				State: snapshot.StatePlain, Children: []string{"loop-child"},
			},
			{
				ID: "loop-child", RootID: "loop", ParentID: parent("loop"),
				Path: "threads/loop/loop-child", Tray: snapshot.TrayThreads,
				State: snapshot.StatePlain, Children: []string{"loop"},
			},
			{
				ID: "done", RootID: "done", Path: "tied/done", Tray: snapshot.TrayTied,
				State: snapshot.StateTied, Archived: true,
			},
			{
				ID: "old", RootID: "old", Path: "legacy-v1/tied/old", Tray: snapshot.TrayLegacyTied,
				State: snapshot.StateTied, Archived: true, Legacy: true,
			},
		},
	}
}

// treeModel is a model over the shaped loom, already switched to the tree.
func treeModel(t *testing.T, snap *snapshot.Snapshot) Model {
	t.Helper()
	m := modelOver(t, snap)
	return m.switchView().switchView()
}

func treeIDs(rows []treeRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	return ids
}

// The tree is what makes a mature loom legible: every active stitch, in its
// thread, under the goal it decomposes from.
func TestTreeShowsEveryActiveStitchInItsThread(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	rows := m.treeRows()

	want := []string{
		"tui", "tui-shell", "tui-frontier-pane", "tui-lifecycle-keys", "tui-thread-tree",
		"backlog", "backlog-timeline", "backlog-tui", "loop", "loop-child",
	}
	if got := strings.Join(treeIDs(rows), ","); got != strings.Join(want, ",") {
		t.Fatalf("tree = %s\nwant %s", got, strings.Join(want, ","))
	}
	for _, r := range rows {
		depth := map[string]int{"tui": 0, "tui-shell": 1, "tui-frontier-pane": 1,
			"tui-lifecycle-keys": 1, "tui-thread-tree": 1, "backlog": 0,
			"backlog-timeline": 1, "backlog-tui": 1, "loop": 0, "loop-child": 1}
		if r.depth != depth[r.id] {
			t.Errorf("%s at depth %d, want %d", r.id, r.depth, depth[r.id])
		}
	}
}

// Terminal children stay in place in v2, so a tree contains completed work
// inline. A tied subtree folds by default, and says how much it folded.
func TestTiedSubtreesFoldByDefault(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	rows := m.treeRows()

	for _, r := range rows {
		if r.id == "pane-notes" || r.id == "pane-detail" {
			t.Fatalf("a tied subtree was drawn: %v", treeIDs(rows))
		}
		if r.id == "tui-frontier-pane" {
			if r.expanded {
				t.Error("the tied node is expanded")
			}
			if r.folded != 2 {
				t.Errorf("folded = %d, want 2", r.folded)
			}
		}
	}
	if !strings.Contains(m.View(), "2 folded") {
		t.Errorf("the fold is silent:\n%s", m.View())
	}
	// A tied leaf is still drawn where it happened, badged for what it is.
	if !strings.Contains(m.View(), "tied") {
		t.Errorf("completed work is not badged:\n%s", m.View())
	}
}

// A background re-snapshot must not fold the tree up under the user, so the
// choice is remembered by ID rather than by position.
func TestExpansionIsRememberedByID(t *testing.T) {
	root := t.TempDir()
	m := treeModel(t, threads(root))
	m = m.moveTo(2)
	if m.selectedID != "tui-frontier-pane" {
		t.Fatalf("selected %q", m.selectedID)
	}

	// Folding is space and ←/→; enter hands the stitch to $EDITOR.
	m = press(t, m, " ")
	if !strings.Contains(strings.Join(treeIDs(m.treeRows()), ","), "pane-notes") {
		t.Fatalf("space did not expand the node: %v", treeIDs(m.treeRows()))
	}

	// A new snapshot lands with the stitches in a different order.
	next := threads(root)
	next.Stitches[0], next.Stitches[9] = next.Stitches[9], next.Stitches[0]
	m.state.Snapshot = next
	m.state.Generation++
	m = m.reselect()

	if !strings.Contains(strings.Join(treeIDs(m.treeRows()), ","), "pane-notes") {
		t.Errorf("the refresh folded the tree back up: %v", treeIDs(m.treeRows()))
	}
	if m.selectedID != "tui-frontier-pane" {
		t.Errorf("selection moved to %q", m.selectedID)
	}
}

func TestCollapseAndExpandKeys(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	if len(m.treeRows()) != 10 {
		t.Fatalf("unexpected starting tree: %v", treeIDs(m.treeRows()))
	}

	// The goal stitch is selected first, and collapsing it hides its thread.
	m = press(t, m, "left")
	if got := treeIDs(m.treeRows()); len(got) != 6 {
		t.Fatalf("left did not collapse the thread: %v", got)
	}
	m = press(t, m, "right")
	if got := treeIDs(m.treeRows()); len(got) != 10 {
		t.Fatalf("right did not expand it again: %v", got)
	}
	// A collapsed goal says how much of it is still to do.
	m = press(t, m, "left")
	if !strings.Contains(m.View(), "2 unresolved") {
		t.Errorf("a collapsed thread does not say what is left:\n%s", m.View())
	}
}

// A directly waiting stitch is a decision that can be resumed; an inherited
// wait is a consequence that cannot. Drawing them the same way invites a
// resume the loom will refuse.
func TestWaitingIsDistinguishedFromInherited(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	rows := m.treeRows()

	var direct, inherited, finished string
	for _, r := range rows {
		switch r.id {
		case "backlog":
			direct = m.treeStatus(r, m.treeLook(r))
		case "backlog-timeline":
			inherited = m.treeStatus(r, m.treeLook(r))
		case "backlog-tui":
			finished = m.treeStatus(r, m.treeLook(r))
		}
	}
	// Finished work under a parked parent inherits the wait too, and saying so
	// about something already dropped is noise.
	if strings.Contains(finished, "waiting") {
		t.Errorf("a dropped stitch rendered as %q", finished)
	}
	if !strings.Contains(direct, "waiting") || strings.Contains(direct, "inherited") {
		t.Errorf("directly waiting rendered as %q", direct)
	}
	if !strings.Contains(inherited, "waiting inherited") {
		t.Errorf("inherited wait rendered as %q", inherited)
	}
}

// A row's look carries how live a stitch is, so what is left to do reads down
// the tree without reading the status column. Finished work under a parked
// parent is finished first: dropped wins over the wait it inherited.
func TestRowsLookAsLiveAsTheirStitch(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	m.styles = newStyles(true)
	s := m.styles

	want := map[string]look{
		"tui":                {"○", s.faint, s.goal},
		"tui-shell":          {"✓", s.faint, s.faint},
		"backlog-tui":        {"✗", s.faint, s.struck},
		"backlog":            {"‖", s.waiting, s.parked},
		"backlog-timeline":   {"‖", s.faint, s.parked},
		"tui-thread-tree":    {"●", s.ready, s.live},
		"tui-lifecycle-keys": {"○", s.warn, s.live},
	}
	same := func(a, b lipgloss.Style) bool {
		return a.GetBold() == b.GetBold() && a.GetItalic() == b.GetItalic() &&
			a.GetStrikethrough() == b.GetStrikethrough() &&
			a.GetForeground() == b.GetForeground()
	}
	for _, r := range m.treeRows() {
		look, ok := want[r.id]
		if !ok {
			continue
		}
		delete(want, r.id)
		got := m.treeLook(r)
		if got.glyph != look.glyph || !same(got.hue, look.hue) || !same(got.name, look.name) {
			t.Errorf("%s: drawn as %q %v %v, want %q %v %v",
				r.id, got.glyph, got.hue, got.name, look.glyph, look.hue, look.name)
		}
	}
	for id := range want {
		t.Errorf("%s was not drawn", id)
	}

	// Live names are never bold below the goal: a tree of mostly-ready work
	// drawn bold is a wall.
	if s.ready.GetBold() {
		t.Error("ready is bold")
	}
	if !s.struck.GetStrikethrough() || s.faint.GetStrikethrough() {
		t.Error("dropped and tied are not told apart")
	}
}

// Faded rows must stay faded on a terminal that enforces minimum contrast,
// which pushes a colour under its floor most of the way to white. Ghostty's
// minimum-contrast = 4.5 against a near-black background is the case that
// showed it: 240 rendered brighter than the live names.
func TestFaintClearsMinimumContrast(t *testing.T) {
	luminance := func(hex string) float64 {
		var rgb [3]float64
		for i := range rgb {
			var v int
			fmt.Sscanf(hex[1+2*i:3+2*i], "%02x", &v)
			c := float64(v) / 255
			if c <= 0.04045 {
				rgb[i] = c / 12.92
			} else {
				rgb[i] = math.Pow((c+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
	}
	contrast := func(a, b string) float64 {
		la, lb := luminance(a), luminance(b)
		return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
	}
	// xterm's 256-colour greys, 232 to 255, step 10 from #080808.
	grey := func(code string) string {
		var n int
		fmt.Sscanf(code, "%d", &n)
		v := 8 + 10*(n-232)
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}

	faint, ok := newStyles(true).faint.GetForeground().(lipgloss.AdaptiveColor)
	if !ok {
		t.Fatal("faint is not adaptive to the background")
	}
	if c := contrast(grey(faint.Dark), "#0d0e1c"); c < 4.5 {
		t.Errorf("faint %s on dark: %.2f:1", faint.Dark, c)
	}
	if c := contrast(grey(faint.Light), "#ffffff"); c < 4.5 {
		t.Errorf("faint %s on light: %.2f:1", faint.Light, c)
	}
}

// A parent says how far through its thread it is, folded or not.
func TestParentsMeasureTheirProgress(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	for _, r := range m.treeRows() {
		if r.id != "tui" {
			continue
		}
		// tui-shell, tui-frontier-pane, pane-notes and pane-detail are tied;
		// tui-lifecycle-keys and tui-thread-tree are not.
		if r.done != 4 || r.total != 6 {
			t.Errorf("tui measured %d/%d, want 4/6", r.done, r.total)
		}
		if got := m.treeStatus(r, m.treeLook(r)); !strings.Contains(got, "4/6 ━━━━━───") {
			t.Errorf("tui's meter rendered as %q", got)
		}
		return
	}
	t.Fatal("tui was not drawn")
}

// A finished stitch has nothing left to need.
func TestFinishedStitchesDoNotSayWhatTheyNeed(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	for _, r := range m.treeRows() {
		if r.id != "tui-lifecycle-keys" {
			continue
		}
		st := *r.stitch
		st.State = snapshot.StateDropped
		r.stitch = &st
		if got := m.treeStatus(r, m.treeLook(r)); strings.Contains(got, "needs") {
			t.Errorf("a dropped stitch rendered as %q", got)
		}
		return
	}
	t.Fatal("tui-lifecycle-keys was not drawn")
}

// Ready is the actionable set, and it is the one thing this pane shares with
// the frontier.
func TestReadyAndBlockedAreMarked(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	view := m.View()
	if !strings.Contains(view, "● tui-thread-tree") || !strings.Contains(view, "ready") {
		t.Errorf("ready is not marked:\n%s", view)
	}
	if !strings.Contains(view, "needs mutation-exec") {
		t.Errorf("a blocked stitch does not say what it needs:\n%s", view)
	}
}

// The same filter as every other view, and the same duty to say how much it
// hid. On bopOS's loom that is 88% of the records.
func TestArchivedAndLegacyAreCountedNotDrawn(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	rows := m.treeRows()
	for _, r := range rows {
		if r.id == "done" || r.id == "old" {
			t.Fatalf("an archived record reached the tree: %v", treeIDs(rows))
		}
	}
	view := m.View()
	for _, want := range []string{"1 ready", "2 waiting", "2 hidden"} {
		if !strings.Contains(view, want) {
			t.Errorf("counts missing %q:\n%s", want, view)
		}
	}
}

// map --json reports cycles as diagnostics, and decomposition is a tree by
// construction — but a repeated ID must not hang the render either way.
func TestCycleDoesNotHangTheWalk(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	ids := treeIDs(m.treeRows())
	if strings.Count(strings.Join(ids, ","), "loop-child") != 1 {
		t.Fatalf("the cycle was walked twice: %v", ids)
	}
}

// Each of the four views keeps its own place, so tabbing round does not lose
// any of them.
func TestTabCyclesTheViewsAndKeepsEachPlace(t *testing.T) {
	m := modelOver(t, threads(t.TempDir()))
	if m.selectedID != "tui-thread-tree" {
		t.Fatalf("frontier selected %q", m.selectedID)
	}

	m = press(t, m, "tab")
	if m.view != viewQueue {
		t.Fatal("tab did not switch to the queue")
	}
	m = press(t, m, "tab")
	if m.view != viewTree {
		t.Fatal("tab did not go on to the tree")
	}
	m = m.moveTo(5)
	if m.selectedID != "backlog" {
		t.Fatalf("tree selected %q", m.selectedID)
	}

	m = press(t, m, "tab")
	if m.view != viewImpact {
		t.Fatal("tab did not go on to the impact pane")
	}

	m = press(t, m, "tab")
	if m.view != viewFrontier || m.selectedID != "tui-thread-tree" {
		t.Fatalf("the frontier came back as %q", m.selectedID)
	}
	m = press(t, m, "tab")
	m = press(t, m, "tab")
	if m.view != viewTree || m.selectedID != "backlog" {
		t.Fatalf("the tree came back as %q", m.selectedID)
	}
}

// What the tree earns for the lifecycle keys: w, resume and d are most useful
// on stitches that are not ready, and this is the only view they can be
// selected in.
func TestLifecycleKeysReachAStitchOffTheFrontier(t *testing.T) {
	snap := threads(t.TempDir())
	cache := snapshot.NewCache(neverFinishes())
	cache.Seed(snap)
	t.Cleanup(cache.Close)
	mutator, argv := stubMutator(t, snap, okJSON("wait"))
	m := New(Options{
		Target:  target.Target{Root: snap.LoomRoot, Script: mutator.Script},
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Mutator: mutator,
		Width:   100,
		Height:  40,
	}).switchView().switchView()

	m = m.moveTo(3)
	if m.selectedID != "tui-lifecycle-keys" {
		t.Fatalf("selected %q", m.selectedID)
	}
	m = act(t, m, "w")

	if got := argv(); len(got) != 1 || got[0] != "wait --json tui-lifecycle-keys " {
		t.Fatalf("argv = %q", got)
	}
}

// The preview follows the tree as it follows the frontier. Selecting a goal
// previews the goal's instructions, which is the thread charter and worth
// reading.
func TestPreviewFollowsTheTreeSelection(t *testing.T) {
	root := loomWith(t, map[string]string{
		"threads/tui":                    "# The terminal frontend\n\nThe thread charter.\n",
		"threads/tui/tui-lifecycle-keys": "# Keys\n\nThe write half.\n",
	})
	m := treeModel(t, threads(root))

	if !strings.Contains(m.View(), "The thread charter") {
		t.Errorf("the goal's instructions are not previewed:\n%s", m.View())
	}
	m = m.moveTo(3)
	if !strings.Contains(m.View(), "The write half") {
		t.Errorf("the preview did not follow the selection:\n%s", m.View())
	}
}

// easyloom at 20 columns still has to draw something legible: the clipping
// discipline is the frontier's, unchanged.
func TestTreeSurvivesANarrowTerminal(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))

	for _, size := range []tea.WindowSizeMsg{{Width: 20, Height: 8}, {Width: 40, Height: 12}, {Width: 200, Height: 60}} {
		next, _ := m.Update(size)
		m = next.(Model)
		view := m.View()
		got := strings.Split(view, "\n")
		if len(got) > size.Height {
			t.Errorf("%dx%d: %d lines drawn", size.Width, size.Height, len(got))
		}
		for _, line := range got {
			if len([]rune(line)) > size.Width {
				t.Errorf("%dx%d: line overflows: %q", size.Width, size.Height, line)
			}
		}
		if !strings.Contains(view, "THREADS") {
			t.Errorf("%dx%d: the tree is gone:\n%s", size.Width, size.Height, view)
		}
	}
}

package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
)

func testTarget() target.Target {
	return target.Target{Root: "/tmp/demo/.loom", Script: "/tmp/demo/.loom/loom.sh"}
}

func testSnapshot(stitches int) *snapshot.Snapshot {
	snap := &snapshot.Snapshot{
		SchemaVersion: snapshot.SupportedSchemaVersion,
		FormatVersion: snapshot.FormatVersionV2,
		LoomRoot:      "/tmp/demo/.loom",
		Frontier:      []string{"first"},
	}
	for i := 0; i < stitches; i++ {
		snap.Stitches = append(snap.Stitches, snapshot.Stitch{ID: "s", Ready: i == 0})
	}
	return snap
}

// seeded returns a model over a cache holding one snapshot and a source the
// test controls.
func seeded(t *testing.T, source snapshot.Source) (Model, *snapshot.Cache) {
	t.Helper()
	cache := snapshot.NewCache(source)
	cache.Seed(testSnapshot(3))
	t.Cleanup(cache.Close)
	return New(Options{
		Target:  testTarget(),
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Width:   80,
	}), cache
}

func neverRuns(t *testing.T) snapshot.Source {
	t.Helper()
	return func(context.Context) (*snapshot.Snapshot, error) {
		t.Error("the render path ran map")
		return nil, errors.New("unexpected")
	}
}

func TestStatusBarReportsTheLoom(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))

	view := m.View()
	for _, want := range []string{"demo", "v2", "3 stitches", ": commands"} {
		if !strings.Contains(view, want) {
			t.Errorf("status bar missing %q:\n%s", want, view)
		}
	}
}

// Current() is a pointer load. The render path must never wait on a refresh,
// so drawing must not touch the source at all.
func TestViewNeverRunsMap(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))
	for i := 0; i < 5; i++ {
		m.View()
		next, _ := m.Update(tickMsg(time.Now()))
		m = next.(Model)
	}
}

func TestRefreshReturnsImmediately(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	m, _ := seeded(t, func(ctx context.Context) (*snapshot.Snapshot, error) {
		close(started)
		<-release
		return testSnapshot(4), nil
	})

	m = act(t, m, "r")
	<-started

	// The refresh is in flight: the view still draws the snapshot it has, and
	// says a refresh is happening.
	view := m.View()
	if !strings.Contains(view, "refreshing") {
		t.Errorf("no refresh state in the status bar:\n%s", view)
	}
	if !strings.Contains(view, "3 stitches") {
		t.Errorf("the previous snapshot stopped rendering during a refresh:\n%s", view)
	}

	close(release)
	m = advanceUntil(t, m, func(m Model) bool { return !m.refreshing })
	if !strings.Contains(m.View(), "4 stitches") {
		t.Errorf("the finished refresh was not picked up:\n%s", m.View())
	}
}

// A failed refresh leaves the last good snapshot rendering, and says it is
// stale and why. It never blanks the view.
func TestFailedRefreshKeepsTheLastSnapshot(t *testing.T) {
	m, _ := seeded(t, func(context.Context) (*snapshot.Snapshot, error) {
		return nil, errors.New("loom.sh: exit status 2")
	})

	m = advanceUntil(t, act(t, m, "r"), func(m Model) bool { return !m.refreshing })

	view := m.View()
	if !strings.Contains(view, "3 stitches") {
		t.Errorf("the last good snapshot stopped rendering:\n%s", view)
	}
	if !strings.Contains(view, "stale") || !strings.Contains(view, "exit status 2") {
		t.Errorf("staleness not surfaced with its cause:\n%s", view)
	}
}

// ctrl+c is the one key that quits; q no longer does.
func TestQuitKeys(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c did not quit")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Errorf("ctrl+c produced %T, want quit", msg)
	}
}

// Resizing must not corrupt the layout: every line has to fit the new width.
func TestResizeIsHonoured(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))
	for _, width := range []int{40, 200, 12} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
		m = next.(Model)
		for _, line := range strings.Split(m.View(), "\n") {
			if len([]rune(line)) > width {
				t.Errorf("width %d: line overflows by %d: %q",
					width, len([]rune(line))-width, line)
			}
		}
	}
}

func TestFooterGroupsHintsAndKeepsEveryKey(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))

	for _, width := range []int{80, 24, 8, 1} {
		m.width = width
		footer := m.footer()
		for _, want := range []string{
			"↑/↓ select", "⇧↑/↓ scroll", "tab queue", "enter edit", ": commands",
		} {
			if !strings.Contains(footer, want) {
				t.Errorf("width %d omitted %q:\n%s", width, want, footer)
			}
		}
		if got := strings.Split(footer, "\n")[0]; !strings.HasPrefix(got, "↑/↓ select") {
			t.Errorf("width %d: selected-stitch row starts with %q", width, got)
		}
	}
}

func TestFooterShowsFoldOnlyInFoldableViews(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))
	for _, tc := range []struct {
		view view
		fold bool
	}{{viewFrontier, false}, {viewQueue, false}, {viewTree, true}, {viewImpact, true}} {
		m.view = tc.view
		got := strings.Contains(m.footer(), "←/→ fold")
		if got != tc.fold {
			t.Errorf("view %v: fold hint = %v, want %v", tc.view, got, tc.fold)
		}
	}
}

func TestFooterKeysHaveTheirOwnStyle(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))
	m.styles = newStyles(true)
	footer := m.footer()
	if !strings.Contains(footer, m.styles.hintKey.Render("↑/↓")+" "+m.styles.hint.Render("select")) {
		t.Errorf("key and label were not styled separately:\n%q", footer)
	}
}

func TestWrappedFooterReducesBodyBudget(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))
	m.width, m.height = 24, 24
	view := m.View()
	if got := lines(view); got > m.height {
		t.Fatalf("wrapped footer made a %d-line frame for height %d:\n%s", got, m.height, view)
	}
	for _, want := range []string{"↑/↓ select", ": commands"} {
		if !strings.Contains(view, want) {
			t.Errorf("wrapped frame omitted %q:\n%s", want, view)
		}
	}
}

// A terminal that cannot report its size sends zeros, which must not collapse
// the layout to a single column.
func TestZeroSizeIsIgnored(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	m = next.(Model)
	if m.width != 80 || m.height != 24 {
		t.Fatalf("size collapsed to %dx%d", m.width, m.height)
	}
	if !strings.Contains(m.View(), strings.Repeat("─", 80)) {
		t.Errorf("the rule did not keep the last good width:\n%s", m.View())
	}
}

// No colour assumptions: with colour off the frame carries no escape
// sequences at all.
func TestPlainWhenColourIsOff(t *testing.T) {
	m, _ := seeded(t, neverRuns(t))
	if strings.Contains(m.View(), "\x1b[") {
		t.Errorf("colour is off but the frame is styled:\n%q", m.View())
	}
}

// A cache with nothing in it is a real state with its own message, not a
// blank pane.
func TestEmptyCacheSaysSo(t *testing.T) {
	cache := snapshot.NewCache(neverRuns(t))
	t.Cleanup(cache.Close)
	m := New(Options{Target: testTarget(), Version: target.Version{Tier: target.TierV2}, Cache: cache, Width: 80})
	if !strings.Contains(m.View(), "No snapshot yet") {
		t.Errorf("empty cache renders nothing useful:\n%s", m.View())
	}
}

// Not a terminal: one frame, no program, and it returns rather than blocking
// on a keyboard that is not there.
func TestRunNonInteractiveWritesOneFrame(t *testing.T) {
	cache := snapshot.NewCache(neverRuns(t))
	cache.Seed(testSnapshot(2))
	var out strings.Builder

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{
			Target: testTarget(), Version: target.Version{Tier: target.TierV2},
			Cache: cache, Output: &out,
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a non-interactive run blocked")
	}
	if !strings.Contains(out.String(), "2 stitches") {
		t.Errorf("frame not written:\n%s", out.String())
	}
}

// advanceUntil ticks the model until the condition holds, the way the program
// would.
func advanceUntil(t *testing.T, m Model, done func(Model) bool) Model {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done(m) {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		next, _ := m.Update(tickMsg(time.Now()))
		m = next.(Model)
		time.Sleep(time.Millisecond)
	}
	return m
}

// A deep loom path gives way to the facts rather than pushing them off the
// edge, and it loses its start, keeping the directory the loom lives in.
func TestTrimLeftKeepsTheEnd(t *testing.T) {
	for _, tc := range []struct {
		path  string
		width int
		want  string
	}{
		{"~/repos/easyloom/.loom", 40, "~/repos/easyloom/.loom"},
		{"/var/folders/xx/T/Test123/001/.loom", 11, "…/001/.loom"},
		{"/a/b", 1, ""},
		{"/a/b", -5, ""},
	} {
		if got := trimLeft(tc.path, tc.width); got != tc.want {
			t.Errorf("trimLeft(%q, %d) = %q, want %q", tc.path, tc.width, got, tc.want)
		}
	}
}

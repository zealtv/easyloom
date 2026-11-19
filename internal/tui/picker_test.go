package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zealtv/easyloom/internal/known"
	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/userconfig"
	"github.com/zealtv/easyloom/internal/warm"
)

// pickerConfig points the registry at a throwaway directory.
func pickerConfig(t *testing.T) userconfig.Getenv {
	t.Helper()
	dir := t.TempDir()
	return func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return dir
		}
		return ""
	}
}

// deployLoom writes a real v2 loom that emits an empty projection, so it can
// actually be opened by the warm set.
func deployLoom(t *testing.T, name string) target.Target {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	dir := filepath.Join(root, name, ".loom")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "format-version"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := `{"schema_version":1,"format_version":2,"loom_root":"` + dir + `",
	  "stitches":[],"decomposition_edges":[],"dependency_edges":[],"cycles":[],
	  "frontier":[],"recently_completed":[],"diagnostics":[]}`
	script := "#!/usr/bin/env bash\ncat <<'JSON'\n" + doc + "\nJSON\n"
	if err := os.WriteFile(filepath.Join(dir, "loom.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := target.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// withPicker returns a model over a seeded loom that can also switch looms.
func withPicker(t *testing.T, getenv userconfig.Getenv) (Model, *warm.Set) {
	t.Helper()
	cache := snapshot.NewCache(func(context.Context) (*snapshot.Snapshot, error) {
		return testSnapshot(3), nil
	})
	cache.Seed(testSnapshot(3))
	t.Cleanup(cache.Close)

	set := warm.NewSet(warm.Options{Getenv: getenv})
	t.Cleanup(set.Close)

	return New(Options{
		Target:  testTarget(),
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Set:     set,
		Getenv:  getenv,
		Width:   80,
		Height:  24,
	}), set
}

// pressRaw sends one key and returns the model and whatever command it
// produced, for the tests that assert on the command itself. The package's
// press runs the command; here the point is sometimes that there is not one.
func pressRaw(m Model, k string) (Model, tea.Cmd) {
	next, cmd := m.Update(keyMsg(k))
	return next.(Model), cmd
}

func TestSwitchLoomOpensThePicker(t *testing.T) {
	getenv := pickerConfig(t)
	if err := known.Touch(getenv, deployLoom(t, "alpha"), target.TierV2); err != nil {
		t.Fatal(err)
	}
	m, _ := withPicker(t, getenv)

	m = act(t, m, "p")
	if m.picker == nil {
		t.Fatal("switch loom did not open the picker")
	}
	if !strings.Contains(m.View(), "LOOMS") {
		t.Errorf("the picker is not drawn:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "alpha") {
		t.Errorf("the picker does not list the known loom:\n%s", m.View())
	}

	// esc goes back to the loom that was open.
	m = press(t, m, "esc")
	if m.picker != nil {
		t.Error("esc did not close the picker")
	}
}

// While the picker is up it takes every key, including ones that are otherwise
// commands — the same rule the drop prompt follows.
func TestPickerTakesEveryKey(t *testing.T) {
	getenv := pickerConfig(t)
	for _, name := range []string{"alpha", "beta"} {
		if err := known.Touch(getenv, deployLoom(t, name), target.TierV2); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := withPicker(t, getenv)
	m = act(t, m, "p")

	before := m.view
	// tab would switch views if the picker were not swallowing it.
	m = press(t, m, "tab")
	if m.view != before {
		t.Error("tab changed the view while the picker was up")
	}
	if m.picker == nil {
		t.Fatal("tab closed the picker")
	}

	m = press(t, m, "down")
	if m.picker.Cursor() != 1 {
		t.Errorf("cursor = %d after down, want 1", m.picker.Cursor())
	}
}

// The picker opens on the loom currently open, so it shows where you are
// rather than an arbitrary top of the list.
func TestPickerOpensOnTheCurrentLoom(t *testing.T) {
	getenv := pickerConfig(t)
	current := deployLoom(t, "current")
	for _, tgt := range []target.Target{current, deployLoom(t, "other")} {
		if err := known.Touch(getenv, tgt, target.TierV2); err != nil {
			t.Fatal(err)
		}
	}

	cache := snapshot.NewCache(func(context.Context) (*snapshot.Snapshot, error) {
		return testSnapshot(1), nil
	})
	cache.Seed(testSnapshot(1))
	t.Cleanup(cache.Close)
	set := warm.NewSet(warm.Options{Getenv: getenv})
	t.Cleanup(set.Close)

	m := New(Options{
		Target: current, Version: target.Version{Tier: target.TierV2},
		Cache: cache, Set: set, Getenv: getenv, Width: 80, Height: 24,
	})
	m = act(t, m, "p")

	selected := m.picker.Selected()
	if selected == nil || selected.Target.Key() != current.Key() {
		t.Errorf("the picker opened on %v, want the loom already open", selected)
	}
}

// The bug this exists to prevent: an optimistic overlay planned on one loom's
// projection must never survive into another loom's.
func TestSwitchingResetsEverythingAboutTheOldLoom(t *testing.T) {
	getenv := pickerConfig(t)
	m, set := withPicker(t, getenv)

	// Dirty every piece of per-loom state.
	m.selected = 2
	m.selectedID = "stale"
	m.expanded = map[string]bool{"stale": true}
	m.preview = "old instructions"
	m.previewPath = "/old/path"
	m.over = overlay{generation: 7}
	m.undo = overlay{generation: 7}
	m.failure = "an old refusal"
	m.notice = "an old notice"
	m.refreshing = true

	opened, err := set.Open(context.Background(), deployLoom(t, "next"))
	if err != nil {
		t.Fatal(err)
	}
	m = m.switchTo(opened)

	if m.loom.Key() != opened.Target.Key() {
		t.Errorf("loom = %s, want the one switched to", m.loom.Root)
	}
	if m.cache != opened.Cache {
		t.Error("the model kept the old cache")
	}
	if m.mutator != opened.Mutator {
		t.Error("the model kept the old mutator")
	}
	if !m.over.empty() || !m.undo.empty() {
		t.Error("an optimistic overlay survived the switch — it would be applied to another loom's projection")
	}
	if m.selectedID == "stale" || m.expanded["stale"] || m.preview != "" || m.previewPath != "" {
		t.Error("view state from the old loom survived the switch")
	}
	if m.failure != "" || m.notice != "" || m.refreshing {
		t.Error("status from the old loom survived the switch")
	}
	if m.picker != nil {
		t.Error("the picker stayed open after a switch")
	}
}

// Choosing an entry whose loom has gone says so, rather than opening it and
// reporting a shell error.
func TestChoosingAMissingLoomSaysSo(t *testing.T) {
	getenv := pickerConfig(t)
	gone := deployLoom(t, "gone")
	if err := known.Touch(getenv, gone, target.TierV2); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone.Script); err != nil {
		t.Fatal(err)
	}

	m, _ := withPicker(t, getenv)
	m = act(t, m, "p")

	m, cmd := pressRaw(m, "enter")
	if cmd != nil {
		t.Error("choosing a missing loom tried to open it")
	}
	if !strings.Contains(m.failure, "not there any more") {
		t.Errorf("failure = %q, want it to say the loom is gone", m.failure)
	}
}

// x forgets an entry, which is the only way one whose loom has been deleted
// ever leaves the list.
func TestForgetKeyDropsAnEntry(t *testing.T) {
	getenv := pickerConfig(t)
	if err := known.Touch(getenv, deployLoom(t, "alpha"), target.TierV2); err != nil {
		t.Fatal(err)
	}
	m, _ := withPicker(t, getenv)
	m = act(t, m, "p")

	if len(m.picker.Entries()) != 1 {
		t.Fatalf("the picker holds %d entries, want 1", len(m.picker.Entries()))
	}
	m = press(t, m, "x")
	if len(m.picker.Entries()) != 0 {
		t.Errorf("x left %d entries", len(m.picker.Entries()))
	}

	list, err := known.Load(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Looms) != 0 {
		t.Error("x did not write the registry")
	}
}

// Started with no loom, the picker is the whole program: esc has nothing to go
// back to, and the frame still draws.
func TestColdStartOpensThePickerAndCannotEscapeIt(t *testing.T) {
	getenv := pickerConfig(t)
	if err := known.Touch(getenv, deployLoom(t, "alpha"), target.TierV2); err != nil {
		t.Fatal(err)
	}
	set := warm.NewSet(warm.Options{Getenv: getenv})
	t.Cleanup(set.Close)

	m := New(Options{Set: set, Getenv: getenv, Width: 80, Height: 24})
	if m.picker == nil {
		t.Fatal("starting with no loom did not open the picker")
	}

	view := m.View()
	if !strings.Contains(view, "no loom open") {
		t.Errorf("the status does not say there is no loom:\n%s", view)
	}
	if !strings.Contains(view, "alpha") {
		t.Errorf("the picker does not list the known loom:\n%s", view)
	}
	// The footer offers no way back, because there is nothing behind it.
	if strings.Contains(view, "back") {
		t.Errorf("the footer offers esc with no loom to go back to:\n%s", view)
	}

	m = press(t, m, "esc")
	if m.picker == nil {
		t.Error("esc closed the picker with no loom behind it")
	}
	// No bare letter quits, here either: ctrl+c is the way out.
	if _, cmd := pressRaw(m, "q"); cmd != nil && cmd() == tea.Quit() {
		t.Error("q quit the cold-started picker")
	}
	if !strings.Contains(view, "ctrl+c") {
		t.Errorf("the footer does not offer ctrl+c to quit:\n%s", view)
	}
}

// An empty registry draws a way forward rather than an empty list.
func TestColdStartWithNoLoomsPointsAtTheScan(t *testing.T) {
	getenv := pickerConfig(t)
	set := warm.NewSet(warm.Options{Getenv: getenv})
	t.Cleanup(set.Close)

	m := New(Options{Set: set, Getenv: getenv, Width: 80, Height: 24})
	view := m.View()
	if !strings.Contains(view, "scan") {
		t.Errorf("an empty picker does not point at the scan:\n%s", view)
	}
}

// Opening returns through a message, because opening a cold loom runs map and
// the render loop must never wait on one.
func TestOpeningIsAsynchronous(t *testing.T) {
	getenv := pickerConfig(t)
	next := deployLoom(t, "next")
	if err := known.Touch(getenv, next, target.TierV2); err != nil {
		t.Fatal(err)
	}
	m, _ := withPicker(t, getenv)
	m = act(t, m, "p")

	m, cmd := pressRaw(m, "enter")
	if cmd == nil {
		t.Fatal("enter did not start an open")
	}
	msg, ok := cmd().(openedMsg)
	if !ok {
		t.Fatalf("the open produced %T, want an openedMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("opening: %v", msg.err)
	}

	after, _ := m.Update(msg)
	switched := after.(Model)
	if switched.loom.Key() != next.Key() {
		t.Errorf("loom = %s, want the one chosen", switched.loom.Root)
	}
	if switched.picker != nil {
		t.Error("the picker stayed open after opening a loom")
	}
}

// A loom that cannot be driven is reported rather than switched to, which
// would leave the program pointed at a loom with nothing to draw.
func TestOpeningAnUndriveableLoomIsReported(t *testing.T) {
	getenv := pickerConfig(t)
	m, _ := withPicker(t, getenv)
	before := m.loom

	after, _ := m.Update(openedMsg{loom: &warm.Loom{
		Target:  target.Target{Root: "/elsewhere/.loom"},
		Version: target.Version{Tier: target.TierV1},
	}})
	got := after.(Model)
	if got.loom.Key() != before.Key() {
		t.Error("the model switched to a loom it cannot draw")
	}
	if got.failure == "" {
		t.Error("opening an undriveable loom said nothing")
	}
}

// The picker is offered only when the session can actually switch.
func TestPickerOfferedOnlyWithASet(t *testing.T) {
	withSet, _ := withPicker(t, pickerConfig(t))
	if !offers(withSet, "switch loom") {
		t.Error("a session with a loom set does not offer the picker")
	}

	without, _ := seeded(t, func(context.Context) (*snapshot.Snapshot, error) {
		return testSnapshot(1), nil
	})
	if offers(without, "switch loom") {
		t.Error("a session with no loom set offers the picker")
	}
}

// Quitting out of a cold-started picker without ever opening a loom must not
// crash on the way out. There is no cache to close, and Run used to close one
// unconditionally.
func TestColdStartQuitsCleanly(t *testing.T) {
	getenv := pickerConfig(t)
	set := warm.NewSet(warm.Options{Getenv: getenv})
	t.Cleanup(set.Close)

	var out strings.Builder
	err := Run(context.Background(), Options{
		Set: set, Getenv: getenv, Output: &out, Width: 80, Height: 24,
	})
	if err != nil {
		t.Fatalf("a cold non-interactive run: %v", err)
	}
	if !strings.Contains(out.String(), "no loom open") {
		t.Errorf("the frame does not show the picker:\n%s", out.String())
	}
}

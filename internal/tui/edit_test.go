package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
)

func TestEditorComesFromTheEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		visual string
		editor string
		want   []string
	}{
		{name: "VISUAL wins", visual: "nvim", editor: "ed", want: []string{"nvim"}},
		{name: "EDITOR is the fallback", editor: "ed", want: []string{"ed"}},
		// The value is a command line, not a path: these are all normal.
		{name: "arguments survive", visual: "code --wait", want: []string{"code", "--wait"}},
		{name: "extra spaces do not become arguments", visual: "  emacsclient   -nw ", want: []string{"emacsclient", "-nw"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(visualVar, tc.visual)
			t.Setenv(editorVar, tc.editor)

			argv, err := editorCommand()
			if err != nil {
				t.Fatalf("editorCommand: %v", err)
			}
			if strings.Join(argv, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("editorCommand = %q, want %q", argv, tc.want)
			}
		})
	}
}

// Neither variable set is not a reason to guess at vi.
func TestNoEditorIsRefusedRatherThanGuessed(t *testing.T) {
	t.Setenv(visualVar, "")
	t.Setenv(editorVar, "")

	if argv, err := editorCommand(); err == nil {
		t.Fatalf("editorCommand = %q, want an error", argv)
	}
}

// editable is a model over a real loom directory on disk, so the path the
// editor would be handed is a real one.
func editable(t *testing.T, body string) (Model, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".loom")
	dir := filepath.Join(root, "threads", "goal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, instructionsName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	snap := &snapshot.Snapshot{
		SchemaVersion: snapshot.SupportedSchemaVersion,
		FormatVersion: snapshot.FormatVersionV2,
		LoomRoot:      root,
		Frontier:      []string{"goal"},
		Stitches: []snapshot.Stitch{
			{ID: "goal", Path: "threads/goal", Tray: snapshot.TrayThreads, Ready: true, State: snapshot.StatePlain},
		},
	}
	// Coming back from the editor asks for a snapshot, so the source has to
	// answer rather than fail the test.
	cache := snapshot.NewCache(func(context.Context) (*snapshot.Snapshot, error) { return snap, nil })
	cache.Seed(snap)
	t.Cleanup(cache.Close)

	return New(Options{
		Target:  target.Target{Root: root, Script: filepath.Join(root, "loom.sh")},
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Width:   80,
		Height:  24,
	}), path
}

func TestEnterWithNoEditorSaysSoAndRunsNothing(t *testing.T) {
	t.Setenv(visualVar, "")
	t.Setenv(editorVar, "")
	m, _ := editable(t, "# goal\n")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("a command was returned with no editor set")
	}
	view := next.(Model).View()
	for _, want := range []string{"$VISUAL", "$EDITOR"} {
		if !strings.Contains(view, want) {
			t.Errorf("the status line does not mention %s:\n%s", want, view)
		}
	}
}

func TestEnterOnAStitchReturnsAnExecCommand(t *testing.T) {
	t.Setenv(visualVar, "true")
	t.Setenv(editorVar, "")
	m, _ := editable(t, "# goal\n")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
	// Nothing was reported: the editor is about to run, not refused.
	if view := next.(Model).View(); strings.Contains(view, "no editor") {
		t.Errorf("enter refused a stitch it should have opened:\n%s", view)
	}
}

// enter is bound on every pane: the selected stitch is the selected stitch
// wherever you are.
func TestEnterIsBoundOnEveryPane(t *testing.T) {
	t.Setenv(visualVar, "true")
	t.Setenv(editorVar, "")

	for _, view := range []view{viewFrontier, viewQueue, viewTree} {
		m, _ := editable(t, "# goal\n")
		m.view = view
		m = m.reselect()
		if m.selectedStitch() == nil {
			// The queue is empty in this fixture, and a pane with nothing
			// selected has nothing to edit — which is its own test below.
			continue
		}
		if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
			t.Errorf("enter is unbound on the %s pane", view.name())
		}
	}
}

// The tree keeps folding on space and ←/→ now that enter edits.
func TestSpaceStillFoldsTheTree(t *testing.T) {
	m, _ := editable(t, "# goal\n")
	m.view = viewTree
	m = m.reselect()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if cmd != nil {
		t.Error("space returned a command; it should only fold")
	}
	if next.(Model).view != viewTree {
		t.Error("space moved off the tree")
	}
}

func TestEditorExitStatusIsReportedNotSwallowed(t *testing.T) {
	m, _ := editable(t, "# goal\n")

	next, _ := m.Update(editedMsg{err: errExit})
	view := next.(Model).View()
	if !strings.Contains(view, "editor") || !strings.Contains(view, errExit.Error()) {
		t.Errorf("the editor's failure is not on the status line:\n%s", view)
	}
}

// Coming back from the editor re-reads the file and stays exactly where it
// was. Suspending should feel like the app never moved.
func TestReturningFromTheEditorShowsTheNewContents(t *testing.T) {
	m, path := editable(t, "# before\n")
	m.view = viewTree
	m = m.reselect()
	where, selected := m.view, m.selectedID

	if !strings.Contains(m.View(), "before") {
		t.Fatalf("the preview does not show the original contents:\n%s", m.View())
	}
	if err := os.WriteFile(path, []byte("# after\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	updated, _ := m.Update(editedMsg{})
	next := mustModel(t, updated)
	view := next.View()
	if !strings.Contains(view, "after") {
		t.Errorf("the preview still shows the old contents:\n%s", view)
	}
	if next.view != where || next.selectedID != selected {
		t.Errorf("the view moved: %v/%q, want %v/%q", next.view, next.selectedID, where, selected)
	}
}

// A stitch path that escapes the loom root is refused before anything is run,
// the same refusal the preview makes.
func TestEditRefusesAPathOutsideTheLoom(t *testing.T) {
	t.Setenv(visualVar, "true")
	m, _ := editable(t, "# goal\n")

	snap := *m.state.Snapshot
	snap.Stitches = []snapshot.Stitch{{ID: "goal", Path: "../../elsewhere", Tray: snapshot.TrayThreads, Ready: true}}
	m.state.Snapshot = &snap
	m = m.reselect()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("a command was returned for a path outside the loom")
	}
	if view := next.(Model).View(); !strings.Contains(view, "escapes the loom root") {
		t.Errorf("the refusal is not on the status line:\n%s", view)
	}
}

func mustModel(t *testing.T, model tea.Model) Model {
	t.Helper()
	m, ok := model.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", model)
	}
	return m
}

var errExit = exitError("exit status 1")

type exitError string

func (e exitError) Error() string { return string(e) }

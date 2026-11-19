package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zealtv/easyloom/internal/desktop"
)

// Revealing a folder hands it to the desktop's file manager: Finder on macOS,
// whatever xdg-open names elsewhere. The case it is for is reading a stitch
// whose instructions point at a document sitting beside them — the folder is
// one command away rather than a dive through the file manager.
//
// It changes nothing in the loom, but it stays off bare keys all the same: a
// sentence typed into the wrong tmux pane should not throw windows up on the
// desktop.

// revealedMsg is the opener having finished, with what it opened.
type revealedMsg struct {
	dir string
	err error
}

// reveal runs the opener on dir. It is a variable so the tests can watch it
// rather than open windows.
//
// The opener's output is captured, not inherited: the terminal belongs to the
// TUI, and xdg-open in particular has a lot to say on a box with no desktop.
var reveal = func(dir string) error {
	out, err := desktop.Command(dir).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// fileManager names where the folder will appear, for the notice.
func fileManager() string {
	if runtime.GOOS == "darwin" {
		return "Finder"
	}
	return "the file manager"
}

// stitchDir is the selected stitch's folder, by the same refusal as its
// instructions: nothing out of the projection reaches the desktop from
// outside the loom.
func stitchDir(loomRoot, path string) (string, error) {
	full, err := instructionsPath(loomRoot, path)
	if err != nil {
		return "", err
	}
	return filepath.Dir(full), nil
}

// revealStitch opens the selected stitch's folder.
func (m Model) revealStitch() (Model, tea.Cmd) {
	st := m.selectedStitch()
	if st == nil {
		return m.note("no stitch selected."), nil
	}
	dir, err := stitchDir(m.state.Snapshot.LoomRoot, st.Path)
	if err != nil {
		m.failure = err.Error()
		return m, nil
	}
	return m.revealDir(dir)
}

// revealLoom opens the loom's own folder.
func (m Model) revealLoom() (Model, tea.Cmd) {
	if m.state.Snapshot == nil || m.state.Snapshot.LoomRoot == "" {
		return m.note("no loom root in the projection yet."), nil
	}
	return m.revealDir(m.state.Snapshot.LoomRoot)
}

// revealDir checks the folder is there — open and xdg-open both fail on a
// missing one, but less plainly — and opens it in the background.
func (m Model) revealDir(dir string) (Model, tea.Cmd) {
	m.failure, m.notice = "", ""
	if info, err := os.Stat(dir); err != nil {
		m.failure = err.Error()
		return m, nil
	} else if !info.IsDir() {
		m.failure = dir + " is not a folder"
		return m, nil
	}
	return m, func() tea.Msg { return revealedMsg{dir: dir, err: reveal(dir)} }
}

// revealed reports how the opener went. A window opening behind the terminal
// is easy to miss, so success says so too.
func (m Model) revealed(msg revealedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.failure = fmt.Sprintf("could not open %s: %v", msg.dir, msg.err)
		return m, nil
	}
	return m.note("opened " + msg.dir + " in " + fileManager() + "."), nil
}

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The editor is the user's, and it comes from the environment. $VISUAL first,
// then $EDITOR — the long-standing convention, where VISUAL is the full-screen
// editor and EDITOR the line-oriented fallback. Neither set is not a reason to
// guess: vi is a trap for somebody who has never used it, and picking one for
// the user is not this program's business.
const (
	visualVar = "VISUAL"
	editorVar = "EDITOR"
)

// editedMsg is the editor exiting. Err is the editor's, not ours: a non-zero
// exit is reported and the file is left exactly as the editor left it.
type editedMsg struct{ err error }

// editorCommand returns the editor to run, split into a command and its
// arguments.
//
// The value is a command line, not a bare path — `code --wait`, `emacsclient
// -nw` and `nvim -p` are all normal — so it is split on whitespace the way a
// shell would. It is deliberately not handed to `sh -c`: the file path comes
// from the projection, and a shell would parse it a second time.
func editorCommand() ([]string, error) {
	for _, name := range []string{visualVar, editorVar} {
		fields := strings.Fields(os.Getenv(name))
		if len(fields) > 0 {
			return fields, nil
		}
	}
	return nil, fmt.Errorf("no editor: set $%s or $%s", visualVar, editorVar)
}

// edit hands the selected stitch's instructions to the user's editor.
//
// tea.ExecProcess releases the terminal, runs the editor attached to the real
// stdin and stdout, and sends the result back when it exits. Nothing here
// suspends or restores the terminal itself, and nothing starts a second
// program.
func (m Model) edit() (tea.Model, tea.Cmd) {
	m.failure, m.notice = "", ""

	st := m.selectedStitch()
	if st == nil {
		m.notice = "nothing to edit here."
		if m.view == viewQueue && m.selectedID != "" {
			m.notice = m.selectedID + " is a queue record with no stitch behind it."
		}
		return m, nil
	}

	// The same refusal the preview makes: a path out of the projection must
	// not reach a file operation outside the loom.
	path, err := instructionsPath(m.state.Snapshot.LoomRoot, st.Path)
	if err != nil {
		m.failure = err.Error()
		return m, nil
	}

	argv, err := editorCommand()
	if err != nil {
		m.notice = err.Error()
		return m, nil
	}

	// A stitch always has instructions under the protocol. On a broken loom
	// that has none, the editor creating the file is the right outcome — and
	// if the directory is gone, the editor is the one to say so.
	command := exec.Command(argv[0], append(argv[1:], path)...)
	return m, tea.ExecProcess(command, func(err error) tea.Msg { return editedMsg{err} })
}

// edited comes back when the editor exits.
//
// The view and the selection have not moved: suspending should feel like the
// app never went anywhere. The instructions are re-read at once so the new
// contents are on screen, and a snapshot is asked for in the background —
// nothing here is optimistic about what the edit did.
func (m Model) edited(msg editedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		// It is the user's editor and we do not judge its exit code beyond
		// saying what happened. Whatever it wrote stays written.
		m.failure = fmt.Sprintf("editor: %v", msg.err)
	}
	// loadPreview reads only when the path has changed, and the path has not.
	m.previewPath = ""
	m = m.loadPreview()
	return m.startRefresh(), nil
}

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// The focus names the tabs and the list: its first row is the tab row, and the
// row after it is the rule above the preview.
func TestFocusRectIsTheTabsAndTheList(t *testing.T) {
	for _, size := range [][2]int{{100, 40}, {57, 53}, {120, 12}} {
		m := modelOver(t, projection(t.TempDir()))
		m.width, m.height = size[0], size[1]
		r := m.focusRect()
		rows := strings.Split(ansi.Strip(m.View()), "\n")
		if r.W != m.width || r.Y+r.H >= len(rows) {
			t.Fatalf("%v: focus %+v on %d rows", size, r, len(rows))
		}
		if !strings.Contains(rows[r.Y], "FRONTIER") {
			t.Errorf("%v: focus starts at %q, want the tab row", size, rows[r.Y])
		}
		if !strings.HasPrefix(rows[r.Y+r.H], "───") {
			t.Errorf("%v: the row after the focus is %q, want the rule above the preview", size, rows[r.Y+r.H])
		}
	}
}

// Update reports the focus once per change, and only with a pane.
func TestUpdateReportsTheFocus(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	if _, cmd := m.Update(tea.WindowSizeMsg{Width: 90, Height: 30}); cmd != nil {
		t.Fatal("a model with no pane reported a focus")
	}
	var sent []string
	m.focus.ID = "%3"
	m.focus.Run = func(args ...string) error { sent = append(sent, strings.Join(args, " ")); return nil }
	drain := func(next tea.Model, cmd tea.Cmd) Model {
		if cmd != nil {
			if b, ok := cmd().(tea.BatchMsg); ok {
				for _, c := range b {
					if c != nil {
						c()
					}
				}
			}
		}
		return next.(Model)
	}
	m = drain(m.Update(tea.WindowSizeMsg{Width: 90, Height: 30}))
	m = drain(m.Update(tea.WindowSizeMsg{Width: 90, Height: 30}))
	m = drain(m.Update(tea.WindowSizeMsg{Width: 80, Height: 30}))
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "set-option -p -t %3 @tw-focus 0,2,90,") {
		t.Errorf("sent %q", sent)
	}
}

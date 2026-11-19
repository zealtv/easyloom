package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zealtv/easyloom/internal/picker"
	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/warm"
)

// openedMsg carries the result of opening a loom, which happens off the render
// path: opening a cold loom runs map, and the UI must not block on it.
type openedMsg struct {
	loom *warm.Loom
	err  error
}

// scannedMsg carries a scan's results back to the picker.
type scannedMsg struct {
	found []target.Found
	err   error
}

// openPicker shows the list of known looms over whatever is being drawn.
func (m Model) openPicker() Model {
	if m.set == nil {
		m.notice = "no loom set: this build cannot switch looms"
		return m
	}
	p := picker.New(m.getenv)
	// Start on the loom currently open, so the picker opens showing where you
	// are rather than at an arbitrary top of the list.
	for i, e := range p.Entries() {
		if e.Target.Key() == m.loom.Key() {
			p = p.MoveTo(i)
			break
		}
	}
	m.picker = &p
	m.failure = ""
	m.notice = ""
	return m
}

// updatePicker handles a key while the picker is up. Every key belongs to it,
// the way they belong to the drop prompt.
func (m Model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Closing is only an option when there is something behind it. Started
		// from cold with no loom open, the picker is the whole program, and
		// ctrl+c is the way out: no bare letter quits.
		if m.cache == nil {
			return m, nil
		}
		m.picker = nil
		return m, nil

	case "ctrl+c":
		return m, tea.Quit

	case "up", "k":
		p := m.picker.Move(-1)
		m.picker = &p
		return m, nil

	case "down", "j":
		p := m.picker.Move(1)
		m.picker = &p
		return m, nil

	case "home", "g":
		p := m.picker.MoveTo(0)
		m.picker = &p
		return m, nil

	case "end", "G":
		p := m.picker.MoveTo(len(m.picker.Entries()) - 1)
		m.picker = &p
		return m, nil

	case "s":
		p := m.picker.StartScan()
		m.picker = &p
		return m, m.scanCmd()

	case "x":
		p := m.picker.Forget()
		m.picker = &p
		return m, nil

	case "enter":
		entry := m.picker.Selected()
		if entry == nil {
			return m, nil
		}
		if entry.Missing {
			// Say so rather than opening it and reporting a shell error: the
			// picker already knows the script is not there.
			p := *m.picker
			m.picker = &p
			m.failure = entry.Target.Root + " is not there any more. x forgets it."
			return m, nil
		}
		return m, m.openCmd(entry.Target)
	}
	return m, nil
}

// openCmd opens a loom off the render path.
func (m Model) openCmd(t target.Target) tea.Cmd {
	set := m.set
	return func() tea.Msg {
		opened, err := set.Open(context.Background(), t)
		return openedMsg{loom: opened, err: err}
	}
}

// scanCmd walks for looms off the render path.
func (m Model) scanCmd() tea.Cmd {
	root := m.scanRoot
	prober := m.prober
	return func() tea.Msg {
		found, err := picker.Scan(context.Background(), root, prober)
		return scannedMsg{found: found, err: err}
	}
}

// opened switches to the loom that finished opening.
func (m Model) opened(msg openedMsg) (tea.Model, tea.Cmd) {
	if msg.loom == nil {
		m.failure = fmt.Sprintf("%v", msg.err)
		return m, nil
	}
	if !msg.loom.Full() {
		m.failure = fmt.Sprintf("%s is %s and cannot be driven", msg.loom.Target.Root, msg.loom.Version.Tier)
		if msg.err != nil {
			m.failure = fmt.Sprintf("%v", msg.err)
		}
		return m, nil
	}
	return m.switchTo(msg.loom), nil
}

// switchTo points the model at another loom.
//
// Everything that is about a loom is reset; everything that is about the user
// is kept. The overlays especially: an optimistic queue overlay planned on one
// loom's projection, left in place across a switch, would be applied to
// another loom's — which is the bug this function exists to prevent.
func (m Model) switchTo(l *warm.Loom) Model {
	m.loom = l.Target
	m.version = l.Version
	m.cache = l.Cache
	m.mutator = l.Mutator
	m.state = l.Cache.Current()

	m.picker = nil
	m.selected = 0
	m.selectedID = ""
	m.places = [viewCount]cursor{}
	m.expanded = map[string]bool{}
	m.preview = ""
	m.previewErr = nil
	m.previewPath = ""
	m.pending = nil
	m.over = overlay{}
	m.undo = overlay{}
	m.lastMove = nil
	m.failure = ""
	m.notice = ""
	m.refreshing = false
	m.baseline = 0

	return m.reselect()
}

// scanned folds a scan's results into the picker.
func (m Model) scanned(msg scannedMsg) (tea.Model, tea.Cmd) {
	if m.picker == nil {
		return m, nil
	}
	if msg.err != nil {
		p := m.picker.Merge(nil)
		m.picker = &p
		m.failure = fmt.Sprintf("%v", msg.err)
		return m, nil
	}
	p := m.picker.Merge(msg.found)
	m.picker = &p
	return m, nil
}

// pickerBody draws the list of looms.
func (m Model) pickerBody(height int) string {
	rows := m.picker.Rows()
	var out []string

	heading := " LOOMS"
	summary := fmt.Sprintf("%d known", len(rows))
	if m.picker.Scanning() {
		summary = "scanning…"
	}
	out = append(out, m.heading(heading, summary))

	if len(rows) == 0 {
		out = append(out, "", m.styles.dim.Render("  No looms remembered yet. Press s to scan for them."))
		return strings.Join(out, "\n")
	}

	// The list scrolls around the cursor once it is longer than the space.
	budget := height - len(out) - 1
	if budget < 1 {
		budget = 1
	}
	first := 0
	if cursor := m.picker.Cursor(); cursor >= budget {
		first = cursor - budget + 1
	}
	last := first + budget
	if last > len(rows) {
		last = len(rows)
	}

	for _, r := range rows[first:last] {
		marker := "  "
		if r.Selected {
			marker = "▸ "
		}
		tier := m.styles.dim.Render(fmt.Sprintf("%-8s", r.Tier))
		if r.Missing {
			tier = m.styles.warn.Render(fmt.Sprintf("%-8s", r.Tier))
		}
		line := marker + fmt.Sprintf("%-24s", r.Name) + " " + tier + " " + m.styles.dim.Render(r.Root)
		if r.Selected {
			line = m.styles.selected.Render(line)
		}
		out = append(out, line)
	}

	if notice := m.picker.Notice(); notice != "" {
		out = append(out, "", m.styles.dim.Render("  "+notice))
	}
	return strings.Join(out, "\n")
}

// heading is the picker's own title row, matching the shape the other views
// draw so the frame does not change under the overlay.
func (m Model) heading(title, summary string) string {
	left := m.styles.heading.Render(title)
	right := m.styles.dim.Render(summary + " ")
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// pickerHints are the picker's keys. Closing is offered only when there is a
// loom behind the picker to close it onto.
func (m Model) pickerHints() [2][]hint {
	first := []hint{{"↑/↓", "select"}, {"enter", "open"}, {"s", "scan"}, {"x", "forget"}}
	second := []hint{}
	if m.cache != nil {
		second = append(second, hint{"esc", "back"})
	}
	second = append(second, hint{"ctrl+c", "quit"})
	return [2][]hint{first, second}
}

// pickerStatus is the status line while the picker is up and no loom is open,
// where the usual line has no loom to describe.
func (m Model) pickerStatus() string {
	left := m.styles.name.Render("easyloom")
	right := m.styles.dim.Render("no loom open")
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right
	if m.failure != "" {
		for _, l := range strings.Split(m.failure, "\n") {
			line += "\n" + m.styles.warn.Render(" "+l)
		}
	}
	return line
}

// coldState is the cache state of a model with no loom open.
var coldState = snapshot.CacheState{}

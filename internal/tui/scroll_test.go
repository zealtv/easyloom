package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// longModel is the frontier with the selected stitch's instructions long
// enough to scroll: 200 numbered lines.
func longModel(t *testing.T) Model {
	t.Helper()
	root := t.TempDir()
	m := modelOver(t, projection(root))
	st := m.selectedStitch()
	if st == nil {
		t.Fatal("nothing selected")
	}
	dir := filepath.Join(root, filepath.FromSlash(st.Path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "line %03d\n\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, instructionsName), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	m.previewPath = ""
	return m.loadPreview()
}

func TestInstructionsScrollWithinWhatThereIs(t *testing.T) {
	m := longModel(t)
	if !strings.Contains(m.View(), "line 001") {
		t.Fatalf("the instructions do not start at the top:\n%s", m.View())
	}

	m = press(t, m, "pgdown")
	if m.scroll == 0 || strings.Contains(m.View(), "line 001") {
		t.Fatalf("pgdown did not scroll (scroll %d)", m.scroll)
	}
	page := m.scroll

	m = press(t, m, "shift+up")
	if m.scroll != page-1 {
		t.Errorf("shift+up scrolled to %d, want %d", m.scroll, page-1)
	}

	// Never past the end: the last line is in view and the window is full.
	for range 100 {
		m = press(t, m, "pgdown")
	}
	end := m.scroll
	if !strings.Contains(m.View(), "line 200") {
		t.Errorf("scrolled to the end and line 200 is not in view:\n%s", m.View())
	}
	if m = press(t, m, "shift+down"); m.scroll != end {
		t.Errorf("scrolled past the end: %d after %d", m.scroll, end)
	}

	// Never before the start.
	for range 100 {
		m = press(t, m, "ctrl+u")
	}
	if m.scroll != 0 {
		t.Errorf("scrolled before the start: %d", m.scroll)
	}
}

// The facts stay put while the instructions move beneath them, and the bar
// says how far down the reader is.
func TestScrollingKeepsTheFactsAndDrawsABar(t *testing.T) {
	m := longModel(t)
	id := m.selectedID
	m = press(t, m, "pgdown")
	view := m.View()
	if !strings.Contains(view, id) {
		t.Errorf("the stitch's title scrolled away:\n%s", view)
	}
	if !strings.Contains(view, "┃") || !strings.Contains(view, "│") {
		t.Errorf("no scroll bar:\n%s", view)
	}
	if !strings.Contains(view, "/399") {
		t.Errorf("no position on the facts line:\n%s", view)
	}
}

// A short stitch has nothing to scroll and no bar to draw.
func TestShortInstructionsHaveNoBar(t *testing.T) {
	m := modelOver(t, projection(loomWith(t, nil)))
	if strings.Contains(m.View(), "┃") {
		t.Errorf("a bar with nothing to scroll:\n%s", m.View())
	}
}

// The thumb's length is the share in view, and it reaches each end exactly
// when the text does.
func TestScrollbarGeometry(t *testing.T) {
	m := longModel(t)
	thumb := func(bar []string) (first, n int) {
		first = -1
		for i, cell := range bar {
			if strings.Contains(cell, "┃") {
				if first < 0 {
					first = i
				}
				n++
			}
		}
		return first, n
	}
	if first, n := thumb(m.scrollbar(0, 10, 40)); first != 0 || n != 2 {
		t.Errorf("at the top: thumb at %d, %d long", first, n)
	}
	if first, n := thumb(m.scrollbar(30, 10, 40)); first != 8 || n != 2 {
		t.Errorf("at the bottom: thumb at %d, %d long", first, n)
	}
	if _, n := thumb(m.scrollbar(0, 10, 1000)); n != 1 {
		t.Errorf("a long text's thumb is %d long, want at least the floor of 1", n)
	}
}

// A new stitch's instructions start at the top.
func TestSelectingAnotherStitchScrollsToTheTop(t *testing.T) {
	m := longModel(t)
	m = press(t, m, "pgdown")
	m = press(t, m, "down")
	if m.scroll != 0 {
		t.Errorf("the next stitch opened scrolled to %d", m.scroll)
	}
}

// The wheel scrolls the pane under the pointer.
func TestWheelScrollsThePaneUnderThePointer(t *testing.T) {
	m := longModel(t)
	top, _ := m.layout()

	next, _ := m.Update(tea.MouseMsg{Y: top + 2, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = next.(Model)
	if m.scroll != wheelStep {
		t.Errorf("the wheel over the instructions scrolled to %d", m.scroll)
	}

	was := m.selected
	next, _ = m.Update(tea.MouseMsg{Y: 4, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = next.(Model)
	if m.selected != was+1 {
		t.Errorf("the wheel over the list moved the selection from %d to %d", was, m.selected)
	}
}

// The acceptance-tour case: a large dependency list must not consume the
// fixed header, and every detail must still be reachable with normal scrolling.
func previewCommand(t *testing.T, m Model) Model {
	t.Helper()
	name := "show dependencies"
	if m.dependencies {
		name = "show instructions"
	}
	m = press(t, m, paletteKey)
	for _, r := range name {
		m = press(t, m, string(r))
	}
	if matches := m.filtered(); len(matches) == 0 || matches[0].name != name || matches[0].key != "" {
		t.Fatalf("preview command is missing or has a bare shortcut: %v", matches)
	}
	return press(t, m, "enter")
}

func TestManyDependenciesLeaveInstructionsVisible(t *testing.T) {
	m := longModel(t)
	st := m.selectedStitch()
	st.Dependencies = nil
	for i := range 25 {
		st.Dependencies = append(st.Dependencies, snapshot.Dependency{
			To: fmt.Sprintf("dependency-%02d", i), Status: snapshot.DependencySatisfied,
		})
	}
	st.Dependencies[23].Status = snapshot.DependencyBlocked
	reason := snapshot.BrokenReason("missing")
	st.Dependencies[24].Status = snapshot.DependencyBroken
	st.Dependencies[24].Reason = &reason

	for _, height := range []int{24, 40} {
		m.height = height
		view := m.View()
		for _, want := range []string{"25 dependencies", "23 satisfied", "1 blocked", "1 broken", "line 001"} {
			if !strings.Contains(view, want) {
				t.Errorf("height %d missing %q:\n%s", height, want, view)
			}
		}
		_, h := m.layout()
		head, _, window := m.previewParts(st, h)
		if len(head) != 4 || window < 3 {
			t.Errorf("height %d: %d header rows leave only %d body rows", height, len(head), window)
		}
	}

	m = press(t, m, "d")
	if m.dependencies {
		t.Fatal("plain d opened dependency details")
	}
	m = previewCommand(t, m)
	if view := m.View(); !offers(m, "show instructions") || !strings.Contains(view, "needs dependency-00") || strings.Contains(view, "line 001") {
		t.Fatalf("dependency details did not open:\n%s", view)
	}
	m = m.scrollBy(1000)
	if view := m.View(); !strings.Contains(view, "dependency-24 — broken (missing)") {
		t.Fatalf("last dependency and its reason are unreachable:\n%s", view)
	}
	m = previewCommand(t, m)
	if !strings.Contains(m.View(), "line 001") || m.scroll != 0 {
		t.Fatal("returning to instructions did not reset the viewport")
	}
	m = previewCommand(t, m)
	m = press(t, m, "down")
	if m.dependencies {
		t.Fatal("a new stitch opened on dependencies")
	}
}

func TestDependencyPreviewFitsSmallWindows(t *testing.T) {
	m := longModel(t)
	st := m.selectedStitch()
	st.Dependencies[0].To = strings.Repeat("long-dependency-name-", 8)
	m.width = 24
	m = previewCommand(t, m)
	for height := 1; height <= 10; height++ {
		head, body, window := m.previewParts(st, height)
		if len(head)+window > height {
			t.Errorf("height %d: header and window exceed the pane", height)
		}
		for _, line := range body {
			if lipgloss.Width(line) > m.width-2 {
				t.Errorf("dependency detail overflows: %q", line)
			}
		}
		if got := lines(m.renderPreview(st, height)); got > height {
			t.Errorf("height %d rendered %d rows", height, got)
		}
	}
}

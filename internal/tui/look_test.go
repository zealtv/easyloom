package tui

import (
	"strings"
	"testing"
)

// Every pane draws a stitch the same way: the frontier carries the tree's
// glyphs. (Impact folds this fixture's ready stitch away: it releases nothing.)
func TestPanesShareTheStitchLook(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	for _, v := range []view{viewFrontier, viewTree} {
		m.view = v
		if view := m.View(); !strings.Contains(view, "● tui-thread-tree") {
			t.Errorf("%s does not draw the ready stitch with its glyph:\n%s", v.name(), view)
		}
	}
}

// With colour, the header names every view and lights the current one; with
// colour off nothing can be lit, so only the current view is named.
func TestTabsNameEveryViewOnlyWithColour(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	m.view = viewTree

	m.colour, m.styles = false, newStyles(false)
	if got := m.tabs(); got != " THREADS" {
		t.Errorf("colourless tabs = %q", got)
	}

	m.colour, m.styles = true, newStyles(true)
	got := m.tabs()
	for _, title := range []string{"FRONTIER", "QUEUE", "THREADS", "IMPACT"} {
		if !strings.Contains(got, title) {
			t.Errorf("tabs omit %s: %q", title, got)
		}
	}
	if !strings.Contains(got, m.styles.tab.Render("THREADS")) {
		t.Errorf("the current view is not lit: %q", got)
	}
}

// A header's counts keep their words, and each number takes the hue of what
// it counts; a zero recedes.
func TestTallyColoursTheCounts(t *testing.T) {
	m := treeModel(t, threads(t.TempDir()))
	m.styles = newStyles(true)
	got := m.tally("3 ready · 0 waiting · 2 hidden")
	for _, want := range []string{
		m.styles.ready.Render("3"),
		m.styles.faint.Render("0"),
		m.styles.dim.Render("2"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tally %q does not contain %q", got, want)
		}
	}

	m.styles = newStyles(false)
	if got := m.tally("3 ready · 0 waiting"); got != "3 ready · 0 waiting" {
		t.Errorf("colourless tally = %q", got)
	}
}

package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// One visual language for a stitch, in every pane. A stitch is drawn as a
// glyph and a name: the glyph says what it is doing, in colour, and the name
// says how live it is, quietly. The panes ask the same question of the same
// projection, and a stitch that looked one way in the tree and another in the
// queue would be two answers.

// look is how a stitch is drawn: a glyph and a hue for what it is doing, and a
// style for its name.
type look struct {
	glyph string
	hue   lipgloss.Style
	name  lipgloss.Style
}

// stitchLook decides a stitch's look, so that what is left to do reads down a
// list without reading its status column. Colour lives in the glyph and the
// status, and the name stays quiet:
//
//   - live work — ready, in hand, blocked — is drawn in the plain foreground.
//   - parked work — waiting, directly or inherited — is italic and softer. It
//     is still work, only paused, and must not look finished.
//   - finished work recedes to the faintest grey: tied, and dropped or
//     abandoned struck through as well, because "done" and "will not be done"
//     are not the same outcome.
//
// The glyph carries the state with colour off, where every style is plain:
// ● ready, ◐ stitching, ◆ tending, ‖ waiting, ○ blocked or open, ✓ tied,
// ✗ dropped or abandoned.
func (m Model) stitchLook(st *snapshot.Stitch) look {
	switch {
	case st == nil:
		return look{"!", m.styles.warn, m.styles.warn}
	case over(st) && st.State != snapshot.StateTied:
		return look{"✗", m.styles.faint, m.styles.struck}
	case st.State == snapshot.StateTied:
		return look{"✓", m.styles.faint, m.styles.faint}
	case st.State == snapshot.StateWaiting:
		return look{"‖", m.styles.waiting, m.styles.parked}
	case st.WaitingInherited:
		return look{"‖", m.styles.faint, m.styles.parked}
	case st.State == snapshot.StateStitching:
		return look{"◐", m.styles.stitching, m.styles.live}
	case st.State == snapshot.StateTending:
		return look{"◆", m.styles.tending, m.styles.live}
	case st.Ready:
		return look{"●", m.styles.ready, m.styles.live}
	case blocking(st) != "":
		return look{"○", m.styles.warn, m.styles.live}
	default:
		return look{"○", m.styles.faint, m.styles.live}
	}
}

// stitchName draws a glyph and a name. Under the cursor the selection's
// colour wins, but the name keeps the rest of its cue — a dropped stitch is
// still struck through.
func (m Model) stitchName(l look, id string, selected bool) string {
	name := l.name
	if selected {
		name = m.styles.selected.Inherit(name)
	}
	return l.hue.Render(l.glyph) + " " + name.Render(id)
}

// over reports a stitch that is no longer work: tied, dropped or abandoned.
func over(st *snapshot.Stitch) bool {
	return terminal(st) || st.State == snapshot.StateAbandoned
}

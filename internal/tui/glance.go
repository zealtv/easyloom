package tui

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/twglance"
)

// EasyLoom's glance for tireless-watcher's map (twglance): a badge line that
// counts the queue by state, and the queue itself, one row per record in the
// order the loom will read it, each with the glyph the queue pane draws.
// An id too long for a row is cut in the middle, keeping its head and its
// tail (glance-…1-plain), as the map cuts names; twglance would cut it at the
// end.

// glanceRole is the map's role for a stitch, beside the glyph stitchLook
// gives it, so the colour on the map says what the colour in EasyLoom says.
func glanceRole(st *snapshot.Stitch) string {
	switch {
	case st == nil:
		return twglance.Bad
	case over(st):
		return twglance.Quiet
	case st.State == snapshot.StateWaiting || st.WaitingInherited:
		return twglance.Hold
	case st.State == snapshot.StateStitching:
		return twglance.Busy
	case st.State == snapshot.StateTending:
		return twglance.Other
	case st.Ready:
		return twglance.OK
	case blocking(st) != "":
		return twglance.Hold
	default:
		return twglance.Quiet
	}
}

// badgeOrder is the badge line's order, most pressing first, by the glyph
// stitchLook gives. A record the loom cannot resolve leads: the map drops
// badges from the end, and that one needs repair. ○ counts blocked and not
// yet ready together, as one mark, so the line reads the same with colour
// off; it takes hold when any of them is blocked, quiet otherwise. Finished
// stitches are not counted.
var badgeOrder = []string{"!", "◐", "◆", "●", "‖", "○"}

// glance is what the map should draw of EasyLoom now. It is empty, which
// unsets both options, when no loom is open.
func (m Model) glance() twglance.Glance {
	if m.cache == nil || m.state.Snapshot == nil {
		return twglance.Glance{}
	}
	rows, _ := queueList(m.state.Snapshot, m.over)
	counts := map[string]int{}
	roles := map[string]string{}
	g := twglance.Glance{Badges: []twglance.Entry{{Role: twglance.Label, Text: "loom"}}}
	for _, r := range rows {
		e := twglance.Entry{Role: glanceRole(r.stitch), Text: m.stitchLook(r.stitch).glyph}
		if r.problem != "" { // as the queue pane draws it: a record the loom cannot resolve
			e = twglance.Entry{Role: twglance.Bad, Text: "!"}
		}
		id := r.id
		if id == "" {
			id = "(unnamed record)"
		}
		g.Rows = append(g.Rows, twglance.Entry{Role: e.Role,
			Text: e.Text + " " + cutMiddle(id, twglance.RowCells-ansi.StringWidth(e.Text)-1)})
		if e.Role == twglance.Bad || !over(r.stitch) {
			counts[e.Text]++
			if roles[e.Text] != twglance.Hold {
				roles[e.Text] = e.Role
			}
		}
	}
	for _, glyph := range badgeOrder {
		if n := counts[glyph]; n > 0 {
			g.Badges = append(g.Badges, twglance.Entry{Role: roles[glyph], Text: fmt.Sprintf("%s%d", glyph, n)})
		}
	}
	return g
}

// cutMiddle is s at most cells wide: whole when it fits, else its head, "…"
// and its tail, the tail at most half the room (a wide rune can leave the
// head a cell shorter).
func cutMiddle(s string, cells int) string {
	if ansi.StringWidth(s) <= cells {
		return s
	}
	if cells < 1 {
		return ""
	}
	runes := []rune(s)
	room := cells - 1 // for "…"
	tailRoom := room / 2
	tail, w := len(runes), 0
	for tail > 0 {
		rw := ansi.StringWidth(string(runes[tail-1]))
		if w+rw > tailRoom {
			break
		}
		w += rw
		tail--
	}
	return ansi.Truncate(string(runes[:tail]), room-w, "") + "…" + string(runes[tail:])
}

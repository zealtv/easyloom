package tui

import (
	"fmt"
	"strings"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// row is one line of the frontier list.
type row struct {
	id string
	// thread is the goal the stitch descends from. On this repo's loom the
	// frontier spans four threads at once, and the ID alone does not say which.
	thread string
	// position is the one-based queue position, or 0 for an unqueued stitch.
	// The queue is sparse and opt-in: most items having none is normal.
	position int
}

// counts summarise the active loom behind the frontier. An almost-empty
// frontier must read as "mostly parked" rather than "nothing to do", so the
// waiting count sits beside the ready count permanently.
type counts struct {
	ready   int
	waiting int
	hidden  int
}

// frontier turns the projection into the list, in the order the projection
// gives. That order is already the effective preference order — queued first,
// then unqueued ready stitches in bytewise path order — so it is never
// re-sorted here.
func frontier(snap *snapshot.Snapshot) ([]row, counts) {
	index := snap.Index()
	c := summarise(snap)

	rows := make([]row, 0, len(snap.Frontier))
	for _, id := range snap.Frontier {
		st, ok := index[id]
		if !ok || st.Archived || st.Legacy {
			continue
		}
		r := row{id: id, thread: st.RootID}
		if st.QueuePosition != nil {
			r.position = *st.QueuePosition
		}
		rows = append(rows, r)
	}
	return rows, c
}

// summarise counts the active loom behind whichever view is drawing it. Both
// views filter and count the same way, so both say the same thing about what
// is not on screen.
func summarise(snap *snapshot.Snapshot) counts {
	var c counts
	for i := range snap.Stitches {
		st := &snap.Stitches[i]
		if hidden(st) {
			c.hidden++
			continue
		}
		switch {
		// Terminal children stay in place in v2, so the active trays are full
		// of finished work. It is neither ready nor parked, whatever it
		// inherits from a parked ancestor.
		case terminal(st):
		case st.State == snapshot.StateWaiting || st.WaitingInherited:
			c.waiting++
		case st.Ready:
			c.ready++
		}
	}
	return c
}

// renderList draws the frontier, windowed so the selection stays visible.
func (m Model) renderList(rows []row, height int) string {
	if len(rows) == 0 {
		// An empty frontier is a real state with a real cause: everything left
		// is parked, claimed, or blocked. This repo's own loom reaches it.
		return m.styles.dim.Render(
			" Nothing is ready. Every remaining stitch is parked, claimed, or blocked.")
	}
	if height < 1 {
		height = 1
	}

	start := m.offset(len(rows), height)
	id, thread := columnWidths(rows)
	index := m.state.Snapshot.Index()

	var b strings.Builder
	for i := start; i < len(rows) && i < start+height; i++ {
		if i > start {
			b.WriteString("\n")
		}
		b.WriteString(m.renderRow(rows[i], i, i == m.selected, id, thread, index[rows[i].id]))
	}
	return b.String()
}

func (m Model) renderRow(r row, index int, selected bool, id, thread int, st *snapshot.Stitch) string {
	marker := " "
	if selected {
		marker = "❯"
	}
	// The rank is the order the loom prefers, and quieter than the work it
	// ranks. Padding goes outside the styles so the columns line up.
	rank := m.styles.faint.Render(fmt.Sprintf("%2d", index+1))
	name := m.stitchName(m.stitchLook(st), r.id, selected) + strings.Repeat(" ", id-len(r.id))
	line := fmt.Sprintf("%s %s  %s  %s", marker, rank, name, m.styles.soft.Render(r.thread))
	if r.position > 0 {
		line += strings.Repeat(" ", thread-len(r.thread)) +
			m.styles.queue.Render(fmt.Sprintf("   queued %d", r.position))
	}
	if selected {
		return m.styles.selected.Render(line)
	}
	return line
}

// offset scrolls the window just enough to keep the selection on screen.
func (m Model) offset(total, height int) int {
	if total <= height {
		return 0
	}
	start := m.selected - height/2
	if start < 0 {
		start = 0
	}
	if start > total-height {
		start = total - height
	}
	return start
}

// columnWidths sizes the ID and thread columns to the widest entry, so the
// queue badges line up.
func columnWidths(rows []row) (id, thread int) {
	for _, r := range rows {
		if len(r.id) > id {
			id = len(r.id)
		}
		if len(r.thread) > thread {
			thread = len(r.thread)
		}
	}
	return id, thread
}

// header is the count line beside the FRONTIER title.
func (c counts) header() string {
	parts := []string{
		fmt.Sprintf("%d ready", c.ready),
		fmt.Sprintf("%d waiting", c.waiting),
	}
	if c.hidden > 0 {
		parts = append(parts, fmt.Sprintf("%d hidden", c.hidden))
	}
	return strings.Join(parts, " · ")
}

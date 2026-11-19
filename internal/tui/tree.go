package tui

import (
	"fmt"
	"strings"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// The thread tree: an indented view of the decomposition, one root per goal
// stitch. Nesting is the only relationship drawn — this is deliberately not the
// dependency graph. A stitch's needs belong in the preview, which already shows
// them with their status.
//
// The frontier answers "what next". On a mature loom that is one line, and it
// says nothing about shape. This is the view that makes the other thirty-odd
// stitches legible, and the one where a stitch that is not ready — the only
// kind w, resume and d are interesting on — can be selected at all.

// treeRow is one line of the tree.
type treeRow struct {
	id    string
	depth int
	// children is how many children the node has that are drawn at all.
	children int
	expanded bool
	// folded counts the descendants hidden because this node is collapsed,
	// and unresolved how many of those are still active. A tree contains
	// completed work inline in v2, so saying how much is folded away is the
	// difference between a tidy tree and a lie.
	folded     int
	unresolved int
	// done and total measure the whole subtree under a node, folded or not:
	// how far through its thread a goal is.
	done   int
	total  int
	stitch *snapshot.Stitch
}

// terminal reports a stitch that is finished, whichever way it finished.
func terminal(st *snapshot.Stitch) bool {
	return st.State == snapshot.StateTied || st.State == snapshot.StateDropped
}

// tree lays the decomposition out in draw order.
//
// Structure comes from the projection — parent_id for the roots and children
// for the edges — and never from walking .loom. Decomposition is a tree by
// construction, but a repeated ID is guarded against rather than trusted not
// to happen: a cycle must not hang the render.
func tree(snap *snapshot.Snapshot, expanded map[string]bool) ([]treeRow, counts) {
	index := snap.Index()
	seen := map[string]bool{}
	var rows []treeRow

	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		st, ok := index[id]
		if !ok || hidden(st) || seen[id] {
			return
		}
		seen[id] = true

		children := visibleChildren(index, st)
		row := treeRow{
			id:       id,
			depth:    depth,
			children: len(children),
			expanded: isExpanded(expanded, st, len(children)),
			stitch:   st,
		}
		if len(children) > 0 {
			row.done, row.total = progress(index, children, map[string]bool{id: true})
		}
		here := len(rows)
		rows = append(rows, row)

		if len(children) == 0 {
			return
		}
		if !rows[here].expanded {
			rows[here].folded, rows[here].unresolved = fold(index, children, seen)
			return
		}
		for _, child := range children {
			walk(child, depth+1)
		}
	}

	for i := range snap.Stitches {
		st := &snap.Stitches[i]
		// A goal stitch is a root of threads/. An archived goal is history and
		// a legacy record has no ancestry at all.
		if st.ParentID == nil && st.Tray == snapshot.TrayThreads && !hidden(st) {
			walk(st.ID, 0)
		}
	}
	return rows, summarise(snap)
}

// hidden is the filter every view shares: archived and legacy records are
// counted, not drawn. On a mature loom they are most of the file tree.
func hidden(st *snapshot.Stitch) bool { return st.Archived || st.Legacy }

func visibleChildren(index map[string]*snapshot.Stitch, st *snapshot.Stitch) []string {
	var out []string
	for _, id := range st.Children {
		if child, ok := index[id]; ok && !hidden(child) {
			out = append(out, id)
		}
	}
	return out
}

// isExpanded applies the user's choice for a node, or the default: open,
// except for a subtree whose root is already finished.
func isExpanded(expanded map[string]bool, st *snapshot.Stitch, children int) bool {
	if children == 0 {
		return false
	}
	if choice, ok := expanded[st.ID]; ok {
		return choice
	}
	return !terminal(st)
}

// fold counts what a collapsed node hides, and marks it seen so the cycle
// guard stays consistent with what was drawn.
func fold(index map[string]*snapshot.Stitch, children []string, seen map[string]bool) (folded, unresolved int) {
	for _, id := range children {
		st, ok := index[id]
		if !ok || hidden(st) || seen[id] {
			continue
		}
		seen[id] = true
		folded++
		if !terminal(st) {
			unresolved++
		}
		f, u := fold(index, visibleChildren(index, st), seen)
		folded += f
		unresolved += u
	}
	return folded, unresolved
}

// progress counts a subtree and how much of it is finished, with its own
// guard: fold marks what it hides as seen, so the draw's guard cannot be
// shared.
func progress(index map[string]*snapshot.Stitch, children []string, seen map[string]bool) (done, total int) {
	for _, id := range children {
		st, ok := index[id]
		if !ok || hidden(st) || seen[id] {
			continue
		}
		seen[id] = true
		total++
		if over(st) {
			done++
		}
		d, t := progress(index, visibleChildren(index, st), seen)
		done += d
		total += t
	}
	return done, total
}

// renderTree draws the tree, windowed so the selection stays visible.
func (m Model) renderTree(rows []treeRow, height int) string {
	if len(rows) == 0 {
		return m.styles.dim.Render(" This loom has no active threads.")
	}
	if height < 1 {
		height = 1
	}

	start := m.offset(len(rows), height)
	var b strings.Builder
	for i := start; i < len(rows) && i < start+height; i++ {
		if i > start {
			b.WriteString("\n")
		}
		b.WriteString(m.renderTreeRow(rows[i], i == m.selected))
	}
	return b.String()
}

func (m Model) renderTreeRow(r treeRow, selected bool) string {
	marker := " "
	if selected {
		marker = "❯"
	}
	// Expansion is drawn where the node is, so the shape of the tree survives
	// a terminal with no colour at all.
	knob := " "
	if r.children > 0 {
		knob = "▸"
		if r.expanded {
			knob = "▾"
		}
	}
	l := m.treeLook(r)
	left := fmt.Sprintf("%s %s%s %s", marker, strings.Repeat("  ", r.depth),
		m.styles.faint.Render(knob), m.stitchName(l, r.id, selected))
	right := m.treeStatus(r, l)

	line := m.pad(left, right+" ")
	if selected {
		return m.styles.selected.Render(line)
	}
	return line
}

// treeLook is a row's look: the stitch's own, with goals bold as the
// headings of their threads.
func (m Model) treeLook(r treeRow) look {
	l := m.stitchLook(r.stitch)
	st := r.stitch
	if r.depth == 0 && !over(st) && st.State != snapshot.StateWaiting && !st.WaitingInherited {
		l.name = m.styles.goal
	}
	return l
}

// treeStatus is the right-hand column: what state this node is in, and what is
// folded underneath it, in the hue of the row's glyph.
func (m Model) treeStatus(r treeRow, look look) string {
	st := r.stitch
	over := over(st)
	var parts []string

	if st.State != snapshot.StatePlain {
		parts = append(parts, string(st.State))
	}
	// A directly waiting stitch is a decision somebody made and can be
	// resumed; an inherited wait is a consequence and cannot. Drawing them the
	// same way invites a resume the loom will refuse. Finished work under a
	// parked parent inherits the wait too, and saying so about a stitch that
	// is already dropped is noise — as is saying what it still needs.
	if st.WaitingInherited && st.State != snapshot.StateWaiting && !over {
		parts = append(parts, "waiting inherited")
	}
	if st.Ready {
		parts = append(parts, "ready")
	} else if needs := blocking(st); needs != "" && !over {
		parts = append(parts, needs)
	}
	if r.folded > 0 {
		if r.unresolved > 0 {
			parts = append(parts, fmt.Sprintf("%d unresolved", r.unresolved))
		} else {
			parts = append(parts, fmt.Sprintf("%d folded", r.folded))
		}
	}
	text := look.hue.Render(strings.Join(parts, " · "))
	if r.total == 0 {
		return text
	}
	if text != "" {
		text += "  "
	}
	return text + m.meter(r.done, r.total)
}

// meterWidth is the progress bar's length in cells.
const meterWidth = 8

// meter draws how far through its subtree a node is: a bar, finished part in
// the ready hue, after the count, which is what survives with colour off.
func (m Model) meter(done, total int) string {
	filled := done * meterWidth / total
	if done > 0 && filled == 0 {
		filled = 1
	}
	// The count leads so the bars line up down the right edge.
	return m.styles.faint.Render(fmt.Sprintf("%d/%d ", done, total)) +
		m.styles.ready.Render(strings.Repeat("━", filled)) +
		m.styles.faint.Render(strings.Repeat("─", meterWidth-filled))
}

// blocking names the dependencies in this stitch's way. The projection resolves
// them; nothing here decides what satisfies what.
func blocking(st *snapshot.Stitch) string {
	var ids []string
	for _, dep := range st.Dependencies {
		if dep.Status != snapshot.DependencySatisfied && dep.Status != snapshot.DependencyInert {
			ids = append(ids, dep.To)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	if len(ids) > 2 {
		return fmt.Sprintf("needs %s +%d", strings.Join(ids[:2], ", "), len(ids)-2)
	}
	return "needs " + strings.Join(ids, ", ")
}

// treeRows is the tree as the model currently draws it.
func (m Model) treeRows() []treeRow {
	if m.state.Snapshot == nil {
		return nil
	}
	rows, _ := tree(m.state.Snapshot, m.expanded)
	return rows
}

// toggleExpand opens or closes the selected node, or sets it explicitly for
// the arrow keys. The choice is remembered by ID, so a background re-snapshot
// does not fold the tree up under the user.
//
// The impact pane's zero-release folds live in the same map, keyed by their
// group header's pseudo-ID. Two panes with two kinds of fold would be two
// keystrokes to learn for one idea, and the reader already knows this one.
func (m Model) toggleExpand(open *bool) Model {
	var id string
	var was bool
	switch m.view {
	case viewTree:
		rows := m.treeRows()
		if m.selected < 0 || m.selected >= len(rows) {
			return m
		}
		row := rows[m.selected]
		if row.children == 0 {
			return m
		}
		id, was = row.id, row.expanded
	case viewImpact:
		rows := m.impactRows()
		if m.selected < 0 || m.selected >= len(rows) {
			return m
		}
		row := rows[m.selected]
		if row.kind != impactHeader || row.hidden == 0 {
			return m
		}
		id, was = row.id, row.open
	default:
		return m
	}

	want := !was
	if open != nil {
		want = *open
	}
	if want == was {
		return m
	}

	// Copy on write: the model is passed by value, and an expansion choice
	// should not reach back into the frame that produced it.
	next := make(map[string]bool, len(m.expanded)+1)
	for key, choice := range m.expanded {
		next[key] = choice
	}
	next[id] = want
	m.expanded = next
	return m.reselect()
}

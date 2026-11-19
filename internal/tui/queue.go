package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// The queue pane: `.loom/queue` in order, one row per record, with what the
// loom will really do with each one.
//
// The queue is the current milestone, and until this view it was legible only
// as a badge on the frontier — mixed in with unqueued rows, and reordered
// against a neighbour that was not the one the eye was on. Here every row is a
// queue record, in queue order, so a move is a move past the row above or
// below it.
//
// Order comes from queue_position in the projection and is never recomputed.
// Readiness comes from ready. This view decides nothing about the queue; it
// says what the projection already says, in the order the loom will read it.

// queueRow is one record in the queue.
type queueRow struct {
	// position is the projection's one-based position among ID records.
	position int
	// id is the record's stitch ID. A stale record still has one — it names a
	// stitch — and that is what makes it repairable.
	id string
	// stitch is nil for a record the projection has no stitch for.
	stitch *snapshot.Stitch
	// next marks the row `loom next` would actually hand over. It is not
	// always row 1.
	next bool
	// skipped is a record next passes over: parked, claimed, finished,
	// dependency-blocked or invalid.
	skipped bool
	// problem is the loom's own words about an invalid or stale record.
	problem string
	// impossible says why this row can never become ready without somebody
	// repairing the loom.
	impossible string
	// inverted names a dependency of this row that is queued below it, or not
	// queued at all.
	inverted string
}

// queueCounts is what the queue adds up to. A queue whose head is skipped is a
// queue that is not saying what it looks like it is saying, so the skipped
// count sits beside the length permanently.
type queueCounts struct {
	queued  int
	skipped int
}

func (c queueCounts) header() string {
	return fmt.Sprintf("%d queued · %d skipped", c.queued, c.skipped)
}

// queueErrorCode is the diagnostic the loom raises for a record it cannot
// resolve. The code set is open, so an unrecognised code is ignored rather
// than treated as a failure.
const queueErrorCode = "queue_error"

// queueRecord picks the ID and position out of a queue_error message. The
// diagnostic's structured fields are null for these, and the position is what
// places the record in the list — but a message that does not match is still
// shown, at the end, rather than dropped.
var queueRecord = regexp.MustCompile(`'([^']*)'.*\bposition ([0-9]+)`)

// queueList is the queue as the loom will read it, with any unconfirmed
// mutation laid over it so a move redraws on the keystroke.
func queueList(snap *snapshot.Snapshot, over overlay) ([]queueRow, queueCounts) {
	index := snap.Index()
	positions := effectiveQueue(snap, over)

	rows := make([]queueRow, 0, len(positions))
	for id, position := range positions {
		rows = append(rows, queueRow{position: position, id: id, stitch: index[id]})
	}
	rows = appendQueueProblems(rows, snap, over)
	sortQueue(rows)

	return markQueue(rows, positions, over)
}

// effectiveQueue is queue_position per stitch with the optimistic overlay
// applied. A tie or drop evicts an ID from the queue without anyone touching
// it, which is why the overlay records that separately from leaving the
// frontier: a claim or a park does not.
func effectiveQueue(snap *snapshot.Snapshot, over overlay) map[string]int {
	positions := map[string]int{}
	for i := range snap.Stitches {
		st := &snap.Stitches[i]
		if hidden(st) || st.QueuePosition == nil {
			continue
		}
		positions[st.ID] = *st.QueuePosition
	}
	for id, position := range over.queued {
		if position == nil {
			delete(positions, id)
			continue
		}
		positions[id] = *position
	}
	for id := range over.evicted {
		delete(positions, id)
	}
	return positions
}

// appendQueueProblems folds the loom's queue diagnostics into the list: onto
// the row they concern when the projection has one, and as a row of their own
// when it does not.
//
// These matter more than they look. An unrelated invalid record makes an
// unrelated queue mutation fail without changing the file, which is otherwise
// an inexplicable refusal.
func appendQueueProblems(rows []queueRow, snap *snapshot.Snapshot, over overlay) []queueRow {
	// A move renumbers the queue optimistically, so a diagnostic's position no
	// longer places anything. Its message still belongs to its ID.
	moved := len(over.queued) > 0

	for _, d := range snap.Diagnostics {
		if d.Code != queueErrorCode {
			continue
		}
		id, position := "", 0
		if match := queueRecord.FindStringSubmatch(d.Message); match != nil {
			id = match[1]
			position, _ = strconv.Atoi(match[2])
		}

		if found := indexOfQueueRow(rows, id); id != "" && found >= 0 {
			rows[found].problem = d.Message
			continue
		}
		// The record names no stitch the projection has, so nothing else in
		// this view would mention it at all.
		if moved {
			position = 0
		}
		rows = append(rows, queueRow{position: position, id: id, problem: d.Message})
	}
	return rows
}

func indexOfQueueRow(rows []queueRow, id string) int {
	for i := range rows {
		if rows[i].id == id {
			return i
		}
	}
	return -1
}

// sortQueue puts the rows in queue order. Position is the projection's and
// unique among records; a record whose position could not be read draws at the
// end, where it is visible without displacing anything that has one.
func sortQueue(rows []queueRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].position, rows[j].position
		if (left == 0) != (right == 0) {
			return right == 0
		}
		if left != right {
			return left < right
		}
		return rows[i].id < rows[j].id
	})
}

// markQueue works out what the loom will do with each record: which one it
// would take next, which it will skip, which can never become ready, and which
// sits above something it needs.
func markQueue(rows []queueRow, positions map[string]int, over overlay) ([]queueRow, queueCounts) {
	c := queueCounts{queued: len(rows)}
	taken := false

	for i := range rows {
		r := &rows[i]
		st := r.stitch

		switch {
		case st == nil:
			r.skipped = true
		case terminal(st):
			r.skipped = true
		// A mutation in flight has already taken this stitch out of play, and
		// the row must not still offer it as the next thing to do.
		case over.gone[r.id]:
			r.skipped = true
		case st.Ready:
			// next scans queued IDs in order and takes the first ready one.
			if !taken {
				r.next, taken = true, true
			}
		default:
			r.skipped = true
		}
		if r.skipped {
			c.skipped++
		}

		if st != nil {
			r.impossible = impossible(st)
			if r.impossible == "" {
				r.inverted = inversion(st, positions, r.position)
			}
		}
	}
	return rows, c
}

// impossible names a row that cannot become ready until somebody repairs the
// loom: a broken dependency, or membership of a dependency cycle. The
// projection resolves both. A queue made of these is a plan that cannot run.
func impossible(st *snapshot.Stitch) string {
	if len(st.Cycle) > 0 {
		return "never: in a dependency cycle"
	}
	for _, dep := range st.Dependencies {
		if dep.Status != snapshot.DependencyBroken {
			continue
		}
		reason := "broken"
		if dep.Reason != nil {
			reason = string(*dep.Reason)
		}
		return fmt.Sprintf("never: %s is %s", dep.To, reason)
	}
	return ""
}

// inversion reports a dependency of this row queued below it, or not queued at
// all. That is not impossible — next skips the row and takes the dependency
// anyway — but then the queue is no longer the order the work will happen in,
// which is the only thing the queue is for.
func inversion(st *snapshot.Stitch, positions map[string]int, position int) string {
	var below []string
	for _, dep := range st.Dependencies {
		if dep.Status != snapshot.DependencyBlocked {
			continue
		}
		at, queued := positions[dep.To]
		if queued && at < position {
			continue
		}
		if queued {
			below = append(below, dep.To+" is queued below")
		} else {
			below = append(below, dep.To+" is not queued")
		}
	}
	if len(below) == 0 {
		return ""
	}
	if len(below) > 1 {
		return fmt.Sprintf("inverted: %s +%d", below[0], len(below)-1)
	}
	return "inverted: " + below[0]
}

// queueOrder is the queue as a list of IDs the loom would accept as anchors: a
// record with no stitch behind it is not one, and a queue mutation naming it
// would be refused.
func queueOrder(rows []queueRow) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.stitch != nil {
			ids = append(ids, r.id)
		}
	}
	return ids
}

// renderQueue draws the queue, windowed so the selection stays visible.
func (m Model) renderQueue(rows []queueRow, height int) string {
	if len(rows) == 0 {
		// The queue draining is the milestone finishing. This repo's own loom
		// reaches it, and an empty box would say nothing about why.
		return m.styles.dim.Render(
			" The queue is empty. That is a milestone finished — queue the next bite with Q.")
	}
	if height < 1 {
		height = 1
	}

	start := m.offset(len(rows), height)
	width := queuePositionWidth(rows)

	var b strings.Builder
	for i := start; i < len(rows) && i < start+height; i++ {
		if i > start {
			b.WriteString("\n")
		}
		b.WriteString(m.renderQueueRow(rows[i], i == m.selected, width))
	}
	return b.String()
}

func (m Model) renderQueueRow(r queueRow, selected bool, width int) string {
	marker := " "
	if selected {
		marker = "❯"
	}
	position := strings.Repeat(" ", width)
	if r.position > 0 {
		position = fmt.Sprintf("%*d", width, r.position)
	}
	id := r.id
	if id == "" {
		id = "(unnamed record)"
	}
	l := m.stitchLook(r.stitch)
	if r.problem != "" && r.stitch != nil {
		l.glyph, l.hue = "!", m.styles.warn
	}

	left := fmt.Sprintf("%s %s  %s", marker, m.styles.queue.Render(position), m.stitchName(l, id, selected))
	line := m.pad(left, m.queueStatus(r, l)+" ")
	if selected {
		return m.styles.selected.Render(line)
	}
	return line
}

// queueStatus is the right-hand column: what the loom will do with this record.
func (m Model) queueStatus(r queueRow, l look) string {
	if r.stitch == nil {
		text := "stale record — no such stitch"
		if r.problem != "" {
			text = r.problem
		}
		return m.styles.warn.Render(text)
	}

	st := r.stitch
	var parts []string
	if st.State != snapshot.StatePlain {
		parts = append(parts, string(st.State))
	}
	if st.WaitingInherited && st.State != snapshot.StateWaiting && !over(st) {
		parts = append(parts, "waiting inherited")
	}
	if st.Ready {
		parts = append(parts, "ready")
	} else if needs := blocking(st); needs != "" && !over(st) {
		parts = append(parts, needs)
	}
	text := l.hue.Render(strings.Join(parts, " · "))

	// What is wrong with a record comes after what it is, in the colour of
	// something to fix.
	var wrong []string
	for _, w := range []string{r.impossible, r.inverted, r.problem} {
		if w != "" {
			wrong = append(wrong, w)
		}
	}
	if len(wrong) > 0 {
		text = joinStatus(text, m.styles.warn.Render(strings.Join(wrong, " · ")), m.styles.faint)
	}
	// The row loom next would hand over leads, and is the brightest thing on
	// the line: it is not always row 1.
	if r.next {
		text = joinStatus(m.styles.next.Render("▶ next"), text, m.styles.faint)
	}
	return text
}

// joinStatus joins two rendered status segments with a separator, either of
// which may be empty.
func joinStatus(a, b string, sep lipgloss.Style) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + sep.Render(" · ") + b
}

// queuePositionWidth sizes the position column so the IDs line up.
func queuePositionWidth(rows []queueRow) int {
	width := 1
	for _, r := range rows {
		if n := len(strconv.Itoa(r.position)); n > width {
			width = n
		}
	}
	return width
}

// queueRows is the queue as the model currently draws it.
func (m Model) queueRows() []queueRow {
	rows, _ := m.queueRowsAndCounts()
	return rows
}

func (m Model) queueRowsAndCounts() ([]queueRow, queueCounts) {
	if m.state.Snapshot == nil {
		return nil, queueCounts{}
	}
	return queueList(m.state.Snapshot, m.over)
}

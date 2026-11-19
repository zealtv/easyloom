package tui

import (
	"fmt"
	"strings"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/unblock"
)

// The impact pane: what resolving each stitch would release, and what getting
// there would cost.
//
// The arithmetic is all in internal/unblock; this file only draws it — the same
// split web/impact.js and web/unblock.js are on, and the reason the tree and the
// frontier could grow an impact badge later without a third implementation of
// the one thing that must never disagree with itself.
//
// Two groups, never merged. "What should I work on now" and "what should I
// un-park" are different questions and their answers are not comparable: a
// parked stitch releasing six is not six times better than a ready one
// releasing one, it is a different decision. One leaderboard over both would
// invite exactly that comparison.
//
// The vocabulary is the browser's, word for word, because the two panes are
// answering the same question about the same loom and a reader moving between
// them should not have to translate.

// Pseudo-IDs for the rows that are not stitches. A stitch ID matches
// [A-Za-z0-9._-]+, so a space makes these impossible to collide with one —
// which matters because selection here is an ID like it is in every other pane.
const (
	impactLooseEndsID = "impact loose-ends"
	impactParkedID    = "impact parked"
	impactAlarmID     = "impact disagreement"
)

// impactKind is what a row in this pane is.
type impactKind int

const (
	// impactStitch is a ranked stitch: the rows the pane exists for.
	impactStitch impactKind = iota
	// impactHeader titles a group and carries its fold. Heading and fold are
	// one row because a terminal has lines to spare only where they say
	// something, and "loose ends, and 6 of them release nothing" is one fact.
	impactHeader
	// impactAlarm is the simulation disagreeing with the projection.
	impactAlarm
)

// impactRow is one line of the pane.
type impactRow struct {
	id   string
	kind impactKind
	// entry is the ranking for a stitch row, and zero otherwise.
	entry unblock.Entry
	// title is a header's words.
	title string
	// hidden is how many zero-impact rows a header's fold is holding, and open
	// whether they are being shown.
	hidden int
	open   bool
	// quiet marks a row that releases nothing: the ones behind the fold.
	quiet bool
	// note is a header's right-hand text when its group has nothing to rank.
	note string
	// alarm is the words of a disagreement.
	alarm string
}

// impactCounts is what the ranking adds up to.
type impactCounts struct {
	looseEnds int
	parked    int
	// wrong is the number of stitches the simulation and the projection
	// disagree about. It is always zero, and it is in the header anyway: a
	// ranking computed from arithmetic already known to be wrong is worse than
	// no ranking, so it must not be possible to read this pane without seeing
	// it.
	wrong int
}

func (c impactCounts) header() string {
	text := fmt.Sprintf("%d loose ends · %d parked", c.looseEnds, c.parked)
	if c.wrong > 0 {
		text += fmt.Sprintf(" · %d disagree", c.wrong)
	}
	return text
}

// impactList ranks the loom and lays it out in draw order.
//
// Nothing here recomputes anything internal/unblock computes, and nothing here
// reads .loom.
func impactList(snap *snapshot.Snapshot, expanded map[string]bool) ([]impactRow, impactCounts) {
	ranked := unblock.Rank(snap)
	c := impactCounts{looseEnds: len(ranked.LooseEnds), parked: len(ranked.Parked)}

	var rows []impactRow

	// The simulation re-derives readiness, and it must agree with the loom
	// about the present to be trusted about the future. Asserting it here costs
	// one pass over the projection and turns a silently wrong ranking into a
	// visible one.
	if wrong := unblock.Disagreements(snap); len(wrong) > 0 {
		c.wrong = len(wrong)
		ids := make([]string, 0, len(wrong))
		for _, d := range wrong {
			ids = append(ids, d.ID)
		}
		rows = append(rows, impactRow{
			id:   impactAlarmID,
			kind: impactAlarm,
			alarm: fmt.Sprintf(
				"This ranking cannot be trusted: the simulation disagrees with the loom about %s. That is a bug in internal/unblock, not in the loom.",
				summarised(ids, 3)),
		})
	}

	rows = appendImpactGroup(rows, impactLooseEndsID, "LOOSE ENDS — what to work on now",
		ranked.LooseEnds, expanded, impactBlurb{
			empty: "nothing active — every thread is tied, dropped or parked",
			none:  "nothing active would release anything, which is a healthy loom",
		})
	rows = appendImpactGroup(rows, impactParkedID, "PARKED — what to un-park",
		ranked.Parked, expanded, impactBlurb{
			empty: "nothing is parked",
			none:  "nothing parked is holding anything back",
		})

	return rows, c
}

// impactBlurb is what a group says when it has no rows worth ranking. The two
// are different answers: an empty group and a group where everything releases
// nothing are not the same loom, and neither is a broken view.
type impactBlurb struct {
	empty string
	none  string
}

func appendImpactGroup(rows []impactRow, id, title string, entries []unblock.Entry, expanded map[string]bool, blurb impactBlurb) []impactRow {
	var carries, zero []unblock.Entry
	for _, entry := range entries {
		if len(entry.Releases) > 0 {
			carries = append(carries, entry)
		} else {
			zero = append(zero, entry)
		}
	}

	// The fold is the tree's fold, keyed by this header's ID. Closed by
	// default, which is the opposite of the tree's default and the right one
	// here: on this repo's own loom most rows release nothing, and a list of
	// zeroes buries the three that matter.
	open := expanded[id]
	header := impactRow{
		id: id, kind: impactHeader, title: title,
		hidden: len(zero), open: open,
	}
	switch {
	case len(entries) == 0:
		header.note = blurb.empty
	case len(carries) == 0:
		header.note = blurb.none
	}
	rows = append(rows, header)

	for _, entry := range carries {
		rows = append(rows, impactRow{id: entry.ID, kind: impactStitch, entry: entry})
	}
	if open {
		for _, entry := range zero {
			rows = append(rows, impactRow{id: entry.ID, kind: impactStitch, entry: entry, quiet: true})
		}
	}
	return rows
}

// renderImpact draws the ranking, windowed so the selection stays visible.
func (m Model) renderImpact(rows []impactRow, height int) string {
	if len(rows) == 0 {
		return m.styles.dim.Render(" Nothing to rank. This loom has no active stitches.")
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
		b.WriteString(m.renderImpactRow(rows[i], i == m.selected))
	}
	return b.String()
}

func (m Model) renderImpactRow(r impactRow, selected bool) string {
	marker := " "
	if selected {
		marker = "❯"
	}

	switch r.kind {
	case impactAlarm:
		return m.styles.warn.Render(m.pad(marker+" "+r.alarm, ""))

	case impactHeader:
		left := marker + " " + m.styles.heading.Render(r.title)
		right := m.styles.dim.Render(r.note)
		if r.hidden > 0 {
			knob, text := "▸", fmt.Sprintf("%d release nothing", r.hidden)
			if r.open {
				knob, text = "▾", fmt.Sprintf("hide %d that release nothing", r.hidden)
			}
			right = m.styles.dim.Render(knob + " " + text)
		}
		line := m.pad(left, right+" ")
		if selected {
			return m.styles.selected.Render(line)
		}
		return line

	default:
		l := m.stitchLook(m.state.Snapshot.Index()[r.id])
		// A stitch that releases nothing is behind the fold for a reason.
		if r.quiet && !r.entry.Ready {
			l.name = m.styles.faint
		}
		left := fmt.Sprintf("%s   %s", marker, m.stitchName(l, r.id, selected))
		line := m.pad(left, m.impactStatus(r)+" ")
		if selected {
			return m.styles.selected.Render(line)
		}
		return line
	}
}

// impactStatus is the right-hand column: what this stitch would release, then
// what resolving it would take.
//
// The names come first because the names are the feature. "2" is a
// leaderboard; "2: mutation-refresh, queue-reorder-ui" is an answer, and the
// reader can check it against the loom without leaving the row.
func (m Model) impactStatus(r impactRow) string {
	text := m.styles.faint.Render("releases nothing")
	if n := len(r.entry.Releases); n > 0 {
		text = m.styles.ready.Render(fmt.Sprintf("releases %d", n)) +
			m.styles.soft.Render(": "+summarised(r.entry.Releases, 2))
	}
	cost := impactCost(r.entry)
	if cost == "" {
		return text
	}
	hue := m.styles.faint
	switch r.entry.Action {
	case unblock.ActionTie:
		hue = m.styles.ready
	case unblock.ActionUnblock, unblock.ActionCycle:
		hue = m.styles.warn
	case unblock.ActionResume, unblock.ActionPromote:
		hue = m.styles.waiting
	case unblock.ActionDecompose:
		hue = m.styles.dim
	default:
		hue = m.styles.stitching
	}
	if r.quiet && r.entry.Action != unblock.ActionCycle {
		hue = m.styles.faint
	}
	return joinStatus(text, hue.Render(cost), m.styles.faint)
}

// impactCost is what resolving this stitch would take, in the loom's own words.
//
// A stitch parked by inheritance is the case worth spelling out. `resume` lifts
// only a stitch's own suffix, so `resume <child>` fails with "not directly
// waiting" — the reader has to promote it to its own thread or resume the
// ancestor, and which of those they want is a real decision. This repo has been
// bitten by that difference.
func impactCost(entry unblock.Entry) string {
	switch entry.Action {
	case unblock.ActionTie:
		return "ready"
	case unblock.ActionUnblock:
		names := make([]string, 0, len(entry.BlockedBy))
		for _, blocker := range entry.BlockedBy {
			names = append(names, namedBlocker(blocker))
		}
		return fmt.Sprintf("blocked by %d: %s", len(names), summarised(names, 2))
	case unblock.ActionDecompose:
		return fmt.Sprintf("%s: %s",
			plural(len(entry.OpenChildren), "open child", "open children"),
			summarised(entry.OpenChildren, 2))
	case unblock.ActionResume:
		// Not "loom resume <id>": the ID is already the left-hand end of this
		// row, and repeating it is what pushes the line off a narrow terminal.
		return "waiting: loom resume"
	case unblock.ActionPromote:
		if entry.ResumeVia == nil {
			return "parked by inheritance: promote it to its own thread"
		}
		return fmt.Sprintf("parked by %s: promote it, or resume %s", *entry.ResumeVia, *entry.ResumeVia)
	case unblock.ActionCycle:
		return "in a cycle: never ready until an edge is removed"
	default:
		return "in hand: already claimed or tended"
	}
}

func namedBlocker(blocker unblock.Blocker) string {
	if blocker.Status != snapshot.DependencyBroken {
		return blocker.ID
	}
	reason := "broken"
	if blocker.Reason != nil {
		reason = string(*blocker.Reason)
	}
	return blocker.ID + " (" + reason + ")"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// summarised names the first few and counts the rest, the way blocking() and
// inversion() do. The terminal is narrower than the browser and a row that
// wraps is a row that breaks the windowing, so a long list is shortened rather
// than allowed to run off the end unannounced.
func summarised(ids []string, keep int) string {
	if len(ids) <= keep {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s +%d", strings.Join(ids[:keep], ", "), len(ids)-keep)
}

// impactRows is the ranking as the model currently draws it.
func (m Model) impactRows() []impactRow {
	rows, _ := m.impactRowsAndCounts()
	return rows
}

func (m Model) impactRowsAndCounts() ([]impactRow, impactCounts) {
	if m.state.Snapshot == nil {
		return nil, impactCounts{}
	}
	return impactList(m.state.Snapshot, m.expanded)
}

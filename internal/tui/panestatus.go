package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zealtv/easyloom/internal/twpane"
)

// What EasyLoom tells tireless-watcher's map about itself through its own
// pane's options (twpane): what is ready to do, and what it is doing while it
// acts on a stitch.

// paneValues is what the pane's options should say now.
func (m Model) paneValues() twpane.Values {
	v := twpane.Values{twpane.Next: m.nextLine()}
	if m.act.state != "" {
		v[twpane.Status] = m.act.state
		v[twpane.StatusText] = m.act.text
		v[twpane.StatusAt] = strconv.FormatInt(m.act.at, 10)
	}
	return v
}

// nextLine is the map's ready line, the stitch `loom next` would take from
// the queue and how many more queued stitches are still to do:
// "fix-flake +2 queued". The map adds "next: " itself. It is empty, which
// unsets the option, when no loom is open or nothing queued is ready.
func (m Model) nextLine() string {
	if m.cache == nil || m.state.Snapshot == nil {
		return ""
	}
	rows, _ := queueList(m.state.Snapshot, m.over)
	next, more := "", 0
	for _, r := range rows {
		switch {
		case r.next:
			next = r.id
		case r.stitch != nil && !terminal(r.stitch) && !m.over.gone[r.id]:
			more++
		}
	}
	if next == "" {
		return ""
	}
	if more == 0 {
		return next
	}
	return fmt.Sprintf("%s +%d queued", next, more)
}

// activity is what EasyLoom is doing to the loom, for @tw_status: "work"
// while a mutation runs, "fail" while its refusal is still on screen, and
// nothing otherwise. at is when it started, in Unix seconds.
type activity struct {
	state, text string
	at          int64
}

// currentActivity is the activity the model's state shows.
func (m Model) currentActivity() activity {
	switch {
	case m.pending != nil:
		text := m.pending.gerund
		if text == "" {
			text = fmt.Sprintf("%s %s", m.pending.command, m.pending.id)
		}
		return activity{state: "work", text: text}
	case m.refused != "" && m.failure == m.refused:
		head, _, _ := strings.Cut(m.refused, "\n")
		return activity{state: "fail", text: head}
	}
	return activity{}
}

// updateActivity brings act up to date, stamping a change with now.
func (m *Model) updateActivity(now time.Time) {
	a := m.currentActivity()
	if a.state != m.act.state || a.text != m.act.text {
		if a.state != "" {
			a.at = now.Unix()
		}
		m.act = a
	}
}

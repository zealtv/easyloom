package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/twglance"
)

// glanceFixture queues the projection's four stitches in four states:
// stitching, ready, waiting and blocked.
func glanceFixture(t *testing.T) *snapshot.Snapshot {
	snap := projection(t.TempDir())
	setState(snap, "http-server", snapshot.StateStitching, false)
	setState(snap, "snapshot-refresh", snapshot.StateWaiting, false)
	setState(snap, "docs", snapshot.StatePlain, false)
	for i := range snap.Stitches {
		switch snap.Stitches[i].ID {
		case "snapshot-refresh":
			snap.Stitches[i].QueuePosition = position(3)
		case "docs":
			snap.Stitches[i].QueuePosition = position(4)
		}
	}
	return snap
}

// The list is the queue in the loom's order, each row the glyph the queue
// pane draws and the id, with the role that state takes on the map; the badge
// line is "loom" and a count per state, most pressing first, zeros left out.
func TestGlanceIsTheQueue(t *testing.T) {
	g := modelOver(t, glanceFixture(t)).glance()
	if got, want := twglance.MidValue(g.Rows),
		"busy:◐ http-server;ok:● ci-workflow;hold:‖ snapshot-refresh;hold:○ docs"; got != want {
		t.Errorf("list = %q\nwant  %q", got, want)
	}
	if got, want := twglance.FarValue(g.Badges), ":loom;busy:◐1;ok:●1;hold:‖1;hold:○1"; got != want {
		t.Errorf("badges = %q, want %q", got, want)
	}
}

// Counts add up per state, in the badge order whatever the queue's order.
func TestGlanceBadgesCountAndOrder(t *testing.T) {
	snap := glanceFixture(t)
	setState(snap, "http-server", snapshot.StatePlain, true)
	setState(snap, "snapshot-refresh", snapshot.StateStitching, false)
	snap.Stitches[3].Dependencies = nil // docs: not ready, and blocked by nothing
	g := modelOver(t, snap).glance()
	if got, want := twglance.FarValue(g.Badges), ":loom;busy:◐1;ok:●2;quiet:○1"; got != want {
		t.Errorf("badges = %q, want %q", got, want)
	}
}

// Records the loom cannot resolve (a finished stitch still queued, an id no
// stitch has) are rows of their own, bad "!", and lead the badge line, which
// the map cuts from the end. Blocked and not-ready share one ○ count, hold
// while any of them is blocked.
func TestGlanceUnresolvedRecordsLead(t *testing.T) {
	g := modelOver(t, queueLoom(t.TempDir())).glance()
	if got, want := twglance.MidValue(g.Rows),
		"hold:○ embedded-assets;hold:‖ loom-url-routing;ok:● http-server;hold:○ graph-layout;"+
			"bad:! shell-skeleton;bad:! ghost-stitch"; got != want {
		t.Errorf("list = %q\nwant  %q", got, want)
	}
	if got, want := twglance.FarValue(g.Badges), ":loom;bad:!2;ok:●1;hold:‖1;hold:○2"; got != want {
		t.Errorf("badges = %q, want %q", got, want)
	}
}

// Tending counts as someone else's (◆), a finished stitch in the queue shows
// quiet and is not counted, and ○ is quiet when none of its stitches is
// blocked, hold as soon as one is.
func TestGlanceTendingFinishedAndOneCircle(t *testing.T) {
	snap := glanceFixture(t)
	setState(snap, "http-server", snapshot.StateTending, false)
	setState(snap, "ci-workflow", snapshot.StateTied, false)
	setState(snap, "snapshot-refresh", snapshot.StatePlain, false)
	g := modelOver(t, snap).glance()
	if got, want := twglance.MidValue(g.Rows),
		"other:◆ http-server;quiet:✓ ci-workflow;quiet:○ snapshot-refresh;hold:○ docs"; got != want {
		t.Errorf("list = %q\nwant  %q", got, want)
	}
	if got, want := twglance.FarValue(g.Badges), ":loom;other:◆1;hold:○2"; got != want {
		t.Errorf("badges = %q, want %q", got, want)
	}
	snap.Stitches[3].Dependencies = nil
	g = modelOver(t, snap).glance()
	if got, want := twglance.FarValue(g.Badges), ":loom;other:◆1;quiet:○2"; got != want {
		t.Errorf("with nothing blocked, badges = %q, want %q", got, want)
	}
}

// Every state stitchLook draws has the role that says the same on the map.
func TestGlanceRolesFollowTheLook(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	cases := []struct {
		st   *snapshot.Stitch
		role string
	}{
		{nil, twglance.Bad},
		{&snapshot.Stitch{State: snapshot.StatePlain, Ready: true}, twglance.OK},
		{&snapshot.Stitch{State: snapshot.StateStitching}, twglance.Busy},
		{&snapshot.Stitch{State: snapshot.StateTending}, twglance.Other},
		{&snapshot.Stitch{State: snapshot.StateWaiting}, twglance.Hold},
		{&snapshot.Stitch{State: snapshot.StatePlain, WaitingInherited: true}, twglance.Hold},
		{&snapshot.Stitch{State: snapshot.StatePlain, Dependencies: []snapshot.Dependency{{To: "x", Status: snapshot.DependencyBlocked}}}, twglance.Hold},
		{&snapshot.Stitch{State: snapshot.StatePlain}, twglance.Quiet},
		{&snapshot.Stitch{State: snapshot.StateTied}, twglance.Quiet},
		{&snapshot.Stitch{State: snapshot.StateDropped}, twglance.Quiet},
	}
	for _, c := range cases {
		if got := glanceRole(c.st); got != c.role {
			t.Errorf("%+v (%s): role %q, want %q", c.st, m.stitchLook(c.st).glyph, got, c.role)
		}
	}
}

// With no loom open (the picker, at a cold start) there is no glance, which
// unsets both options.
func TestNoLoomNoGlance(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	m.cache = nil
	if g := m.glance(); len(g.Badges) != 0 || len(g.Rows) != 0 {
		t.Errorf("glance with no loom: %+v", g)
	}
}

// An empty queue keeps the label and has no list.
func TestEmptyQueueKeepsTheLabel(t *testing.T) {
	snap := projection(t.TempDir())
	for i := range snap.Stitches {
		snap.Stitches[i].QueuePosition = nil
	}
	g := modelOver(t, snap).glance()
	if twglance.FarValue(g.Badges) != ":loom" || twglance.MidValue(g.Rows) != "" {
		t.Errorf("empty queue: far %q mid %q", twglance.FarValue(g.Badges), twglance.MidValue(g.Rows))
	}
}

// Update keeps the pane's glance current, on the pane only; a model that is
// not in tmux (New, outside Run) has no glance pane and writes nothing.
func TestUpdateWritesTheGlance(t *testing.T) {
	m := modelOver(t, glanceFixture(t))
	if m.glancePane.ID != "" {
		t.Fatal("a model outside tmux has a glance pane")
	}
	var calls []string
	m.glancePane = twglance.New("%9")
	m.glancePane.Run = func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	want := "set-option -p -t %9 @tw-glance-far :loom;busy:◐1;ok:●1;hold:‖1;hold:○1 ; " +
		"set-option -p -t %9 @tw-glance-mid busy:◐ http-server;ok:● ci-workflow;hold:‖ snapshot-refresh;hold:○ docs"
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("calls = %q\nwant %q", calls, want)
	}
}

// An id too long for a row is cut in the middle, so the row keeps the mark,
// the id's head and its tail, and twglance has nothing left to cut at the end.
func TestGlanceCutsALongIdInTheMiddle(t *testing.T) {
	snap := glanceFixture(t)
	long := "glance-phase-" + strings.Repeat("x", 50) + "-1-plain"
	for i := range snap.Stitches {
		if snap.Stitches[i].ID == "http-server" {
			snap.Stitches[i].ID = long
			for j := range snap.Stitches[i].Dependencies {
				snap.Stitches[i].Dependencies[j].From = long
			}
		}
	}
	g := modelOver(t, snap).glance()
	row := strings.SplitN(twglance.MidValue(g.Rows), ";", 2)[0]
	text := strings.TrimPrefix(row, "busy:")
	if !strings.HasPrefix(text, "◐ glance-phase-") || !strings.HasSuffix(text, "x-1-plain") ||
		!strings.Contains(text, "…") || ansi.StringWidth(text) != twglance.RowCells {
		t.Errorf("row = %q (%d cells), want the mark, head…tail, %d cells", text, ansi.StringWidth(text), twglance.RowCells)
	}
	if got := twglance.MidValue(g.Rows); !strings.Contains(got, ";ok:● ci-workflow;") {
		t.Errorf("short ids are cut: %q", got)
	}
}

func TestCutMiddle(t *testing.T) {
	cases := []struct {
		s     string
		cells int
		want  string
	}{
		{"glance-phase-1-plain", 20, "glance-phase-1-plain"},
		{"glance-phase-1-plain", 15, "glance-…1-plain"},
		{"abcdef", 4, "ab…f"},
		{"abcdef", 1, "…"},
		{"abcdef", 0, ""},
		{"日本語の-id", 6, "日…id"},
	}
	for _, c := range cases {
		got := cutMiddle(c.s, c.cells)
		if got != c.want || ansi.StringWidth(got) > c.cells {
			t.Errorf("cutMiddle(%q, %d) = %q, want %q", c.s, c.cells, got, c.want)
		}
	}
}

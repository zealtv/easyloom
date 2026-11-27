package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/twpane"
)

// The palette opens on ">", tireless-watcher's palette key, and ":" no longer
// opens it: one key across the easy tools.
func TestPaletteOpensOnGreaterThanOnly(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	if m = press(t, m, ":"); m.palette != nil {
		t.Fatal(": still opens the palette")
	}
	if m = press(t, m, ">"); m.palette == nil {
		t.Fatal("> did not open the palette")
	}
}

// ">" is text wherever text is typed: in the palette's own query, and in a
// drop reason.
func TestGreaterThanStaysTextInInputs(t *testing.T) {
	m, _ := mutableModel(t, okJSON("drop"))
	m = press(t, m, ">")
	for _, k := range []string{"a", ">", "b"} {
		m = press(t, m, k)
	}
	if m.palette == nil || m.palette.query != "a>b" {
		t.Fatalf("palette query = %+v, want a>b", m.palette)
	}
	m = press(t, m, "esc")

	m = act(t, m, "d")
	if m.prompt == nil {
		t.Fatal("no drop prompt")
	}
	for _, k := range []string{"x", ">", "y"} {
		m = press(t, m, k)
	}
	if m.prompt == nil || m.prompt.reason != "x>y" {
		t.Fatalf("drop reason = %+v, want x>y", m.prompt)
	}
}

// The ready line names the stitch `loom next` would take from the queue and
// how many more queued stitches are still to do.
func TestNextLineIsTheQueuesReadyStitch(t *testing.T) {
	snap := projection(t.TempDir())
	m := modelOver(t, snap)
	if got := m.nextLine(); got != "http-server +1 queued" {
		t.Errorf("nextLine = %q, want %q", got, "http-server +1 queued")
	}

	// The head is not ready (claimed): next skips it, and the line says the
	// stitch next would really take. The claimed head still counts as to do.
	snap = projection(t.TempDir())
	setState(snap, "http-server", snapshot.StateStitching, false)
	m = modelOver(t, snap)
	if got := m.nextLine(); got != "ci-workflow +1 queued" {
		t.Errorf("with the head claimed, nextLine = %q", got)
	}

	// One queued stitch alone has no count.
	snap = projection(t.TempDir())
	snap.Stitches[1].QueuePosition = nil
	m = modelOver(t, snap)
	if got := m.nextLine(); got != "http-server" {
		t.Errorf("one queued, nextLine = %q", got)
	}

	// Nothing queued is ready: no line, so the option is unset.
	snap = projection(t.TempDir())
	setState(snap, "http-server", snapshot.StateStitching, false)
	setState(snap, "ci-workflow", snapshot.StateWaiting, false)
	m = modelOver(t, snap)
	if got := m.nextLine(); got != "" {
		t.Errorf("nothing ready, nextLine = %q", got)
	}
}

func setState(snap *snapshot.Snapshot, id string, state snapshot.State, ready bool) {
	for i := range snap.Stitches {
		if snap.Stitches[i].ID == id {
			snap.Stitches[i].State, snap.Stitches[i].Ready = state, ready
		}
	}
}

// paneRecorder gives m a pane whose tmux calls are recorded, one string per
// option set ("@tw-next=x") or unset ("@tw-next=").
func paneRecorder(m *Model) func() map[string]string {
	now := map[string]string{}
	m.pane = twpane.Pane{ID: "%9", Run: func(args ...string) error {
		for _, part := range strings.Split(strings.Join(args, " "), " ; ") {
			f := strings.SplitN(part, " ", 6)
			switch {
			case len(f) == 5 && f[1] == "-pu":
				now[f[4]] = ""
			case len(f) == 6 && f[1] == "-p":
				now[f[4]] = f[5]
			}
		}
		return nil
	}}
	return func() map[string]string { return now }
}

// update runs one message through Update and every command it returns,
// batches included, stopping at the poll.
func update(m Model, msg tea.Msg) Model {
	next, cmd := m.Update(msg)
	m = next.(Model)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch got := c().(type) {
		case tea.BatchMsg:
			for _, b := range got {
				run(b)
			}
		case nil, tickMsg:
		default:
			m = update(m, got)
		}
	}
	run(cmd)
	return m
}

// While EasyLoom runs a mutation its pane says "work" with what it is doing;
// once the loom has done it, the status goes and @tw-next stays.
func TestPaneSaysWorkWhileActing(t *testing.T) {
	m, _ := mutableModel(t, okJSON("claim"))
	opts := paneRecorder(&m)
	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if got := opts()["@tw-next"]; got != "http-server +1 queued" {
		t.Fatalf("@tw-next = %q", got)
	}

	m = press(t, m, paletteKey)
	for _, r := range "claim" {
		m = press(t, m, string(r))
	}
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(Model)
	// Run the pane's command but not yet the mutation's.
	if b, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range b[1:] {
			if c != nil {
				c()
			}
		}
		cmd = b[0]
	}
	if s, text := opts()["@tw_status"], opts()["@tw_status_text"]; s != "work" || text != "claiming http-server" {
		t.Fatalf("while claiming: @tw_status=%q text=%q", s, text)
	}
	if opts()["@tw_status_at"] == "" {
		t.Error("no @tw_status_at while working")
	}

	m = update(m, cmd())
	if s := opts()["@tw_status"]; s != "" {
		t.Errorf("after the claim @tw_status = %q, want unset", s)
	}
	if opts()["@tw_status_text"] != "" || opts()["@tw_status_at"] != "" {
		t.Error("the status text or time outlived the status")
	}
}

// A refusal leaves "fail" with the loom's headline for as long as it is on
// screen.
func TestPaneSaysFailWhileARefusalShows(t *testing.T) {
	refusal := "cat <<'JSON'\n" +
		`{"schema_version":1,"format_version":2,"command":"tie","ok":false,"changed":false,` +
		`"id":"http-server","error":{"code":"not_loose_end",` +
		`"message":"http-server has unresolved children: docs","stitch_ids":["docs"]}}` +
		"\nJSON\nexit 1\n"
	m, _ := mutableModel(t, refusal)
	m = act(t, m, "t")
	v := m.paneValues()
	if s, text := v[twpane.Status], v[twpane.StatusText]; s != "fail" || !strings.HasPrefix(text, "tie http-server failed") {
		t.Fatalf("after a refusal: @tw_status=%q text=%q", s, text)
	}
	if strings.Contains(v[twpane.StatusText], "\n") {
		t.Error("the status text has more than one line")
	}

	// Another failure (not a refusal) on screen is not a mutation's.
	m.failure = "editor: exit status 1"
	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if s := m.paneValues()[twpane.Status]; s != "" {
		t.Errorf("an editor failure set @tw_status = %q", s)
	}
}

// A model with no pane (outside tmux) never calls tmux.
func TestNoPaneNoOptions(t *testing.T) {
	m, _ := mutableModel(t, okJSON("claim"))
	m.pane.Run = func(...string) error { t.Fatal("tmux called with no pane"); return nil }
	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	_ = act(t, m, "c")
}

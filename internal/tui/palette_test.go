package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// paletteNames are the palette command each former bare key now is, by the
// state it toggles from.
var paletteNames = map[string][]string{
	"c": {"claim"},
	"t": {"tie off"},
	"w": {"park", "unpark"},
	"T": {"tend", "release"},
	"d": {"drop…"},
	"Q": {"queue", "unqueue", "queue with children"},
	"J": {"move down the queue"},
	"K": {"move up the queue"},
	"u": {"undo queue move"},
	"r": {"refresh"},
	"p": {"switch loom"},
	"q": {"quit"},
}

// names lists the commands the palette offers here.
func names(m Model) []string {
	var out []string
	for _, c := range m.commands() {
		out = append(out, c.name)
	}
	return out
}

// offers reports whether the palette offers a command here.
func offers(m Model, name string) bool { return slices.Contains(names(m), name) }

// act runs a lifecycle command the way a reader now has to: open the
// palette, type its name, select it, and press enter.
func act(t *testing.T, m Model, key string) Model {
	t.Helper()
	name := ""
	for _, candidate := range paletteNames[key] {
		if offers(m, candidate) {
			name = candidate
		}
	}
	if name == "" {
		t.Fatalf("the palette offers nothing for %q: %v", key, names(m))
	}
	m = press(t, m, paletteKey)
	for _, r := range name {
		m = press(t, m, string(r))
	}
	for i, c := range m.filtered() {
		if c.name == name {
			for m.palette.cursor != i {
				m = press(t, m, "down")
			}
			return press(t, m, "enter")
		}
	}
	t.Fatalf("typing %q did not find it: %v", name, m.filtered())
	return m
}

// The keys that used to change a stitch are inert now: a sentence typed into
// the wrong tmux pane must not claim, tie or queue anything.
func TestBareKeysNoLongerChangeTheLoom(t *testing.T) {
	for _, key := range []string{"c", "t", "w", "T", "d", "Q", "J", "K", "u"} {
		m, argv := mutableModel(t, okJSON("claim"))
		m = press(t, m, key)
		if got := argv(); len(got) != 0 {
			t.Errorf("%q ran %q", key, got)
		}
		if m.prompt != nil {
			t.Errorf("%q opened the drop prompt", key)
		}
	}
}

// The keys that used to refresh, switch looms and quit are inert too: a stray
// q is as unwelcome as a stray c.
func TestBareKeysNoLongerQuitRefreshOrSwitch(t *testing.T) {
	for _, key := range []string{"q", "r", "p"} {
		m, _ := mutableModel(t, okJSON("claim"))
		next, cmd := m.Update(keyMsg(key))
		m = next.(Model)
		if cmd != nil {
			if msg := cmd(); msg == tea.Quit() {
				t.Errorf("%q quit", key)
			}
		}
		if m.refreshing {
			t.Errorf("%q started a refresh", key)
		}
		if m.picker != nil {
			t.Errorf("%q opened the picker", key)
		}
	}
}

// Opening the palette and pressing enter runs nothing: no command is selected
// until the reader types or moves.
func TestPaletteSelectsNothingUntilAsked(t *testing.T) {
	m, argv := mutableModel(t, okJSON("claim"))
	m = press(t, m, paletteKey)
	if m.palette == nil {
		t.Fatal("the palette did not open")
	}
	m = press(t, m, "enter")
	if got := argv(); len(got) != 0 {
		t.Errorf("enter on an untouched palette ran %q", got)
	}
	if m.palette == nil {
		t.Error("enter with nothing selected closed the palette")
	}
	m = press(t, m, "esc")
	if m.palette != nil {
		t.Error("esc did not close the palette")
	}
}

// While the palette is open every key is typing: q searches rather than
// quits, and a lifecycle letter is a letter.
func TestPaletteTakesEveryKey(t *testing.T) {
	m, argv := mutableModel(t, okJSON("claim"))
	m = press(t, m, paletteKey)
	for _, k := range []string{"q", "c", "J", "r"} {
		m = press(t, m, k)
	}
	if m.palette == nil || m.palette.query != "qcJr" {
		t.Fatalf("the palette did not keep the typing: %+v", m.palette)
	}
	if got := argv(); len(got) != 0 {
		t.Errorf("typing in the palette ran %q", got)
	}
}

// Typing narrows the list and selects the best match; a lifecycle command
// runs from there.
func TestPaletteRunsTheBestMatch(t *testing.T) {
	m, argv := mutableModel(t, okJSON("tie"))
	m = press(t, m, paletteKey)
	for _, k := range "tie" {
		m = press(t, m, string(k))
	}
	if got := m.filtered(); len(got) == 0 || got[0].name != "tie off" {
		t.Fatalf("tie did not find tie off first: %v", got)
	}
	m = press(t, m, "enter")
	if got := argv(); len(got) != 1 || !strings.HasPrefix(got[0], "tie ") {
		t.Errorf("argv = %q", got)
	}
	if m.palette != nil {
		t.Error("running a command left the palette open")
	}
}

// A read-only loom lists its commands but runs none of them.
func TestReadOnlyCommandsAreListedButDisabled(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	m = press(t, m, paletteKey)
	for _, k := range "claim" {
		m = press(t, m, string(k))
	}
	m = press(t, m, "enter")
	if !strings.Contains(m.View(), "read-only") {
		t.Errorf("a read-only claim did not say why:\n%s", m.View())
	}
}

func TestFuzzyPrefersWordStartsAndWholeNames(t *testing.T) {
	if _, _, ok := fuzzy("tof", "tie off"); !ok {
		t.Error("tof does not match tie off")
	}
	if _, _, ok := fuzzy("xyz", "tie off"); ok {
		t.Error("xyz matches tie off")
	}
	whole, _, _ := fuzzy("queue", "queue")
	inner, _, _ := fuzzy("queue", "unqueue")
	if whole <= inner {
		t.Errorf("queue scores %d against itself and %d inside unqueue", whole, inner)
	}
	start, _, _ := fuzzy("tie", "tie off")
	scattered, _, _ := fuzzy("tie", "edit time")
	if start <= scattered {
		t.Errorf("tie scores %d at a word start and %d scattered", start, scattered)
	}
}

// The palette draws as a box over the faded frame, and keeps the frame's
// size.
func TestPaletteDrawsOverTheFrame(t *testing.T) {
	m, _ := mutableModel(t, okJSON("claim"))
	before := strings.Count(m.View(), "\n")
	m = press(t, m, paletteKey)
	view := m.View()
	if got := strings.Count(view, "\n"); got != before {
		t.Errorf("the palette changed the frame's height: %d lines, was %d", got, before)
	}
	for _, want := range []string{"╭", "╯", "claim", "tie off", "enter run"} {
		if !strings.Contains(view, want) {
			t.Errorf("the palette is missing %q:\n%s", want, view)
		}
	}
}

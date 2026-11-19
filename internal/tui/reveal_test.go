package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// watchReveal stands in for the opener and records what it was handed.
func watchReveal(t *testing.T, err error) *[]string {
	t.Helper()
	var got []string
	old := reveal
	reveal = func(dir string) error {
		got = append(got, dir)
		return err
	}
	t.Cleanup(func() { reveal = old })
	return &got
}

// runCommand opens the palette, types name, selects it and presses enter.
func runCommand(t *testing.T, m Model, name string) Model {
	t.Helper()
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
	t.Fatalf("typing %q did not find it: %v", name, names(m))
	return m
}

func TestOpenFolderRevealsTheSelectedStitch(t *testing.T) {
	got := watchReveal(t, nil)
	m, path := editable(t, "# goal\n")

	m = runCommand(t, m, "open folder")
	if want := filepath.Dir(path); len(*got) != 1 || (*got)[0] != want {
		t.Fatalf("revealed %q, want [%q]", *got, want)
	}
	if !strings.Contains(m.View(), "opened ") {
		t.Errorf("success was not reported:\n%s", m.View())
	}
}

func TestOpenLoomFolderRevealsTheRoot(t *testing.T) {
	got := watchReveal(t, nil)
	m, _ := editable(t, "# goal\n")

	runCommand(t, m, "open loom folder")
	if want := m.state.Snapshot.LoomRoot; len(*got) != 1 || (*got)[0] != want {
		t.Fatalf("revealed %q, want [%q]", *got, want)
	}
}

// A folder that has gone is said plainly, and the opener is not asked.
func TestOpenFolderOnAMissingFolderSaysSo(t *testing.T) {
	got := watchReveal(t, nil)
	m, path := editable(t, "# goal\n")
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}

	m = runCommand(t, m, "open folder")
	if len(*got) != 0 {
		t.Errorf("the opener was asked for a missing folder: %q", *got)
	}
	if m.failure == "" {
		t.Error("a missing folder was not reported")
	}
}

func TestOpenFolderReportsTheOpenersError(t *testing.T) {
	watchReveal(t, errors.New("no display"))
	m, _ := editable(t, "# goal\n")

	m = runCommand(t, m, "open folder")
	if !strings.Contains(m.failure, "no display") {
		t.Errorf("failure = %q, want the opener's words", m.failure)
	}
}

// The projection's path is refused if it leaves the loom, as for the editor.
func TestStitchDirRefusesAnEscape(t *testing.T) {
	if dir, err := stitchDir("/loom", "../elsewhere"); err == nil {
		t.Errorf("stitchDir = %q, want a refusal", dir)
	}
}

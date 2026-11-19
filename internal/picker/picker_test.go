package picker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zealtv/easyloom/internal/known"
	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/userconfig"
)

// config points the registry at a throwaway directory.
func config(t *testing.T) userconfig.Getenv {
	t.Helper()
	dir := t.TempDir()
	return func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return dir
		}
		return ""
	}
}

// deployLoom writes a stand-in script so the entry is not marked missing.
func deployLoom(t *testing.T, name string) target.Target {
	t.Helper()
	return deployIn(t, t.TempDir(), name)
}

// deployIn puts a loom under a shared root, for tests that care about the
// order paths sort in.
func deployIn(t *testing.T, root, name string) target.Target {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	dir := filepath.Join(root, name, ".loom")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "loom.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return target.Target{Script: script, Root: dir}
}

func remember(t *testing.T, getenv userconfig.Getenv, ts []target.Target, tier target.Tier) {
	t.Helper()
	for _, tt := range ts {
		if err := known.Touch(getenv, tt, tier); err != nil {
			t.Fatal(err)
		}
	}
}

func names(m Model) []string {
	out := make([]string, len(m.Entries()))
	for i, e := range m.Entries() {
		out[i] = e.Name()
	}
	return out
}

func TestNewListsTheRegistryNewestFirst(t *testing.T) {
	getenv := config(t)
	alpha, beta := deployLoom(t, "alpha"), deployLoom(t, "beta")
	remember(t, getenv, []target.Target{alpha, beta}, target.TierV2)

	m := New(getenv)
	if got := names(m); len(got) != 2 || got[0] != "beta" || got[1] != "alpha" {
		t.Errorf("entries = %v, want the most recently opened first", got)
	}
	if m.Empty() {
		t.Error("a picker with two entries reports empty")
	}
	for _, e := range m.Entries() {
		if e.Missing {
			t.Errorf("%s is marked missing but its script is there", e.Name())
		}
		if e.Tier != target.TierV2 {
			t.Errorf("%s has tier %q, want the cached v2", e.Name(), e.Tier)
		}
	}
}

// An entry whose loom has been deleted is still shown — a loom on an unmounted
// drive is not a loom to forget — but it is marked.
func TestMissingLoomIsMarkedNotDropped(t *testing.T) {
	getenv := config(t)
	gone := deployLoom(t, "gone")
	remember(t, getenv, []target.Target{gone}, target.TierV2)
	if err := os.Remove(gone.Script); err != nil {
		t.Fatal(err)
	}

	m := New(getenv)
	if len(m.Entries()) != 1 {
		t.Fatalf("entries = %v, want the deleted loom still listed", names(m))
	}
	if !m.Entries()[0].Missing {
		t.Error("a deleted loom is not marked missing")
	}
	if m.Rows()[0].Tier != "missing" {
		t.Errorf("row tier = %q, want it to say missing", m.Rows()[0].Tier)
	}
}

// An empty registry is the case that opens straight into a scan.
func TestEmptyRegistryIsEmpty(t *testing.T) {
	m := New(config(t))
	if !m.Empty() {
		t.Error("a picker over an empty registry does not report empty")
	}
	if m.Selected() != nil {
		t.Error("an empty picker has a selection")
	}
	// And the keys that act on a selection do nothing rather than panicking.
	if got := len(m.Forget().Entries()); got != 0 {
		t.Errorf("Forget on an empty picker left %d entries", got)
	}
	if got := m.Move(1).Cursor(); got != 0 {
		t.Errorf("cursor = %d on an empty picker, want 0", got)
	}
}

// The ends of a list are a place: the cursor clamps rather than wrapping.
func TestMoveClamps(t *testing.T) {
	getenv := config(t)
	remember(t, getenv, []target.Target{deployLoom(t, "a"), deployLoom(t, "b")}, target.TierV2)

	m := New(getenv)
	if got := m.Move(-1).Cursor(); got != 0 {
		t.Errorf("cursor = %d after moving up from the top, want 0", got)
	}
	if got := m.Move(5).Cursor(); got != 1 {
		t.Errorf("cursor = %d after moving past the end, want the last index", got)
	}
	if got := m.MoveTo(99).Cursor(); got != 1 {
		t.Errorf("MoveTo past the end = %d, want the last index", got)
	}
}

func TestForgetRemovesFromTheListAndTheRegistry(t *testing.T) {
	getenv := config(t)
	alpha, beta := deployLoom(t, "alpha"), deployLoom(t, "beta")
	remember(t, getenv, []target.Target{alpha, beta}, target.TierV2)

	// beta is newest, so it is under the cursor.
	m := New(getenv).Forget()
	if got := names(m); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("entries = %v, want just alpha", got)
	}
	if m.Notice() == "" {
		t.Error("forgetting says nothing")
	}

	list, err := known.Load(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Looms) != 1 || list.Looms[0].Key != alpha.Key() {
		t.Error("the registry still holds the forgotten loom")
	}
}

// A scan adds what is new and does not reorder the looms already worked in.
func TestMergeAppendsOnlyWhatIsNew(t *testing.T) {
	getenv := config(t)
	alpha := deployLoom(t, "alpha")
	remember(t, getenv, []target.Target{alpha}, target.TierV2)

	// Both under one root, so the path order the merge sorts by is knowable.
	scanned := t.TempDir()
	fresh, other := deployIn(t, scanned, "zeta"), deployIn(t, scanned, "mid")
	m := New(getenv).StartScan()
	if !m.Scanning() {
		t.Error("StartScan does not report scanning")
	}

	m = m.Merge([]target.Found{
		{Target: alpha, Version: target.Version{Tier: target.TierV2}},
		{Target: fresh, Version: target.Version{Tier: target.TierV1}},
		{Target: other, Version: target.Version{Tier: target.TierV2}},
	})
	if m.Scanning() {
		t.Error("Merge left the picker scanning")
	}

	got := names(m)
	if len(got) != 3 || got[0] != "alpha" {
		t.Fatalf("entries = %v, want alpha kept at the front", got)
	}
	// New entries are sorted by path, so a scan reads the same twice.
	if got[1] != "mid" || got[2] != "zeta" {
		t.Errorf("new entries = %v, want them in path order", got[1:])
	}
	if m.Entries()[2].Tier != target.TierV1 {
		t.Errorf("tier = %q, want the scan's", m.Entries()[2].Tier)
	}
	if m.Notice() == "" {
		t.Error("a scan that found something says nothing")
	}
}

func TestMergeSaysWhenNothingIsNew(t *testing.T) {
	getenv := config(t)
	alpha := deployLoom(t, "alpha")
	remember(t, getenv, []target.Target{alpha}, target.TierV2)

	m := New(getenv).Merge([]target.Found{{Target: alpha}})
	if len(m.Entries()) != 1 {
		t.Errorf("entries = %v, want no duplicate", names(m))
	}
	if m.Notice() == "" {
		t.Error("a scan that found nothing new says nothing")
	}
}

// A corrupt registry is a notice and an empty list, never a failure: the scan
// action is still the way out.
func TestCorruptRegistryIsANoticeNotAFailure(t *testing.T) {
	getenv := config(t)
	path, err := userconfig.File(getenv, known.FileName)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := New(getenv)
	if !m.Empty() {
		t.Error("a corrupt registry produced entries")
	}
	if m.Notice() == "" {
		t.Error("a corrupt registry says nothing")
	}
}

func TestScanRootIsTheParentOfTheWorkingDirectory(t *testing.T) {
	got := ScanRoot(func() (string, error) { return filepath.FromSlash("/home/someone/repos/project"), nil })
	if want := filepath.FromSlash("/home/someone/repos"); got != want {
		t.Errorf("ScanRoot = %q, want %q", got, want)
	}

	// A working directory that cannot be read is not a reason to refuse to
	// scan.
	if got := ScanRoot(func() (string, error) { return "", os.ErrPermission }); got != "." {
		t.Errorf("ScanRoot with no working directory = %q, want \".\"", got)
	}
}

func TestNameIsTheRepositoryDirectory(t *testing.T) {
	e := Entry{Target: target.Target{Root: filepath.FromSlash("/home/someone/repos/easyloom/.loom")}}
	if got := e.Name(); got != "easyloom" {
		t.Errorf("Name = %q, want the repository directory", got)
	}
}

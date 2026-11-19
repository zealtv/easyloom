package known

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/userconfig"
)

// config points the user configuration directory at a temp dir and returns the
// Getenv to pass in, plus the path registry.json will land at.
func config(t *testing.T) (userconfig.Getenv, string) {
	t.Helper()
	root := t.TempDir()
	getenv := func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return root
		}
		return ""
	}
	return getenv, filepath.Join(root, "easyloom", FileName)
}

// tick makes the clock advance a second per call, so MRU ordering is asserted
// against distinct times rather than whatever the timer resolution happens to
// be on the machine running the test.
func tick(t *testing.T) {
	t.Helper()
	current := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	previous := now
	now = func() time.Time {
		current = current.Add(time.Second)
		return current
	}
	t.Cleanup(func() { now = previous })
}

func loom(t *testing.T, name string) target.Target {
	t.Helper()
	return target.Target{
		Script: filepath.Join("/repos", name, ".loom", "loom.sh"),
		Root:   filepath.Join("/repos", name, ".loom"),
	}
}

func keys(list *List) []string {
	out := make([]string, len(list.Looms))
	for i, l := range list.Looms {
		out[i] = l.Key
	}
	return out
}

func mustLoad(t *testing.T, getenv userconfig.Getenv) *List {
	t.Helper()
	list, err := Load(getenv)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A machine that has never opened a second loom has no registry, and that is
// not an error.
func TestLoadMissingFileIsEmptyAndFine(t *testing.T) {
	getenv, path := config(t)

	list, err := Load(getenv)
	if err != nil {
		t.Fatalf("Load with no registry: %v", err)
	}
	if len(list.Looms) != 0 {
		t.Errorf("Load = %v, want nothing", keys(list))
	}
	if list.Path != path {
		t.Errorf("Path = %q, want %q", list.Path, path)
	}
	// Reading must not create the directory either.
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("Load created %s", filepath.Dir(path))
	}
}

func TestTouchPersistsAcrossRestart(t *testing.T) {
	tick(t)
	getenv, _ := config(t)

	alpha := loom(t, "alpha")
	if err := Touch(getenv, alpha, target.TierV2); err != nil {
		t.Fatal(err)
	}

	list := mustLoad(t, getenv)
	if len(list.Looms) != 1 {
		t.Fatalf("Load = %v, want one entry", keys(list))
	}
	entry := list.Looms[0]
	if entry.Key != alpha.Key() {
		t.Errorf("Key = %q, want %q", entry.Key, alpha.Key())
	}
	if entry.Root != alpha.Root {
		t.Errorf("Root = %q, want %q", entry.Root, alpha.Root)
	}
	if entry.Tier != target.TierV2 {
		t.Errorf("Tier = %q, want %q", entry.Tier, target.TierV2)
	}
	if entry.LastOpened.IsZero() {
		t.Error("LastOpened is zero")
	}
	if got := entry.Target(); got != alpha {
		t.Errorf("Target() = %+v, want %+v", got, alpha)
	}
}

// Opening a loom moves it to the front, and does not duplicate it.
func TestTouchIsMostRecentlyUsed(t *testing.T) {
	tick(t)
	getenv, _ := config(t)

	alpha, beta, gamma := loom(t, "alpha"), loom(t, "beta"), loom(t, "gamma")
	for _, l := range []target.Target{alpha, beta, gamma} {
		if err := Touch(getenv, l, target.TierV2); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{gamma.Key(), beta.Key(), alpha.Key()}
	if got := keys(mustLoad(t, getenv)); !equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}

	// Reopening the oldest brings it to the front and leaves three entries.
	if err := Touch(getenv, alpha, target.TierV1); err != nil {
		t.Fatal(err)
	}
	list := mustLoad(t, getenv)
	want = []string{alpha.Key(), gamma.Key(), beta.Key()}
	if got := keys(list); !equal(got, want) {
		t.Errorf("order after reopening = %v, want %v", got, want)
	}
	// And the tier it was last seen at is the one now recorded.
	if list.Looms[0].Tier != target.TierV1 {
		t.Errorf("Tier = %q, want the tier of the most recent open", list.Looms[0].Tier)
	}
}

// A corrupt registry is a broken cache of paths, not a reason to refuse to
// start: everything in it can be rebuilt by a scan.
func TestLoadCorruptFileIsEmptyWithAWarning(t *testing.T) {
	getenv, path := config(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := Load(getenv)
	if err == nil {
		t.Error("Load of a corrupt registry = nil error, want one to warn with")
	}
	if list == nil || len(list.Looms) != 0 {
		t.Errorf("Load = %v, want an empty usable list", list)
	}

	// And the next open replaces it rather than failing forever.
	if err := Touch(getenv, loom(t, "alpha"), target.TierV2); err != nil {
		t.Fatalf("Touch over a corrupt registry: %v", err)
	}
	if got := len(mustLoad(t, getenv).Looms); got != 1 {
		t.Errorf("after Touch the registry has %d entries, want 1", got)
	}
}

func TestLoadRejectsAnUnknownFileVersion(t *testing.T) {
	getenv, path := config(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"version": 99, "looms": [{"key": "/repos/alpha/.loom/loom.sh", "root": "/repos/alpha/.loom"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := Load(getenv)
	if err == nil {
		t.Error("Load of a future registry = nil error, want one")
	}
	if len(list.Looms) != 0 {
		t.Errorf("Load = %v, want nothing", keys(list))
	}
}

// The file is written by us but lives in a home directory, so it may have been
// edited by hand into something not quite right.
func TestLoadCleansTheFile(t *testing.T) {
	getenv, path := config(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"version": 1, "looms": [
	  {"key": "", "root": "/repos/nameless/.loom"},
	  {"key": "/repos/alpha/.loom/loom.sh", "root": "/repos/alpha/.loom", "last_opened": "2026-08-01T00:00:00Z"},
	  {"key": "/repos/beta/.loom/loom.sh", "root": "/repos/beta/.loom", "last_opened": "2026-08-02T00:00:00Z"},
	  {"key": "/repos/alpha/.loom/loom.sh", "root": "/repos/alpha/.loom", "last_opened": "2026-08-03T00:00:00Z"},
	  {"key": "/repos/rootless/.loom/loom.sh", "root": ""}
	]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := Load(getenv)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/repos/beta/.loom/loom.sh", "/repos/alpha/.loom/loom.sh"}
	if got := keys(list); !equal(got, want) {
		t.Errorf("Load = %v, want %v — deduplicated, ordered, and the broken entries dropped", got, want)
	}
}

// Two EasyLooms open at once is the normal case. Whatever interleaving they
// manage, the file that results must be readable.
func TestConcurrentTouchesCannotCorruptTheFile(t *testing.T) {
	getenv, _ := config(t)

	const writers = 8
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				if err := Touch(getenv, loom(t, string(rune('a'+i))), target.TierV2); err != nil {
					t.Errorf("Touch: %v", err)
					return
				}
				if _, err := Load(getenv); err != nil {
					t.Errorf("Load during concurrent writes: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	list, err := Load(getenv)
	if err != nil {
		t.Fatalf("Load after concurrent writes: %v", err)
	}
	if len(list.Looms) == 0 {
		t.Fatal("the registry is empty after concurrent writes")
	}
	// Whichever touches won, the file is one valid document with no duplicates.
	seen := map[string]bool{}
	for _, l := range list.Looms {
		if seen[l.Key] {
			t.Errorf("key %q appears twice", l.Key)
		}
		seen[l.Key] = true
	}
}

// A save must not leave its temporary files behind for the picker to find.
func TestSaveLeavesNoTemporaryFiles(t *testing.T) {
	getenv, path := config(t)
	for _, name := range []string{"alpha", "beta"} {
		if err := Touch(getenv, loom(t, name), target.TierV2); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("config directory holds %v, want just %s", names, FileName)
	}
}

func TestForgetRemovesAnEntry(t *testing.T) {
	tick(t)
	getenv, _ := config(t)

	alpha, beta := loom(t, "alpha"), loom(t, "beta")
	for _, l := range []target.Target{alpha, beta} {
		if err := Touch(getenv, l, target.TierV2); err != nil {
			t.Fatal(err)
		}
	}
	if err := Forget(getenv, alpha.Key()); err != nil {
		t.Fatal(err)
	}
	if got := keys(mustLoad(t, getenv)); !equal(got, []string{beta.Key()}) {
		t.Errorf("after Forget = %v, want just beta", got)
	}

	// Forgetting something absent is not an error and rewrites nothing.
	if err := Forget(getenv, "/repos/absent/.loom/loom.sh"); err != nil {
		t.Errorf("Forget of an absent key: %v", err)
	}
}

func TestRegistryIsCapped(t *testing.T) {
	tick(t)
	getenv, path := config(t)

	for i := range maxEntries + 10 {
		if err := Touch(getenv, loom(t, "repo"+string(rune('a'+i%26))+string(rune('a'+i/26))), target.TierV2); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded file
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Looms) != maxEntries {
		t.Errorf("the registry holds %d entries, want it capped at %d", len(decoded.Looms), maxEntries)
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

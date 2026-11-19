package warm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zealtv/easyloom/internal/known"
	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/userconfig"
)

// projection is a minimal v2 map document, with the loom's own root patched in
// so the probe's root check passes.
const projection = `{
  "schema_version": 1,
  "format_version": 2,
  "loom_root": "%ROOT%",
  "stitches": [],
  "decomposition_edges": [],
  "dependency_edges": [],
  "cycles": [],
  "frontier": [],
  "recently_completed": [],
  "diagnostics": []
}`

// stub is a deployed loom that counts its own map runs.
//
// Counting is done by the script itself rather than by injecting a fake
// snapshot source, because "opening a warm loom does not run map" is a claim
// about subprocesses. A seam that let the test count something else would be
// measuring the seam.
type stub struct {
	target  target.Target
	counter string
}

// runs is how many times map has been executed against this loom.
func (s stub) runs(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(s.counter)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// stubUnsupported deploys a loom emitting a map schema this build cannot read.
func stubUnsupported(t *testing.T) stub {
	s := stubLoom(t, false)
	body := strings.ReplaceAll(projection, "%ROOT%", s.target.Root)
	body = strings.Replace(body, `"schema_version": 1`, `"schema_version": 99`, 1)
	script := "#!/usr/bin/env bash\necho run >> " + s.counter + "\ncat <<'JSON'\n" + body + "\nJSON\n"
	if err := os.WriteFile(s.target.Script, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

// stubLoom deploys a counting loom. A v1 one refuses to produce a projection
// the way a script predating map does.
func stubLoom(t *testing.T, v1 bool) stub {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	loomDir := filepath.Join(root, ".loom")
	if err := os.MkdirAll(loomDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// The counter lives beside the loom rather than inside it, so appending to
	// it never wakes the loom's own watcher.
	counter := filepath.Join(root, "runs")

	body := "cat <<'JSON'\n" + strings.ReplaceAll(projection, "%ROOT%", loomDir) + "\nJSON\n"
	if v1 {
		body = "echo 'unknown command: map' >&2\nexit 1\n"
	} else {
		if err := os.WriteFile(filepath.Join(loomDir, "format-version"), []byte("2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/usr/bin/env bash\necho run >> " + counter + "\n" + body
	if err := os.WriteFile(filepath.Join(loomDir, "loom.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, err := target.Resolve(loomDir)
	if err != nil {
		t.Fatal(err)
	}
	return stub{target: resolved, counter: counter}
}

// config points the registry at a throwaway directory. Opening a loom records
// it, and a test suite must never write to the developer's real ~/.config.
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

// tick makes the clock advance a second per call, so recency ordering is
// asserted against distinct times rather than timer resolution.
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

func newSet(t *testing.T, opts Options) *Set {
	t.Helper()
	if opts.Getenv == nil {
		opts.Getenv = config(t)
	}
	if opts.Warn == nil {
		opts.Warn = func(err error) { t.Logf("warning: %v", err) }
	}
	set := NewSet(opts)
	t.Cleanup(set.Close)
	return set
}

// The whole point of the set: switching back to a loom is a redraw, not a map
// run.
func TestSecondOpenDoesNotRunMap(t *testing.T) {
	loom := stubLoom(t, false)
	set := newSet(t, Options{})

	first, err := set.Open(context.Background(), loom.target)
	if err != nil {
		t.Fatal(err)
	}
	if got := loom.runs(t); got != 1 {
		t.Fatalf("opening ran map %d times, want 1 — the probe's snapshot should seed the cache", got)
	}

	second, err := set.Open(context.Background(), loom.target)
	if err != nil {
		t.Fatal(err)
	}
	if got := loom.runs(t); got != 1 {
		t.Errorf("reopening ran map %d times in total, want 1", got)
	}
	if first != second {
		t.Error("reopening built a second loom rather than returning the warm one")
	}
}

func TestOpenSeedsTheCacheFromTheProbe(t *testing.T) {
	loom := stubLoom(t, false)
	set := newSet(t, Options{})

	opened, err := set.Open(context.Background(), loom.target)
	if err != nil {
		t.Fatal(err)
	}
	if !opened.Full() {
		t.Fatal("a v2 loom opened without a cache")
	}
	if opened.Cache.Current().Snapshot == nil {
		t.Error("the cache is empty, so the first frame would have to wait on map")
	}
	if opened.Mutator == nil {
		t.Error("a v2 loom opened read-only")
	}
	if opened.Version.Tier != target.TierV2 {
		t.Errorf("tier = %q, want v2", opened.Version.Tier)
	}
}

// A v1 loom is still one the user opened and may switch back to, but there is
// no projection for a cache to hold and no point paying for a watcher.
func TestV1LoomIsWarmButNotFull(t *testing.T) {
	loom := stubLoom(t, true)
	set := newSet(t, Options{})

	opened, err := set.Open(context.Background(), loom.target)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Version.Tier != target.TierV1 {
		t.Fatalf("tier = %q, want v1", opened.Version.Tier)
	}
	if opened.Full() {
		t.Error("a v1 loom opened with a cache")
	}
	if opened.Mutator != nil {
		t.Error("a v1 loom opened writable")
	}
	if set.Len() != 1 {
		t.Error("a v1 loom was not kept warm")
	}
}

func TestSetEvictsLeastRecentlyUsed(t *testing.T) {
	tick(t)
	set := newSet(t, Options{Limit: 2})

	looms := []stub{stubLoom(t, false), stubLoom(t, false), stubLoom(t, false)}
	opened := make([]*Loom, len(looms))
	for i, l := range looms {
		var err error
		if opened[i], err = set.Open(context.Background(), l.target); err != nil {
			t.Fatal(err)
		}
	}

	if set.Len() != 2 {
		t.Fatalf("the set holds %d looms, want its bound of 2", set.Len())
	}
	// Eviction is closing: the evicted loom's watcher and cache are released.
	if opened[0].Cache != nil {
		t.Error("the evicted loom still holds a cache")
	}
	if opened[0].watcher != nil {
		t.Error("the evicted loom still holds a watcher")
	}
	for _, l := range set.Warm() {
		if l.Target.Key() == looms[0].target.Key() {
			t.Error("the least recently used loom was not the one evicted")
		}
	}

	// Reopening it costs another map run, which is exactly the cost the bound
	// is trading for file descriptors.
	if _, err := set.Open(context.Background(), looms[0].target); err != nil {
		t.Fatal(err)
	}
	if got := looms[0].runs(t); got != 2 {
		t.Errorf("reopening an evicted loom ran map %d times in total, want 2", got)
	}
}

// Use, not open, is what recency means: a loom switched back to is the most
// recently used one.
func TestOpenRefreshesRecency(t *testing.T) {
	tick(t)
	set := newSet(t, Options{Limit: 2})

	first, second, third := stubLoom(t, false), stubLoom(t, false), stubLoom(t, false)
	for _, l := range []stub{first, second} {
		if _, err := set.Open(context.Background(), l.target); err != nil {
			t.Fatal(err)
		}
	}
	// Switching back to the first makes the second the oldest.
	if _, err := set.Open(context.Background(), first.target); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Open(context.Background(), third.target); err != nil {
		t.Fatal(err)
	}

	warm := set.Warm()
	if len(warm) != 2 {
		t.Fatalf("the set holds %d looms, want 2", len(warm))
	}
	if warm[0].Target.Key() != third.target.Key() {
		t.Errorf("most recent is %s, want the last opened", warm[0].Target.Root)
	}
	if warm[1].Target.Key() != first.target.Key() {
		t.Errorf("the loom switched back to was evicted instead of the one left alone")
	}
}

// The seam agent-trigger needs: a loom whose agent is mid-queue must not have
// its cache closed out from under it.
func TestPinnedLoomsAreNeverEvicted(t *testing.T) {
	tick(t)

	pinned := stubLoom(t, false)
	set := newSet(t, Options{
		Limit: 2,
		Pinned: func(l *Loom) bool {
			return l.Target.Key() == pinned.target.Key()
		},
	})

	if _, err := set.Open(context.Background(), pinned.target); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := set.Open(context.Background(), stubLoom(t, false).target); err != nil {
			t.Fatal(err)
		}
	}

	var found bool
	for _, l := range set.Warm() {
		if l.Target.Key() == pinned.target.Key() {
			found = true
			if l.Cache == nil {
				t.Error("the pinned loom was closed without being evicted")
			}
		}
	}
	if !found {
		t.Fatal("the pinned loom was evicted")
	}
}

// A set that is entirely pinned exceeds its bound rather than closing a loom
// an agent is working.
func TestAnEntirelyPinnedSetExceedsItsBound(t *testing.T) {
	tick(t)
	set := newSet(t, Options{Limit: 1, Pinned: func(*Loom) bool { return true }})

	for range 3 {
		if _, err := set.Open(context.Background(), stubLoom(t, false).target); err != nil {
			t.Fatal(err)
		}
	}
	if set.Len() != 3 {
		t.Errorf("the set holds %d looms, want all 3 kept over the bound", set.Len())
	}
}

func TestCloseIsIdempotentAndClosesEverything(t *testing.T) {
	set := NewSet(Options{Getenv: config(t)})

	opened := make([]*Loom, 0, 2)
	for range 2 {
		l, err := set.Open(context.Background(), stubLoom(t, false).target)
		if err != nil {
			t.Fatal(err)
		}
		opened = append(opened, l)
	}

	set.Close()
	set.Close() // the deferred shutdown of a program that also closes on error paths

	if set.Len() != 0 {
		t.Errorf("the set holds %d looms after Close", set.Len())
	}
	for _, l := range opened {
		if l.Cache != nil || l.watcher != nil {
			t.Errorf("%s was not closed", l.Target.Root)
		}
	}

	// Pinning does not survive Close: the process is going away, and the whole
	// point is that no subprocess outlives it.
	pinnedSet := NewSet(Options{Getenv: config(t), Pinned: func(*Loom) bool { return true }})
	pin, err := pinnedSet.Open(context.Background(), stubLoom(t, false).target)
	if err != nil {
		t.Fatal(err)
	}
	pinnedSet.Close()
	if pin.Cache != nil {
		t.Error("a pinned loom survived Close")
	}
}

func TestOpenAfterCloseFails(t *testing.T) {
	set := NewSet(Options{Getenv: config(t)})
	set.Close()

	if _, err := set.Open(context.Background(), stubLoom(t, false).target); err == nil {
		t.Error("Open on a closed set = nil error, want one")
	}
}

// Opening is precisely what the registry remembers.
func TestOpenRecordsInTheRegistry(t *testing.T) {
	getenv := config(t)
	set := newSet(t, Options{Getenv: getenv})

	v2, v1 := stubLoom(t, false), stubLoom(t, true)
	for _, l := range []stub{v2, v1} {
		if _, err := set.Open(context.Background(), l.target); err != nil {
			t.Fatal(err)
		}
	}

	list, err := known.Load(getenv)
	if err != nil {
		t.Fatal(err)
	}
	tiers := map[string]target.Tier{}
	for _, l := range list.Looms {
		tiers[l.Key] = l.Tier
	}
	if got := tiers[v2.target.Key()]; got != target.TierV2 {
		t.Errorf("the v2 loom was remembered as %q", got)
	}
	if got := tiers[v1.target.Key()]; got != target.TierV1 {
		t.Errorf("the v1 loom was remembered as %q — a v1 loom is still one the user opened", got)
	}
}

func TestOpenReportsAnUnreachableLoom(t *testing.T) {
	set := newSet(t, Options{})

	absent := target.Target{Script: "/nonexistent/.loom/loom.sh", Root: "/nonexistent/.loom"}
	if _, err := set.Open(context.Background(), absent); err == nil {
		t.Error("Open of an unreachable loom = nil error, want one")
	}
	if set.Len() != 0 {
		t.Error("a loom that failed to open was kept warm")
	}
}

// Two switches to the same cold loom at once must not build two of it, or the
// two would have separate caches and separate watchers on one loom.
func TestConcurrentOpensShareOneLoom(t *testing.T) {
	loom := stubLoom(t, false)
	set := newSet(t, Options{})

	const openers = 6
	results := make([]*Loom, openers)
	var wg sync.WaitGroup
	for i := range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opened, err := set.Open(context.Background(), loom.target)
			if err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			results[i] = opened
		}()
	}
	wg.Wait()

	for i, l := range results {
		if l == nil {
			t.Fatalf("opener %d got nothing", i)
		}
		if l != results[0] {
			t.Errorf("opener %d got a different loom", i)
		}
	}
	if set.Len() != 1 {
		t.Errorf("the set holds %d looms, want 1", set.Len())
	}
	// Duplicates built in the race are closed rather than leaked.
	if results[0].Cache == nil {
		t.Error("the shared loom has no cache")
	}
}

func TestWarmIsMostRecentlyUsedFirst(t *testing.T) {
	tick(t)
	set := newSet(t, Options{})

	looms := []stub{stubLoom(t, false), stubLoom(t, false), stubLoom(t, false)}
	for _, l := range looms {
		if _, err := set.Open(context.Background(), l.target); err != nil {
			t.Fatal(err)
		}
	}

	warm := set.Warm()
	for i, l := range warm {
		want := looms[len(looms)-1-i].target.Key()
		if l.Target.Key() != want {
			t.Errorf("Warm[%d] = %s, want the looms newest first", i, l.Target.Root)
		}
	}
}

func TestWarnReportsANonFatalProblem(t *testing.T) {
	// An unwritable configuration directory costs the next picker an entry and
	// nothing else, so it is a warning and the loom still opens.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root writes read-only directories")
	}

	var warnings []string
	set := NewSet(Options{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return dir
			}
			return ""
		},
		Warn: func(err error) { warnings = append(warnings, err.Error()) },
	})
	t.Cleanup(set.Close)

	if _, err := set.Open(context.Background(), stubLoom(t, false).target); err != nil {
		t.Fatalf("a loom failed to open over an unwritable config directory: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("no warning for a registry that could not be written")
	}
	if !strings.Contains(fmt.Sprint(warnings), "remembering") {
		t.Errorf("warnings = %v, want one about not remembering the loom", warnings)
	}
}

// A loom whose map schema this build cannot read is warm and listable, but
// every attempt to use it says why it cannot be. Dropping that error would
// leave the caller with a loom that silently draws nothing.
func TestUnsupportedSchemaIsCarriedNotDiscarded(t *testing.T) {
	loom := stubUnsupported(t)
	set := newSet(t, Options{})

	opened, err := set.Open(context.Background(), loom.target)
	if opened == nil {
		t.Fatalf("Open returned nothing for an unsupported loom: %v", err)
	}
	if err == nil {
		t.Fatal("Open of an unsupported loom = nil error, want one")
	}
	if opened.Err == nil {
		t.Error("the loom does not carry its own error")
	}
	if opened.Full() {
		t.Error("an unsupported loom opened with a cache")
	}
	if opened.Version.Tier != target.TierUnsupported {
		t.Errorf("tier = %q, want unsupported", opened.Version.Tier)
	}

	// And reopening the warm one reports the same thing rather than nil.
	if _, again := set.Open(context.Background(), loom.target); again == nil {
		t.Error("reopening a warm unsupported loom = nil error, want the same one")
	}
}

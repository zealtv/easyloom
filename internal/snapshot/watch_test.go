package snapshot

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// triggers records what the watcher asked for, and lets a test wait for the
// next request rather than sleeping for one.
type triggers struct {
	mu    sync.Mutex
	count int
	fired chan struct{}
}

func newTriggers() *triggers { return &triggers{fired: make(chan struct{}, 64)} }

func (t *triggers) fire() {
	t.mu.Lock()
	t.count++
	t.mu.Unlock()
	select {
	case t.fired <- struct{}{}:
	default:
	}
}

func (t *triggers) total() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// await waits for one trigger. The budget is generous because it covers a
// debounce plus whatever the filesystem takes to report.
func (t *triggers) await(tb testing.TB) {
	tb.Helper()
	select {
	case <-t.fired:
	case <-time.After(5 * time.Second):
		tb.Fatal("no refresh was triggered")
	}
}

// fakeLoom builds the parts of a loom the watcher cares about: a queue file,
// a thread with a stitch in it, and the archive trays.
func fakeLoom(tb testing.TB) string {
	tb.Helper()
	root := filepath.Join(tb.TempDir(), ".loom")
	for _, dir := range []string{
		filepath.Join(root, "threads", "web-shell", "live-updates"),
		filepath.Join(root, "threads", "web-shell", "http-server.tied"),
		filepath.Join(root, "tied"),
		filepath.Join(root, "dropped"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatal(err)
		}
	}
	write(tb, filepath.Join(root, "queue"), "live-updates\n")
	write(tb, filepath.Join(root, "threads", "web-shell", "live-updates", "instructions.md"), "# push it\n")
	return root
}

func write(tb testing.TB, path, contents string) {
	tb.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		tb.Fatal(err)
	}
}

// The debounce a test runs at. Short enough not to pad the suite, long enough
// that one write is still one trigger.
const testDebounce = 60 * time.Millisecond

func startWatch(tb testing.TB, root string) *triggers {
	tb.Helper()
	fired := newTriggers()
	w, err := Watch(root, testDebounce, fired.fire)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { w.Close() })
	return fired
}

func TestWatchTriggersOnStitchEdit(t *testing.T) {
	root := fakeLoom(t)
	fired := startWatch(t, root)

	write(t, filepath.Join(root, "threads", "web-shell", "live-updates", "instructions.md"), "# pushed\n")
	fired.await(t)
}

func TestWatchTriggersOnQueueRewrite(t *testing.T) {
	root := fakeLoom(t)
	fired := startWatch(t, root)

	// loom replaces the queue rather than editing it in place, which is why
	// the watch is on the directory and not on the file.
	tmp := filepath.Join(root, "queue.tmp")
	write(t, tmp, "snapshot-refresh\nlive-updates\n")
	if err := os.Rename(tmp, filepath.Join(root, "queue")); err != nil {
		t.Fatal(err)
	}
	fired.await(t)
}

// A claim renames the stitch directory. The watcher must not only notice it,
// it must end up watching the new path — otherwise the second edit is silent.
func TestWatchFollowsAClaimRename(t *testing.T) {
	root := fakeLoom(t)
	fired := startWatch(t, root)

	thread := filepath.Join(root, "threads", "web-shell")
	claimed := filepath.Join(thread, "live-updates.stitching")
	if err := os.Rename(filepath.Join(thread, "live-updates"), claimed); err != nil {
		t.Fatal(err)
	}
	fired.await(t)

	write(t, filepath.Join(claimed, "instructions.md"), "# claimed\n")
	fired.await(t)
}

// One mutation is a burst of events. The debounce is what keeps that from
// becoming a queue of snapshots that each cost seconds.
func TestWatchDebouncesABurst(t *testing.T) {
	root := fakeLoom(t)
	fired := startWatch(t, root)

	dir := filepath.Join(root, "threads", "web-shell", "live-updates")
	for i := 0; i < 20; i++ {
		write(t, filepath.Join(dir, "artifact.md"), string(rune('a'+i)))
	}
	fired.await(t)

	// Nothing further should arrive from that burst.
	time.Sleep(4 * testDebounce)
	if got := fired.total(); got != 1 {
		t.Fatalf("a burst of 20 writes triggered %d refreshes, want 1", got)
	}
}

func TestWatchIgnoresArchivesAndNoise(t *testing.T) {
	root := fakeLoom(t)
	fired := startWatch(t, root)

	write(t, filepath.Join(root, "tied", "old-stitch"), "x")
	write(t, filepath.Join(root, "threads", "web-shell", "http-server.tied", "instructions.md"), "x")
	write(t, filepath.Join(root, "threads", "web-shell", ".DS_Store"), "x")

	time.Sleep(6 * testDebounce)
	if got := fired.total(); got != 0 {
		t.Fatalf("archives and .DS_Store triggered %d refreshes, want 0", got)
	}
}

// Close must be final: no trigger may run after it returns, or a cache that
// has been closed gets a refresh request.
func TestWatchCloseStopsTriggers(t *testing.T) {
	root := fakeLoom(t)
	fired := newTriggers()
	w, err := Watch(root, testDebounce, fired.fire)
	if err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(root, "queue"), "x\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w.Close() // idempotent

	before := fired.total()
	time.Sleep(6 * testDebounce)
	if got := fired.total(); got != before {
		t.Fatalf("triggered after Close: %d then %d", before, got)
	}
}

func TestWatchNeedsATrigger(t *testing.T) {
	if _, err := Watch(fakeLoom(t), 0, nil); err == nil {
		t.Fatal("watching with no trigger succeeded")
	}
}

// A loom with no threads directory yet is not a reason to refuse to watch: the
// root, and so the queue, is still worth knowing about.
func TestWatchSurvivesAMissingThreadsDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".loom")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fired := startWatch(t, root)

	write(t, filepath.Join(root, "queue"), "x\n")
	fired.await(t)
}

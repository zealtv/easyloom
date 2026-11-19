package snapshot

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultDebounce is how long the watcher waits for the filesystem to go quiet
// before it triggers.
//
// A snapshot can cost seconds — 7.8s on a large loom under loom.sh v2.1.0,
// still the case for any loom not yet on v2.1.1 — so a tight
// debounce does not make the UI fresher, it just queues work that is already
// stale by the time it runs. One mutation is a burst of events: loom renames a
// directory, writes files inside it, and rewrites the queue.
const DefaultDebounce = 750 * time.Millisecond

// archives are the directory names a watch is never established inside: the
// trays, and the lifecycle suffixes that mark a stitch as finished with.
//
// They change only when a stitch is tied or dropped, and that rename is seen
// on the parent, which is watched. What they hold is every stitch the loom has
// ever finished — the largest part of a mature loom, and the part that says
// nothing about the work in front of anyone.
var archives = map[string]bool{"tied": true, "dropped": true, "legacy-v1": true}

// archivedSuffixes mark a stitch directory that has left the working set.
var archivedSuffixes = []string{".tied", ".dropped"}

// Watcher turns filesystem activity under a loom into refresh requests.
//
// It is an invalidation trigger and nothing more. It never parses a path, and
// it never infers from an event what changed — the only thing it concludes is
// "something moved, ask the loom again". Every fact about the loom still comes
// from `map --json`, so a watcher that misses an event costs freshness and can
// never cost correctness.
type Watcher struct {
	root     string
	debounce time.Duration
	trigger  func()

	fsw     *fsnotify.Watcher
	watched map[string]bool

	stop     chan struct{}
	stopped  chan struct{}
	closeOne sync.Once
}

// Watch starts watching a loom root and calls trigger — on its own goroutine —
// once the filesystem has been quiet for the debounce interval.
//
// Cache.Refresh is the trigger in practice: it is safe at any rate, and
// collapses whatever arrives while a snapshot is running into one further run.
//
// A debounce of zero takes DefaultDebounce. Failing to establish the watch is
// an error the caller may ignore: without a watcher the UI is a still
// photograph with a refresh key, which is what it was before.
func Watch(root string, debounce time.Duration, trigger func()) (*Watcher, error) {
	if trigger == nil {
		return nil, fmt.Errorf("watch %s: no trigger", root)
	}
	if debounce <= 0 {
		debounce = DefaultDebounce
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch %s: %w", root, err)
	}

	w := &Watcher{
		root:     root,
		debounce: debounce,
		trigger:  trigger,
		fsw:      fsw,
		watched:  map[string]bool{},
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}

	if err := w.rewatch(); err != nil {
		fsw.Close()
		return nil, err
	}

	go w.run()
	return w, nil
}

// Close stops the watcher. It is safe to call more than once, and it waits for
// the goroutine to finish so a trigger never fires after it returns.
func (w *Watcher) Close() error {
	if w == nil {
		return nil
	}
	w.closeOne.Do(func() {
		close(w.stop)
		w.fsw.Close()
		<-w.stopped
	})
	return nil
}

func (w *Watcher) run() {
	defer close(w.stopped)

	// timer runs only while events are pending; fire is nil the rest of the
	// time so an idle watcher wakes for nothing.
	timer := time.NewTimer(w.debounce)
	if !timer.Stop() {
		<-timer.C
	}
	var fire <-chan time.Time
	defer timer.Stop()

	for {
		select {
		case <-w.stop:
			return

		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if !interesting(event) {
				continue
			}
			if fire != nil && !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.debounce)
			fire = timer.C

		case <-w.fsw.Errors:
			// An overflowed or dropped event means we may have missed
			// something, which is exactly the case a refresh answers.
			if fire == nil {
				timer.Reset(w.debounce)
				fire = timer.C
			}

		case <-fire:
			fire = nil
			// Claim and tie rename directories, so the watch set the burst just
			// invalidated is the one we are holding. Re-walking is simpler than
			// tracking renames and cheap next to the snapshot about to run.
			_ = w.rewatch()
			w.trigger()
		}
	}
}

// interesting drops events that cannot mean the projection changed.
//
// Chmod is not one of them: loom encodes state in path names and file
// contents, never in a mode bit, and some platforms report a chmod for a read.
func interesting(event fsnotify.Event) bool {
	if event.Op == fsnotify.Chmod {
		return false
	}
	return !ignoredName(filepath.Base(event.Name))
}

// ignoredName is the noise several of these looms carry.
func ignoredName(name string) bool {
	return name == ".DS_Store"
}

// rewatch walks the loom and brings the watch set up to date: the root itself,
// so the queue file is covered however it is rewritten, and every directory
// under threads/ that is not an archive.
//
// A directory that has just been renamed out from under the walk is not an
// error — that is the mutation we are about to snapshot.
func (w *Watcher) rewatch() error {
	found := map[string]bool{}

	// The root is watched rather than .loom/queue itself: loom rewrites the
	// queue by replacing it, and a watch on a file follows the inode that has
	// just been unlinked.
	add := func(dir string) {
		found[dir] = true
		if w.watched[dir] {
			return
		}
		if err := w.fsw.Add(dir); err != nil {
			return
		}
		w.watched[dir] = true
	}
	add(w.root)

	threads := filepath.Join(w.root, "threads")
	err := filepath.WalkDir(threads, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Missing or vanished mid-walk: skip it and keep the rest of the
			// tree watched rather than giving up on the whole loom.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != threads && archived(d.Name()) {
			return fs.SkipDir
		}
		add(path)
		return nil
	})

	for dir := range w.watched {
		if found[dir] {
			continue
		}
		w.fsw.Remove(dir)
		delete(w.watched, dir)
	}

	if err != nil && len(w.watched) == 0 {
		return fmt.Errorf("watch %s: %w", w.root, err)
	}
	return nil
}

// archived reports a directory whose contents cannot change without a
// mutation we already refresh after.
func archived(name string) bool {
	if archives[name] {
		return true
	}
	for _, suffix := range archivedSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

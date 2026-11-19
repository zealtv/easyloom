package warm

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/zealtv/easyloom/internal/known"
	"github.com/zealtv/easyloom/internal/loom"
	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/userconfig"
)

// DefaultLimit is how many looms stay warm. Enough that switching between the
// two or three a person actually works in is free, small enough that a long
// session over a big registry is not holding watchers for looms nobody has
// looked at in an hour.
const DefaultLimit = 4

// now is the clock, replaced in tests: recency ordering must not depend on
// timer resolution.
var now = time.Now

// Loom is one loom held open, with everything needed to draw and drive it.
//
// It is the per-loom bundle main used to assemble inline. Run state for an
// agent working this loom's queue belongs here too, which is why eviction
// consults [Options.Pinned] rather than simply closing the oldest.
type Loom struct {
	// Target is the resolved loom.
	Target target.Target
	// Version is the probe result.
	Version target.Version
	// Cache holds this loom's projection, seeded from the probe. It is nil for
	// a loom that produced no projection — a v1 or unsupported one — because
	// there is nothing for a cache to hold and no point paying for a watcher.
	Cache *snapshot.Cache
	// Mutator is the write half. Nil means read-only, which is how a v1 loom
	// already travels through the rest of EasyLoom.
	Mutator *loom.Runner
	// Err is why this loom cannot be driven, for a tier that parsed but is not
	// supported. Nil for a healthy loom and for a plain v1 one, which is a
	// normal state of the world rather than a failure.
	Err error

	watcher  *snapshot.Watcher
	lastUsed time.Time
}

// Full reports whether this loom can be drawn and driven.
func (l *Loom) Full() bool { return l.Cache != nil }

// close releases the watcher and the cache, and is safe to call twice.
func (l *Loom) close() {
	if l.watcher != nil {
		l.watcher.Close()
		l.watcher = nil
	}
	if l.Cache != nil {
		l.Cache.Close()
		l.Cache = nil
	}
}

// Options configure a Set. The zero value is usable.
type Options struct {
	// Limit is how many looms stay warm. Zero means DefaultLimit.
	Limit int
	// Prober probes each loom as it is opened. Nil means the Set makes its
	// own.
	//
	// Opening deliberately forgets any remembered probe first, so a loom is
	// always warmed from a projection taken now. Sharing a Prober with a scan
	// therefore saves nothing on open; it is worth passing only when something
	// else reads tiers from it.
	Prober *target.Prober
	// Getenv resolves the user configuration directory, so opening a loom can
	// record it in the registry. Nil reads the real environment.
	Getenv userconfig.Getenv
	// Pinned reports whether a loom must not be evicted. Nil pins nothing.
	//
	// This is the seam an agent working a loom's queue hangs off: leaving that
	// loom must neither orphan nor kill it, so the set has to be told it
	// cannot have its cache and watcher back yet.
	Pinned func(*Loom) bool
	// Warn receives problems that cost something but stop nothing — a
	// registry that could not be written, a watcher that could not be
	// established. Nil discards them.
	Warn func(error)
}

// Set is the looms held open.
type Set struct {
	opts Options

	mu     sync.Mutex
	looms  map[string]*Loom
	closed bool
}

// NewSet returns an empty set.
func NewSet(opts Options) *Set {
	if opts.Limit <= 0 {
		opts.Limit = DefaultLimit
	}
	if opts.Prober == nil {
		opts.Prober = &target.Prober{}
	}
	return &Set{opts: opts, looms: map[string]*Loom{}}
}

// Open returns the loom, opening it if it is not already warm.
//
// It mirrors [target.Prober.Probe]: a loom this build cannot drive is still
// returned, alongside the error saying why. Only a loom that could not be
// reached at all comes back nil. Callers that mean to draw it check
// [Loom.Full].
//
// A loom already in the set is returned as it stands: no probe, no map run, no
// new watcher. That is the whole point of the set, and it is what makes
// switching back to a loom a redraw rather than seconds of waiting.
//
// Opening always records the loom in the registry, since opening is precisely
// what that file remembers. A registry that cannot be written is a warning:
// it costs the next picker an entry and nothing else.
func (s *Set) Open(ctx context.Context, t target.Target) (*Loom, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("open %s: the loom set is closed", t.Root)
	}
	if warm, ok := s.looms[t.Key()]; ok {
		warm.lastUsed = now()
		s.mu.Unlock()
		s.remember(warm)
		return warm, warm.Err
	}
	s.mu.Unlock()

	// Probing and the first snapshot happen outside the lock: they are
	// subprocesses, and holding the set locked across them would make a switch
	// wait on an unrelated loom opening.
	opened, err := s.build(ctx, t)
	if opened == nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		opened.close()
		return nil, fmt.Errorf("open %s: the loom set is closed", t.Root)
	}
	// Another Open may have built the same loom while this one was probing.
	// Keep the one already in the set so that everybody switching to this loom
	// shares a cache, and close the duplicate.
	if existing, ok := s.looms[t.Key()]; ok {
		existing.lastUsed = now()
		s.mu.Unlock()
		opened.close()
		s.remember(existing)
		return existing, existing.Err
	}
	s.looms[t.Key()] = opened
	s.evictLocked()
	s.mu.Unlock()

	s.remember(opened)
	return opened, err
}

// build assembles one loom: probe, cache seeded from the probe's snapshot,
// watcher, mutator.
func (s *Set) build(ctx context.Context, t target.Target) (*Loom, error) {
	// A registry entry may name a loom that has since been deleted, and the
	// probe would call that a v1 loom: a script that cannot be run refuses to
	// produce a projection exactly like one that predates map. Check the
	// script is there rather than warming a loom that is not.
	if info, err := os.Stat(t.Script); err != nil {
		return nil, fmt.Errorf("open %s: %w", t.Root, err)
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("open %s: %s is not a regular file", t.Root, t.Script)
	}

	// Forget any remembered probe, so the snapshot seeded below is one taken
	// now. The memo exists to stop a tier being re-probed on a render path;
	// reused here it would seed the cache from a projection taken before this
	// loom went cold — or before a scan minutes ago — and nothing would
	// correct it until the watcher happened to fire.
	s.opts.Prober.Forget(t)

	version, err := s.opts.Prober.Probe(ctx, t)
	if version.Tier == "" {
		return nil, err
	}

	// A tier this build cannot drive — an unsupported map schema — is carried
	// rather than discarded. The loom is warm and listable, and every caller
	// that tries to use it gets told why it cannot.
	opened := &Loom{Target: t, Version: version, Err: err, lastUsed: now()}

	// No projection means nothing for a cache to hold. A v1 loom is still
	// remembered and still switchable to; it just has nothing to draw, and
	// paying for a watcher on it would buy nothing.
	if version.Snapshot == nil {
		return opened, err
	}

	runner := snapshot.Runner{Script: t.Script, Dir: t.Root}
	cache := snapshot.NewCache(runner.Run)
	// The probe already paid for this snapshot, so opening runs map once
	// rather than twice.
	cache.Seed(version.Snapshot)
	opened.Cache = cache

	opened.Mutator = loom.New(t.Script, t.Root, func() *snapshot.Snapshot {
		return cache.Current().Snapshot
	})

	// A watcher that cannot be established costs freshness, not correctness:
	// the loom is still open and still refreshable by hand.
	if watcher, err := snapshot.Watch(t.Root, 0, cache.Refresh); err == nil {
		opened.watcher = watcher
	} else {
		s.warn(fmt.Errorf("not watching %s: %w", t.Root, err))
	}

	return opened, err
}

// evictLocked closes least-recently-used looms until the set is within its
// bound, skipping any the caller has pinned.
//
// A set that is entirely pinned stays over its bound. Exceeding a limit that
// exists to save file descriptors is the better failure: the alternative is
// closing the cache out from under an agent that is mid-queue.
func (s *Set) evictLocked() {
	for len(s.looms) > s.opts.Limit {
		var oldest *Loom
		var oldestKey string
		for key, candidate := range s.looms {
			if s.pinned(candidate) {
				continue
			}
			if oldest == nil || candidate.lastUsed.Before(oldest.lastUsed) {
				oldest, oldestKey = candidate, key
			}
		}
		if oldest == nil {
			return
		}
		delete(s.looms, oldestKey)
		oldest.close()
	}
}

// Warm lists the looms held open, most recently used first.
func (s *Set) Warm() []*Loom {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*Loom, 0, len(s.looms))
	for _, l := range s.looms {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].lastUsed.After(out[j].lastUsed) })
	return out
}

// Len is how many looms are warm.
func (s *Set) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.looms)
}

// Close closes every warm loom. It is safe to call twice, which matters
// because it is the deferred shutdown of a program that also closes on its way
// out of an error path.
//
// Pinning does not survive Close: the process is going away, and a subprocess
// outliving it is the thing being prevented.
func (s *Set) Close() {
	s.mu.Lock()
	looms := s.looms
	s.looms = map[string]*Loom{}
	s.closed = true
	s.mu.Unlock()

	for _, l := range looms {
		l.close()
	}
}

// remember records the open in the registry, outside the lock: it is a file
// write, and the set should not be held closed across one.
func (s *Set) remember(l *Loom) {
	if err := known.Touch(s.opts.Getenv, l.Target, l.Version.Tier); err != nil {
		s.warn(fmt.Errorf("not remembering %s: %w", l.Target.Root, err))
	}
}

func (s *Set) pinned(l *Loom) bool {
	return s.opts.Pinned != nil && s.opts.Pinned(l)
}

func (s *Set) warn(err error) {
	if s.opts.Warn != nil {
		s.opts.Warn(err)
	}
}

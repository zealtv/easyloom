package snapshot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed reports a refresh requested on a closed cache.
var ErrClosed = errors.New("snapshot cache is closed")

// Source produces one snapshot. Runner.Run satisfies it, and a test can
// substitute anything.
type Source func(context.Context) (*Snapshot, error)

// CacheState is one atomically published view of a loom. It is a value: reading it
// takes no lock and it never changes underneath the reader.
type CacheState struct {
	// Snapshot is the most recent snapshot that parsed. It is nil only before
	// the first successful refresh, and it is never blanked by a later
	// failure. Treat it as immutable — every reader shares it.
	Snapshot *Snapshot
	// Generation counts published snapshots, starting at 1. Zero means none
	// yet. Live updates compare it to decide whether there is anything to
	// push; a failed refresh does not advance it.
	Generation uint64
	// CompletedAt is when Snapshot finished. The UI shows its age.
	CompletedAt time.Time
	// Err is the error from the most recent refresh attempt, whether or not it
	// produced a snapshot. With Stale false it is schema drift on the current
	// snapshot; with Stale true it is why the refresh failed.
	Err error
	// Stale reports that the last attempt failed, so Snapshot predates it. A
	// broken loom shows stale data behind an error banner, never nothing.
	Stale bool
	// Attempts counts finished refresh attempts, successful or not.
	Attempts uint64
	// Refreshing reports a snapshot running right now. It is what lets a UI
	// show progress through a multi-second run instead of appearing frozen,
	// and it is the only field that moves without the projection moving.
	Refreshing bool
}

// Age is how long ago the current snapshot completed. It is zero when there is
// no snapshot yet.
func (s CacheState) Age() time.Duration {
	if s.CompletedAt.IsZero() {
		return 0
	}
	return time.Since(s.CompletedAt)
}

// Cache holds one loom's snapshot and refreshes it in the background.
//
// A snapshot can cost seconds on a large loom running loom.sh v2.1.0 or
// earlier, so nothing that renders may wait on one. Readers take the current state and
// carry on; refreshes happen behind them and swap the result in.
type Cache struct {
	source Source

	// state is the published value. Readers touch only this.
	state atomic.Pointer[CacheState]

	// mu guards refresh bookkeeping, never the published state.
	mu       sync.Mutex
	running  bool
	dirty    bool
	closed   bool
	changed  chan struct{}
	cancel   context.CancelFunc
	finished chan struct{}
}

// NewCache returns a cache with no snapshot yet. Nothing runs until Refresh.
func NewCache(source Source) *Cache {
	return &Cache{source: source, changed: make(chan struct{})}
}

// Seed publishes a snapshot somebody else already paid for — the version
// probe's, in practice — so the first render does not wait on a second run of
// a command that costs seconds. It is a no-op once anything has been
// published, so a refresh in flight always wins.
func (c *Cache) Seed(snap *Snapshot) {
	if snap == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Anything published, or any refresh already running that is about to
	// publish, outranks a seed.
	if c.state.Load() != nil || c.running {
		return
	}
	c.applyLocked(func(CacheState) CacheState {
		return CacheState{Snapshot: snap, Generation: 1, CompletedAt: time.Now(), Attempts: 1}
	})
}

// Current returns the published state without blocking, whatever a refresh in
// flight is doing.
func (c *Cache) Current() CacheState {
	if s := c.state.Load(); s != nil {
		return *s
	}
	return CacheState{}
}

// Changed returns a channel that is closed the next time the published state
// changes — a new projection, a failed attempt, or a run starting or stopping.
//
// It is the honest signal that something moved, and it is what live updates
// are pushed off. A caller takes the channel, reads Current, and takes the
// channel again: because the channel is handed out before the state is read,
// no change can slip between the two.
func (c *Cache) Changed() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.changed
}

// Refresh asks for a new snapshot and returns immediately.
//
// It is safe to call from anywhere at any rate. Exactly one refresh runs per
// cache: a request arriving while one is in flight sets a dirty flag that
// causes a single re-run on completion, so a burst of file system events
// collapses into one extra snapshot rather than a queue of them.
func (c *Cache) Refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	if c.running {
		c.dirty = true
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.running = true
	c.cancel = cancel
	c.finished = make(chan struct{})
	// Say that a run has started before it starts. On a large loom this is
	// the only thing anyone hears for the next several seconds.
	c.applyLocked(func(previous CacheState) CacheState {
		previous.Refreshing = true
		return previous
	})
	go c.refreshLoop(ctx, c.finished)
}

// RefreshNow triggers a refresh and waits for an attempt that finishes after
// the call, then returns the resulting state. It is for callers that have
// nothing to show yet — the CLI, or a first page load — and never for a
// render path that already has a state to draw.
//
// The returned error is the context's, if it expires first; the refresh's own
// error is in the state.
func (c *Cache) RefreshNow(ctx context.Context) (CacheState, error) {
	c.mu.Lock()
	closed := c.closed
	start := c.attemptsLocked()
	c.mu.Unlock()
	if closed {
		return c.Current(), ErrClosed
	}

	c.Refresh()

	for {
		c.mu.Lock()
		changed := c.changed
		done := c.attemptsLocked() > start
		c.mu.Unlock()
		if done {
			return c.Current(), nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return c.Current(), ctx.Err()
		}
	}
}

// Close cancels any refresh in flight and stops the cache accepting new ones.
// The last published state stays readable.
func (c *Cache) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.dirty = false
	cancel, finished := c.cancel, c.finished
	c.mu.Unlock()

	if cancel != nil {
		cancel()
		<-finished
	}
}

func (c *Cache) refreshLoop(ctx context.Context, finished chan struct{}) {
	defer close(finished)

	for {
		snap, err := c.source(ctx)

		c.mu.Lock()
		// Coalesced requests arrived during the run: one more pass covers all
		// of them, however many there were.
		again := c.dirty && !c.closed
		if again {
			c.dirty = false
		} else {
			c.running = false
			c.cancel = nil
			c.finished = nil
		}
		// The result and whether a run is still going are one state change, so
		// they are published together: a listener never sees a generation land
		// and then, separately, the spinner stop.
		c.applyLocked(func(previous CacheState) CacheState {
			next := published(previous, snap, err)
			next.Refreshing = again
			return next
		})
		c.mu.Unlock()

		if !again {
			return
		}
	}
}

// published is the result of one attempt laid over the state before it.
//
// A snapshot alongside an error is schema drift, which is usable; only a nil
// snapshot is a failure, and that leaves the previous one in place.
func published(previous CacheState, snap *Snapshot, err error) CacheState {
	next := CacheState{
		Snapshot:    previous.Snapshot,
		Generation:  previous.Generation,
		CompletedAt: previous.CompletedAt,
		Err:         err,
		Stale:       snap == nil,
		Attempts:    previous.Attempts + 1,
	}
	if snap != nil {
		next.Snapshot = snap
		next.Generation = previous.Generation + 1
		next.CompletedAt = time.Now()
	}
	return next
}

// applyLocked swaps in a new state and wakes everyone waiting on a change. The
// caller holds mu, which is what makes read-modify-write of the published
// state atomic against a concurrent refresh.
func (c *Cache) applyLocked(mutate func(CacheState) CacheState) {
	previous := CacheState{}
	if s := c.state.Load(); s != nil {
		previous = *s
	}
	next := mutate(previous)
	c.state.Store(&next)
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *Cache) attemptsLocked() uint64 {
	if s := c.state.Load(); s != nil {
		return s.Attempts
	}
	return 0
}

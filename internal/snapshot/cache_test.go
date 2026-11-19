package snapshot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSource is a Source a test drives directly: it reports how many runs
// started, and can be held mid-run to create the in-flight window the
// coalescing rules are about.
type fakeSource struct {
	mu      sync.Mutex
	started int
	release chan struct{} // if non-nil, each run waits on it before returning

	result func(run int) (*Snapshot, error)
}

func (f *fakeSource) run(ctx context.Context) (*Snapshot, error) {
	f.mu.Lock()
	f.started++
	run := f.started
	release := f.release
	f.mu.Unlock()

	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.result(run)
}

func (f *fakeSource) runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func snapshotWithRoot(root string) *Snapshot {
	return &Snapshot{SchemaVersion: SupportedSchemaVersion, FormatVersion: FormatVersionV2, LoomRoot: root}
}

func TestCacheEmptyBeforeFirstRefresh(t *testing.T) {
	cache := NewCache(func(context.Context) (*Snapshot, error) {
		t.Error("source ran before Refresh")
		return nil, nil
	})

	state := cache.Current()
	if state.Snapshot != nil || state.Generation != 0 || state.Err != nil {
		t.Fatalf("got %+v, want an empty state", state)
	}
	if state.Age() != 0 {
		t.Errorf("Age = %s, want 0 with no snapshot", state.Age())
	}
}

func TestCachePublishesSnapshot(t *testing.T) {
	source := &fakeSource{result: func(int) (*Snapshot, error) { return snapshotWithRoot("/repo/.loom"), nil }}
	cache := NewCache(source.run)
	defer cache.Close()

	state, err := cache.RefreshNow(context.Background())
	if err != nil {
		t.Fatalf("RefreshNow: %v", err)
	}
	if state.Snapshot == nil || state.Snapshot.LoomRoot != "/repo/.loom" {
		t.Fatalf("got %+v, want the published snapshot", state.Snapshot)
	}
	if state.Generation != 1 {
		t.Errorf("generation = %d, want 1", state.Generation)
	}
	if state.CompletedAt.IsZero() {
		t.Error("CompletedAt not set")
	}
	if state.Stale {
		t.Error("state marked stale after a good refresh")
	}
}

func TestCacheGenerationAdvancesPerSnapshot(t *testing.T) {
	source := &fakeSource{result: func(run int) (*Snapshot, error) { return snapshotWithRoot("/repo/.loom"), nil }}
	cache := NewCache(source.run)
	defer cache.Close()

	for want := uint64(1); want <= 3; want++ {
		state, err := cache.RefreshNow(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if state.Generation != want {
			t.Fatalf("generation = %d, want %d", state.Generation, want)
		}
	}
}

// The rule the whole cache exists for: one bad refresh must not blank the UI.
func TestCacheFailedRefreshKeepsPreviousSnapshot(t *testing.T) {
	failing := errors.New("loom is on fire")
	source := &fakeSource{result: func(run int) (*Snapshot, error) {
		if run == 1 {
			return snapshotWithRoot("/repo/.loom"), nil
		}
		return nil, failing
	}}
	cache := NewCache(source.run)
	defer cache.Close()

	first, err := cache.RefreshNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	state, err := cache.RefreshNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Snapshot != first.Snapshot {
		t.Fatal("failed refresh replaced the good snapshot")
	}
	if state.Generation != first.Generation {
		t.Errorf("generation = %d, want it held at %d", state.Generation, first.Generation)
	}
	if !state.Stale {
		t.Error("state not marked stale after a failed refresh")
	}
	if !errors.Is(state.Err, failing) {
		t.Errorf("Err = %v, want %v", state.Err, failing)
	}
	if state.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", state.Attempts)
	}
}

// Drift comes back with a usable snapshot, so it is a successful refresh that
// happens to carry a warning — not a stale one.
func TestCacheDriftPublishesSnapshotWithError(t *testing.T) {
	drift := &DriftError{Issues: []string{"unknown field \"weft\""}}
	source := &fakeSource{result: func(int) (*Snapshot, error) { return snapshotWithRoot("/repo/.loom"), drift }}
	cache := NewCache(source.run)
	defer cache.Close()

	state, err := cache.RefreshNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Snapshot == nil {
		t.Fatal("drift discarded the snapshot")
	}
	if state.Stale {
		t.Error("drift marked the state stale")
	}
	if !errors.Is(state.Err, error(drift)) {
		t.Errorf("Err = %v, want the drift", state.Err)
	}
}

// Requests arriving during a run collapse into exactly one re-run, not one per
// request. On a loom where a snapshot takes seconds, queueing N runs would
// mean minutes of pointless work.
func TestCacheCoalescesRefreshesInFlight(t *testing.T) {
	release := make(chan struct{})
	source := &fakeSource{
		release: release,
		result:  func(int) (*Snapshot, error) { return snapshotWithRoot("/repo/.loom"), nil },
	}
	cache := NewCache(source.run)
	defer cache.Close()

	cache.Refresh()
	waitFor(t, func() bool { return source.runs() == 1 }, "first run to start")

	for i := 0; i < 50; i++ {
		cache.Refresh()
	}
	if got := source.runs(); got != 1 {
		t.Fatalf("%d runs started while one was in flight, want 1", got)
	}

	close(release)
	waitFor(t, func() bool { return cache.Current().Attempts == 2 }, "the coalesced re-run to finish")

	// Give any surplus runs a chance to show up before asserting there are none.
	time.Sleep(20 * time.Millisecond)
	if got := source.runs(); got != 2 {
		t.Fatalf("%d runs total, want 2: the burst should collapse into one re-run", got)
	}
}

// Current must never wait on a refresh, however long the snapshot takes.
func TestCacheCurrentDoesNotBlockOnRefresh(t *testing.T) {
	release := make(chan struct{})
	source := &fakeSource{
		release: release,
		result:  func(int) (*Snapshot, error) { return snapshotWithRoot("/repo/.loom"), nil },
	}
	cache := NewCache(source.run)
	defer cache.Close()

	cache.Refresh()
	waitFor(t, func() bool { return source.runs() == 1 }, "the run to start")

	done := make(chan CacheState, 1)
	go func() { done <- cache.Current() }()

	select {
	case state := <-done:
		if state.Snapshot != nil {
			t.Error("got a snapshot before the first refresh finished")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Current blocked on a refresh in flight")
	}
	close(release)
}

func TestCacheCloseCancelsRefreshInFlight(t *testing.T) {
	started := make(chan struct{})
	source := &fakeSource{result: func(int) (*Snapshot, error) {
		t.Error("source returned a result despite cancellation")
		return nil, nil
	}}
	cache := NewCache(func(ctx context.Context) (*Snapshot, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})

	cache.Refresh()
	<-started
	cache.Close()

	if _, err := cache.RefreshNow(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("RefreshNow after Close: %v, want ErrClosed", err)
	}
	if source.runs() != 0 {
		t.Error("a refresh started after Close")
	}
}

func TestCacheRefreshNowHonoursContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	source := &fakeSource{
		release: release,
		result:  func(int) (*Snapshot, error) { return snapshotWithRoot("/repo/.loom"), nil },
	}
	cache := NewCache(source.run)
	defer cache.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	state, err := cache.RefreshNow(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if state.Snapshot != nil {
		t.Error("got a snapshot from a run that had not finished")
	}
}

// Hammer the cache from every direction; the race detector does the assertion.
func TestCacheConcurrentReadersAndRefreshers(t *testing.T) {
	var reads atomic.Int64
	source := &fakeSource{result: func(int) (*Snapshot, error) { return snapshotWithRoot("/repo/.loom"), nil }}
	cache := NewCache(source.run)
	defer cache.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				cache.Refresh()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if cache.Current().Snapshot != nil {
					reads.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if _, err := cache.RefreshNow(context.Background()); err != nil {
		t.Fatalf("cache unusable after the hammering: %v", err)
	}
	if cache.Current().Snapshot == nil {
		t.Fatal("no snapshot published")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// A snapshot somebody else already paid for must not cost a second run.
func TestCacheSeed(t *testing.T) {
	source := &fakeSource{result: func(int) (*Snapshot, error) { return snapshotWithRoot("/fresh/.loom"), nil }}
	cache := NewCache(source.run)
	defer cache.Close()

	cache.Seed(snapshotWithRoot("/seeded/.loom"))

	state := cache.Current()
	if state.Snapshot == nil || state.Snapshot.LoomRoot != "/seeded/.loom" {
		t.Fatalf("got %+v, want the seeded snapshot", state.Snapshot)
	}
	if state.Generation != 1 {
		t.Errorf("generation = %d, want 1", state.Generation)
	}
	if source.runs() != 0 {
		t.Error("seeding ran the source")
	}

	// A seed never displaces a real snapshot.
	if _, err := cache.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	cache.Seed(snapshotWithRoot("/late/.loom"))
	if root := cache.Current().Snapshot.LoomRoot; root != "/fresh/.loom" {
		t.Errorf("loom root = %q, want the refreshed snapshot to stand", root)
	}
}

func TestCacheSeedIgnoresNil(t *testing.T) {
	cache := NewCache(func(context.Context) (*Snapshot, error) { return nil, nil })
	defer cache.Close()

	cache.Seed(nil)
	if state := cache.Current(); state.Snapshot != nil || state.Generation != 0 {
		t.Fatalf("got %+v, want an empty state", state)
	}
}

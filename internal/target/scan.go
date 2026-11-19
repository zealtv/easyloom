package target

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DefaultScanDepth bounds how far below the scan root a loom is looked for.
// A loom sits at a repository root, not buried, and an uncapped walk of a home
// directory is the difference between instant and unusable.
const DefaultScanDepth = 4

// DefaultScanConcurrency bounds the probes a scan runs at once. Each is a map
// subprocess, so a serial scan of a dozen looms is a visible stall and an
// unbounded one is a dozen shells.
const DefaultScanConcurrency = 8

// prunedDirs are never descended into. They are large, they are common, and a
// loom inside one is a copy of somebody else's rather than work on this
// machine.
var prunedDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"vendor":       true,
}

// Found is one loom a scan turned up.
type Found struct {
	Target Target
	// Version is the probe result, and is the zero Version when the scan was
	// asked not to probe.
	Version Version
	// Err is why this loom could not be probed. The loom is still reported:
	// a target that cannot be probed is a thing the user may want to look at,
	// and often the reason they are scanning.
	Err error
}

// ScanOptions tunes a scan. The zero value is the sensible one.
type ScanOptions struct {
	// Depth caps how far below the root directories are descended into. Zero
	// means DefaultScanDepth; negative means the root only.
	Depth int
	// Concurrency bounds simultaneous probes. Zero means
	// DefaultScanConcurrency.
	Concurrency int
	// Prober probes each hit for its tier. Nil skips probing entirely, which
	// is the fast path for a caller that only wants paths.
	//
	// Passing the same Prober the rest of the process uses means a loom probed
	// by a scan is not probed again when it is opened.
	Prober *Prober
}

// Scan walks root looking for deployed looms and reports what it found, in
// path order.
//
// Every hit goes through Resolve, so a scan produces targets with the same
// realpath normalisation as every other entry point and two paths reaching one
// loom collapse to one result.
//
// Unreadable directories are skipped rather than fatal: a scan that dies on
// one permission error found nothing.
func Scan(ctx context.Context, root string, opts ScanOptions) ([]Found, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("scan %q: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("scan %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan %q: not a directory", root)
	}

	depth := opts.Depth
	if depth == 0 {
		depth = DefaultScanDepth
	}

	targets, err := walk(ctx, abs, depth)
	if err != nil {
		return nil, err
	}

	found := make([]Found, len(targets))
	for i, t := range targets {
		found[i] = Found{Target: t}
	}
	probe(ctx, found, opts)
	return found, ctx.Err()
}

// walk collects the looms under root, deduplicated by key and ordered by root
// path so a scan of the same tree twice reads the same.
func walk(ctx context.Context, root string, maxDepth int) ([]Target, error) {
	seen := map[string]Target{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// An unreadable directory is one directory that was not searched, not
		// the end of the search.
		if err != nil {
			return nil //nolint:nilerr // skipping is the whole point
		}
		if !d.IsDir() {
			return nil
		}
		if path != root {
			name := d.Name()
			if prunedDirs[name] {
				return fs.SkipDir
			}
			// A loom's own directory holds no repositories, and its script was
			// already found by looking at the directory above it.
			if name == loomDirName {
				return fs.SkipDir
			}
		}

		if script, ok := scriptIn(filepath.Join(path, loomDirName)); ok {
			if t, err := newTarget(script); err == nil {
				seen[t.Key()] = t
			}
		}

		if depthOf(root, path) >= maxDepth {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}

	targets := make([]Target, 0, len(seen))
	for _, t := range seen {
		targets = append(targets, t)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Root < targets[j].Root })
	return targets, nil
}

// probe fills in tiers, bounded so that a scan of many looms does not fork a
// shell per loom at once.
func probe(ctx context.Context, found []Found, opts ScanOptions) {
	if opts.Prober == nil || len(found) == 0 {
		return
	}
	limit := opts.Concurrency
	if limit <= 0 {
		limit = DefaultScanConcurrency
	}
	if limit > len(found) {
		limit = len(found)
	}

	work := make(chan int)
	var wg sync.WaitGroup
	for range limit {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				found[i].Version, found[i].Err = opts.Prober.Probe(ctx, found[i].Target)
			}
		}()
	}
	for i := range found {
		if ctx.Err() != nil {
			break
		}
		work <- i
	}
	close(work)
	wg.Wait()
}

// depthOf reports how many directories below root path sits. The root is 0.
func depthOf(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

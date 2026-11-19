package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// Tier is how much of EasyLoom a target supports.
type Tier string

const (
	// TierV2 is a format v2 loom whose projection this build understands:
	// everything works.
	TierV2 Tier = "v2"
	// TierV1 is a loom with no usable map command, so the JSON boundary does
	// not exist for it. Read-only, with a migration preview.
	TierV1 Tier = "v1"
	// TierUnsupported is a loom that speaks a map schema this build does not.
	// Refuse rather than guess at fields that may have moved.
	TierUnsupported Tier = "unsupported"
)

// ProbeSchemaVersion is the newest map schema this build's model transcribes;
// it reads back to snapshot.MinSchemaVersion. The probe gates on the schema
// rather than on the format version: they are independent integers, and it is
// the schema that decides whether the parser is right.
const ProbeSchemaVersion = snapshot.SupportedSchemaVersion

// markerName holds the loom's format version. It is a deployment marker, not
// protocol state: for a v1 loom there is no map command and so no projection
// to read a version out of, and its absence is the only positive evidence
// available. Nothing else about the loom is ever read off disk.
const markerName = "format-version"

// Version is one target's probe result.
type Version struct {
	Tier Tier
	// SchemaVersion is the map schema the loom emitted, or 0 if it emitted no
	// projection.
	SchemaVersion int
	// FormatVersion comes from the projection when there is one, and from the
	// format-version marker otherwise. It is 0 when the loom is markerless,
	// which is itself the v1 signal.
	FormatVersion int
	// Snapshot is the projection the probe already paid for. Seed the cache
	// with it rather than immediately taking a second snapshot; it is nil for
	// any tier but v2.
	Snapshot *snapshot.Snapshot
	// Drift is set when the projection parsed but did not match the model
	// exactly. It does not change the tier.
	Drift error
	// Refusal is what the loom said when it declined to produce a projection.
	// It is the thing to show a v1 user.
	Refusal  string
	ProbedAt time.Time
}

// Full reports whether the target supports mutation and the full UI. Every
// other tier is read-only.
func (v Version) Full() bool { return v.Tier == TierV2 }

// UnsupportedError reports a map schema this build cannot read, naming the
// version seen so the mismatch is diagnosable rather than mysterious.
type UnsupportedError struct {
	Script        string
	SchemaVersion int
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("loom %s emits map schema version %d, and this build reads versions %d–%d",
		e.Script, e.SchemaVersion, snapshot.MinSchemaVersion, ProbeSchemaVersion)
}

// MismatchError reports that the loom named a root other than the one that was
// resolved — cheap proof that the script being driven is not the loom the user
// asked for.
type MismatchError struct {
	Script   string
	Resolved string
	Reported string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("loom %s reports root %s, but %s was resolved", e.Script, e.Reported, e.Resolved)
}

// Prober probes targets and remembers the answer.
//
// A probe costs a whole snapshot — seconds on a large loom — so it happens
// once per target and never on a render path.
type Prober struct {
	// Timeout bounds the probe's map run. Zero means the snapshot default.
	Timeout time.Duration

	mu       sync.Mutex
	probed   map[string]Version
	inFlight map[string]chan struct{}
}

// Probe returns the target's tier, running map at most once per target.
//
// Concurrent probes of the same target share one run rather than each paying
// for a snapshot.
func (p *Prober) Probe(ctx context.Context, t Target) (Version, error) {
	for {
		p.mu.Lock()
		if version, ok := p.probed[t.Key()]; ok {
			p.mu.Unlock()
			return version, versionError(t, version)
		}
		if wait, ok := p.inFlight[t.Key()]; ok {
			p.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return Version{}, ctx.Err()
			}
		}
		done := make(chan struct{})
		if p.inFlight == nil {
			p.inFlight = map[string]chan struct{}{}
		}
		p.inFlight[t.Key()] = done
		p.mu.Unlock()

		version, err := p.probe(ctx, t)

		p.mu.Lock()
		// A probe that failed on the caller's context says nothing about the
		// loom, so it is not remembered.
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			if p.probed == nil {
				p.probed = map[string]Version{}
			}
			p.probed[t.Key()] = version
		}
		delete(p.inFlight, t.Key())
		p.mu.Unlock()
		close(done)

		return version, err
	}
}

// Forget drops a remembered tier, so the next Probe runs again. Migrating a v1
// loom is the reason this exists.
func (p *Prober) Forget(t Target) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.probed, t.Key())
}

func (p *Prober) probe(ctx context.Context, t Target) (Version, error) {
	runner := snapshot.Runner{Script: t.Script, Dir: t.Root, Timeout: p.Timeout}
	snap, err := runner.Run(ctx)

	version := Version{ProbedAt: time.Now(), FormatVersion: markerVersion(t)}

	if snap == nil {
		if ctx.Err() != nil {
			return Version{}, err
		}
		return classifyRefusal(t, version, err)
	}

	var drift *snapshot.DriftError
	if errors.As(err, &drift) {
		version.Drift = drift
	}

	version.SchemaVersion = snap.SchemaVersion
	version.FormatVersion = snap.FormatVersion
	if !snapshot.SupportsSchema(snap.SchemaVersion) {
		version.Tier = TierUnsupported
		return version, versionError(t, version)
	}

	// The loom produced a projection, so map exists and works: v2 whatever the
	// marker on disk says.
	version.Tier = TierV2
	version.Snapshot = snap

	if !sameRoot(t.Root, snap.LoomRoot) {
		return version, &MismatchError{Script: t.Script, Resolved: t.Root, Reported: snap.LoomRoot}
	}
	return version, nil
}

// classifyRefusal decides whether a loom that produced no projection is a v1
// loom or simply broken. Calling every failure v1 would silently downgrade a
// healthy loom to read-only, so the refusal has to be recognised.
func classifyRefusal(t Target, version Version, err error) (Version, error) {
	var execErr *snapshot.ExecError
	if !errors.As(err, &execErr) {
		return version, err
	}
	version.Refusal = execErr.Stderr

	// A markerless loom is v1 by definition: the protocol says a deployed
	// markerless loom stays v1 until an operator explicitly migrates it. It
	// was never going to produce a projection.
	if version.FormatVersion == 0 {
		version.Tier = TierV1
		return version, nil
	}

	// Marked v2 but still refusing. A v2 script over an unmigrated loom names
	// the migration; a script old enough to predate map has no such command.
	// Anything else is a broken loom, and calling that v1 would silently
	// downgrade a healthy install to read-only.
	refusal := strings.ToLower(execErr.Stderr)
	switch {
	case strings.Contains(refusal, "migrate-v2"),
		strings.Contains(refusal, "format v2"),
		strings.Contains(refusal, "unknown command"):
		version.Tier = TierV1
		return version, nil
	}
	return version, err
}

// versionError is the error a tier implies, so a cached probe reports the same
// thing the original run did.
func versionError(t Target, v Version) error {
	if v.Tier == TierUnsupported {
		return &UnsupportedError{Script: t.Script, SchemaVersion: v.SchemaVersion}
	}
	return nil
}

// markerVersion reads the format-version marker, returning 0 when the loom is
// markerless or the marker is unreadable. A markerless loom is v1.
func markerVersion(t Target) int {
	data, err := os.ReadFile(filepath.Join(t.Root, markerName))
	if err != nil {
		return 0
	}
	version, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return version
}

// sameRoot compares the resolved root with the one the loom reported. The
// resolved root came through EvalSymlinks and the reported one did not, so
// compare both directly and resolved.
func sameRoot(resolved, reported string) bool {
	if reported == "" {
		return false
	}
	if filepath.Clean(resolved) == filepath.Clean(reported) {
		return true
	}
	real, err := filepath.EvalSymlinks(reported)
	if err != nil {
		return false
	}
	return filepath.Clean(resolved) == filepath.Clean(real)
}

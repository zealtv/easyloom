// Package unblock answers what resolving a stitch would make ready.
//
// Graph arithmetic and nothing else: a projection in, sets of IDs out. No
// rendering, no I/O and no package state — the same split `internal/snapshot`
// and `internal/tui` already keep, and the split `web/unblock.js` is on.
//
// # Why re-derive readiness at all
//
// The projection ships Ready per stitch and it is authoritative. Nothing here
// replaces it. What the loom cannot be asked is a hypothetical — "if this were
// done, what would that let someone start?" — and answering that means running
// the same rule with one input changed.
//
// Which makes agreement the whole test. With nothing simulated, Derive must
// return exactly the set of stitches the loom marked ready, so Disagreements
// exists to be asserted.
//
// # The second implementation
//
// This is a port of web/unblock.js, and two implementations of one idea are
// how two frontends start telling the user different things about what to work
// on next. The rule below is ported from the protocol — .loom/docs/protocol-v2.md,
// "Decomposition and readiness" — rather than transliterated from the JS, so a
// bug in that file is not inherited and then agreed with for the wrong reason.
//
// What holds the two together is testdata: both run over web/testdata/, both
// must equal the projection's own Ready flags, and the full rankings are
// compared against web/testdata/rankings.golden.json. Neither side can move an
// answer without a test going red.
//
// # One step, not transitive
//
// The impact of a stitch is what becomes ready next. A transitive count over a
// chain rewards the head of the longest thread regardless of what that thread
// costs, and cannot be checked by looking at the screen. If a transitive figure
// is ever wanted it is a second number beside this one, not a redefinition of
// it.
package unblock

import (
	"sort"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// Action is what a reader would actually have to do to resolve a stitch. The
// vocabulary is the JS module's, word for word: two vocabularies for one idea
// is how the frontends start disagreeing in front of the user rather than in a
// test.
type Action string

const (
	// ActionTie is ready work; finish it.
	ActionTie Action = "tie"
	// ActionUnblock is a loose end waiting on dependencies, named in BlockedBy.
	ActionUnblock Action = "unblock"
	// ActionDecompose means the children are the work, named in OpenChildren.
	ActionDecompose Action = "decompose"
	// ActionResume is directly waiting; `loom resume <id>` lifts it.
	ActionResume Action = "resume"
	// ActionPromote is waiting by inheritance, so resume refuses it: promote it
	// to its own thread, or resume ResumeVia.
	ActionPromote Action = "promote"
	// ActionCycle is in a dependency cycle, and no amount of working the loom
	// makes it or anything behind it ready.
	ActionCycle Action = "cycle"
	// ActionInHand is active, unblocked, childless and still not ready: claimed
	// or tended, which is someone already doing it.
	ActionInHand Action = "in-hand"
)

// terminalStates resolve a decomposition edge. The protocol says a direct child
// resolves when its direct state is .tied or .dropped; abandoned is the third
// way a stitch stops being work anyone can do.
var terminalStates = map[snapshot.State]bool{
	snapshot.StateTied:      true,
	snapshot.StateDropped:   true,
	snapshot.StateAbandoned: true,
}

// Hypothesis is a change to the loom that has not happened. Resolved is the
// IDs to treat as tied; Resumed is the IDs whose waiting — their own or
// inherited — is lifted. The zero value simulates nothing, which is what makes
// Derive re-derive the present.
type Hypothesis struct {
	Resolved map[string]bool
	Resumed  map[string]bool
}

// Blocker is one dependency a stitch is waiting on.
type Blocker struct {
	ID     string                    `json:"id"`
	Status snapshot.DependencyStatus `json:"status"`
	// Reason is nil unless Status is broken.
	Reason *snapshot.BrokenReason `json:"reason"`
}

// Entry is what resolving one stitch would release, and what resolving it
// would take.
//
// Releases is the set — named, not counted. "3" is a leaderboard; "3 —
// live-updates, mutation-refresh, queue-reorder-ui" is an answer, and it is
// what makes the number checkable by looking at the loom.
//
// The JSON tags are the JS module's field names, so the golden file is one
// document both implementations read without a translation layer in the middle.
type Entry struct {
	ID           string    `json:"id"`
	Releases     []string  `json:"releases"`
	BlockedBy    []Blocker `json:"blockedBy"`
	OpenChildren []string  `json:"openChildren"`
	// ResumeVia is the ancestor actually holding a parked stitch, or nil.
	ResumeVia *string `json:"resumeVia"`
	Parked    bool    `json:"parked"`
	Ready     bool    `json:"ready"`
	Action    Action  `json:"action"`
}

// Ranking is every stitch worth asking about, in two groups.
//
// They are two different questions and their answers are not comparable, so
// they are never merged into one leaderboard. LooseEnds is what to work on now;
// Parked is what to un-park.
type Ranking struct {
	LooseEnds []Entry `json:"looseEnds"`
	Parked    []Entry `json:"parked"`
}

// Disagreement is one stitch where this package's readiness differs from the
// projection's own flag.
type Disagreement struct {
	ID         string `json:"id"`
	Projection bool   `json:"projection"`
	Derived    bool   `json:"derived"`
}

// context is the lookups every derivation needs.
type context struct {
	byID    map[string]*snapshot.Stitch
	inCycle map[string]bool
}

func newContext(snap *snapshot.Snapshot) context {
	ctx := context{byID: map[string]*snapshot.Stitch{}, inCycle: map[string]bool{}}
	if snap == nil {
		return ctx
	}
	ctx.byID = snap.Index()
	for _, members := range snap.Cycles {
		for _, id := range members {
			ctx.inCycle[id] = true
		}
	}
	return ctx
}

// terminal reports a stitch that no longer holds its parent up.
func terminal(st *snapshot.Stitch, resolved map[string]bool) bool {
	if st == nil {
		return false
	}
	return resolved[st.ID] || terminalStates[st.State]
}

// ready applies the protocol's readiness rule to one stitch, under a hypothesis
// about what has been resolved and what has been un-parked.
func ready(st *snapshot.Stitch, ctx context, h Hypothesis) bool {
	// A stitch that has been resolved is not ready work; it is done work.
	if h.Resolved[st.ID] {
		return false
	}

	// 1. under threads/, with no terminal or dropped ancestor. A terminal
	//    ancestor takes its whole subtree into an archive tray with it, so the
	//    tray is the answer to both halves.
	if st.Tray != snapshot.TrayThreads {
		return false
	}

	// 2 and 3. not directly waiting, no waiting ancestor, not claimed or tended.
	state := st.State
	if state == snapshot.StateWaiting && h.Resumed[st.ID] {
		state = snapshot.StatePlain
	}
	if state != snapshot.StatePlain {
		return false
	}
	if st.WaitingInherited && !h.Resumed[st.ID] {
		return false
	}

	// 6. cycle members are permanently un-ready, and resolving one member does
	//    not change that — the edge is still there.
	if ctx.inCycle[st.ID] {
		return false
	}

	// 4. every immediate child is terminal.
	for _, child := range st.Children {
		record, ok := ctx.byID[child]
		// 7. a child the projection does not carry is a structurally invalid
		//    record, which is not ready and not something to guess about.
		if !ok || !terminal(record, h.Resolved) {
			return false
		}
	}

	// 5. every hard dependency satisfied. A blocked edge onto something the
	//    hypothesis resolves becomes satisfied; a broken one does not, because
	//    what is broken about it is the record, not the target's state.
	for _, edge := range st.Dependencies {
		if edge.Status == snapshot.DependencySatisfied || edge.Status == snapshot.DependencyInert {
			continue
		}
		if edge.Status == snapshot.DependencyBlocked && h.Resolved[edge.To] {
			continue
		}
		return false
	}
	return true
}

// Derive returns the set of stitch IDs that are ready under a hypothesis.
//
// With the zero Hypothesis it re-derives the present, and must agree with the
// projection.
func Derive(snap *snapshot.Snapshot, hypothetical Hypothesis) map[string]bool {
	return derive(snap, newContext(snap), hypothetical)
}

func derive(snap *snapshot.Snapshot, ctx context, h Hypothesis) map[string]bool {
	out := map[string]bool{}
	if snap == nil {
		return out
	}
	for i := range snap.Stitches {
		st := &snap.Stitches[i]
		if ready(st, ctx, h) {
			out[st.ID] = true
		}
	}
	return out
}

// Disagreements lists every stitch where this package's readiness differs from
// the projection's own flag.
//
// It must always be empty. The projection is authoritative and this is a
// re-derivation of it; anything here is a bug in this file, not in the loom.
func Disagreements(snap *snapshot.Snapshot) []Disagreement {
	derived := Derive(snap, Hypothesis{})
	out := []Disagreement{}
	if snap == nil {
		return out
	}
	for i := range snap.Stitches {
		st := &snap.Stitches[i]
		if derived[st.ID] != st.Ready {
			out = append(out, Disagreement{ID: st.ID, Projection: st.Ready, Derived: derived[st.ID]})
		}
	}
	return out
}

// parkedAncestor is the nearest ancestor holding a stitch waiting.
//
// Waiting is inherited and `resume` lifts only a stitch's own suffix, so
// `resume <child>` fails with "not directly waiting" for a child that was never
// individually parked. Naming the ancestor is what makes the difference
// actionable: this repo has already been bitten by it.
func parkedAncestor(st *snapshot.Stitch, byID map[string]*snapshot.Stitch) *string {
	at := parent(st, byID)
	for at != nil {
		if at.State == snapshot.StateWaiting {
			id := at.ID
			return &id
		}
		at = parent(at, byID)
	}
	return nil
}

func parent(st *snapshot.Stitch, byID map[string]*snapshot.Stitch) *snapshot.Stitch {
	if st.ParentID == nil {
		return nil
	}
	return byID[*st.ParentID]
}

// unparkedBy is the stitch itself plus every descendant whose parking it is
// holding.
//
// A descendant with a `.waiting` suffix of its own stays parked when its parent
// is resumed, and so does everything under it — that is what makes resuming a
// parent a different act from resuming a subtree.
func unparkedBy(st *snapshot.Stitch, byID map[string]*snapshot.Stitch) map[string]bool {
	out := map[string]bool{st.ID: true}
	walk := []*snapshot.Stitch{st}
	for len(walk) > 0 {
		at := walk[len(walk)-1]
		walk = walk[:len(walk)-1]
		for _, id := range at.Children {
			child, ok := byID[id]
			if !ok || child.State == snapshot.StateWaiting {
				continue
			}
			out[id] = true
			walk = append(walk, child)
		}
	}
	return out
}

// unresolvedChildren lists the children still holding a stitch open.
func unresolvedChildren(st *snapshot.Stitch, byID map[string]*snapshot.Stitch) []string {
	out := []string{}
	for _, id := range st.Children {
		if !terminal(byID[id], nil) {
			out = append(out, id)
		}
	}
	return out
}

// blockers lists the dependency targets a stitch is waiting on.
func blockers(st *snapshot.Stitch) []Blocker {
	out := []Blocker{}
	for _, edge := range st.Dependencies {
		if edge.Status == snapshot.DependencySatisfied || edge.Status == snapshot.DependencyInert {
			continue
		}
		out = append(out, Blocker{ID: edge.To, Status: edge.Status, Reason: edge.Reason})
	}
	return out
}

// hypothesisFor is what "resolving this stitch" means for this stitch.
//
// A leaf is tied. A stitch with unresolved children cannot be — resolving it
// means resolving its subtree, which is not one step — so for one the act is
// the resume, and what that releases is the subtree becoming reachable.
func hypothesisFor(st *snapshot.Stitch, byID map[string]*snapshot.Stitch) (Hypothesis, []string) {
	h := Hypothesis{Resolved: map[string]bool{}, Resumed: map[string]bool{}}
	if st.State == snapshot.StateWaiting || st.WaitingInherited {
		h.Resumed = unparkedBy(st, byID)
	}
	open := unresolvedChildren(st, byID)
	if len(open) == 0 {
		h.Resolved[st.ID] = true
	}
	return h, open
}

// Impact is what resolving one stitch would release. It is nil for an ID the
// projection does not carry, rather than a guess.
func Impact(snap *snapshot.Snapshot, id string) *Entry {
	ctx := newContext(snap)
	return impact(snap, ctx, id, derive(snap, ctx, Hypothesis{}))
}

func impact(snap *snapshot.Snapshot, ctx context, id string, was map[string]bool) *Entry {
	st, ok := ctx.byID[id]
	if !ok {
		return nil
	}

	h, open := hypothesisFor(st, ctx.byID)

	// A cycle member is permanently un-ready and so is everything behind it, so
	// there is no hypothesis to run: the answer is nothing, and saying why is
	// more use than saying zero.
	inCycle := ctx.inCycle[id]
	now := was
	if !inCycle {
		now = derive(snap, ctx, h)
	}

	releases := []string{}
	for other := range now {
		// The id guard mirrors the JS and is unreachable in both: a stitch with
		// no open children is resolved by its own hypothesis and so is never
		// ready under it, and one with open children cannot become ready while
		// they stay open. It is kept because the two files are read side by
		// side, and a line that is in one and not the other reads as a
		// difference in the rule.
		if other != id && !was[other] {
			releases = append(releases, other)
		}
	}
	sort.Strings(releases)

	blockedBy := blockers(st)
	return &Entry{
		ID:           id,
		Releases:     releases,
		BlockedBy:    blockedBy,
		OpenChildren: open,
		ResumeVia:    parkedAncestor(st, ctx.byID),
		Parked:       st.State == snapshot.StateWaiting || st.WaitingInherited,
		Ready:        was[id],
		Action:       actionFor(st, inCycle, was[id], open, blockedBy),
	}
}

func actionFor(st *snapshot.Stitch, inCycle, isReady bool, open []string, blockedBy []Blocker) Action {
	switch {
	case inCycle:
		return ActionCycle
	case st.WaitingInherited:
		return ActionPromote
	case st.State == snapshot.StateWaiting:
		return ActionResume
	case isReady:
		return ActionTie
	case len(open) > 0:
		return ActionDecompose
	case len(blockedBy) > 0:
		return ActionUnblock
	default:
		return ActionInHand
	}
}

// Rank scores every stitch worth asking about, in two groups. Both are sorted
// by what they release, then by ID, so the order is stable across snapshots.
func Rank(snap *snapshot.Snapshot) Ranking {
	ctx := newContext(snap)
	before := derive(snap, ctx, Hypothesis{})
	out := Ranking{LooseEnds: []Entry{}, Parked: []Entry{}}
	if snap == nil {
		return out
	}

	for i := range snap.Stitches {
		st := &snap.Stitches[i]
		if st.Tray != snapshot.TrayThreads || terminalStates[st.State] {
			continue
		}
		scored := impact(snap, ctx, st.ID, before)
		if scored == nil {
			continue
		}
		if scored.Parked {
			out.Parked = append(out.Parked, *scored)
		} else {
			out.LooseEnds = append(out.LooseEnds, *scored)
		}
	}

	sort.SliceStable(out.LooseEnds, byImpact(out.LooseEnds))
	sort.SliceStable(out.Parked, byImpact(out.Parked))
	return out
}

func byImpact(entries []Entry) func(i, j int) bool {
	return func(i, j int) bool {
		a, b := entries[i], entries[j]
		if len(a.Releases) != len(b.Releases) {
			return len(a.Releases) > len(b.Releases)
		}
		// A ready stitch outranks a blocked one with the same impact: it is the
		// one that can actually be started.
		if a.Ready != b.Ready {
			return a.Ready
		}
		return a.ID < b.ID
	}
}

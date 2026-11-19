package unblock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// The fixtures are web/testdata/: real `map --json` output from this repo's own
// loom — one as it stands, one from earlier, before a slice of work was done — plus one
// synthetic cyclic loom, because this repo's loom does not have a cycle in it
// and should not acquire one to be tested against.
//
// They are not copied here. One copy means one set of inputs both
// implementations are held to, and a fixture added for either side is
// immediately a fixture for both.
const fixtures = "../../web/testdata"

const goldenFile = "rankings.golden.json"

func fixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".golden.json") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no fixtures found")
	}
	return names
}

func load(t *testing.T, name string) *snapshot.Snapshot {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	snap, err := snapshot.Decode(data)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return snap
}

// The first of the two tests that are the point of this package. The projection
// is authoritative; this package re-derives its rule only to ask a hypothetical
// the loom cannot be asked, and a re-derivation that disagrees about the
// present is wrong about the future. It is the same assertion disagreements()
// makes in JS.
func TestReadinessAgreesWithTheProjection(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			snap := load(t, name)
			if got := Disagreements(snap); len(got) > 0 {
				t.Errorf("readiness disagrees with the projection: %+v", got)
			}

			ready := Derive(snap, Hypothesis{})
			derived := make([]string, 0, len(ready))
			for id := range ready {
				derived = append(derived, id)
			}
			sort.Strings(derived)
			want := make([]string, 0, len(snap.Frontier))
			want = append(want, snap.Frontier...)
			sort.Strings(want)
			if !reflect.DeepEqual(derived, want) {
				t.Errorf("derived ready set %v, projection frontier %v", derived, want)
			}
		})
	}
}

// The second, and the whole risk of this thread: the terminal and the browser
// must not be able to tell the user different things about what to work on
// next. The golden file is generated from web/unblock.js by `make golden` and
// asserted current by its own node test, so a drift on either side lands here.
func TestRankingsMatchTheJavaScript(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtures, goldenFile))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]Ranking
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("decode golden: %v", err)
	}

	names := fixtureNames(t)
	if len(golden) != len(names) {
		t.Errorf("golden covers %d fixtures, there are %d — run `make golden`", len(golden), len(names))
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			want, ok := golden[name]
			if !ok {
				t.Fatalf("%s is not in the golden file — run `make golden`", name)
			}
			got := Rank(load(t, name))
			compare(t, "looseEnds", got.LooseEnds, want.LooseEnds)
			compare(t, "parked", got.Parked, want.Parked)
		})
	}
}

// compare reports the entries that moved rather than the whole ranking. A
// diff of two forty-entry lists says nothing; "queue-mutations: releases
// [a b] want [a b c]" says what changed.
func compare(t *testing.T, group string, got, want []Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d entries, want %d (%v vs %v)", group, len(got), len(want), ids(got), ids(want))
		return
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("%s[%d]:\n go %+v\n js %+v", group, i, got[i], want[i])
		}
	}
}

func ids(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.ID
	}
	return out
}

// Beyond the two agreements, the cases the JS names in prose — so a reader of
// this package sees the rules stated, and so a fixture regenerated into
// agreement with a broken port still fails on what the rules say.

func TestImpactIsOneStepAndNotTransitive(t *testing.T) {
	snap := load(t, "easyloom-bite7.json")
	// web-shell is live-updates' parent and its last open child, so it is one
	// step. queue-mutations is two steps behind and must not be counted.
	got := Impact(snap, "live-updates")
	if !reflect.DeepEqual(got.Releases, []string{"web-shell"}) {
		t.Errorf("releases %v, want [web-shell]", got.Releases)
	}
}

func TestResolvingTheLastChildMakesTheParentReady(t *testing.T) {
	snap := load(t, "easyloom-bite7.json")
	got := Impact(snap, "snapshot-refresh")
	want := []string{"live-updates", "snapshot-model"}
	if !reflect.DeepEqual(got.Releases, want) {
		t.Errorf("releases %v, want %v", got.Releases, want)
	}
	if got.Action != ActionTie || !got.Ready {
		t.Errorf("action %q ready %v, want tie/true", got.Action, got.Ready)
	}
}

func TestALooseEndThatUnblocksNoWorkSaysSo(t *testing.T) {
	// Nothing needs loom-commands and its parent is a goal root with nothing
	// above it, so finishing it releases nothing at all. That is a real answer
	// and the view has to be able to say it.
	got := Impact(load(t, "easyloom-bite7.json"), "loom-commands")
	if len(got.Releases) != 0 {
		t.Errorf("releases %v, want none", got.Releases)
	}
	if got.Action != ActionTie {
		t.Errorf("action %q, want tie", got.Action)
	}
}

func TestResumingAParkedParentReleasesItsSubtree(t *testing.T) {
	snap := load(t, "easyloom-bite7.json")
	got := Impact(snap, "feature-backlog")
	if got.Action != ActionResume {
		t.Fatalf("action %q, want resume", got.Action)
	}
	if len(got.Releases) == 0 {
		t.Fatal("feature-backlog is parked over live stitches")
	}
	index := snap.Index()
	for _, id := range got.Releases {
		if !index[id].WaitingInherited {
			t.Errorf("%s was not parked by inheritance", id)
		}
	}
}

// Waiting is inherited and `resume` lifts only a stitch's own suffix, so a
// child that was never individually parked cannot be resumed. Naming the
// ancestor is what makes the difference actionable.
func TestAStitchParkedByInheritanceSaysPromoteAndNamesTheParent(t *testing.T) {
	snap := load(t, "easyloom-bite7.json")
	var child *snapshot.Stitch
	for i := range snap.Stitches {
		if st := &snap.Stitches[i]; st.WaitingInherited && st.State != snapshot.StateWaiting {
			child = st
			break
		}
	}
	if child == nil {
		t.Fatal("the fixture has no stitch parked by inheritance")
	}

	got := Impact(snap, child.ID)
	if got.Action != ActionPromote || !got.Parked {
		t.Errorf("action %q parked %v, want promote/true", got.Action, got.Parked)
	}
	if got.ResumeVia == nil {
		t.Fatal("the ancestor holding it parked is not named")
	}
	if state := snap.Index()[*got.ResumeVia].State; state != snapshot.StateWaiting {
		t.Errorf("resumeVia %s is %q, want waiting", *got.ResumeVia, state)
	}
}

// A thread root is not a piece of work; its children are. Resolving it is
// resolving the subtree, which is not one step.
func TestAStitchWithUnresolvedChildrenReportsThem(t *testing.T) {
	got := Impact(load(t, "easyloom-bite7.json"), "queue-control")
	if got.Action != ActionDecompose || len(got.OpenChildren) == 0 {
		t.Errorf("action %q open %v, want decompose with children", got.Action, got.OpenChildren)
	}
}

// A cycle is a hard stall: every member is permanently un-ready, and resolving
// one does not remove the edge that makes it so.
func TestACycleMemberReleasesNothing(t *testing.T) {
	got := Impact(load(t, "cyclic.json"), "a")
	if got.Action != ActionCycle {
		t.Errorf("action %q, want cycle", got.Action)
	}
	if len(got.Releases) != 0 {
		t.Errorf("releases %v, want none", got.Releases)
	}
}

func TestAnUnknownStitchIsNilRatherThanAGuess(t *testing.T) {
	if got := Impact(load(t, "easyloom.json"), "no-such-stitch"); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestNothingArchivedOrTerminalIsRanked(t *testing.T) {
	snap := load(t, "easyloom.json")
	index := snap.Index()
	ranked := Rank(snap)
	for _, entry := range append(append([]Entry{}, ranked.LooseEnds...), ranked.Parked...) {
		st := index[entry.ID]
		if st.Tray != snapshot.TrayThreads || terminalStates[st.State] {
			t.Errorf("%s is %s/%s and should not be ranked", entry.ID, st.Tray, st.State)
		}
	}
}

// The groups answer different questions and their answers are not comparable,
// so they are never merged into one leaderboard.
func TestTheGroupsAreDisjointAndDescendByImpact(t *testing.T) {
	ranked := Rank(load(t, "easyloom-bite7.json"))
	for _, group := range [][]Entry{ranked.LooseEnds, ranked.Parked} {
		for i := 1; i < len(group); i++ {
			if len(group[i-1].Releases) < len(group[i].Releases) {
				t.Errorf("%s releases fewer than %s but ranks above it", group[i-1].ID, group[i].ID)
			}
		}
	}
	ends := map[string]bool{}
	for _, entry := range ranked.LooseEnds {
		ends[entry.ID] = true
	}
	for _, entry := range ranked.Parked {
		if ends[entry.ID] {
			t.Errorf("%s is in both groups", entry.ID)
		}
	}
	if ranked.LooseEnds[0].ID != "queue-mutations" {
		t.Errorf("the biggest loose end is %s, want queue-mutations", ranked.LooseEnds[0].ID)
	}
}

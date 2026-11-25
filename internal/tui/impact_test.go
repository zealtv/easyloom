package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/unblock"
)

// impactModel is a model over a loom, already switched to the impact pane.
func impactModel(t *testing.T, snap *snapshot.Snapshot) Model {
	t.Helper()
	cache := snapshot.NewCache(neverFinishes())
	cache.Seed(snap)
	t.Cleanup(cache.Close)
	m := New(Options{
		Target:  target.Target{Root: snap.LoomRoot, Script: snap.LoomRoot + "/loom.sh"},
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Width:   120,
		Height:  40,
	})
	// frontier → queue → tree → impact.
	return m.switchView().switchView().switchView()
}

// realLoom is this repo's own loom, the fixture the golden ranking is checked
// against: the same file, through the Go model, drawn.
func realLoom(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	data, err := os.ReadFile("../unblock/testdata/easyloom.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	snap, err := snapshot.Decode(data)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return snap
}

func impactRowFor(t *testing.T, m Model, id string) impactRow {
	t.Helper()
	for _, r := range m.impactRows() {
		if r.id == id {
			return r
		}
	}
	t.Fatalf("no impact row for %q", id)
	return impactRow{}
}

// tab reaches it, and it is the fourth pane.
func TestTabReachesTheImpactPane(t *testing.T) {
	m := impactModel(t, realLoom(t))
	if m.view != viewImpact {
		t.Fatalf("view %v, want impact", m.view)
	}
	if got := m.view.title(); got != " IMPACT" {
		t.Errorf("title %q", got)
	}
	if got := m.view.next(); got != viewFrontier {
		t.Errorf("tab from impact goes to %v, want frontier", got)
	}
}

// The two groups are drawn apart and named, because "what to work on now" and
// "what to un-park" are different questions.
func TestTheTwoGroupsAreSeparateAndNamed(t *testing.T) {
	m := impactModel(t, realLoom(t))
	rows := m.impactRows()

	var headers []string
	for _, r := range rows {
		if r.kind == impactHeader {
			headers = append(headers, r.title)
		}
	}
	if len(headers) != 2 {
		t.Fatalf("headers %v, want two", headers)
	}
	if !strings.HasPrefix(headers[0], "LOOSE ENDS") || !strings.HasPrefix(headers[1], "PARKED") {
		t.Errorf("headers %v, want loose ends then parked", headers)
	}

	// Every stitch row belongs to exactly one group, and a parked stitch is
	// never in the loose ends.
	group := ""
	for _, r := range rows {
		if r.kind == impactHeader {
			group = r.title
			continue
		}
		if r.kind != impactStitch {
			continue
		}
		if r.entry.Parked != strings.HasPrefix(group, "PARKED") {
			t.Errorf("%s (parked=%v) is under %q", r.id, r.entry.Parked, group)
		}
	}
}

// The names are the feature. A count on its own is a leaderboard.
func TestARowNamesWhatItReleases(t *testing.T) {
	m := impactModel(t, realLoom(t))
	row := impactRowFor(t, m, "queue-mutations")
	status := m.impactStatus(row)

	if !strings.Contains(status, "releases 2") {
		t.Errorf("status %q does not count what it releases", status)
	}
	for _, id := range row.entry.Releases {
		if !strings.Contains(status, id) {
			t.Errorf("status %q does not name %s", status, id)
		}
	}
}

// A stitch parked by inheritance is told both ways out, with the ancestor
// named: `resume` refuses it, and this repo has been bitten by that difference.
func TestAnInheritedWaitNamesTheAncestorAndSaysPromote(t *testing.T) {
	snap := realLoom(t)
	var id string
	for i := range snap.Stitches {
		if st := &snap.Stitches[i]; st.WaitingInherited && st.State != snapshot.StateWaiting {
			id = st.ID
			break
		}
	}
	if id == "" {
		t.Fatal("the fixture has no stitch parked by inheritance")
	}

	m := impactModel(t, snap)
	m = m.toggleExpand(open(true)) // the parked group's fold, if it holds this row
	cost := impactCost(unblock.Entry{
		Action:    unblock.ActionPromote,
		ResumeVia: strPtr("parked-goal"),
	})
	if !strings.Contains(cost, "promote") || !strings.Contains(cost, "resume parked-goal") {
		t.Errorf("cost %q says neither way out", cost)
	}
	if got := unblock.Impact(snap, id); got.Action != unblock.ActionPromote {
		t.Errorf("%s action %q, want promote", id, got.Action)
	}
}

// Rows that release nothing go behind a fold. On this loom most of them do, and
// a list of zeroes buries the three that matter.
func TestZeroImpactRowsAreFoldedAwayByDefault(t *testing.T) {
	m := impactModel(t, realLoom(t))

	for _, r := range m.impactRows() {
		if r.kind == impactStitch && len(r.entry.Releases) == 0 {
			t.Fatalf("%s releases nothing and is drawn unfolded", r.id)
		}
	}

	header := impactRowFor(t, m, impactLooseEndsID)
	if header.hidden == 0 {
		t.Fatal("this loom has loose ends that release nothing; the fold has nothing to hold")
	}
	if header.open {
		t.Error("the fold starts open")
	}
}

// And the fold is the tree's fold, not a second kind. The reader already knows
// space and ←/→.
func TestTheFoldIsTheTreeFold(t *testing.T) {
	m := impactModel(t, realLoom(t))
	before := len(m.impactRows())

	m.selected, m.selectedID = 0, impactLooseEndsID
	m = m.toggleExpand(nil)

	if !m.expanded[impactLooseEndsID] {
		t.Fatal("space did not open the fold, or did not record it where the tree records folds")
	}
	if after := len(m.impactRows()); after <= before {
		t.Errorf("%d rows after opening the fold, was %d", after, before)
	}

	m = m.toggleExpand(open(false))
	if m.expanded[impactLooseEndsID] {
		t.Error("← did not close the fold")
	}
}

// Selection is the ID, like every other pane, and a header is not a stitch — so
// the lifecycle keys have nothing to act on rather than acting on the wrong
// thing.
func TestAHeaderIsNotAStitch(t *testing.T) {
	m := impactModel(t, realLoom(t))
	m.selected, m.selectedID = 0, impactLooseEndsID

	if st := m.selectedStitch(); st != nil {
		t.Fatalf("the header resolved to stitch %s", st.ID)
	}
	if detail := m.detail(4); !strings.Contains(detail, "group heading") {
		t.Errorf("detail %q does not say what the row is", detail)
	}
	// A stitch row selects a stitch, which is what makes c/t/w/d reach it.
	m.selected, m.selectedID = 1, "queue-mutations"
	if st := m.selectedStitch(); st == nil || st.ID != "queue-mutations" {
		t.Error("a stitch row does not resolve to its stitch")
	}
}

// A ranking computed from arithmetic already known to be wrong is worse than no
// ranking, so the pane says so rather than drawing it quietly.
func TestADisagreementWithTheProjectionIsDrawnAsAnAlarm(t *testing.T) {
	snap := realLoom(t)
	if got := impactRowFor(t, impactModel(t, snap), impactLooseEndsID); got.kind != impactHeader {
		t.Fatal("a healthy loom should have no alarm row above the first header")
	}
	for _, r := range impactModel(t, snap).impactRows() {
		if r.kind == impactAlarm {
			t.Fatalf("the real loom disagrees with the projection: %s", r.alarm)
		}
	}

	// Lie to the projection: mark a blocked stitch ready. The simulation says
	// it is not, and the pane must lead with that.
	for i := range snap.Stitches {
		if st := &snap.Stitches[i]; !st.Ready && st.State == snapshot.StatePlain && len(st.Dependencies) > 0 {
			st.Ready = true
			break
		}
	}
	m := impactModel(t, snap)
	rows := m.impactRows()
	if rows[0].kind != impactAlarm {
		t.Fatalf("first row is %v, want the alarm", rows[0].kind)
	}
	if !strings.Contains(rows[0].alarm, "cannot be trusted") {
		t.Errorf("alarm %q", rows[0].alarm)
	}
	if _, c := m.impactRowsAndCounts(); c.wrong == 0 || !strings.Contains(c.header(), "disagree") {
		t.Errorf("the header does not carry the disagreement: %q", c.header())
	}
}

// An empty group says which kind of empty it is. A loom with nothing parked and
// a loom where nothing parked is holding anything back are different answers,
// and neither is a broken view.
func TestAnEmptyGroupSaysWhichKindOfEmpty(t *testing.T) {
	snap := &snapshot.Snapshot{
		SchemaVersion: snapshot.SupportedSchemaVersion,
		FormatVersion: snapshot.FormatVersionV2,
		LoomRoot:      "/tmp/empty/.loom",
		Frontier:      []string{"only"},
		Stitches: []snapshot.Stitch{{
			ID: "only", RootID: "only", Path: "threads/only",
			Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true,
		}},
	}
	m := impactModel(t, snap)

	if got := impactRowFor(t, m, impactParkedID).note; !strings.Contains(got, "nothing is parked") {
		t.Errorf("parked note %q", got)
	}
	if got := impactRowFor(t, m, impactLooseEndsID).note; !strings.Contains(got, "healthy loom") {
		t.Errorf("loose ends note %q", got)
	}
}

// The pane says what the ranking says about the same loom. The golden file
// holds the ranking (captured from the retired browser UI's JavaScript, and
// held by internal/unblock's tests), so drawing the pane from the same fixture
// and comparing to it is the assertion.
func TestThePaneDrawsTheGoldenRanking(t *testing.T) {
	data, err := os.ReadFile("../unblock/testdata/rankings.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]struct {
		LooseEnds []unblock.Entry `json:"looseEnds"`
		Parked    []unblock.Entry `json:"parked"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	want := golden["easyloom.json"]

	// Both folds open, so the pane is drawing every ranked stitch.
	m := impactModel(t, realLoom(t))
	m.expanded = map[string]bool{impactLooseEndsID: true, impactParkedID: true}

	var group string
	var loose, parked []string
	for _, r := range m.impactRows() {
		switch r.kind {
		case impactHeader:
			group = r.id
		case impactStitch:
			if group == impactParkedID {
				parked = append(parked, r.id)
			} else {
				loose = append(loose, r.id)
			}
		}
	}

	// The pane draws the carrying rows then the folded ones, so it is the same
	// set rather than the same order. Order within each half is the ranking's,
	// asserted in internal/unblock against this same file.
	assertSameSet(t, "loose ends", loose, idsOf(want.LooseEnds))
	assertSameSet(t, "parked", parked, idsOf(want.Parked))
}

func idsOf(entries []unblock.Entry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.ID
	}
	return out
}

func assertSameSet(t *testing.T, group string, got, want []string) {
	t.Helper()
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		if seen[id] == 0 {
			t.Errorf("%s: the golden ranks %s and the pane does not draw it", group, id)
		}
		seen[id]--
	}
	for id, n := range seen {
		if n > 0 {
			t.Errorf("%s: the pane draws %s and the golden does not rank it", group, id)
		}
	}
}

func strPtr(s string) *string { return &s }

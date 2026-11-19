package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zealtv/easyloom/internal/loom"
	"github.com/zealtv/easyloom/internal/snapshot"
)

// The write half of the frontier: the keys that turn it from a report into a
// tool. Every one of them goes through internal/loom. This file builds no argv
// and never touches .loom.

// mutation is one command in flight.
type mutation struct {
	command loom.Command
	id      string
	// anchor is the second stitch before and after move against. It is empty
	// for every other command.
	anchor string
	// reason is the words a drop is recorded with.
	reason string
	// gerund is what to say while it runs: "claiming http-server".
	gerund string
	// announce is what to say once it has happened. A mutation nobody meant
	// must not be silent, so every queue move carries one.
	announce string
	// inverse is the move that puts this one back, recorded before it runs and
	// kept only if the loom accepts it.
	inverse *mutation
	// steps, when there are any, are the queue commands this mutation is made
	// of, run one after another and stopping at the first refusal. command and
	// id then name the whole: queue, and the thread it was asked of.
	steps []mutation
	// told is what a batch says it did, since its steps would say too much one
	// at a time.
	told string
}

// parts is the commands a mutation runs: its steps, or itself.
func (mut mutation) parts() []mutation {
	if len(mut.steps) > 0 {
		return mut.steps
	}
	return []mutation{mut}
}

// mutationMsg is a finished mutation coming back to the update loop.
type mutationMsg struct {
	mutation
	result *loom.Result
	err    error
	// done is how many steps of a batch the loom accepted before err.
	done int
}

// overlay is the optimistic view of a mutation that has not been confirmed by
// a snapshot yet. It exists so the list responds on the keystroke rather than
// seconds later, and it is display state with a lifetime of one snapshot
// generation — never a second model of the loom. The moment a newer projection
// lands the overlay is discarded whole, whatever it thought was true.
type overlay struct {
	// generation is the snapshot generation this was applied over.
	generation uint64
	// gone are stitches a mutation took off the frontier: claimed, tied,
	// waited, dropped.
	gone map[string]bool
	// evicted are the stitches a mutation also took out of the queue. Only tie
	// and drop do that — the protocol removes every ID they make inactive —
	// and a claim or a park leaves the record exactly where it was.
	evicted map[string]bool
	// queued is the new queue position per stitch, with nil for unqueued.
	queued map[string]*int
}

func (o overlay) empty() bool {
	return len(o.gone) == 0 && len(o.queued) == 0 && len(o.evicted) == 0
}

// clone copies the overlay so a mutation that turns out to fail can be rolled
// back to exactly what was on screen before it.
func (o overlay) clone() overlay {
	next := overlay{
		generation: o.generation,
		gone:       map[string]bool{},
		evicted:    map[string]bool{},
		queued:     map[string]*int{},
	}
	for id := range o.gone {
		next.gone[id] = true
	}
	for id := range o.evicted {
		next.evicted[id] = true
	}
	for id, position := range o.queued {
		next.queued[id] = position
	}
	return next
}

func (o *overlay) remove(generation uint64, id string) {
	o.start(generation)
	o.gone[id] = true
}

// evict is remove for the two commands that also take the stitch out of the
// queue.
func (o *overlay) evict(generation uint64, id string) {
	o.remove(generation, id)
	o.evicted[id] = true
}

func (o *overlay) setQueue(generation uint64, id string, position *int) {
	o.start(generation)
	o.queued[id] = position
}

func (o *overlay) start(generation uint64) {
	if o.gone == nil {
		o.gone = map[string]bool{}
	}
	if o.evicted == nil {
		o.evicted = map[string]bool{}
	}
	if o.queued == nil {
		o.queued = map[string]*int{}
	}
	o.generation = generation
}

// apply rewrites the frontier as the pending mutations would leave it: gone
// stitches drop out, and the queue order follows the new positions. Queued
// stitches come first in queue order and unqueued ones keep their relative
// order behind them, which is the order the projection itself gives.
func (o overlay) apply(rows []row, c counts) ([]row, counts) {
	if o.empty() {
		return rows, c
	}

	out := make([]row, 0, len(rows))
	for _, r := range rows {
		if o.gone[r.id] {
			if c.ready > 0 {
				c.ready--
			}
			continue
		}
		if position, ok := o.queued[r.id]; ok {
			r.position = 0
			if position != nil {
				r.position = *position
			}
		}
		out = append(out, r)
	}

	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i].position, out[j].position
		if (left == 0) != (right == 0) {
			return right == 0
		}
		return left < right
	})
	return out, c
}

// prompt is the reason a drop is waiting on. Dropping is the one action the
// protocol will not take without words, so it is the one action that asks.
type prompt struct {
	id     string
	reason string
}

// mutate turns a key the palette runs into a command. Nothing here decides whether the
// loom will accept it: readiness, stewardship and unresolved children are the
// loom's to rule on, and guessing them locally is exactly the second state
// model the boundary forbids.
func (m Model) mutate(key string) (Model, tea.Cmd) {
	if m.mutator == nil {
		return m.note("this loom is open read-only"), nil
	}
	if m.pending != nil {
		return m.note(fmt.Sprintf("%s already", m.pending.gerund)), nil
	}
	// Undo works on the move that happened, not on what is selected now: a
	// move the user did not mean is one they will not have the cursor on.
	if key == "u" {
		return m.undoMove()
	}
	st := m.selectedStitch()
	if st == nil {
		// A queue record naming a stitch the projection does not have is the
		// one row in any view with nothing to act on.
		if m.view == viewQueue && m.selectedID != "" {
			return m.note(m.selectedID + " is a stale queue record — the loom has no such stitch"), nil
		}
		return m, nil
	}
	id := st.ID

	switch key {
	case "c":
		return m.start(mutation{command: loom.Claim, id: id, gerund: "claiming " + id}, gone(id))
	case "t":
		return m.start(mutation{command: loom.Tie, id: id, gerund: "tying off " + id}, evicts(id))
	case "w":
		if m.selectedHasSuffix("waiting") {
			return m.start(mutation{command: loom.Resume, id: id, gerund: "unparking " + id}, unchanged)
		}
		return m.start(mutation{command: loom.Wait, id: id, gerund: "parking " + id}, gone(id))
	case "T":
		if m.selectedHasSuffix("tending") {
			return m.start(mutation{command: loom.Release, id: id, gerund: "releasing " + id}, unchanged)
		}
		return m.start(mutation{command: loom.Tend, id: id, gerund: "tending " + id}, gone(id))
	case "d":
		// The reason comes first; the mutation starts when it is confirmed.
		m.prompt = &prompt{id: id}
		m.failure = ""
		return m, nil
	case "Q":
		return m.toggleQueue(id)
	case "J":
		return m.reorder(id, 1)
	case "K":
		return m.reorder(id, -1)
	}
	return m, nil
}

// gone is the optimistic effect of a mutation that takes a stitch off the
// frontier.
func gone(id string) func(*Model) {
	return func(m *Model) { m.over.remove(m.state.Generation, id) }
}

// unchanged is the honest optimistic effect of returning a stitch from wait
// or stewardship: the subtractive overlay cannot add a row back, so the next
// projection is allowed to land it instead.
func unchanged(*Model) {}

// evicts is gone for a tie or a drop, which also takes the ID out of the queue
// without anyone touching the file.
func evicts(id string) func(*Model) {
	return func(m *Model) { m.over.evict(m.state.Generation, id) }
}

func (m Model) toggleQueue(id string) (Model, tea.Cmd) {
	if beneath := m.unqueuedBeneath(id); len(beneath) > 0 {
		return m.queueWithChildren(id, beneath)
	}
	if m.isQueued(id) {
		mut := mutation{command: loom.Unqueue, id: id, gerund: "unqueueing " + id}
		mut.inverse = m.reinsert(id)
		return m.startQueue(mut, func(m *Model) {
			m.over.setQueue(m.state.Generation, id, nil)
		})
	}
	// A queue appends, so the optimistic position is the end of the queue.
	end := m.lastPosition() + 1
	mut := mutation{command: loom.Queue, id: id, gerund: "queueing " + id}
	mut.inverse = &mutation{command: loom.Unqueue, id: id, gerund: "unqueueing " + id}
	return m.startQueue(mut, func(m *Model) {
		m.over.setQueue(m.state.Generation, id, &end)
	})
}

// queueWithChildren queues the unqueued stitches beneath id, then id itself.
// queue moves an ID that is already there to the end, so the thread ends up
// behind all of its children whether it was queued before or not. Children
// already on the queue keep their places: somebody put them there.
//
// It is one queue command per stitch rather than one queue --set. A set
// rewrites the whole queue from this snapshot, which would silently undo a
// queue change another agent made since; the single commands only ever touch
// the IDs they name.
func (m Model) queueWithChildren(id string, beneath []string) (Model, tea.Cmd) {
	mut := mutation{command: loom.Queue, id: id, gerund: fmt.Sprintf("queueing %s and %s", id, children(len(beneath)))}
	undo := mutation{command: loom.Unqueue, id: id, gerund: fmt.Sprintf("taking %s and %s back off the queue", id, children(len(beneath)))}
	for _, child := range beneath {
		mut.steps = append(mut.steps, mutation{command: loom.Queue, id: child})
		undo.steps = append(undo.steps, mutation{command: loom.Unqueue, id: child})
	}
	mut.steps = append(mut.steps, mutation{command: loom.Queue, id: id})
	undo.steps = append(undo.steps, *m.reinsert(id))
	mut.told = fmt.Sprintf("queued %s and %s at the end", id, children(len(beneath)))
	undo.told = fmt.Sprintf("took %s and %s back off the queue", id, children(len(beneath)))
	if m.isQueued(id) {
		mut.told = fmt.Sprintf("queued %s beneath %s, and moved it behind them", children(len(beneath)), id)
		undo.told = fmt.Sprintf("took %s back off the queue and put %s back", children(len(beneath)), id)
	}
	mut.inverse = &undo
	return m.startQueue(mut, m.optimistic(mut))
}

func children(n int) string {
	if n == 1 {
		return "1 child"
	}
	return fmt.Sprintf("%d children", n)
}

// reorder moves a stitch one place along the queue. A move is one command, not
// a rewrite of the queue file.
//
// In the queue view every row is a queue record, so the anchor is the row
// above or below — what the eye is on is what the keystroke moves past. On the
// frontier the list is mixed, so the anchor is the nearest queued neighbour
// there instead, which is not always the neighbouring queue record. That is
// what the announcement is for.
func (m Model) reorder(id string, delta int) (Model, tea.Cmd) {
	if !m.isQueued(id) {
		// The queue is sparse: an unqueued stitch shares the list without a
		// position, so a move on one queues it rather than erroring.
		end := m.lastPosition() + 1
		mut := mutation{command: loom.Queue, id: id, gerund: "queueing " + id}
		mut.inverse = &mutation{command: loom.Unqueue, id: id, gerund: "unqueueing " + id}
		return m.startQueue(mut, func(m *Model) {
			m.over.setQueue(m.state.Generation, id, &end)
		})
	}

	neighbours := m.neighbours()
	here := indexOfRef(neighbours, id)
	if here < 0 {
		return m, nil
	}
	dest := here + delta
	if dest < 0 || dest >= len(neighbours) {
		return m.note(edgeOfQueue(id, delta)), nil
	}
	anchor := neighbours[dest]

	mut := mutation{command: loom.After, id: id, anchor: anchor.id, gerund: "moving " + id + " down"}
	if delta < 0 {
		mut = mutation{command: loom.Before, id: id, anchor: anchor.id, gerund: "moving " + id + " up"}
		// Nothing can go before the first record, so the top of the queue is
		// first rather than before.
		if anchor.position == 1 {
			mut = mutation{command: loom.First, id: id, gerund: "moving " + id + " to the top"}
		}
	}
	mut.inverse = m.reinsert(id)
	return m.startQueue(mut, m.optimistic(mut))
}

// queueRef is one queue record a move can be made against: an ID the loom will
// accept as an anchor, and where it currently sits.
type queueRef struct {
	id       string
	position int
}

// neighbours is the list a move steps along, which is the list the user is
// looking at: the whole queue in the queue view, and the queued rows of the
// frontier on the frontier.
func (m Model) neighbours() []queueRef {
	var refs []queueRef
	if m.view == viewQueue {
		for _, r := range m.queueRows() {
			// A record with no stitch behind it is not an anchor the loom will
			// take, and a mutation naming one would be refused.
			if r.stitch != nil {
				refs = append(refs, queueRef{r.id, r.position})
			}
		}
		return refs
	}
	rows, _ := m.rowsAndCounts()
	for _, r := range rows {
		if r.position > 0 {
			refs = append(refs, queueRef{r.id, r.position})
		}
	}
	return refs
}

func indexOfRef(refs []queueRef, id string) int {
	for i, r := range refs {
		if r.id == id {
			return i
		}
	}
	return -1
}

// reinsert is the command that would put id back where it is now: first when
// it is at the head, after its predecessor otherwise, and unqueue when it is
// not in the queue at all.
//
// One level of undo is enough. This is a keystroke guard, not a history.
func (m Model) reinsert(id string) *mutation {
	order := queueOrder(m.queueRows())
	for i, queued := range order {
		if queued != id {
			continue
		}
		if i == 0 {
			return &mutation{command: loom.First, id: id, gerund: "putting " + id + " back at the top"}
		}
		return &mutation{
			command: loom.After, id: id, anchor: order[i-1],
			gerund: "putting " + id + " back after " + order[i-1],
		}
	}
	return &mutation{command: loom.Unqueue, id: id, gerund: "taking " + id + " back off the queue"}
}

// optimistic renumbers the queue as the move would leave it, so the list
// redraws on the keystroke.
//
// Records the projection has no stitch for keep no place in the renumbering:
// their positions are unknowable after a move, and a queue holding one makes
// every queue mutation fail anyway. The projection replaces all of this a
// moment later regardless.
func (m Model) optimistic(mut mutation) func(*Model) {
	was := queueOrder(m.queueRows())
	order := was
	for _, step := range mut.parts() {
		order = moved(order, step)
	}
	return func(m *Model) {
		// Whatever a step took off the queue is off it, unless a later step
		// put it back, which the positions below then say.
		for _, id := range was {
			m.over.setQueue(m.state.Generation, id, nil)
		}
		for i, id := range order {
			position := i + 1
			m.over.setQueue(m.state.Generation, id, &position)
		}
	}
}

// moved is the queue's IDs with one of them taken out and put back where the
// command puts it, or left out for an unqueue.
func moved(order []string, mut mutation) []string {
	rest := make([]string, 0, len(order))
	for _, id := range order {
		if id != mut.id {
			rest = append(rest, id)
		}
	}
	if mut.command == loom.Unqueue {
		return rest
	}

	at := 0
	if mut.command != loom.First {
		at = len(rest)
		for i, id := range rest {
			if id != mut.anchor {
				continue
			}
			at = i
			if mut.command == loom.After {
				at = i + 1
			}
			break
		}
	}
	next := make([]string, 0, len(order))
	next = append(next, rest[:at]...)
	next = append(next, mut.id)
	return append(next, rest[at:]...)
}

func edgeOfQueue(id string, delta int) string {
	if delta < 0 {
		return id + " is already at the top of the queue"
	}
	return id + " is already at the end of the queue"
}

// undoMove puts the last queue move back. It is a mutation like any other: if
// the loom refuses it, the refusal is shown and the queue is left alone.
func (m Model) undoMove() (Model, tea.Cmd) {
	if m.lastMove == nil {
		return m.note("nothing to undo"), nil
	}
	mut := *m.lastMove
	m.lastMove = nil
	// Undoing an undo would be a history, and one level is the guard this is
	// meant to be.
	mut.inverse = nil

	if mut.command == loom.Unqueue && len(mut.steps) == 0 {
		return m.startQueue(mut, func(m *Model) {
			m.over.setQueue(m.state.Generation, mut.id, nil)
		})
	}
	return m.startQueue(mut, m.optimistic(mut))
}

// startQueue runs a queue mutation, working out what it will have done by the
// time it lands. The announcement is composed here, against the queue as the
// move leaves it, and said when the loom confirms it.
func (m Model) startQueue(mut mutation, optimistic func(*Model)) (Model, tea.Cmd) {
	before := m.queuePositions()
	// The overlay's maps are shared by every copy of the model, so working out
	// what the move leaves behind has to happen on a clone. Answering the
	// question must not be the thing that changes the answer.
	next := m
	next.over = m.over.clone()
	optimistic(&next)
	mut.announce = m.announcement(mut, before, next.queuePositions())
	return m.start(mut, optimistic)
}

// queuePositions is the effective queue as the model currently draws it.
func (m Model) queuePositions() map[string]int {
	if m.state.Snapshot == nil {
		return map[string]int{}
	}
	return effectiveQueue(m.state.Snapshot, m.over)
}

// announcement is what to say a queue move did. Every move says something,
// including one made from the frontier, where the anchor is not the row above
// or below — a mutation nobody meant must not be silent.
//
// A move that leaves the queue in an order the work cannot happen in says that
// too. It is announced, never refused: parking an item below its dependency on
// purpose is legitimate, and undo is right there.
func (m Model) announcement(mut mutation, before, after map[string]int) string {
	if len(mut.steps) > 0 {
		return m.batchAnnouncement(mut, before, after)
	}
	var said string
	switch mut.command {
	case loom.Queue:
		said = "queued " + mut.id + " at the end"
	case loom.Unqueue:
		said = "unqueued " + mut.id
	case loom.First:
		said = "moved " + mut.id + " to the top"
	case loom.Before:
		said = "moved " + mut.id + " before " + mut.anchor
	case loom.After:
		said = "moved " + mut.id + " after " + mut.anchor
	default:
		return ""
	}

	fresh := newInversions(m.state.Snapshot, mut.id, before, after)
	if len(fresh) == 0 {
		return said
	}
	said += " — " + fresh[0].say(mut.id, mut.anchor)
	if len(fresh) > 1 {
		said += fmt.Sprintf(" (+%d more)", len(fresh)-1)
	}
	return said
}

// batchAnnouncement is announcement for a batch. A subtree goes on in an order
// with no inversions inside it, so whatever it does invert involves a stitch
// outside it — and naming the move it came from would be no help.
func (m Model) batchAnnouncement(mut mutation, before, after map[string]int) string {
	said := mut.told
	seen := map[inverted]bool{}
	var fresh []inverted
	for _, step := range mut.steps {
		for _, i := range newInversions(m.state.Snapshot, step.id, before, after) {
			if !seen[i] {
				seen[i] = true
				fresh = append(fresh, i)
			}
		}
	}
	if len(fresh) == 0 {
		return said
	}
	said += " — " + fresh[0].dependent + " needs " + fresh[0].dependency
	if len(fresh) > 1 {
		said += fmt.Sprintf(" (+%d more)", len(fresh)-1)
	}
	return said
}

// inverted is one dependency edge the queue now runs backwards along: the
// dependent is queued above something it needs.
type inverted struct {
	dependent  string
	dependency string
}

// say puts the inversion in terms of the move that caused it. The anchor is
// what the user just moved past, so naming it is the shortest true thing.
func (i inverted) say(id, anchor string) string {
	switch {
	case i.dependency == id && i.dependent == anchor:
		return "which needs it"
	case i.dependent == id && i.dependency == anchor:
		return "which it needs"
	case i.dependency == id:
		return i.dependent + " needs it"
	default:
		return "it needs " + i.dependency
	}
}

// newInversions are the backwards edges this move created that were not there
// before it, restricted to the ones the moved stitch is part of.
//
// A dependency that is not queued at all counts: next will skip the row and
// take the dependency anyway, so the queue is no longer the order the work
// will happen in — which is the only thing the queue is for.
func newInversions(snap *snapshot.Snapshot, id string, before, after map[string]int) []inverted {
	if snap == nil {
		return nil
	}
	was := map[inverted]bool{}
	for _, i := range inversionsAround(snap, id, before) {
		was[i] = true
	}
	var fresh []inverted
	for _, i := range inversionsAround(snap, id, after) {
		if !was[i] {
			fresh = append(fresh, i)
		}
	}
	return fresh
}

// inversionsAround is every backwards edge involving id in one ordering:
// id above what it needs, and above it, whatever needs id.
func inversionsAround(snap *snapshot.Snapshot, id string, positions map[string]int) []inverted {
	var out []inverted
	index := snap.Index()

	if st, ok := index[id]; ok {
		if at, queued := positions[id]; queued {
			for _, dep := range st.Dependencies {
				if dep.Status != snapshot.DependencyBlocked {
					continue
				}
				if to, queued := positions[dep.To]; !queued || to > at {
					out = append(out, inverted{dependent: id, dependency: dep.To})
				}
			}
		}
	}

	dependents := make([]string, 0, len(positions))
	for other := range positions {
		if other != id {
			dependents = append(dependents, other)
		}
	}
	sort.Slice(dependents, func(i, j int) bool {
		return positions[dependents[i]] < positions[dependents[j]]
	})
	for _, other := range dependents {
		st, ok := index[other]
		if !ok {
			continue
		}
		for _, dep := range st.Dependencies {
			if dep.Status != snapshot.DependencyBlocked || dep.To != id {
				continue
			}
			if at, queued := positions[id]; !queued || positions[other] < at {
				out = append(out, inverted{dependent: other, dependency: id})
			}
		}
	}
	return out
}

// start applies the optimistic change and runs the mutation in the background.
func (m Model) start(mut mutation, optimistic func(*Model)) (Model, tea.Cmd) {
	m.undo = m.over.clone()
	m.failure, m.notice = "", ""
	optimistic(&m)
	m.pending = &mut
	return m.reselect(), m.runMutation(mut)
}

// runMutation is the only place the TUI reaches the loom. It returns a command
// so the update loop keeps drawing while a mutation that takes seconds on a
// large loom runs.
//
// A batch runs its steps in order and stops at the first refusal. The steps
// before it have happened, and the refresh that follows shows them.
func (m Model) runMutation(mut mutation) tea.Cmd {
	runner := m.mutator
	return func() tea.Msg {
		ctx := context.Background()
		var res *loom.Result
		var err error
		done := 0
		for _, step := range mut.parts() {
			if res, err = runOne(ctx, runner, step); err != nil {
				break
			}
			done++
		}
		return mutationMsg{mutation: mut, result: res, err: err, done: done}
	}
}

func runOne(ctx context.Context, runner *loom.Runner, mut mutation) (*loom.Result, error) {
	switch mut.command {
	case loom.Claim:
		return runner.Claim(ctx, mut.id)
	case loom.Tie:
		return runner.Tie(ctx, mut.id)
	case loom.Wait:
		return runner.Wait(ctx, mut.id)
	case loom.Resume:
		return runner.Resume(ctx, mut.id)
	case loom.Tend:
		return runner.Tend(ctx, mut.id)
	case loom.Release:
		return runner.Release(ctx, mut.id)
	case loom.Drop:
		return runner.Drop(ctx, mut.id, mut.reason)
	case loom.Queue:
		return runner.Queue(ctx, mut.id)
	case loom.Unqueue:
		return runner.Unqueue(ctx, mut.id)
	case loom.First:
		return runner.First(ctx, mut.id)
	case loom.Before:
		return runner.Before(ctx, mut.id, mut.anchor)
	case loom.After:
		return runner.After(ctx, mut.id, mut.anchor)
	}
	return nil, fmt.Errorf("no such command %q", mut.command)
}

// finish reconciles a mutation that has come back.
//
// A failure rolls the optimistic change back and shows the loom's own words:
// an unrelated stale queue record makes a mutation fail without changing
// anything, and nothing else can explain that. The UI is never left asserting
// something the loom rejected.
func (m Model) finish(msg mutationMsg) (Model, tea.Cmd) {
	m.pending = nil
	if msg.err != nil {
		m.over = m.undo
		m.failure = refusal(msg)
		m = m.reselect()
	} else {
		// Said now rather than on the keystroke: what is announced is what the
		// loom did, not what we asked it for.
		m.notice = msg.announce
		// A lifecycle mutation carries no inverse, and clearing the undo is
		// the point — a tie evicts its ID from the queue, so the move that
		// came before it can no longer be put back.
		m.lastMove = msg.inverse
	}

	// A mutation makes the snapshot stale immediately, and a tie or drop
	// evicts IDs from the queue without anyone touching it — so the refresh
	// covers stitches this result says nothing about. It starts now rather
	// than beside the mutation, because a snapshot taken while the command was
	// still running would project the loom before it.
	return m.startRefresh(), nil
}

// refusal is what to show for a mutation the loom would not do: its message,
// verbatim, with the failing command above it.
func refusal(msg mutationMsg) string {
	head := fmt.Sprintf("%s %s failed", msg.command, msg.id)
	if len(msg.steps) > 0 && msg.done < len(msg.steps) {
		// A batch that got partway has changed the queue, and saying only
		// that it failed would hide that.
		step := msg.steps[msg.done]
		head = fmt.Sprintf("%s %s failed after %d of %d", step.command, step.id, msg.done, len(msg.steps))
	}
	var body string
	var mutErr *loom.Error
	var idErr *loom.IDError
	switch {
	case errors.As(msg.err, &mutErr):
		body = mutErr.Message
		if body == "" {
			body = mutErr.Error()
		}
		if mutErr.Code != "" {
			head += " (" + string(mutErr.Code) + ")"
		}
	case errors.As(msg.err, &idErr):
		body = idErr.Error()
	default:
		body = msg.err.Error()
	}
	return head + ": " + firstLines(body, 3)
}

// firstLines keeps a long refusal from taking the screen, without editing what
// the loom said inside what it does show.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(append(lines[:n:n], "…"), "\n")
}

// updatePrompt drives the drop reason line. Enter drops, escape abandons it.
func (m Model) updatePrompt(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		p := *m.prompt
		m.prompt = nil
		if strings.TrimSpace(p.reason) == "" {
			return m.note("a drop needs a reason"), nil
		}
		mut := mutation{
			command: loom.Drop, id: p.id, reason: p.reason,
			gerund: "dropping " + p.id,
		}
		return m.start(mut, evicts(p.id))
	case tea.KeyEsc, tea.KeyCtrlC:
		m.prompt = nil
		return m, nil
	case tea.KeyBackspace:
		if r := []rune(m.prompt.reason); len(r) > 0 {
			next := *m.prompt
			next.reason = string(r[:len(r)-1])
			m.prompt = &next
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		// The space key arrives with its rune, so taking the runes covers it.
		// Adding one of our own is how a reason ends up with double spaces in
		// it — and a drop reason is a durable record.
		typed := string(msg.Runes)
		if msg.Type == tea.KeySpace && typed == "" {
			typed = " "
		}
		next := *m.prompt
		next.reason += typed
		m.prompt = &next
		return m, nil
	}
	return m, nil
}

// note says something that is not a failure: a key that had nothing to do.
func (m Model) note(text string) Model {
	m.notice = text
	return m
}

// isQueued asks the queue, not the frontier: a queued stitch that is blocked
// or parked is not on the frontier at all, and Q on one of those is an
// unqueue.
func (m Model) isQueued(id string) bool {
	_, queued := m.queuePositions()[id]
	return queued
}

// lastPosition is the end of the queue as the projection knows it, which is
// where a queue command appends.
func (m Model) lastPosition() int {
	last := 0
	for _, position := range m.queuePositions() {
		if position > last {
			last = position
		}
	}
	return last
}

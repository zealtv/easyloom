package tui

import (
	"github.com/zealtv/easyloom/internal/snapshot"
)

// Queueing a thread with its children.
//
// The loom's next skips a queued parent until every child is terminal, and it
// never looks inside one: the children are reached only by the unqueued
// fallback, in path order. So queueing a thread on its own says nothing about
// the work beneath it. Queueing it with its children puts the whole unresolved
// subtree on the queue, in an order the work can happen in, with the thread
// itself last.
//
// This is an expansion at the moment of queueing, not a standing rule: a child
// added to the thread afterwards is not queued until the thread is queued
// again. The loom's queue holds stitch IDs and nothing else.

// subtreeOrder is id and its unresolved descendants, in an order the work can
// happen in: children before their parent, and anything blocked on a stitch in
// the subtree after it. Between those, the order is the tree's own, depth
// first with siblings in the projection's order.
//
// A dependency cycle cannot be ordered. Its members keep their tree order,
// which is as good as any — none of them will ever be ready.
func subtreeOrder(snap *snapshot.Snapshot, id string) []string {
	index := snap.Index()
	root, ok := index[id]
	if !ok {
		return nil
	}

	// Post-order: a stitch after everything beneath it.
	var tree []string
	var walk func(st *snapshot.Stitch)
	walk = func(st *snapshot.Stitch) {
		for _, child := range st.Children {
			if c, ok := index[child]; ok && unresolved(c) {
				walk(c)
			}
		}
		tree = append(tree, st.ID)
	}
	walk(root)

	rank := make(map[string]int, len(tree))
	for i, member := range tree {
		rank[member] = i
	}
	// first[x] are the members that have to come before x.
	first := make(map[string][]string, len(tree))
	for _, member := range tree {
		st := index[member]
		for _, child := range st.Children {
			if _, in := rank[child]; in {
				first[member] = append(first[member], child)
			}
		}
		for _, dep := range st.Dependencies {
			if _, in := rank[dep.To]; in && dep.Status == snapshot.DependencyBlocked {
				first[member] = append(first[member], dep.To)
			}
		}
	}

	// Repeatedly take the earliest member in tree order whose predecessors are
	// all placed. A subtree is small, so the quadratic scan is fine.
	placed := make(map[string]bool, len(tree))
	out := make([]string, 0, len(tree))
	for len(out) < len(tree) {
		next := ""
		for _, member := range tree {
			if placed[member] {
				continue
			}
			ready := true
			for _, before := range first[member] {
				if !placed[before] {
					ready = false
					break
				}
			}
			if ready {
				next = member
				break
			}
		}
		if next == "" {
			// Only a cycle is left. Place the rest in tree order.
			for _, member := range tree {
				if !placed[member] {
					placed[member] = true
					out = append(out, member)
				}
			}
			break
		}
		placed[next] = true
		out = append(out, next)
	}
	return out
}

// unresolved is a stitch still to be done: active, visible, and not tied or
// dropped. It is what the loom will accept on the queue.
func unresolved(st *snapshot.Stitch) bool {
	if hidden(st) || st.Tray != snapshot.TrayThreads {
		return false
	}
	switch st.State {
	case snapshot.StateTied, snapshot.StateDropped, snapshot.StateAbandoned:
		return false
	}
	return true
}

// unqueuedBeneath is the part of id's unresolved subtree that is not on the
// queue yet, in subtreeOrder, without id itself. Empty means queueing id is
// the plain single-stitch queue.
func (m Model) unqueuedBeneath(id string) []string {
	if m.state.Snapshot == nil {
		return nil
	}
	positions := m.queuePositions()
	var out []string
	for _, member := range subtreeOrder(m.state.Snapshot, id) {
		if _, queued := positions[member]; !queued && member != id {
			out = append(out, member)
		}
	}
	return out
}

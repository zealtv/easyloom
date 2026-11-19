// What resolving a stitch would make ready.
//
// Graph arithmetic and nothing else: a projection in, numbers and sets of IDs
// out. There is no DOM here, no fetch, and no module state — `graph.js` and
// `graphview.js` are already split along the line between deciding and
// drawing, and this is a third piece that only decides. The view that renders
// it is unblock-ranking-view.
//
// ## Why re-derive readiness at all
//
// The projection ships `ready` per stitch and it is authoritative. Nothing
// here replaces it. What the loom cannot be asked is a hypothetical — "if this
// were done, what would that let someone start?" — and answering that means
// running the same rule with one input changed.
//
// Which makes agreement the whole test. With nothing simulated, `derive` must
// return exactly the set of stitches the loom marked ready. A re-derivation
// that disagrees with the loom about the present is wrong about the future
// too, so `disagreements` exists to be asserted in a test and checked at
// runtime by the view.
//
// The rule is the protocol's, from .loom/docs/protocol-v2.md, "Decomposition
// and readiness". A plain stitch is effectively ready exactly when it is under
// threads/ with no terminal or dropped ancestor, has no waiting ancestor and
// is not directly waiting, is not claimed or tended, every immediate child is
// terminal, every hard dependency is satisfied, it is in no dependency cycle,
// and its records are structurally valid.
//
// ## One step, not transitive
//
// The impact of a stitch is what becomes ready *next*. A transitive count over
// a chain rewards the head of the longest thread regardless of what that
// thread costs, and it cannot be checked by looking at the screen. If a
// transitive figure is ever wanted it is a second number beside this one, not
// a redefinition of it.

// The states that resolve a decomposition edge. The protocol says a direct
// child resolves when its direct state is .tied or .dropped; abandoned is the
// third way a stitch stops being work anyone can do.
const terminalStates = new Set(["tied", "dropped", "abandoned"]);

/** index builds the lookups every derivation needs. */
function index(snapshot) {
  const byId = new Map();
  for (const stitch of snapshot?.stitches ?? []) byId.set(stitch.id, stitch);

  const inCycle = new Set();
  for (const members of snapshot?.cycles ?? []) {
    for (const id of members) inCycle.add(id);
  }
  return { byId, inCycle };
}

/** terminal reports a stitch that no longer holds its parent up. */
function terminal(stitch, resolved) {
  if (!stitch) return false;
  return resolved.has(stitch.id) || terminalStates.has(stitch.state);
}

/** ready applies the protocol's readiness rule to one stitch, under a
 * hypothesis about what has been resolved and what has been un-parked. */
function ready(stitch, { byId, inCycle }, resolved, resumed) {
  // A stitch that has been resolved is not ready work; it is done work.
  if (resolved.has(stitch.id)) return false;

  // 1. under threads/, with no terminal or dropped ancestor. A terminal
  //    ancestor takes its whole subtree into an archive tray with it, so the
  //    tray is the answer to both halves.
  if (stitch.tray !== "threads") return false;

  // 2 and 3. not directly waiting, no waiting ancestor, not claimed or tended.
  const state = stitch.state === "waiting" && resumed.has(stitch.id) ? "plain" : stitch.state;
  if (state !== "plain") return false;
  if (stitch.waiting_inherited && !resumed.has(stitch.id)) return false;

  // 6. cycle members are permanently un-ready, and resolving one member does
  //    not change that — the edge is still there.
  if (inCycle.has(stitch.id)) return false;

  // 4. every immediate child is terminal.
  for (const child of stitch.children ?? []) {
    const record = byId.get(child);
    // 7. a child the projection does not carry is a structurally invalid
    //    record, which is not ready and not something to guess about.
    if (!record) return false;
    if (!terminal(record, resolved)) return false;
  }

  // 5. every hard dependency satisfied. A blocked edge onto something the
  //    hypothesis resolves becomes satisfied; a broken one does not, because
  //    what is broken about it is the record, not the target's state.
  for (const edge of stitch.dependencies ?? []) {
    if (edge.status === "satisfied" || edge.status === "inert") continue;
    if (edge.status === "blocked" && resolved.has(edge.to)) continue;
    return false;
  }
  return true;
}

/** derive returns the set of stitch IDs that are ready.
 *
 * With no hypothesis it re-derives the present, and must agree with the
 * projection. `resolved` is the IDs to treat as tied; `resumed` is the IDs
 * whose waiting — their own or inherited — is lifted.
 */
export function derive(snapshot, { resolved = new Set(), resumed = new Set() } = {}) {
  const ctx = index(snapshot);
  const out = new Set();
  for (const stitch of snapshot?.stitches ?? []) {
    if (ready(stitch, ctx, resolved, resumed)) out.add(stitch.id);
  }
  return out;
}

/** disagreements lists every stitch where this module's readiness differs from
 * the projection's own flag.
 *
 * It must always be empty. The projection is authoritative and this is a
 * re-derivation of it; anything here is a bug in this file, not in the loom.
 */
export function disagreements(snapshot) {
  const derived = derive(snapshot);
  const out = [];
  for (const stitch of snapshot?.stitches ?? []) {
    const mine = derived.has(stitch.id);
    if (mine !== Boolean(stitch.ready)) {
      out.push({ id: stitch.id, projection: Boolean(stitch.ready), derived: mine });
    }
  }
  return out;
}

/** parkedAncestor is the nearest ancestor holding a stitch waiting.
 *
 * Waiting is inherited and `resume` lifts only a stitch's own suffix, so
 * `resume <child>` fails with "not directly waiting" for a child that was
 * never individually parked. Naming the ancestor is what makes the difference
 * actionable: this repo has already been bitten by it.
 */
function parkedAncestor(stitch, byId) {
  let at = stitch.parent_id ? byId.get(stitch.parent_id) : null;
  while (at) {
    if (at.state === "waiting") return at.id;
    at = at.parent_id ? byId.get(at.parent_id) : null;
  }
  return null;
}

/** unparkedBy is the stitch itself plus every descendant whose parking it is
 * holding.
 *
 * A descendant with a `.waiting` suffix of its own stays parked when its
 * parent is resumed, and so does everything under it — that is what makes
 * resuming a parent a different act from resuming a subtree.
 */
function unparkedBy(stitch, byId) {
  const out = new Set([stitch.id]);
  const walk = [stitch];
  while (walk.length) {
    const at = walk.pop();
    for (const id of at.children ?? []) {
      const child = byId.get(id);
      if (!child || child.state === "waiting") continue;
      out.add(id);
      walk.push(child);
    }
  }
  return out;
}

/** unresolvedChildren lists the children still holding a stitch open. */
function unresolvedChildren(stitch, byId) {
  const out = [];
  for (const id of stitch.children ?? []) {
    const child = byId.get(id);
    if (!terminal(child, new Set())) out.push(id);
  }
  return out;
}

/** blockers lists the dependency targets a stitch is waiting on. */
function blockers(stitch) {
  return (stitch.dependencies ?? [])
    .filter((edge) => edge.status !== "satisfied" && edge.status !== "inert")
    .map((edge) => ({ id: edge.to, status: edge.status, reason: edge.reason ?? null }));
}

/** hypothesis is what "resolving this stitch" means for this stitch.
 *
 * A leaf is tied. A stitch with unresolved children cannot be — resolving it
 * means resolving its subtree, which is not one step — so for one the act is
 * the resume, and what that releases is the subtree becoming reachable.
 */
function hypothesis(stitch, byId) {
  const parked = stitch.state === "waiting" || Boolean(stitch.waiting_inherited);
  const resumed = parked ? unparkedBy(stitch, byId) : new Set();
  const open = unresolvedChildren(stitch, byId);
  return {
    resumed,
    resolved: open.length === 0 ? new Set([stitch.id]) : new Set(),
    open,
  };
}

/** impact is what resolving one stitch would release.
 *
 * `releases` is the set — named, not counted. "3" is a leaderboard; "3 —
 * live-updates, mutation-refresh, queue-reorder-ui" is an answer, and it is
 * what makes the number checkable by looking at the loom.
 *
 * `action` is what the reader would actually have to do, because the cost of
 * the three cases is not the same:
 *
 *   tie       it is ready; finish it
 *   unblock   it is a loose end waiting on dependencies, named in `blockedBy`
 *   decompose its children are the work, named in `openChildren`
 *   resume    it is directly waiting; `loom resume <id>` lifts it
 *   promote   it is waiting by inheritance, so resume refuses it: promote it
 *             to its own thread, or resume `resumeVia`
 *   cycle     it is in a dependency cycle, and no amount of working the loom
 *             makes it or anything behind it ready
 */
export function impact(snapshot, id, before = null) {
  const ctx = index(snapshot);
  const stitch = ctx.byId.get(id);
  if (!stitch) return null;

  const was = before ?? derive(snapshot);
  const { resumed, resolved, open } = hypothesis(stitch, ctx.byId);

  // A cycle member is permanently un-ready and so is everything behind it, so
  // there is no hypothesis to run: the answer is nothing, and saying why is
  // more use than saying zero.
  const inCycle = ctx.inCycle.has(id);
  const now = inCycle ? was : derive(snapshot, { resolved, resumed });

  const releases = [];
  for (const other of now) {
    if (other !== id && !was.has(other)) releases.push(other);
  }
  releases.sort();

  const blockedBy = blockers(stitch);
  return {
    id,
    releases,
    blockedBy,
    openChildren: open,
    resumeVia: parkedAncestor(stitch, ctx.byId),
    parked: stitch.state === "waiting" || Boolean(stitch.waiting_inherited),
    ready: was.has(id),
    action: actionFor(stitch, { inCycle, ready: was.has(id), open, blockedBy }),
  };
}

function actionFor(stitch, { inCycle, ready, open, blockedBy }) {
  if (inCycle) return "cycle";
  if (stitch.waiting_inherited) return "promote";
  if (stitch.state === "waiting") return "resume";
  if (ready) return "tie";
  if (open.length) return "decompose";
  if (blockedBy.length) return "unblock";
  // Active, unblocked, childless and still not ready: claimed or tended, which
  // is someone already doing it.
  return "in-hand";
}

/** ranking scores every stitch worth asking about, in two groups.
 *
 * They are two different questions and their answers are not comparable, so
 * they are never merged into one leaderboard. `looseEnds` is what to work on
 * now; `parked` is what to un-park. Both are sorted by what they release, then
 * by ID so the order is stable across snapshots.
 */
export function ranking(snapshot) {
  const ctx = index(snapshot);
  const before = derive(snapshot);
  const looseEnds = [];
  const parked = [];

  for (const stitch of snapshot?.stitches ?? []) {
    if (stitch.tray !== "threads") continue;
    if (terminalStates.has(stitch.state)) continue;
    const scored = impact(snapshot, stitch.id, before);
    if (!scored) continue;
    (scored.parked ? parked : looseEnds).push(scored);
  }

  looseEnds.sort(byImpact);
  parked.sort(byImpact);
  return { looseEnds, parked, ready: before };
}

function byImpact(a, b) {
  if (a.releases.length !== b.releases.length) return b.releases.length - a.releases.length;
  // A ready stitch outranks a blocked one with the same impact: it is the one
  // that can actually be started.
  if (a.ready !== b.ready) return a.ready ? -1 : 1;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

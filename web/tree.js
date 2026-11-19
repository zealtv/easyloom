// The thread tree: the decomposition, one root per goal stitch.
//
// Nesting is the only relationship drawn — this is deliberately not the
// dependency graph. The frontier answers "what next", which on a mature loom
// is one line and says nothing about shape. This is the view that makes the
// other thirty-odd stitches legible.
//
// Structure comes from the projection: parent_id for the roots, children for
// the edges. Nothing here walks .loom.

import { element, hidden, selectable, selectionPath, terminal } from "./stitches.js";

/** rows lays the decomposition out in draw order.
 *
 * `collapsed` is the set of node IDs the reader has folded, and `expanded` the
 * ones they have opened against the default. The default is open, except for a
 * subtree whose root is already finished: v2 keeps terminal children in place,
 * so a tree contains completed work inline.
 */
export function rows(snapshot, { collapsed = new Set(), expanded = new Set() } = {}) {
  const index = new Map(snapshot.stitches.map((stitch) => [stitch.id, stitch]));
  const seen = new Set();
  const out = [];

  const isOpen = (stitch, children) => {
    if (children === 0) return false;
    if (collapsed.has(stitch.id)) return false;
    if (expanded.has(stitch.id)) return true;
    return !terminal(stitch);
  };

  const walk = (id, depth) => {
    const stitch = index.get(id);
    // A repeated ID is guarded against rather than trusted not to happen: a
    // cycle in the edges must not hang the render.
    if (!stitch || hidden(stitch) || seen.has(id)) return;
    seen.add(id);

    const children = (stitch.children ?? []).filter((child) => {
      const node = index.get(child);
      return node && !hidden(node);
    });
    const open = isOpen(stitch, children.length);
    const row = { id, depth, stitch, children: children.length, expanded: open, folded: 0, unresolved: 0 };
    out.push(row);

    if (children.length === 0) return;
    if (!open) {
      // Saying how much is folded away is the difference between a tidy tree
      // and a lie.
      const counted = fold(index, children, seen);
      row.folded = counted.folded;
      row.unresolved = counted.unresolved;
      return;
    }
    for (const child of children) walk(child, depth + 1);
  };

  for (const stitch of snapshot.stitches) {
    // A goal stitch is a root of threads/. An archived goal is history, and a
    // legacy record has no ancestry at all.
    if (stitch.parent_id == null && stitch.tray === "threads" && !hidden(stitch)) {
      walk(stitch.id, 0);
    }
  }
  return out;
}

/** fold counts what a collapsed node hides, and how much of it is still live. */
function fold(index, children, seen) {
  let folded = 0;
  let unresolved = 0;
  const walk = (id) => {
    const stitch = index.get(id);
    if (!stitch || hidden(stitch) || seen.has(id)) return;
    seen.add(id);
    folded += 1;
    if (!terminal(stitch)) unresolved += 1;
    for (const child of stitch.children ?? []) walk(child);
  };
  for (const child of children) walk(child);
  return { folded, unresolved };
}

/** stateBadge describes a stitch's lifecycle state.
 *
 * Directly waiting and waiting-inherited are different things: the first is a
 * decision someone made and the only one `resume` can lift; the second is a
 * consequence of an ancestor.
 */
export function stateBadge(stitch) {
  if (stitch.state === "waiting") return { label: "waiting", className: "waiting" };
  if (stitch.waiting_inherited) return { label: "waiting above", className: "inherited" };
  if (stitch.state === "plain") return null;
  return { label: stitch.state, className: stitch.state };
}

/** liveness says how live a stitch is, for the row to be drawn by: `parked`
 * for waiting, directly or inherited — still work, only paused; `finished` for
 * tied; `dropped` for dropped or abandoned, the work that will not be done.
 * Ready and everything else in play need no class: ready already has its own.
 * The TUI's thread tree draws the same tiers.
 */
export function liveness(stitch) {
  if (stitch.state === "dropped" || stitch.state === "abandoned") return "dropped";
  if (stitch.state === "tied") return "finished";
  if (stitch.state === "waiting" || stitch.waiting_inherited) return "parked";
  return null;
}

function row(entry, slug, onToggle) {
  const item = selectable(element("li", `node depth-${Math.min(entry.depth, 6)}`), entry.id);
  const tier = liveness(entry.stitch);
  if (tier) item.classList.add(tier);
  item.style.setProperty("--depth", String(entry.depth));

  const twisty = element("button", "twisty", entry.children === 0 ? "" : entry.expanded ? "▾" : "▸");
  twisty.type = "button";
  if (entry.children === 0) {
    twisty.disabled = true;
    twisty.setAttribute("aria-hidden", "true");
  } else {
    twisty.setAttribute("aria-expanded", String(entry.expanded));
    twisty.setAttribute("aria-label", `${entry.expanded ? "collapse" : "expand"} ${entry.id}`);
    twisty.addEventListener("click", () => onToggle(entry));
  }
  item.append(twisty);

  const link = element("a", "id", entry.id);
  link.href = selectionPath(slug, "threads", entry.id);
  if (entry.stitch.ready) link.classList.add("ready");
  item.append(link);

  // The actionable set, marked distinctly.
  if (entry.stitch.ready) item.append(element("span", "badge ready", "ready"));

  const state = stateBadge(entry.stitch);
  if (state) item.append(element("span", `badge state ${state.className}`, state.label));

  if (entry.stitch.queue_position != null) {
    item.append(element("span", "badge queue", `queue ${entry.stitch.queue_position}`));
  }
  if (!entry.expanded && entry.folded > 0) {
    item.append(
      element("span", "folded", `${entry.folded} folded${entry.unresolved > 0 ? `, ${entry.unresolved} unresolved` : ""}`),
    );
  }
  return item;
}

/** renderTree returns the thread tree view. */
export function renderTree(snapshot, slug, state, onToggle) {
  const view = element("section", "tree");
  const laid = rows(snapshot, state);

  const drawn = laid.length;
  const active = snapshot.stitches.filter((stitch) => !hidden(stitch)).length;
  const archived = snapshot.stitches.length - active;

  const header = element("header", "counts");
  header.append(element("span", "count ready", `${snapshot.frontier.length} ready`));
  header.append(element("span", "count", `${active} active`));
  if (archived > 0) {
    // Never silently: on bopOS's loom 393 of 446 records are legacy, and a
    // view that hides 88% of the file tree has to say so.
    header.append(element("span", "count muted", `${archived} archived or legacy, hidden`));
  }
  view.append(header);

  if (drawn === 0) {
    view.append(element("p", "muted", "No active threads."));
    return view;
  }

  const list = element("ul", "nodes");
  for (const entry of laid) list.append(row(entry, slug, onToggle));
  view.append(list);
  return view;
}

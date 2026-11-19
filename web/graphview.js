// The graph view: what the laid-out graph looks like.
//
// `graph.js` decides where everything goes and nothing about how it reads.
// This file is the other half — it takes the boxes and routed points that
// layout produces and draws them, and it is where the loom's own language
// turns into colour:
//
//   * decomposition and dependency must not look alike. Decomposition is
//     containment, drawn as a solid spine with no arrowhead. A dependency is a
//     dashed arrow that crosses threads and points at the thing that has to
//     happen first.
//   * a dependency edge is coloured by its status. `satisfied` is history and
//     is muted; `blocked` is why work is not moving and is prominent; `broken`
//     means the loom itself is malformed, and it is drawn as an error, labelled
//     with the reason, and cross-referenced with the diagnostic that reported
//     it.
//   * a cycle is a hard stall. Every member of a strongly connected component
//     is permanently un-ready, so the whole component is boxed and named.
//
// The palette is shared with the thread tree — ready is the same weight in
// both, waiting is muted in both — so the two views teach one vocabulary
// rather than two. The variables live in app.css.
//
// Nothing here fetches, and nothing here decides what is selected: a node
// carries the stitch it is about (`selectable`), and the shell does the rest.

import { CHAR_WIDTH, layout } from "./graph.js";
import { fitted, renderControls, wireHover, wirePanZoom } from "./graphinteraction.js";
import { element, hidden, selectable, terminal } from "./stitches.js";

const SVG = "http://www.w3.org/2000/svg";

/** svg builds an SVG node. The HTML `element` helper cannot: SVG lives in its
 * own namespace, and an element created in the HTML one renders as nothing at
 * all. */
function svg(tag, className, attributes = {}) {
  const node = document.createElementNS(SVG, tag);
  if (className) node.setAttribute("class", className);
  for (const [name, value] of Object.entries(attributes)) {
    if (value != null) node.setAttribute(name, String(value));
  }
  return node;
}

/** fit truncates a label to what its box can hold.
 *
 * Boxes are sized from the label by `nodeWidth`, so this only bites on an ID
 * longer than the maximum width — and there it is better to lose the tail of a
 * name than to have it run out over the drawing. The whole ID is on the node's
 * tooltip either way. */
export function fit(id, width) {
  const room = Math.max(3, Math.floor((width - 14) / CHAR_WIDTH));
  return id.length <= room ? id : `${id.slice(0, room - 1)}…`;
}

/** path turns routed points into a polyline.
 *
 * Straight segments between dagre's points, rather than a curve through them:
 * the points *are* the route, and smoothing them would draw an edge that goes
 * somewhere the layout did not put it. */
export function path(points) {
  if (!points?.length) return "";
  return points.map((point, index) => `${index === 0 ? "M" : "L"} ${round(point.x)} ${round(point.y)}`).join(" ");
}

function round(value) {
  return Math.round(value * 10) / 10;
}

/** cycleIndex maps a stitch to the strongly connected component it is in.
 *
 * `cycles` is the projection's, and it is authoritative — nothing here walks
 * the edges looking for one. */
export function cycleIndex(snapshot) {
  const index = new Map();
  (snapshot?.cycles ?? []).forEach((members, at) => {
    for (const id of members ?? []) index.set(id, at);
  });
  return index;
}

/** focused is the set of stitches one thread's focus mode draws.
 *
 * The thread itself, and both ends of every dependency edge that touches it.
 * A cross-thread dependency is exactly what focus mode is for — isolating a
 * thread and then cutting the edges that leave it would hide the only thing
 * the graph knows that the thread tree does not.
 */
export function focused(snapshot, root) {
  const members = new Set(
    (snapshot?.stitches ?? []).filter((stitch) => stitch.root_id === root).map((stitch) => stitch.id),
  );
  const set = new Set(members);
  for (const edge of snapshot?.dependency_edges ?? []) {
    if (members.has(edge.from)) set.add(edge.to);
    if (members.has(edge.to)) set.add(edge.from);
  }
  return set;
}

/** keeper turns the reader's toggles into the predicate the layout filters on.
 *
 * Three separate questions, because they have three separate answers. Archived
 * and legacy are the archive trays — off by default on a loom that is 88%
 * history. Tied is different: a tied stitch that is still in threads/ is what
 * a satisfied dependency points at, so it is drawn unless asked otherwise.
 */
export function keeper(snapshot, { archived = false, legacy = false, tied = true, focus = "" } = {}) {
  const within = focus ? focused(snapshot, focus) : null;
  return (stitch) => {
    if (within && !within.has(stitch.id)) return false;
    if (stitch.legacy) return legacy;
    if (stitch.archived) return archived;
    if (terminal(stitch)) return tied;
    return true;
  };
}

/** stateClass is how a node's lifecycle state reads.
 *
 * `waiting_inherited` is deliberately not folded into `waiting`: one is a
 * decision someone made and the only one `resume` can lift, the other is a
 * consequence of an ancestor. The tree draws that distinction too. */
export function stateClass(stitch) {
  const parts = [`state-${stitch.state}`];
  if (stitch.ready) parts.push("ready");
  if (stitch.waiting_inherited && stitch.state !== "waiting") parts.push("inherited");
  if (hidden(stitch)) parts.push("dim");
  return parts.join(" ");
}

/** brokenNote finds the diagnostic that explains a broken edge.
 *
 * The reason (`missing`, `dropped`, `ambiguous`, `invalid`) says what kind of
 * broken it is; the diagnostic says it in the loom's own words. Showing the
 * first without the second makes a repairable problem look like a rendering
 * bug. */
export function brokenNote(snapshot, edge) {
  const found = (snapshot?.diagnostics ?? []).find(
    (diagnostic) => diagnostic.stitch_id === edge.from && diagnostic.target_id === edge.to,
  );
  const reason = edge.reason ?? "broken";
  return found ? `${reason}: ${found.message}` : `${edge.from} needs ${edge.to} — ${reason}`;
}

/** marker defines one arrowhead. One per status, because a marker does not
 * inherit the stroke of the path that uses it. */
function marker(id, className) {
  const node = svg("marker", `arrow ${className}`, {
    id,
    viewBox: "0 0 8 8",
    refX: 7,
    refY: 4,
    markerWidth: 7,
    markerHeight: 7,
    markerUnits: "strokeWidth",
    orient: "auto-start-reverse",
  });
  node.append(svg("path", null, { d: "M 0 0 L 8 4 L 0 8 z" }));
  return node;
}

/** drawEdges puts the two edge types on the drawing, decomposition first so a
 * dependency arrow is never buried under a containment spine. */
function drawEdges(laid, cycles) {
  const group = svg("g", "edges");

  for (const edge of laid.decomposition) {
    const line = svg("path", "edge decomposition", { d: path(edge.points), fill: "none" });
    line.append(title(`${edge.from} contains ${edge.to}`));
    group.append(line);
  }

  for (const edge of laid.dependencies) {
    // Both ends inside one component: this edge is part of the stall, not
    // merely touching it.
    const stalled =
      cycles.has(edge.from) && cycles.get(edge.from) === cycles.get(edge.to);
    const status = stalled ? "cycle" : edge.status;
    const line = svg("path", `edge dependency ${status}`, {
      d: path(edge.points),
      fill: "none",
      "marker-end": `url(#arrow-${status})`,
      // Which stitches an edge runs between, so hovering one can light the
      // edges in and out of it without re-deriving the graph from the DOM.
      "data-from": edge.from,
      "data-to": edge.to,
    });
    line.append(title(`${edge.from} needs ${edge.to} — ${stalled ? "in a cycle" : edge.status}`));
    group.append(line);
  }
  return group;
}

/** drawBrokenLabels writes the reason beside every broken edge.
 *
 * Separately from the edges so a label is never drawn under a later path. */
function drawBrokenLabels(laid, snapshot) {
  const group = svg("g", "edge-labels");
  for (const edge of laid.dependencies) {
    if (edge.status !== "broken") continue;
    const at = midpoint(edge.points);
    if (!at) continue;
    const text = svg("text", "edge-label broken", { x: round(at.x), y: round(at.y - 4), "text-anchor": "middle" });
    text.textContent = edge.reason ?? "broken";
    text.append(title(brokenNote(snapshot, edge)));
    group.append(text);
  }
  return group;
}

function midpoint(points) {
  if (!points?.length) return null;
  return points[Math.floor(points.length / 2)];
}

/** drawCycles boxes each strongly connected component and names it.
 *
 * Nothing in a cycle can ever become ready, and no amount of working the loom
 * changes that until an edge is removed. It is the single most useful thing
 * this view can say, so it is said in the largest terms the drawing has. */
function drawCycles(laid, snapshot) {
  const group = svg("g", "cycle-marks");
  for (const members of snapshot?.cycles ?? []) {
    const boxes = (members ?? []).map((id) => laid.boxes.get(id)).filter(Boolean);
    if (boxes.length === 0) continue;

    const pad = 14;
    const left = Math.min(...boxes.map((box) => box.x)) - pad;
    const top = Math.min(...boxes.map((box) => box.y)) - pad - 14;
    const right = Math.max(...boxes.map((box) => box.x + box.width)) + pad;
    const bottom = Math.max(...boxes.map((box) => box.y + box.height)) + pad;

    const mark = svg("g", "cycle-mark");
    mark.append(
      svg("rect", "cycle-box", {
        x: round(left),
        y: round(top),
        width: round(right - left),
        height: round(bottom - top),
        rx: 10,
      }),
    );
    const label = svg("text", "cycle-label", { x: round(left + 8), y: round(top + 14) });
    label.textContent = `cycle — never ready: ${members.join(" → ")} → ${members[0]}`;
    mark.append(label);
    group.append(mark);
  }
  return group;
}

/** drawNodes draws the boxes, in the drawing's own order.
 *
 * A node is a row about a stitch, the same contract the lists use: it carries
 * `data-stitch-id` and decides nothing about selection. */
function drawNodes(laid, cycles, focus) {
  const group = svg("g", "nodes");
  for (const stitch of laid.nodes) {
    const box = laid.boxes.get(stitch.id);
    if (!box) continue;

    const classes = ["node", stateClass(stitch)];
    if (cycles.has(stitch.id)) classes.push("in-cycle");
    // In focus mode, a node from another thread is here only because an edge
    // reaches it. It is drawn as the outsider it is.
    if (focus && stitch.root_id !== focus) classes.push("outside");
    const node = selectable(svg("g", classes.join(" "), { tabindex: -1 }), stitch.id);

    node.append(
      svg("rect", "box", {
        x: round(box.x),
        y: round(box.y),
        width: round(box.width),
        height: round(box.height),
        rx: 7,
      }),
    );

    const text = svg("text", "label", {
      x: round(box.cx),
      y: round(box.cy),
      "text-anchor": "middle",
      "dominant-baseline": "central",
    });
    text.textContent = fit(stitch.id, box.width);
    node.append(text);

    // The whole ID, the state, and why it cannot move — a box is too small to
    // say any of it, and hovering is free.
    node.append(title(describe(stitch, cycles)));
    group.append(node);
  }
  return group;
}

function describe(stitch, cycles) {
  const parts = [stitch.id, stitch.state];
  if (stitch.ready) parts.push("ready");
  if (stitch.waiting_inherited && stitch.state !== "waiting") parts.push("waiting above");
  if (stitch.queue_position != null) parts.push(`queue ${stitch.queue_position}`);
  if (hidden(stitch)) parts.push(stitch.legacy ? "legacy" : "archived");
  if (cycles.has(stitch.id)) parts.push("in a cycle");
  return parts.join(" · ");
}

function title(text) {
  const node = svg("title");
  node.textContent = text;
  return node;
}

/** counts summarises what the drawing contains, and what it left out. */
export function counts(snapshot, laid) {
  const dependencies = laid.dependencies;
  return {
    nodes: laid.nodes.length,
    hidden: (snapshot?.stitches?.length ?? 0) - laid.nodes.length,
    dependencies: dependencies.length,
    blocked: dependencies.filter((edge) => edge.status === "blocked").length,
    broken: dependencies.filter((edge) => edge.status === "broken").length,
    cycles: (snapshot?.cycles ?? []).length,
  };
}

function legend() {
  const bar = element("p", "legend");
  for (const [className, text] of [
    ["decomposition", "contains"],
    ["dependency satisfied", "needs — satisfied"],
    ["dependency inert", "needs — inert (archived)"],
    ["dependency blocked", "needs — blocked"],
    ["dependency broken", "needs — broken"],
  ]) {
    const key = element("span", `key ${className}`);
    key.append(element("span", "swatch"));
    key.append(element("span", "text", text));
    bar.append(key);
  }
  return bar;
}

/** renderGraph returns the graph view for a projection.
 *
 * `options` is what the reader has chosen — which of archived, legacy and tied
 * to draw, which thread is in focus, and where in the drawing they are looking.
 * `onChange` is handed the next set of choices and decides nothing itself; the
 * shell holds them, the way it holds the tree's folds.
 *
 * `viewport` is mutated in place by panning and zooming rather than reported,
 * so the shell's copy is the same object and a re-render lands the reader
 * where they were.
 */
export function renderGraph(snapshot, options = {}) {
  const { onChange = () => {}, onReveal = () => {}, viewport = null } = options;
  const view = element("section", "graph");
  const laid = layout(snapshot, { keep: keeper(snapshot, options), rankdir: options.rankdir });
  const summary = counts(snapshot, laid);

  const header = element("header", "counts");
  header.append(element("span", "count ready", `${summary.nodes} drawn`));
  header.append(element("span", "count", `${summary.dependencies} dependencies`));
  if (summary.blocked > 0) header.append(element("span", "count blocked", `${summary.blocked} blocked`));
  if (summary.broken > 0) header.append(element("span", "count broken", `${summary.broken} broken`));
  if (summary.cycles > 0) header.append(element("span", "count broken", `${summary.cycles} cycle(s)`));
  if (summary.hidden > 0) {
    // Never silently: a filtered graph that does not say what it left out is a
    // lie about the shape of the work.
    header.append(element("span", "count muted", `${summary.hidden} not shown`));
  }
  view.append(header);

  if (laid.nodes.length === 0) {
    // An empty loom is a real state — this repo's own started as one — and it
    // is not an error. Neither is a filter that has hidden everything, and the
    // controls have to stay reachable so it can be undone.
    view.append(renderControls(snapshot, options, onChange, { zoom() {}, fit() {} }));
    view.append(
      element(
        "p",
        "muted",
        summary.hidden > 0
          ? "Nothing to draw with these filters."
          : "Nothing to draw. No active stitches in this loom.",
      ),
    );
    return view;
  }

  const canvas = element("div", "canvas");
  const drawing = svg("svg", "drawing", {
    // The drawing fills the canvas and the viewBox is what moves: panning and
    // zooming are arithmetic on it, so nothing here measures an element or
    // watches it resize.
    preserveAspectRatio: "xMidYMid meet",
    viewBox: `0 0 ${laid.width} ${laid.height}`,
    role: "img",
    "aria-label": `dependency graph: ${summary.nodes} stitches, ${summary.dependencies} dependency edges`,
  });

  const defs = svg("defs");
  for (const status of ["satisfied", "inert", "blocked", "broken", "cycle"]) {
    defs.append(marker(`arrow-${status}`, status));
  }
  drawing.append(defs);

  const cycles = cycleIndex(snapshot);
  drawing.append(drawCycles(laid, snapshot));
  drawing.append(drawEdges(laid, cycles));
  drawing.append(drawBrokenLabels(laid, snapshot));
  drawing.append(drawNodes(laid, cycles, options.focus ?? ""));

  // A viewport the shell has been holding is kept, so flipping a toggle does
  // not teleport the reader back to where they started. A first render, or one
  // the shell has nothing to hand over for, is placed by the drawing itself
  // once it can be measured.
  const fresh = !(viewport && viewport.w > 0);
  const place = fresh ? fitted(laid) : viewport;
  if (viewport) Object.assign(viewport, place);
  const zoom = wirePanZoom(drawing, viewport ?? place, laid, { fresh });
  wireHover(drawing);
  // The shell moves the selection — with the arrow keys, or from a link in the
  // detail pane — and this is how it says so. A selected node off the edge of
  // the viewport is a selection the reader cannot see.
  onReveal((id) => zoom.reveal(laid.boxes.get(id)));

  view.append(renderControls(snapshot, options, onChange, zoom));
  view.append(legend());
  canvas.append(drawing);
  view.append(canvas);
  return view;
}

// The graph view's layout: positions for nodes and edges, computed here and
// drawn by whoever asks.
//
// This stitch is the layout only. What the nodes and edges *look like* —
// colour by state, dependency status, cycles rendered as the hard stall they
// are — is graph-edge-rendering, and it reads the boxes and points this
// produces.
//
// ## Why dagre
//
// The rule the loom set is to pick a library rather than write a layout
// engine, and the choice was dagre or elkjs. The reasoning is written up in
// vendor/README.md; the short version is that elkjs is 1.6 MB against dagre's
// 48 KB in a binary whose entire distribution is that binary, and it has no ES
// module build in a frontend that has no build step.
//
// What that costs, stated plainly because it is the thing to revisit: dagre's
// cluster support is not real containment. So decomposition is laid out as a
// layered tree — parent above child, with the edges between them drawn as the
// containment spine — rather than as nested boxes. The fallback the stitch
// allows, taken deliberately.
//
// ## What runs where
//
// Layout runs here, on the client. The server ships the projection and nothing
// more: there is no layout endpoint, no cached geometry, and no second state
// model. Nothing in this file reads or writes anything under `.loom/`.

import dagre from "./vendor/dagre.esm.js";
import { hidden } from "./stitches.js";

// Node geometry. Fixed rather than measured: measuring means rendering every
// node, measuring it, laying out, and re-rendering, and a stitch ID is short
// enough that a width from its length is honest.
const NODE_HEIGHT = 34;
const NODE_MIN_WIDTH = 96;
const NODE_MAX_WIDTH = 260;
// Exported because the renderer truncates a label against the same figure the
// box was sized with: two different guesses about how wide a character is
// would draw text that overflows its own node.
export const CHAR_WIDTH = 7.5;
const PADDING = 24;

/** nodeWidth sizes a box to its label, within bounds. */
export function nodeWidth(id) {
  const wanted = Math.round(id.length * CHAR_WIDTH) + PADDING;
  return Math.max(NODE_MIN_WIDTH, Math.min(NODE_MAX_WIDTH, wanted));
}

/** visible is the set of stitches the graph draws.
 *
 * Archived and legacy records are filtered out by default, the same rule every
 * other view follows. bopOS's loom is 446 stitches with 393 of them legacy: a
 * graph of all of them is unreadable, and laying it out is slow enough to be
 * felt.
 *
 * `keep` replaces that default outright. Which stitches a reader has asked to
 * see — archived, legacy, tied, one thread in focus — is the view's business,
 * not the layout's; this takes the answer rather than growing an option per
 * question.
 */
export function visible(snapshot, { keep = null } = {}) {
  const stitches = snapshot?.stitches ?? [];
  return stitches.filter(keep ?? ((stitch) => !hidden(stitch)));
}

/** layout positions the combined decomposition and dependency graph.
 *
 * Returns boxes keyed by stitch ID, the two edge lists with their routed
 * points, and the size of the whole drawing. It is a pure function of the
 * projection: no DOM, no fetch, no measurement.
 */
export function layout(snapshot, options = {}) {
  // Left-to-right by default, because a rank is a depth and every goal stitch
  // is depth 0. Top-down puts all of them in one row and makes the drawing as
  // wide as the loom has threads and only as tall as it is deep — a shape that
  // has to be zoomed out to be seen at all. Left-to-right turns each thread
  // into a band and stacks the threads down the page, which is the direction a
  // pane scrolls and the shape the thread tree already reads in.
  const { keep = null, rankdir = "LR" } = options;

  const nodes = visible(snapshot, { keep });
  const known = new Set(nodes.map((stitch) => stitch.id));

  // An empty loom is a real state, and this repo's own loom started as one.
  // dagre handles an empty graph, but the callers of this should not have to
  // find that out by trying it.
  if (nodes.length === 0) {
    return { nodes: [], boxes: new Map(), decomposition: [], dependencies: [], width: 0, height: 0 };
  }

  const g = new dagre.graphlib.Graph({ multigraph: true, compound: false });
  g.setGraph({
    rankdir,
    // Room for a dependency edge to travel sideways between ranks without
    // running along a node's edge.
    ranksep: 56,
    nodesep: 28,
    edgesep: 16,
    marginx: 16,
    marginy: 16,
  });
  g.setDefaultEdgeLabel(() => ({}));

  for (const stitch of nodes) {
    g.setNode(stitch.id, { width: nodeWidth(stitch.id), height: NODE_HEIGHT, stitch });
  }

  // Decomposition first: it is the strict tree, and it is what gives the
  // drawing its shape. Ranking follows it, so a child always sits below its
  // parent whatever the dependency edges do.
  const decomposition = [];
  for (const edge of snapshot.decomposition_edges ?? []) {
    if (!known.has(edge.parent) || !known.has(edge.child)) continue;
    g.setEdge(edge.parent, edge.child, { weight: 4, minlen: 1 }, "decomposition");
    decomposition.push({ from: edge.parent, to: edge.child });
  }

  // Dependencies are laid over that. They cross branches and threads by
  // design, so they get no ranking weight to speak of — a needs/ edge must not
  // drag a stitch out of its own thread's column.
  const dependencies = [];
  for (const edge of snapshot.dependency_edges ?? []) {
    if (!known.has(edge.from) || !known.has(edge.to)) continue;
    // The edge points from the dependency to the dependent, so the thing that
    // has to happen first is drawn above the thing waiting on it.
    g.setEdge(edge.to, edge.from, { weight: 1, minlen: 1 }, "dependency");
    dependencies.push({ from: edge.from, to: edge.to, status: edge.status, reason: edge.reason ?? null });
  }

  dagre.layout(g);

  const boxes = new Map();
  for (const id of g.nodes()) {
    const node = g.node(id);
    if (!node) continue;
    boxes.set(id, {
      id,
      stitch: node.stitch,
      // dagre centres a node on x/y; a box is easier to draw from its corner.
      x: node.x - node.width / 2,
      y: node.y - node.height / 2,
      cx: node.x,
      cy: node.y,
      width: node.width,
      height: node.height,
    });
  }

  for (const edge of decomposition) route(g, edge, "decomposition", boxes);
  for (const edge of dependencies) route(g, edge, "dependency", boxes);

  const graph = g.graph();
  return {
    nodes,
    boxes,
    decomposition,
    dependencies,
    // A single node has no edges and dagre still reports a size for it; a
    // caller sizing an SVG from this never gets a zero-by-zero viewport.
    width: Math.max(graph.width ?? 0, 1),
    height: Math.max(graph.height ?? 0, 1),
  };
}

/** route copies dagre's routed points onto an edge, falling back to a straight
 * line between the two boxes when there are none. */
function route(g, edge, name, boxes) {
  const from = name === "dependency" ? edge.to : edge.from;
  const to = name === "dependency" ? edge.from : edge.to;
  const routed = g.edge(from, to, name);
  if (routed?.points?.length) {
    edge.points = name === "dependency" ? routed.points.slice().reverse() : routed.points;
    return;
  }
  const a = boxes.get(edge.from);
  const b = boxes.get(edge.to);
  edge.points = a && b ? [{ x: a.cx, y: a.cy }, { x: b.cx, y: b.cy }] : [];
}

// The shell. Hand-written ES modules, loaded natively by the browser: there is
// no bundler and no framework, so what runs is what is in this directory.
//
// The page is two panes: a list on top, and what the selected stitch entails
// below. That is the shape the TUI has, and the reason for it is the same —
// reading a row and reading what it means is one act, not two pages.
//
// The shell owns three things the views do not: which loom and view the URL
// names, which stitch is selected in each list, and where the reader put the
// split. The views render into whatever element they are handed and say which
// stitch each row is about; they decide nothing about selection or layout.
//
// The loom and the view still come out of the URL on every load. Nothing here
// keeps a "current loom" either — two tabs on two looms are two URLs.

import { watch } from "./events.js";
import { renderDiagnostics, renderFrontier } from "./frontier.js";
import { renderGraph } from "./graphview.js";
import { renderImpact } from "./impact.js";
import { Mutations, neighbourMove, toggle } from "./mutate.js";
import { renderQueue } from "./queue.js";
import { draggable, dropCommand, unchanged } from "./reorder.js";
import { element, hidden, selectionPath, viewPath } from "./stitches.js";
import { renderStitch } from "./stitch.js";
import { renderTree } from "./tree.js";

// The views the nav offers.
const views = ["frontier", "threads", "queue", "graph", "impact"];

// The views that are lists of stitches, in the order `tab` cycles them —
// which is the order the TUI cycles its panes.
const lists = ["frontier", "threads", "queue", "impact"];

/** route parses a URL into the loom, the view, and the selected stitch.
 *
 * The shape is:
 *
 *     /l/<slug>/<view>?stitch=<stitch-id>
 *
 * The view is a path segment because it is a place the reader navigates to;
 * the selection is a query parameter because it moves under them as they arrow
 * down a list. Keeping it out of the path also means the server's routes never
 * have to learn about selection, and a stitch ID containing a slash needs no
 * escaping the server would then have to undo.
 *
 * `/l/<slug>/stitch/<id>` is the old per-page route. It still parses, and the
 * shell resolves it onto a list view with that stitch selected — links to it
 * exist in people's notes.
 */
export function route(href) {
  const url = new URL(href, location.origin);
  const parts = url.pathname.split("/").filter(Boolean);
  if (parts[0] !== "l" || parts.length < 2) return null;

  const slug = decodeURIComponent(parts[1]);
  const named = parts[2] ?? "frontier";
  if (named === "stitch") {
    return { slug, view: "", stitchId: decodeURIComponent(parts.slice(3).join("/")), legacy: true };
  }
  return {
    slug,
    view: views.includes(named) ? named : "frontier",
    stitchId: url.searchParams.get("stitch") ?? "",
    legacy: false,
  };
}

async function getJSON(url) {
  const response = await fetch(url, { headers: { accept: "application/json" } });
  const body = await response.json().catch(() => null);
  if (!response.ok) {
    throw new Error(body?.error?.message ?? `${url}: ${response.status}`);
  }
  return body;
}

/** age renders a snapshot's age the way the status bar wants it. */
export function age(ms) {
  if (ms == null) return "";
  if (ms < 1000) return `${ms}ms old`;
  const seconds = Math.round(ms / 1000);
  if (seconds < 90) return `${seconds}s old`;
  return `${Math.round(seconds / 60)}m old`;
}

// What the reader has arranged, none of which leaves the browser.
//
// Each list keeps its own selection, the way the TUI's panes do: switching to
// the queue and back must not lose your place on the frontier.
const state = {
  slug: "",
  view: "frontier",
  // The graph keeps a selection like the lists do: a node is a row about a
  // stitch, and clicking one has to put that stitch in the detail pane.
  selection: { frontier: "", threads: "", queue: "", graph: "", impact: "" },
  envelope: null,
  // What the live-update stream is saying. `live` is whether the stream is
  // connected at all: a page that has stopped hearing from the server must
  // say so rather than quietly showing a photograph again. `refreshing` is a
  // snapshot running right now, which is seconds on a large loom.
  // Optimistic until proven otherwise: on a fresh load there is no reason yet
  // to warn that the page is not live, and the stream connects in milliseconds.
  live: true,
  refreshing: false,
  // What the last queue command did, or why the loom refused it. Display
  // state with the lifetime of the next one.
  notice: "",
  refused: false,
  // Which subtrees the reader has folded open or shut in the thread tree,
  // against the default of open-unless-finished.
  folds: { collapsed: new Set(), expanded: new Set() },
  // What the graph is showing and where in it the reader is looking. Same
  // standing as the folds: it is theirs, it survives a re-render, and it goes
  // nowhere. `viewport` is mutated in place by panning, so flipping a toggle
  // redraws the graph without moving it under them.
  // Which zero-impact tails the reader has opened in the impact view. Same
  // standing as the folds and the graph's toggles.
  impact: { zeroLooseEnds: false, zeroParked: false },
  graph: {
    archived: false,
    legacy: false,
    tied: true,
    focus: "",
    rankdir: "LR",
    viewport: { x: 0, y: 0, w: 0, h: 0 },
  },
};

// The graph's way of bringing a node into view, handed over by the drawing
// that is currently on screen. Null in every other view.
let revealInGraph = null;

/** say puts a line in the footer: what a queue command did, or why the loom
 * refused it.
 *
 * Every queue move announces itself. A stray keystroke that reorders the queue
 * and says nothing is how this repo lost track of its own plan once already,
 * and it was found in `git diff` rather than on screen. */
function say(text, { refused = false } = {}) {
  state.notice = text;
  state.refused = refused;
  renderStatus();
}

// The queue's write half. It owns the one command in flight and the overlay
// that command is asserting; the shell owns nothing about it but the redraw.
const mutations = new Mutations({
  slug: () => state.slug,
  snapshot: () => projection(),
  generation: () => state.envelope?.generation ?? 0,
  onChange: () => {
    renderList();
    reconcile();
  },
  onSay: (text) => say(text),
  onFail: (err) => {
    // The loom's own words, and its stderr when it had any: an unrelated stale
    // queue record makes an unrelated mutation fail without changing the file,
    // and only the loom's output explains that.
    say(err?.stderr || String(err?.message ?? err), { refused: true });
  },
});

/** onQueueToggle is the one-click way on and off the queue, from any row.
 *
 * The queue is opt-in and unused on the loom this was written against, so
 * promoting a stitch has to be trivial or nobody will ever do it. */
function onQueueToggle(id, { queued } = {}) {
  const command =
    queued === undefined
      ? toggle(projection(), id)
      : { command: queued ? "unqueue" : "append", id };
  mutations
    .run(command)
    .catch((err) => say(String(err?.message ?? err), { refused: true }));
}

/** onDrop turns a drop into one loom command.
 *
 * The index is where the row landed among the others; which anchor that
 * resolves to is `dropCommand`'s decision, because the frontier is a mixed list
 * and the row a stitch lands next to often has no queue position at all. */
function onDrop(id, index) {
  const snapshot = projection();
  const order = rows().map((row) => row.dataset.stitchId);
  const queued = new Set(
    (snapshot?.stitches ?? [])
      .filter((stitch) => stitch.queue_position != null)
      .map((stitch) => stitch.id),
  );

  const command = dropCommand(order, queued, id, index);
  // Picking a row up and putting it back must not cost a subprocess, a
  // re-snapshot, and a line in the footer claiming something happened.
  if (unchanged(snapshot, command)) return;
  mutations.run(command).catch((err) => say(String(err?.message ?? err), { refused: true }));
}

/** mutate runs one queue command against the selected stitch.
 *
 * The keys are the terminal's: Q puts a stitch on the queue or takes it off,
 * J and K move it one place. Dragging a row, and the one-click add/remove on
 * every stitch, are queue-reorder-ui. */
function mutate(key) {
  const id = selected();
  if (!id) return;
  const snapshot = projection();
  if (key === "Q") return mutations.run(toggle(snapshot, id));
  return mutations.run(neighbourMove(snapshot, id, key === "J" ? 1 : -1));
}

const listPane = () => document.getElementById("list");
const detailPane = () => document.getElementById("detail");

/** selected is the stitch the detail pane is drawing. */
function selected() {
  return state.selection[state.view] ?? "";
}

function renderNav() {
  const nav = document.getElementById("views");
  nav.replaceChildren(
    ...views.map((view) => {
      const link = element("a", null, view);
      // A nav link carries the selection that view is already holding, so a
      // middle-click opens the place a plain click would have gone to.
      const held = state.selection[view];
      link.href = held ? selectionPath(state.slug, view, held) : viewPath(state.slug, view);
      if (view === state.view) link.setAttribute("aria-current", "page");
      link.addEventListener("click", (event) => {
        if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey) return;
        event.preventDefault();
        show(view, { push: true });
      });
      return link;
    }),
  );
}

/** report puts an error where the list would have been. */
function report(err) {
  listPane().replaceChildren(element("p", "error", String(err?.message ?? err)));
}

/** ageOf is how old the projection is right now.
 *
 * The server's age_ms is how old it was when it was fetched, which was the
 * whole answer while the page only changed when someone pressed a button. On
 * a page that stays open it would sit there saying "2s old" for an hour, so
 * the elapsed time is counted off captured_at from here.
 */
function ageOf(envelope) {
  if (!envelope) return null;
  if (!envelope.captured_at) return envelope.age_ms ?? null;
  const captured = Date.parse(envelope.captured_at);
  if (Number.isNaN(captured)) return envelope.age_ms ?? null;
  return Math.max(0, Date.now() - captured);
}

/** renderAge says how old the projection is, whether to trust it, and whether
 * the page is still hearing from the server at all. */
function renderAge(envelope) {
  const node = document.getElementById("snapshot-age");
  const parts = [];
  if (state.refreshing) parts.push("refreshing…");
  if (envelope.stale) parts.push("stale");
  // A disconnected stream is the one thing worth saying over the age itself:
  // everything on screen may already be wrong and nothing will correct it.
  if (!state.live) parts.push("not live");
  const old = age(ageOf(envelope));
  if (old) parts.push(old);

  node.textContent = parts.join(" · ");
  node.classList.toggle("stale", Boolean(envelope.stale) || !state.live);
  node.title = envelope.error || (state.live ? "" : "The live-update stream is down. Reconnecting…");
}

/** projection is the snapshot as it should be drawn: the server's, with any
 * unconfirmed queue command laid over it.
 *
 * Every view reads queue_position out of the projection, so laying the overlay
 * on here is what makes the queue, the frontier badge and the impact ranking
 * all respond on the keystroke — without any of them learning that a mutation
 * exists. */
function projection() {
  return mutations.project(state.envelope?.snapshot);
}

/** renderList draws the current view into the list pane. */
function renderList() {
  const pane = listPane();
  const envelope = state.envelope;
  renderAge(envelope);
  // The graph fills its pane and scrolls nothing: panning moves the viewBox,
  // not the scrollbars. Every other view is a list and scrolls normally.
  pane.classList.toggle("filled", state.view === "graph");
  // Whatever drawing was on screen has just been replaced; the one being drawn
  // now hands its own back.
  revealInGraph = null;

  if (!envelope.snapshot) {
    pane.replaceChildren(
      element("p", "muted", envelope.error || "No projection yet. Refresh to take one."),
    );
    return;
  }

  const snapshot = projection();
  const parts = [];
  if (state.view === "frontier") {
    parts.push(renderFrontier(snapshot, state.slug, onQueueToggle));
    const problems = renderDiagnostics(snapshot);
    if (problems) parts.push(problems);
  } else if (state.view === "threads") {
    parts.push(
      renderTree(snapshot, state.slug, state.folds, (entry) => {
        // Folding is the reader's, not the loom's: it survives a re-render but
        // nothing sends it anywhere.
        const set = entry.expanded ? state.folds.collapsed : state.folds.expanded;
        const other = entry.expanded ? state.folds.expanded : state.folds.collapsed;
        set.add(entry.id);
        other.delete(entry.id);
        renderList();
        // Folding can take the selected row away with it, and then the
        // fallback has to reach the detail pane and the URL too.
        reconcile();
      }),
    );
  } else if (state.view === "queue") {
    parts.push(renderQueue(snapshot, state.slug, onQueueToggle));
  } else if (state.view === "impact") {
    parts.push(
      renderImpact(snapshot, state.slug, state.impact, (change) => {
        // Which of the zero-impact tails are open is the reader's, the same
        // standing as the tree's folds: it survives a re-render and goes
        // nowhere.
        Object.assign(state.impact, change);
        renderList();
        reconcile();
      }),
    );
  } else if (state.view === "graph") {
    parts.push(
      renderGraph(snapshot, {
        ...state.graph,
        // How the shell brings a node the reader has selected into the
        // viewport. The graph pans rather than scrolls, so `scrollIntoView`
        // has nothing to move.
        onReveal: (reveal) => {
          revealInGraph = reveal;
        },
        onChange: ({ archived, legacy, tied, focus, rankdir }) => {
          // Turning the ranks the other way is a different drawing, not the
          // same one filtered: the coordinates the reader was looking at mean
          // nothing in it, so the viewport is given up and re-placed. Emptying
          // it rather than replacing it keeps the object the graph holds.
          if (rankdir !== state.graph.rankdir) {
            Object.assign(state.graph.viewport, { x: 0, y: 0, w: 0, h: 0 });
          }
          // A filter change can take the drawing's whole shape with it, so the
          // view is redrawn and the selection settled against what is left —
          // the same thing folding a subtree does to the tree.
          Object.assign(state.graph, { archived, legacy, tied, focus, rankdir });
          renderList();
          reconcile();
        },
      }),
    );
  }
  pane.replaceChildren(...parts);

  // Both ordered lists can be reordered by dragging. The tree and the graph
  // cannot: their order is the decomposition's, not a preference anyone can
  // express, and the impact ranking's order is arithmetic.
  if (state.view === "frontier" || state.view === "queue") {
    const list = pane.querySelector("ol");
    if (list) draggable(list, { onDrop });
  }
}

/** rows is the list pane's selectable rows, in draw order.
 *
 * It is read off the DOM rather than recomputed, so it is exactly what the
 * reader can see: a folded subtree contributes no rows, and neither does a
 * queue record naming a stitch the projection has not got. */
function rows() {
  return Array.from(listPane().querySelectorAll("[data-stitch-id]"));
}

/** known is every stitch ID the projection carries. */
function known() {
  return new Set((state.envelope?.snapshot?.stitches ?? []).map((stitch) => stitch.id));
}

/** markSelection settles the selection against what is drawn, and paints it.
 *
 * A selection the projection no longer has — tied, dropped or renamed under
 * us — is not an error state. It falls back to the first row, and says
 * nothing.
 *
 * A stitch the projection does have but this list is not showing is a
 * different thing and is kept: a dependency link can select something the
 * frontier does not list, and the detail pane is the authority on what is
 * being read. Nothing is highlighted then, and elsewhere() says where it
 * would be. */
function markSelection({ scroll = false } = {}) {
  const drawn = rows();
  const ids = drawn.map((row) => row.dataset.stitchId);
  let id = selected();
  if (id && !known().has(id)) id = "";
  if (!id) id = ids[0] ?? "";
  if (state.view in state.selection) state.selection[state.view] = id;

  let node = null;
  for (const row of drawn) {
    const isSelected = row.dataset.stitchId === id;
    row.classList.toggle("selected", isSelected);
    row.setAttribute("aria-selected", String(isSelected));
    if (isSelected) node = row;
  }
  if (scroll && node) {
    if (state.view === "graph") revealInGraph?.(id);
    else node.scrollIntoView({ block: "nearest" });
  }
  return id;
}

// The stitch the detail pane last asked for. A response for anything else has
// been overtaken and is dropped: arrowing down a list must never land an
// earlier stitch's contents under a later one.
let pending = "";

// What has already been fetched, keyed by generation and ID. Moving up and
// down a list costs one request per stitch, not one per keystroke — and the
// generation in the key is what makes that safe, because a stitch's
// instructions can change under a projection that has not moved.
const details = new Map();
let cachedGeneration = null;

/** cache returns the store for the current generation, emptied if the
 * projection has moved on. */
function cache() {
  const generation = state.envelope?.generation ?? 0;
  if (generation !== cachedGeneration) {
    details.clear();
    cachedGeneration = generation;
  }
  return details;
}

/** elsewhere names the list that would show a stitch the current one does not.
 *
 * Selecting from a link inside the instructions can reach anything. Switching
 * views out from under the reader would be worse than saying where it is. */
function elsewhere(id) {
  if (rows().some((row) => row.dataset.stitchId === id)) return null;
  const snapshot = state.envelope?.snapshot;
  const stitch = snapshot?.stitches?.find((entry) => entry.id === id);
  if (!stitch) return null;
  // The thread tree carries everything active, so a stitch missing from it is
  // inside a subtree the reader folded rather than somewhere else entirely.
  if (state.view === "threads" && !hidden(stitch)) return "it is folded out of sight in this tree";
  if ((snapshot.frontier ?? []).includes(id)) return "the frontier shows it";
  if (stitch.queue_position != null) return "the queue shows it";
  if (!hidden(stitch)) return "the thread tree shows it";
  return "no list shows it — it is archived";
}

// What the pane is currently drawing, and the note drawn above it. Held so the
// note can be brought back into step with a list that has changed shape,
// without asking the server for a stitch we already have.
let shownDetail = null;
let shownNote = null;

/** paint puts one stitch's detail in the pane. */
function paint(detail, { keepScroll = false } = {}) {
  const pane = detailPane();
  const at = pane.scrollTop;
  const where = elsewhere(detail.stitch.id);
  const parts = [];
  if (where) parts.push(element("p", "elsewhere", `Not in this list — ${where}.`));
  parts.push(renderStitch(detail, state.envelope?.snapshot, state.slug));
  pane.replaceChildren(...parts);
  pane.removeAttribute("aria-busy");
  pane.scrollTop = keepScroll ? at : 0;
  shownDetail = detail;
  shownNote = where;
}

/** refreshNote brings the "not in this list" line back into step after the
 * list has changed shape under an unchanged selection — folding away the
 * subtree the selected row was in is the case. */
function refreshNote() {
  if (!shownDetail) return;
  if (elsewhere(shownDetail.stitch.id) === shownNote) return;
  paint(shownDetail, { keepScroll: true });
}

/** renderDetail draws the selected stitch below the list.
 *
 * The projection carries neither the instructions nor the stitch directory, so
 * this is a second request keyed on the selection.
 */
async function renderDetail() {
  const pane = detailPane();
  const id = selected();
  pending = id;

  if (!id) {
    pane.removeAttribute("aria-busy");
    pane.replaceChildren(element("p", "muted", "No stitch selected."));
    return;
  }

  const store = cache();
  if (store.has(id)) {
    paint(store.get(id));
    return;
  }

  // Nothing is cleared before the request: a slow fetch leaves the previous
  // stitch readable rather than blanking the pane under someone mid-sentence.
  pane.setAttribute("aria-busy", "true");
  const detail = await getJSON(
    `/api/l/${encodeURIComponent(state.slug)}/stitch/${encodeURIComponent(id)}`,
  );
  store.set(id, detail);
  // Overtaken: the reader has moved on, and this response belongs to a stitch
  // that is no longer selected. It is kept in the cache and not drawn.
  if (pending !== id) return;
  paint(detail);
}

/** drawDetail renders the detail pane, putting a failure in the pane it
 * belongs to rather than over the list. */
function drawDetail() {
  const asked = selected();
  renderDetail().catch((err) => {
    if (pending !== asked) return;
    const pane = detailPane();
    pane.removeAttribute("aria-busy");
    pane.replaceChildren(element("p", "error", String(err?.message ?? err)));
  });
}

/** syncURL makes the address bar say what is on screen.
 *
 * The selection is not always the one the URL asked for: a load with no
 * `?stitch`, or one naming a stitch that has since been tied, settles on the
 * first row instead. Replacing — never pushing — keeps that honest without
 * putting a place in the history the reader never chose to go. */
function syncURL() {
  const held = selected();
  const url = held ? selectionPath(state.slug, state.view, held) : viewPath(state.slug, state.view);
  if (location.pathname + location.search !== url) history.replaceState(null, "", url);
}

/** reconcile settles the selection against a list that has just been redrawn,
 * and follows it through if the fallback moved it. */
function reconcile() {
  const before = selected();
  if (markSelection() === before) {
    refreshNote();
    return;
  }
  syncURL();
  renderNav();
  drawDetail();
}

/** draw renders everything from the current state. */
function draw() {
  renderList();
  markSelection();
  syncURL();
  renderNav();
  drawDetail();
}

/** select moves the selection within the current list.
 *
 * It replaces the URL rather than pushing it: arrowing through a list must not
 * build twenty entries of back-button history. */
function select(id, { scroll = false } = {}) {
  if (!(state.view in state.selection)) return;
  if (state.selection[state.view] !== id) {
    state.selection[state.view] = id;
    history.replaceState(null, "", selectionPath(state.slug, state.view, id));
    markSelection({ scroll });
    renderNav();
    drawDetail();
    return;
  }
  markSelection({ scroll });
}

/** show switches the view, taking that view's own selection with it.
 *
 * Switching pushes, because a view is a place the reader will want the back
 * button to return to. */
function show(view, { push = false } = {}) {
  if (!views.includes(view)) return;
  state.view = view;
  const held = state.selection[view];
  const url = held ? selectionPath(state.slug, view, held) : viewPath(state.slug, view);
  if (push) history.pushState(null, "", url);
  else history.replaceState(null, "", url);
  draw();
}

/** step moves the selection by rows within the list pane. */
function step(delta) {
  const ids = rows().map((row) => row.dataset.stitchId);
  if (ids.length === 0) return;
  const at = ids.indexOf(selected());
  const next = at < 0 ? 0 : Math.min(ids.length - 1, Math.max(0, at + delta));
  select(ids[next], { scroll: true });
}

/** wireList binds the list pane: clicking a row selects it rather than
 * navigating, and the keys match the TUI's. */
function wireList() {
  const pane = listPane();

  pane.addEventListener("click", (event) => {
    // A row's ID stays a real link — that is what a middle-click or a copied
    // link address should give you — so a plain click is intercepted rather
    // than the anchor being replaced with something unlinkable.
    if (event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    if (event.target.closest(".twisty")) return;
    const row = event.target.closest("[data-stitch-id]");
    if (!row || !pane.contains(row)) return;
    event.preventDefault();
    pane.focus({ preventScroll: true });
    select(row.dataset.stitchId);
  });

  pane.addEventListener("keydown", (event) => {
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      step(1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      step(-1);
    } else if (event.key === "Q" || event.key === "J" || event.key === "K") {
      // Capitals, so a mutation is never one unshifted keystroke away, and the
      // same keys do the same thing in the terminal. Tested on event.key
      // rather than on shiftKey, which caps lock does not set.
      event.preventDefault();
      mutate(event.key).catch((err) => say(String(err?.message ?? err), { refused: true }));
    } else if (event.key === "Tab" && event.target === pane) {
      // Only while the pane itself has focus: once the reader has tabbed onto
      // a row's link or a twisty, Tab is the browser's again.
      event.preventDefault();
      const at = lists.indexOf(state.view);
      const from = at < 0 ? 0 : at;
      const next = (from + (event.shiftKey ? lists.length - 1 : 1)) % lists.length;
      show(lists[next], { push: true });
      pane.focus({ preventScroll: true });
    }
  });
}

/** wireDetail makes the stitch links inside the detail pane select rather than
 * navigate.
 *
 * A `[[stitch-id]]` in the instructions and a dependency edge both name a
 * stitch, and following one should move what is being read — not leave the
 * page the reader is reading it on. */
function wireDetail() {
  const pane = detailPane();
  pane.addEventListener("click", (event) => {
    if (event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const target = event.target.closest("[data-stitch-id]");
    if (!target || !pane.contains(target)) return;
    event.preventDefault();
    // Selecting scrolls the list to the target when the list has it, and
    // elsewhere() says where it is when the list has not.
    select(target.dataset.stitchId, { scroll: true });
  });
}

/** wireSplit lets the reader move the boundary between the panes.
 *
 * The fraction is the whole of the mechanism: no measuring of panes, no resize
 * observer, and nothing stored anywhere. Where they put it is view state, like
 * the tree's folds. */
function wireSplit() {
  const split = document.getElementById("split");
  const panes = document.getElementById("panes");
  let fraction = 0.5;

  // The list pane takes a share of the height and the detail pane takes the
  // rest — one number, so the two can never disagree about what is left.
  const put = (value) => {
    fraction = Math.min(0.85, Math.max(0.15, value));
    panes.style.setProperty("--list-size", `${(fraction * 100).toFixed(2)}%`);
    split.setAttribute("aria-valuenow", String(Math.round(fraction * 100)));
  };
  put(fraction);

  split.addEventListener("pointerdown", (event) => {
    event.preventDefault();
    split.setPointerCapture(event.pointerId);
    document.body.classList.add("splitting");
  });
  split.addEventListener("pointermove", (event) => {
    if (!split.hasPointerCapture(event.pointerId)) return;
    const box = panes.getBoundingClientRect();
    put((event.clientY - box.top) / box.height);
  });
  const release = (event) => {
    if (split.hasPointerCapture(event.pointerId)) split.releasePointerCapture(event.pointerId);
    document.body.classList.remove("splitting");
  };
  split.addEventListener("pointerup", release);
  split.addEventListener("pointercancel", release);

  split.addEventListener("keydown", (event) => {
    if (event.key === "ArrowUp") put(fraction - 0.05);
    else if (event.key === "ArrowDown") put(fraction + 0.05);
    else return;
    event.preventDefault();
  });
}

/** refresh asks for a new projection.
 *
 * It does not wait for one. The API accepts the request and returns at once
 * because the result arrives over the event stream, and the stream is also
 * what says a run is in flight — so the button reports progress it is told
 * about rather than progress it went looking for.
 *
 * With the stream down there is nothing to be told, so it falls back to asking
 * the snapshot endpoint whether the generation has moved. Reads are served
 * from cache, so asking costs nothing.
 */
async function refresh() {
  const before = state.envelope?.generation ?? 0;
  await fetch(`/api/l/${encodeURIComponent(state.slug)}/refresh`, { method: "POST" });
  if (state.live) return;

  const button = document.getElementById("refresh");
  button.disabled = true;
  button.textContent = "refreshing…";
  try {
    for (let attempt = 0; attempt < 120; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 500));
      if (state.live) return; // the stream came back and will say when it lands
      const envelope = await getJSON(snapshotURL(state.slug));
      if (envelope.generation !== before || envelope.stale) {
        state.envelope = envelope;
        draw();
        return;
      }
    }
  } finally {
    button.disabled = false;
    button.textContent = "refresh";
  }
}

/** live starts the event stream and keeps the page in step with the loom.
 *
 * The event says the generation moved; it never carries the projection. So
 * this fetches the snapshot the same way the first load did — the one code
 * path that turns a generation into something drawn — and redraws.
 */
function goLive() {
  return watch(state.slug, {
    onLive(connected) {
      state.live = connected;
      // Nothing is known about a run once the stream is gone; claiming one is
      // still going would leave the button stuck on "refreshing…".
      if (!connected) state.refreshing = false;
      renderStatus();
    },
    onSnapshot(event) {
      state.refreshing = Boolean(event.refreshing);
      const moved =
        event.generation !== (state.envelope?.generation ?? 0) ||
        Boolean(event.stale) !== Boolean(state.envelope?.stale);
      if (!moved) {
        renderStatus();
        return;
      }
      getJSON(snapshotURL(state.slug))
        .then((envelope) => {
          state.envelope = envelope;
          // The projection is authoritative and we are a viewer: whatever the
          // optimistic overlay believed about the queue is discarded here,
          // silently, and whether or not the loom agrees with it.
          mutations.settle();
          draw();
        })
        .catch(report);
    },
  });
}

/** renderStatus updates the chrome that is about the snapshot rather than
 * about the loom — the age, and what the refresh button is doing — without
 * redrawing either pane. Most events change only this. */
function renderStatus() {
  if (state.envelope) renderAge(state.envelope);
  const notice = document.getElementById("notice");
  if (notice) {
    notice.textContent = state.notice;
    notice.classList.toggle("refused", state.refused);
  }
  const button = document.getElementById("refresh");
  if (!button) return;
  button.disabled = state.refreshing;
  button.textContent = state.refreshing ? "refreshing…" : "refresh";
}

function snapshotURL(slug) {
  return `/api/l/${encodeURIComponent(slug)}/snapshot`;
}

/** land resolves an old /stitch/<id> URL onto a list view holding it selected.
 *
 * The frontier if it is ready work, and the thread tree otherwise — the tree
 * carries everything active, so the reader ends up somewhere that can actually
 * show them the row, rather than on a list that does not contain it. */
function land(stitchId) {
  const ready = state.envelope?.snapshot?.frontier ?? [];
  const view = ready.includes(stitchId) ? "frontier" : "threads";
  state.view = view;
  state.selection[view] = stitchId;
  history.replaceState(null, "", selectionPath(state.slug, view, stitchId));
}

/** apply takes the URL as the source of truth. The first load and every
 * back-button press go through here. */
function apply(here) {
  state.slug = here.slug;
  if (here.legacy) {
    land(here.stitchId);
    return;
  }
  state.view = here.view;
  if (here.stitchId && here.view in state.selection) {
    state.selection[here.view] = here.stitchId;
  }
}

async function main() {
  const here = route(location.href);
  if (!here) {
    report(new Error("This URL does not name a loom."));
    return;
  }

  state.slug = here.slug;
  document.getElementById("loom-name").textContent = here.slug;
  document.getElementById("refresh").addEventListener("click", () => {
    refresh().catch(report);
  });
  wireList();
  wireDetail();
  wireSplit();

  window.addEventListener("popstate", () => {
    const back = route(location.href);
    if (!back || back.slug !== state.slug) {
      location.reload();
      return;
    }
    apply(back);
    draw();
  });

  try {
    const looms = await getJSON("/api/looms");
    const loom = looms.looms.find((entry) => entry.slug === here.slug);
    if (loom) {
      document.getElementById("loom-name").textContent = loom.name;
      document.getElementById("status").textContent = `${loom.root} · ${loom.tier}`;
    }
    state.envelope = await getJSON(snapshotURL(here.slug));
    apply(here);
    draw();
  } catch (err) {
    report(err);
  }

  // The stream is opened last, and outside the try: a page that could not
  // load is not a page that should start listening for changes to it, and a
  // stream that cannot connect must not stop the page that did load.
  goLive();

  // The age counts up on its own. Without this the header would say how old
  // the projection was when it arrived, forever.
  setInterval(renderStatus, 1000);
}

main().catch(report);

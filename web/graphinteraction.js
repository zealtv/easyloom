// What the reader can do to the graph: choose what is in it, move around it,
// and ask what one node is wired to.
//
// It is kept out of graphview.js on the same principle that kept layout out of
// rendering. That file answers "what does this loom look like" and is a pure
// function of the projection; this one answers "what is the reader doing", and
// everything in it is state that belongs to a person rather than to a loom.
// None of it leaves the browser and none of it is ever sent anywhere.
//
// Pan and zoom are conventional and deliberately unclever: drag to move, wheel
// to zoom about the pointer, buttons for the same thing from the keyboard. No
// gestures invented here.

import { element } from "./stitches.js";

// How far in and out the drawing can be taken, as a multiple of the size it
// was laid out at. Out far enough to see a whole loom, in far enough to read
// two overlapping edges apart.
const MIN_SCALE = 0.15;
const MAX_SCALE = 8;

// A drag shorter than this is a click. Selecting a node by clicking it must
// survive the hand wobbling three pixels.
const DRAG_SLOP = 3;

/** fitted is the viewport that shows the whole drawing. */
export function fitted(laid) {
  return { x: 0, y: 0, w: Math.max(laid.width, 1), h: Math.max(laid.height, 1) };
}

/** initial is where a reader who has not moved yet should be looking.
 *
 * Not the whole drawing. Threads stack down the page, so a loom of any size is
 * taller than the pane, and fitting all of it means fitting the height — which
 * shrinks the nodes past reading. Fitting the *width* and starting at the top
 * keeps a node the size it was drawn at and leaves the page to be panned, the
 * way every other list here is read.
 *
 * `aspect` is the canvas's width over its height. Without one — nothing has
 * been measured yet — the whole drawing is the honest answer.
 */
export function initial(laid, aspect) {
  const whole = fitted(laid);
  if (!aspect || !Number.isFinite(aspect) || aspect <= 0) return whole;
  const height = whole.w / aspect;
  // The drawing is shorter than a width-fitted viewport: it all fits already.
  if (height >= whole.h) return whole;
  return { x: 0, y: 0, w: whole.w, h: height };
}

/** clamped keeps a viewport within the zoom limits, about its own centre.
 *
 * The scale is read off the width alone: the aspect ratio is the drawing's and
 * nothing here changes it. */
export function clamped(viewport, laid) {
  const natural = Math.max(laid.width, 1);
  const scale = natural / viewport.w;
  if (scale >= MIN_SCALE && scale <= MAX_SCALE) return viewport;
  const wanted = natural / Math.min(MAX_SCALE, Math.max(MIN_SCALE, scale));
  const factor = wanted / viewport.w;
  return {
    x: viewport.x - (viewport.w * (factor - 1)) / 2,
    y: viewport.y - (viewport.h * (factor - 1)) / 2,
    w: viewport.w * factor,
    h: viewport.h * factor,
  };
}

/** zoomed scales a viewport about a fixed point, in the drawing's own
 * coordinates: whatever is under the pointer stays under the pointer. */
export function zoomed(viewport, at, factor, laid) {
  const next = {
    x: at.x - (at.x - viewport.x) / factor,
    y: at.y - (at.y - viewport.y) / factor,
    w: viewport.w / factor,
    h: viewport.h / factor,
  };
  return clamped(next, laid);
}

function apply(drawing, viewport) {
  drawing.setAttribute("viewBox", `${round(viewport.x)} ${round(viewport.y)} ${round(viewport.w)} ${round(viewport.h)}`);
}

function round(value) {
  return Math.round(value * 100) / 100;
}

/** at converts a pointer event to a point in the drawing's coordinates.
 *
 * The browser's own screen matrix does the work, so this holds however the
 * page is scrolled, scaled or laid out — there is no measuring of elements
 * here beyond asking for that matrix. */
function at(drawing, event, matrix) {
  const ctm = matrix ?? drawing.getScreenCTM()?.inverse();
  if (!ctm) return null;
  return new DOMPoint(event.clientX, event.clientY).matrixTransform(ctm);
}

/** wirePanZoom makes the drawing navigable and returns the handle its buttons
 * need.
 *
 * `viewport` is the reader's place in the drawing, and it is mutated in place
 * rather than reported: the shell holds the same object, so panning survives a
 * re-render — flipping a toggle must not throw away where you were looking.
 */
export function wirePanZoom(drawing, viewport, laid, { fresh = false } = {}) {
  apply(drawing, viewport);

  const put = (next) => {
    Object.assign(viewport, next);
    apply(drawing, viewport);
  };

  // A reader who has not moved yet gets the top of the drawing at readable
  // size, which needs the canvas's shape — and the canvas has no shape until
  // it is in the document. One measurement, one frame after this is built, and
  // only when there is no place of the reader's own to honour. Nothing watches
  // for a resize: `preserveAspectRatio` handles a pane that changes size.
  if (fresh) {
    requestAnimationFrame(() => {
      const box = drawing.getBoundingClientRect();
      if (box.width > 0 && box.height > 0) put(initial(laid, box.width / box.height));
    });
  }

  drawing.addEventListener(
    "wheel",
    (event) => {
      event.preventDefault();
      const point = at(drawing, event);
      if (!point) return;
      // Exponential in the wheel delta, so a trackpad and a notched mouse
      // wheel both feel like themselves.
      put(zoomed(viewport, point, Math.exp(-event.deltaY * 0.0015), laid));
    },
    { passive: false },
  );

  let start = null;
  drawing.addEventListener("pointerdown", (event) => {
    if (event.button !== 0) return;
    const matrix = drawing.getScreenCTM()?.inverse();
    const point = at(drawing, event, matrix);
    if (!point) return;
    // The matrix is taken once, at the start: it maps the screen onto the
    // viewport the drag began in, which is what the delta has to be measured
    // against as that viewport moves.
    start = { matrix, point, from: { ...viewport }, clientX: event.clientX, clientY: event.clientY, moved: false };
    // Deliberately not capturing the pointer yet. A captured pointer retargets
    // the click that follows it onto the capture element, so capturing here
    // would hand every click on a node to the canvas instead — and clicking a
    // node is how a stitch gets selected.
  });

  drawing.addEventListener("pointermove", (event) => {
    if (!start) return;
    if (!start.moved) {
      const far = Math.abs(event.clientX - start.clientX) + Math.abs(event.clientY - start.clientY);
      if (far < DRAG_SLOP) return;
      start.moved = true;
      drawing.classList.add("panning");
      // Now it is a pan, and the click it ends with is one this drawing is
      // going to swallow anyway. Capturing here keeps the drag alive when the
      // pointer leaves the canvas, which is the only reason to capture at all.
      drawing.setPointerCapture(event.pointerId);
    }
    const point = at(drawing, event, start.matrix);
    if (!point) return;
    put({ x: start.from.x - (point.x - start.point.x), y: start.from.y - (point.y - start.point.y) });
  });

  const release = (event) => {
    if (!start) return;
    if (drawing.hasPointerCapture(event.pointerId)) drawing.releasePointerCapture(event.pointerId);
    drawing.classList.remove("panning");
    // A drag that moved is not a click on whatever it started over. The shell
    // selects on click, and dragging the canvas must not select a node.
    if (start.moved) {
      drawing.addEventListener("click", (click) => click.stopPropagation(), { capture: true, once: true });
    }
    start = null;
  };
  drawing.addEventListener("pointerup", release);
  drawing.addEventListener("pointercancel", release);

  return {
    zoom(factor) {
      put(zoomed(viewport, { x: viewport.x + viewport.w / 2, y: viewport.y + viewport.h / 2 }, factor, laid));
    },
    fit() {
      put(fitted(laid));
    },
    /** reveal pans the least it can to bring a box inside the viewport.
     *
     * Arrowing through the nodes selects them the way it selects list rows,
     * and a selection you cannot see is not one. It never zooms: what the
     * reader set the scale to is theirs. */
    reveal(box) {
      if (!box) return;
      const pad = 24;
      const next = { ...viewport };
      if (box.x - pad < next.x) next.x = box.x - pad;
      else if (box.x + box.width + pad > next.x + next.w) next.x = box.x + box.width + pad - next.w;
      if (box.y - pad < next.y) next.y = box.y - pad;
      else if (box.y + box.height + pad > next.y + next.h) next.y = box.y + box.height + pad - next.h;
      put(next);
    },
  };
}

/** wireHover lights a node's dependencies and dims everything else.
 *
 * On a graph wide enough to need this view at all, "what does this one need,
 * and what is waiting on it" is unreadable from the edges alone. Hovering
 * answers it without changing what is selected — reading is not choosing.
 */
export function wireHover(drawing) {
  const clear = () => {
    drawing.classList.remove("hovering");
    for (const lit of drawing.querySelectorAll(".lit")) lit.classList.remove("lit");
  };

  const light = (id) => {
    clear();
    drawing.classList.add("hovering");
    const nodes = new Set([id]);
    for (const edge of drawing.querySelectorAll(".edge.dependency")) {
      const from = edge.dataset.from;
      const to = edge.dataset.to;
      if (from !== id && to !== id) continue;
      edge.classList.add("lit");
      nodes.add(from);
      nodes.add(to);
    }
    for (const node of drawing.querySelectorAll(".node")) {
      if (nodes.has(node.dataset.stitchId)) node.classList.add("lit");
    }
  };

  drawing.addEventListener("pointerover", (event) => {
    const node = event.target.closest?.(".node");
    if (!node) return;
    light(node.dataset.stitchId);
  });
  drawing.addEventListener("pointerleave", clear);
  drawing.addEventListener("pointerout", (event) => {
    // Leaving a node for the canvas rather than for another node.
    if (event.relatedTarget?.closest?.(".node")) return;
    clear();
  });
}

/** goals is the thread list the focus control offers. */
export function goals(snapshot) {
  return (snapshot?.stitches ?? [])
    .filter((stitch) => stitch.parent_id == null && stitch.tray === "threads")
    .map((stitch) => stitch.id);
}

function toggle(label, checked, onChange) {
  const wrap = element("label", "toggle");
  const box = document.createElement("input");
  box.type = "checkbox";
  box.checked = checked;
  box.addEventListener("change", () => onChange(box.checked));
  wrap.append(box, element("span", null, label));
  return wrap;
}

/** renderControls builds the toolbar above the drawing.
 *
 * `options` is the reader's current answer to each question and `onChange` is
 * given the next one — this decides nothing about how the change is applied,
 * the same way a view decides nothing about selection.
 */
export function renderControls(snapshot, options, onChange, zoom) {
  const bar = element("div", "controls");

  const shown = element("div", "toggles");
  // Hiding archives is the default every view shares; tied stitches that are
  // still in threads/ are shown, because a satisfied dependency points at one
  // and an edge to nothing is worse than a node you did not need.
  shown.append(toggle("archived", options.archived, (on) => onChange({ ...options, archived: on })));
  shown.append(toggle("legacy", options.legacy, (on) => onChange({ ...options, legacy: on })));
  shown.append(toggle("tied", options.tied, (on) => onChange({ ...options, tied: on })));
  bar.append(shown);

  // Which way the ranks run. A rank is a depth, so this is really "do threads
  // stack down the page or spread across it" — which is what the labels say.
  const direction = element("label", "direction");
  direction.append(element("span", null, "threads"));
  const how = document.createElement("select");
  for (const [value, text] of [
    ["LR", "stacked"],
    ["TB", "side by side"],
  ]) {
    const option = element("option", null, text);
    option.value = value;
    how.append(option);
  }
  how.value = options.rankdir === "TB" ? "TB" : "LR";
  how.addEventListener("change", () => onChange({ ...options, rankdir: how.value }));
  direction.append(how);
  bar.append(direction);

  const threads = goals(snapshot);
  if (threads.length > 1) {
    const wrap = element("label", "focus");
    wrap.append(element("span", null, "focus"));
    const select = document.createElement("select");
    const all = element("option", null, "all threads");
    all.value = "";
    select.append(all);
    for (const id of threads) {
      const option = element("option", null, id);
      option.value = id;
      select.append(option);
    }
    select.value = threads.includes(options.focus) ? options.focus : "";
    select.addEventListener("change", () => onChange({ ...options, focus: select.value }));
    wrap.append(select);
    bar.append(wrap);
  }

  const zoomBar = element("div", "zoom");
  for (const [label, title, action] of [
    ["−", "zoom out", () => zoom.zoom(1 / 1.4)],
    ["fit", "fit the whole graph", () => zoom.fit()],
    ["+", "zoom in", () => zoom.zoom(1.4)],
  ]) {
    const button = element("button", null, label);
    button.type = "button";
    button.title = title;
    button.addEventListener("click", action);
    zoomBar.append(button);
  }
  bar.append(zoomBar);
  return bar;
}

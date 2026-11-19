// Dragging a row into a new place, and what that means to the loom.
//
// A drop is one command, never a rewrite of the queue file. The protocol's
// queue commands are relative — `first`, `before`, `after` — and that is what
// makes two writers safe: a whole new order computed here and sent as a
// document would be a lost update with nothing to detect it.
//
// The awkward part is that the frontier is a mixed list. It draws queued
// stitches first, in queue order, and then the unqueued ready ones — which have
// no position at all, because the queue is sparse and opt-in. So the row a
// stitch is dropped next to is very often not something the loom can anchor
// against, and the drop has to resolve to one that is.

import { queueOrder, reorder } from "./mutate.js";
import { element } from "./stitches.js";

/** dropCommand is the loom command a drop means.
 *
 * `order` is the IDs as drawn, `queued` says which of them the loom can anchor
 * against, and `index` is where the dragged row landed among the others.
 *
 * The rules, in the order they are tried:
 *
 *   1. the nearest queued row *above* the drop is the anchor — `after <id> <it>`
 *   2. nothing queued above but something queued below means the drop is ahead
 *      of every queued record — `first <id>`
 *   3. nothing queued at all — `append <id>`, because there is no order to join
 *
 * There is deliberately no `before`. `before X` and `after` the row above X are
 * the same drop whenever X has a queued row above it, and when it has not, the
 * answer is `first` — so anchoring upward covers every case with one rule
 * fewer, and a rule that is never exercised is a rule that is never right.
 */
export function dropCommand(order, queued, id, index) {
  const others = order.filter((entry) => entry !== id);
  const at = Math.max(0, Math.min(others.length, index));

  for (let i = at - 1; i >= 0; i--) {
    if (queued.has(others[i])) return { command: "after", id, anchor: others[i] };
  }
  for (let i = at; i < others.length; i++) {
    if (queued.has(others[i])) return { command: "first", id };
  }
  return { command: "append", id };
}

/** unchanged reports a drop that would leave the queue exactly as it is, so it
 * can be dropped rather than sent.
 *
 * Picking a row up and putting it back is the commonest gesture there is, and
 * it must not cost a subprocess, a re-snapshot and a line in the footer
 * claiming something happened.
 */
export function unchanged(snapshot, { command, id, anchor }) {
  const before = queueOrder(snapshot);
  const after = reorder(before, command, id, anchor);
  return Boolean(after) && after.length === before.length && after.every((at, i) => at === before[i]);
}

/** draggable wires one list so its rows can be picked up and dropped.
 *
 * The HTML drag-and-drop API and nothing else: no library, no pointer-event
 * reimplementation of it, and no synthetic drag image. A row is a real
 * draggable element, the browser draws the ghost, and the only thing tracked
 * here is where the drop would land.
 *
 * `onDrop` is handed the dragged ID and the index it landed at, among the rows
 * that are not the dragged one. What that means to the loom is `dropCommand`'s
 * decision, not this function's — this one only knows about pixels.
 */
export function draggable(list, { onDrop }) {
  let dragging = "";

  const rows = () => Array.from(list.querySelectorAll("[data-stitch-id]"));

  const clear = () => {
    for (const row of rows()) row.classList.remove("dropping-above", "dropping-below");
  };

  list.addEventListener("dragstart", (event) => {
    const row = event.target.closest("[data-stitch-id]");
    if (!row || !list.contains(row)) return;
    dragging = row.dataset.stitchId;
    row.classList.add("dragging");
    event.dataTransfer.effectAllowed = "move";
    // Firefox will not start a drag without payload. The ID is also exactly
    // what a drop onto something else should carry.
    event.dataTransfer.setData("text/plain", dragging);
  });

  list.addEventListener("dragend", () => {
    dragging = "";
    clear();
    for (const row of rows()) row.classList.remove("dragging");
  });

  list.addEventListener("dragover", (event) => {
    if (!dragging) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    clear();
    const target = placement(event, rows(), dragging);
    if (target) target.row.classList.add(target.above ? "dropping-above" : "dropping-below");
  });

  list.addEventListener("dragleave", (event) => {
    if (event.target === list) clear();
  });

  list.addEventListener("drop", (event) => {
    if (!dragging) return;
    event.preventDefault();
    const id = dragging;
    const order = rows().map((row) => row.dataset.stitchId);
    const target = placement(event, rows(), id);
    clear();
    dragging = "";

    const others = order.filter((entry) => entry !== id);
    const index = target
      ? others.indexOf(target.row.dataset.stitchId) + (target.above ? 0 : 1)
      : others.length;
    onDrop(id, Math.max(0, index));
  });
}

/** placement is the row the pointer is over and which half of it, which is what
 * decides whether the drop lands above or below that row. */
function placement(event, rows, dragging) {
  for (const row of rows) {
    if (row.dataset.stitchId === dragging) continue;
    const box = row.getBoundingClientRect();
    if (event.clientY < box.bottom) {
      return { row, above: event.clientY < box.top + box.height / 2 };
    }
  }
  return null;
}

/** queueButton is the one-click way on and off the queue.
 *
 * The queue is opt-in and unused on the loom this was written against — a
 * zero-byte `queue` file — so promoting a stitch has to be trivial or nobody
 * will ever do it. One click, on any row and on the stitch being read.
 */
export function queueButton(stitch, onToggle) {
  const queued = stitch.queue_position != null;
  const button = element("button", `queue-toggle${queued ? " on" : ""}`, queued ? "queued" : "queue");
  button.type = "button";
  button.title = queued
    ? `Take ${stitch.id} off the queue`
    : `Put ${stitch.id} on the end of the queue`;
  button.setAttribute("aria-pressed", String(queued));
  button.addEventListener("click", (event) => {
    // The row underneath is selectable, and clicking this is not selecting it.
    event.preventDefault();
    event.stopPropagation();
    // Whether the row is queued is passed along rather than re-derived. A
    // stale queue record is not a stitch in the projection, so asking the
    // projection would say "not queued" about the one row whose whole problem
    // is that it *is* in the queue file.
    onToggle(stitch.id, { queued });
  });
  return button;
}

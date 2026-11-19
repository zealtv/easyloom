// Tests for turning a drop into a loom command.
//
// The drag itself is DOM and is not tested here — there is no browser in this
// environment. What is tested is the whole of the decision: which anchor a drop
// resolves to, and when a drop means nothing at all.

import assert from "node:assert/strict";
import { test } from "node:test";

import { dropCommand, unchanged } from "./reorder.js";

// The frontier as it is actually drawn: queued stitches first in queue order,
// then the unqueued ready ones, which have no position at all.
const order = ["alpha", "beta", "gamma", "loose-one", "loose-two"];
const queued = new Set(["alpha", "beta", "gamma"]);

const drop = (id, index) => dropCommand(order, queued, id, index);

test("a drop at the top of the list is first", () => {
  assert.deepEqual(drop("gamma", 0), { command: "first", id: "gamma" });
});

test("a drop below a queued row anchors on it", () => {
  // others = [alpha, beta, loose-one, loose-two]; index 2 is below beta.
  assert.deepEqual(drop("gamma", 2), { command: "after", id: "gamma", anchor: "beta" });
});

// The point of the whole file. The frontier is mixed, so the row a stitch lands
// next to is very often not something the loom can anchor against.
test("a drop next to an unqueued row resolves to the nearest queued one above", () => {
  // others = [alpha, beta, gamma, loose-two]; index 3 is below loose-one's slot
  // — but loose-one has no position, so gamma is the anchor.
  const order2 = ["alpha", "beta", "gamma", "loose-one", "loose-two"];
  assert.deepEqual(dropCommand(order2, queued, "delta", 4), {
    command: "after",
    id: "delta",
    anchor: "gamma",
  });
});

test("a drop above every queued row is first, whatever it landed next to", () => {
  const mixed = ["loose-one", "alpha", "beta"];
  assert.deepEqual(dropCommand(mixed, new Set(["alpha", "beta"]), "gamma", 1), {
    command: "first",
    id: "gamma",
  });
});

// There is no order to join, so there is nothing to anchor against. Falling
// back to append is what makes the first ever drag on an empty queue work.
test("with nothing queued at all, a drop appends", () => {
  const loose = ["loose-one", "loose-two"];
  assert.deepEqual(dropCommand(loose, new Set(), "loose-two", 0), {
    command: "append",
    id: "loose-two",
  });
});

test("an index off either end is clamped rather than refused", () => {
  assert.deepEqual(drop("gamma", -5), { command: "first", id: "gamma" });
  assert.deepEqual(drop("alpha", 99), { command: "after", id: "alpha", anchor: "gamma" });
});

/** snapshot is a projection whose queue is exactly `positions`. */
function snapshot(positions = ["alpha", "beta", "gamma"], loose = ["loose-one"]) {
  const stitch = (id, position) => ({
    id,
    tray: "threads",
    state: "plain",
    ready: true,
    archived: false,
    legacy: false,
    queue_position: position,
  });
  return {
    stitches: [
      ...positions.map((id, index) => stitch(id, index + 1)),
      ...loose.map((id) => stitch(id, null)),
    ],
  };
}

// Picking a row up and putting it back is the commonest gesture there is, and
// it must not cost a subprocess, a re-snapshot and a footer line claiming
// something happened.
test("a drop that changes nothing is recognised", () => {
  const snap = snapshot();
  assert.equal(unchanged(snap, { command: "first", id: "alpha" }), true);
  assert.equal(unchanged(snap, { command: "after", id: "beta", anchor: "alpha" }), true);
  assert.equal(unchanged(snap, { command: "after", id: "gamma", anchor: "beta" }), true);
});

test("a drop that moves something is not unchanged", () => {
  const snap = snapshot();
  assert.equal(unchanged(snap, { command: "first", id: "gamma" }), false);
  assert.equal(unchanged(snap, { command: "after", id: "alpha", anchor: "gamma" }), false);
  assert.equal(unchanged(snap, { command: "unqueue", id: "alpha" }), false);
});

// A stitch that is not on the queue is joining it, so no drop is a no-op.
test("queueing an unqueued stitch is always a change", () => {
  const snap = snapshot();
  assert.equal(unchanged(snap, { command: "append", id: "loose-one" }), false);
  assert.equal(unchanged(snap, { command: "first", id: "loose-one" }), false);
});

// The end of the queue is where append already puts it, but the stitch is not
// on the queue — so this is still a change, and the guard must not swallow it.
test("appending the last queued stitch is a no-op and appending an unqueued one is not", () => {
  const snap = snapshot();
  assert.equal(unchanged(snap, { command: "append", id: "gamma" }), true);
  assert.equal(unchanged(snap, { command: "append", id: "loose-one" }), false);
});

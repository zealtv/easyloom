// The thread tree's liveness tiers: how a row says whether its stitch is work
// in play, paused, done or not going to be done. The TUI draws the same tiers.

import assert from "node:assert/strict";
import { test } from "node:test";

import { liveness } from "./tree.js";

test("liveness tiers a stitch by how live it is", () => {
  assert.equal(liveness({ state: "plain", ready: true }), null);
  assert.equal(liveness({ state: "stitching" }), null);
  assert.equal(liveness({ state: "waiting" }), "parked");
  assert.equal(liveness({ state: "plain", waiting_inherited: true }), "parked");
  assert.equal(liveness({ state: "tied" }), "finished");
  assert.equal(liveness({ state: "dropped" }), "dropped");
  assert.equal(liveness({ state: "abandoned" }), "dropped");
});

// Finished work under a parked parent inherits the wait, but it is finished
// first: a dropped stitch must not read as merely paused.
test("finished wins over an inherited wait", () => {
  assert.equal(liveness({ state: "dropped", waiting_inherited: true }), "dropped");
  assert.equal(liveness({ state: "tied", waiting_inherited: true }), "finished");
});

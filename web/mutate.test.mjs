// Tests for the optimistic queue overlay and its reconciliation.
//
// The rules being asserted are the ones that make an optimistic UI honest
// rather than merely fast: the projection always wins, a refusal always rolls
// back, and nothing here ever edits the projection it was handed.

import assert from "node:assert/strict";
import { test } from "node:test";

import {
  Mutations,
  applyOverlay,
  neighbourMove,
  plan,
  queueOrder,
  reorder,
  said,
  toggle,
} from "./mutate.js";

/** loom is a projection with a three-record queue and one unqueued stitch. */
function loom(queue = ["alpha", "beta", "gamma"], loose = ["delta"]) {
  const stitch = (id, position) => ({
    id,
    root_id: id,
    parent_id: null,
    path: `threads/${id}`,
    tray: "threads",
    state: "plain",
    ready: true,
    waiting_inherited: false,
    queue_position: position,
    completed_at: null,
    archived: false,
    legacy: false,
    children: [],
    dependencies: [],
    cycle: [],
  });
  return {
    schema_version: 1,
    format_version: 2,
    loom_root: "/tmp/loom/.loom",
    stitches: [
      ...queue.map((id, index) => stitch(id, index + 1)),
      ...loose.map((id) => stitch(id, null)),
    ],
    decomposition_edges: [],
    dependency_edges: [],
    cycles: [],
    frontier: [...queue, ...loose],
    recently_completed: [],
    diagnostics: [],
  };
}

const orderOf = (snapshot) => queueOrder(snapshot);

test("the queue order comes from the projection's positions", () => {
  assert.deepEqual(orderOf(loom()), ["alpha", "beta", "gamma"]);
});

test("each command is the protocol's, and relative", () => {
  const order = ["alpha", "beta", "gamma"];
  assert.deepEqual(reorder(order, "unqueue", "beta"), ["alpha", "gamma"]);
  assert.deepEqual(reorder(order, "append", "alpha"), ["beta", "gamma", "alpha"]);
  assert.deepEqual(reorder(order, "first", "gamma"), ["gamma", "alpha", "beta"]);
  assert.deepEqual(reorder(order, "before", "gamma", "alpha"), ["gamma", "alpha", "beta"]);
  assert.deepEqual(reorder(order, "after", "alpha", "gamma"), ["beta", "gamma", "alpha"]);
});

test("a move that cannot mean anything is null rather than a guess", () => {
  const order = ["alpha", "beta"];
  assert.equal(reorder(order, "before", "alpha", "alpha"), null, "against itself");
  assert.equal(reorder(order, "after", "alpha", "nowhere"), null, "against an unqueued anchor");
  assert.equal(reorder(order, "shuffle", "alpha"), null, "not a command");
});

// A move renumbers everything after it. Splicing the row and leaving the
// positions alone would draw a queue that does not add up.
test("the overlay renumbers the whole queue", () => {
  const overlay = plan(loom(), "first", "gamma");
  assert.equal(overlay.get("gamma"), 1);
  assert.equal(overlay.get("alpha"), 2);
  assert.equal(overlay.get("beta"), 3);
});

test("a stitch leaving the queue is null, not missing", () => {
  const overlay = plan(loom(), "unqueue", "alpha");
  assert.equal(overlay.get("alpha"), null);
  assert.equal(overlay.get("beta"), 1);
});

// The snapshot is the server's answer, held for the length of a generation and
// re-read on every draw. Editing it in place would make the optimistic change
// permanent — surviving exactly the reconciliation it is meant to lose to.
test("applying an overlay never writes to the projection it was given", () => {
  const snapshot = loom();
  const before = JSON.stringify(snapshot);
  const drawn = applyOverlay(snapshot, plan(snapshot, "first", "gamma"));

  assert.equal(JSON.stringify(snapshot), before, "the projection was mutated");
  assert.deepEqual(orderOf(drawn), ["gamma", "alpha", "beta"]);
  assert.notEqual(drawn.stitches, snapshot.stitches);
});

test("an empty overlay is the projection itself", () => {
  const snapshot = loom();
  assert.equal(applyOverlay(snapshot, null), snapshot);
  assert.equal(applyOverlay(snapshot, new Map()), snapshot);
});

// The queue is sparse, so a move on a stitch that is not on it queues it rather
// than refusing — the same call the terminal makes.
test("moving an unqueued stitch queues it", () => {
  assert.deepEqual(neighbourMove(loom(), "delta", -1), { command: "append", id: "delta" });
});

test("nothing can go before the first record, so the top is first", () => {
  assert.deepEqual(neighbourMove(loom(), "beta", -1), { command: "first", id: "beta" });
  assert.deepEqual(neighbourMove(loom(), "gamma", -1), {
    command: "before",
    id: "gamma",
    anchor: "beta",
  });
});

test("the ends of the queue say so rather than doing nothing", () => {
  assert.match(neighbourMove(loom(), "alpha", -1).refusal, /top of the queue/);
  assert.match(neighbourMove(loom(), "gamma", 1).refusal, /end of the queue/);
});

test("Q is on the queue or off it", () => {
  assert.deepEqual(toggle(loom(), "alpha"), { command: "unqueue", id: "alpha" });
  assert.deepEqual(toggle(loom(), "delta"), { command: "append", id: "delta" });
});

// Every queue move says what it did. A stray keystroke that reorders the queue
// silently is how this repo lost track of its own plan once already.
test("every accepted move announces itself", () => {
  for (const command of [
    { command: "unqueue", id: "alpha" },
    { command: "append", id: "alpha" },
    { command: "first", id: "alpha" },
    { command: "before", id: "alpha", anchor: "beta" },
    { command: "after", id: "alpha", anchor: "beta" },
  ]) {
    assert.ok(said(command).includes("alpha"), `${command.command} said nothing useful`);
  }
});

/** harness wires a Mutations over a projection the test controls. */
function harness({ fail = null, generation = 7 } = {}) {
  const world = {
    raw: loom(),
    generation,
    draws: 0,
    says: [],
    fails: [],
    sent: [],
  };
  const mutations = new Mutations({
    slug: () => "test",
    snapshot: () => mutations.project(world.raw),
    generation: () => world.generation,
    onChange: () => {
      world.draws += 1;
    },
    onSay: (text) => world.says.push(text),
    onFail: (err) => world.fails.push(err),
    send: async (slug, command) => {
      world.sent.push({ slug, ...command });
      if (fail) throw fail;
      return { command: command.command, id: command.id, changed: true };
    },
  });
  world.mutations = mutations;
  world.drawn = () => mutations.project(world.raw);
  return world;
}

test("a command is applied before the server has answered", async () => {
  const world = harness();
  const running = world.mutations.run({ command: "first", id: "gamma" });

  // Drawn from the overlay, with the request still in flight.
  assert.deepEqual(orderOf(world.drawn()), ["gamma", "alpha", "beta"]);
  await running;

  assert.deepEqual(world.sent, [{ slug: "test", command: "first", id: "gamma" }]);
  assert.deepEqual(orderOf(world.drawn()), ["gamma", "alpha", "beta"], "still asserted");
  assert.match(world.says[0], /gamma/);
});

// The one thing worse than a slow UI is a UI asserting something that did not
// happen.
test("a refusal rolls the overlay back and says why", async () => {
  const refusal = new Error("queue records are invalid: unknown queue entry 'ghost' at position 6");
  const world = harness({ fail: refusal });

  await world.mutations.run({ command: "first", id: "gamma" });

  assert.deepEqual(orderOf(world.drawn()), ["alpha", "beta", "gamma"], "the change survived");
  assert.deepEqual(world.fails, [refusal]);
  assert.deepEqual(world.says, [], "a refused move announced itself as done");
});

// The projection is authoritative and we are a viewer. Whatever the overlay
// believed is discarded whole when a newer one lands — silently, and whether or
// not it agrees.
test("a newer projection discards the overlay whole", async () => {
  const world = harness();
  await world.mutations.run({ command: "first", id: "gamma" });
  assert.deepEqual(orderOf(world.drawn()), ["gamma", "alpha", "beta"]);

  // The refresh lands, and the loom disagrees: the move did not take.
  world.generation += 1;
  world.raw = loom(["alpha", "beta", "gamma"]);

  assert.equal(world.mutations.settle(), true);
  assert.deepEqual(orderOf(world.drawn()), ["alpha", "beta", "gamma"]);
  assert.equal(world.mutations.overlay, null);
});

// Belt and braces: even before settle() runs, an overlay from an older
// generation is not drawn over a newer projection.
test("an overlay is never applied to a projection it was not planned on", async () => {
  const world = harness();
  await world.mutations.run({ command: "first", id: "gamma" });

  world.generation += 1;
  assert.deepEqual(orderOf(world.drawn()), ["alpha", "beta", "gamma"]);
});

test("settling does nothing while the generation has not moved", async () => {
  const world = harness();
  await world.mutations.run({ command: "first", id: "gamma" });
  assert.equal(world.mutations.settle(), false);
  assert.deepEqual(orderOf(world.drawn()), ["gamma", "alpha", "beta"]);
});

// Queue commands contend for the loom's queue lock, so a burst of keystrokes
// must not become a burst of commands racing each other into it.
test("only one command is in flight at a time", async () => {
  let release;
  const world = harness();
  world.mutations.send = () => new Promise((resolve) => (release = resolve));

  const first = world.mutations.run({ command: "first", id: "gamma" });
  await world.mutations.run({ command: "unqueue", id: "alpha" });

  assert.match(world.says.at(-1), /already running/);
  release();
  await first;
});

// Two commands can land before one refresh does, and the second must not undo
// the first.
test("a second command layers over the first rather than replacing it", async () => {
  const world = harness();
  await world.mutations.run({ command: "unqueue", id: "alpha" });
  assert.deepEqual(orderOf(world.drawn()), ["beta", "gamma"]);

  await world.mutations.run({ command: "first", id: "gamma" });
  assert.deepEqual(orderOf(world.drawn()), ["gamma", "beta"], "alpha came back");
});

test("a move at the end of the queue is reported, not sent", async () => {
  const world = harness();
  await world.mutations.run(neighbourMove(world.raw, "alpha", -1));

  assert.deepEqual(world.sent, []);
  assert.match(world.says[0], /top of the queue/);
});

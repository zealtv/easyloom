// Tests for the unblock simulation, run with `node --test web/`.
//
// Node is a convenience for testing, never a build step: the frontend ships as
// the sources in this directory and `go build` needs nothing beyond Go. The
// .mjs extension keeps this file out of `//go:embed *.js`, so a test never
// travels inside the binary.
//
// The fixtures are testdata/, described in testdata/README.md: real `map --json`
// output from this repo's own loom — one as it stands, one from earlier, before
// a slice of work was done — plus hand-written looms for the states a healthy loom does
// not have. The arithmetic this module exists to do was done by hand against
// that second real one to plan that slice, so it is the fixture that says
// whether the module agrees with the person.

import assert from "node:assert/strict";
import { test } from "node:test";

import { load, names } from "./fixtures.mjs";
import { derive, disagreements, impact, ranking } from "./unblock.js";

// The loom as it was before that slice, and as it is later.
const before = load("easyloom-bite7.json");
const now = load("easyloom.json");

// The whole test, and it runs over every fixture rather than a chosen two: the
// projection is authoritative, this module re-derives its rule only to ask a
// hypothetical the loom cannot be asked, and a re-derivation that disagrees
// about the present is wrong about the future. `internal/unblock` asserts the
// same thing over the same files.
for (const name of names()) {
  const snapshot = load(name);

  test(`readiness agrees with the projection on ${name}`, () => {
    assert.deepEqual(disagreements(snapshot), []);
  });

  test(`the derived ready set is the projection's frontier on ${name}`, () => {
    assert.deepEqual([...derive(snapshot)].sort(), [...snapshot.frontier].sort());
  });
}

// The states a real loom does not reach, and the reason the synthetic fixtures
// exist. Both are silent on the captured looms: reordering these two rules, or
// dropping the cycle guard, went red on nothing until these were added.
test("a stitch waiting both directly and by inheritance says promote", () => {
  const got = impact(load("synthetic.json"), "twice-parked");
  assert.equal(got.action, "promote");
  assert.equal(got.resumeVia, "parked-goal");
});

test("a cycle releases nothing even when work sits behind it", () => {
  // `c` needs `a` and is outside the cycle, so dropping the cycle guard would
  // claim resolving `a` releases it. Nothing about `a` can be resolved.
  const got = impact(load("cyclic.json"), "a");
  assert.deepEqual(got.releases, []);
  assert.equal(got.action, "cycle");
});

// The arithmetic a person did by hand to plan that slice. These two are the
// reason this module exists.
test("snapshot-refresh releases live-updates", () => {
  const got = impact(before, "snapshot-refresh");
  assert.ok(got.releases.includes("live-updates"));
  assert.equal(got.action, "tie");
  assert.equal(got.ready, true);
});

// The hand arithmetic counted dependency edges. The readiness rule has a
// second clause the hand missed: a stitch whose last unresolved child goes
// terminal becomes ready itself. snapshot-refresh was the last thing open
// under snapshot-model, so tying it makes that thread tieable — which is a
// real thing to have become possible, and the rule says so whether or not
// anyone was counting it.
test("resolving the last child makes the parent ready", () => {
  const got = impact(before, "snapshot-refresh");
  assert.deepEqual(got.releases, ["live-updates", "snapshot-model"]);
  const parent = before.stitches.find((s) => s.id === "snapshot-model");
  assert.deepEqual(parent.children.filter((id) => id !== "snapshot-refresh").length > 0, true);
});

test("queue-mutations releases mutation-refresh and queue-reorder-ui", () => {
  const got = impact(before, "queue-mutations");
  assert.ok(got.releases.includes("mutation-refresh"));
  assert.ok(got.releases.includes("queue-reorder-ui"));
  assert.equal(got.action, "tie");
});

// One step, not transitive. live-updates is behind snapshot-refresh and
// nothing is behind live-updates, so the chain must not be counted twice.
test("impact is one step and not transitive", () => {
  // web-shell is live-updates' parent and its last open child, so it is one
  // step. queue-mutations is two steps behind and must not be counted.
  assert.deepEqual(impact(before, "live-updates").releases, ["web-shell"]);
});

test("a loose end that unblocks no work says so", () => {
  // Nothing needs loom-commands and its parent is a goal root with nothing
  // above it, so finishing it releases nothing at all. That is a real answer
  // and the view has to be able to say it.
  const got = impact(before, "loom-commands");
  assert.deepEqual(got.releases, []);
  assert.equal(got.action, "tie");
});

// Waiting is inherited, and `resume` lifts only a stitch's own suffix.
// Resuming feature-backlog un-parks its subtree; resuming one of its children
// is refused, and the reader has to promote it or resume the parent.
test("resuming a parked parent releases its subtree", () => {
  const got = impact(before, "feature-backlog");
  assert.equal(got.action, "resume");
  assert.ok(got.releases.length > 0, "feature-backlog is parked over live stitches");
  for (const id of got.releases) {
    const stitch = before.stitches.find((s) => s.id === id);
    assert.equal(stitch.waiting_inherited, true, `${id} was parked by inheritance`);
  }
});

test("a stitch parked by inheritance says to promote it, and names the parent", () => {
  const child = before.stitches.find((s) => s.waiting_inherited && s.state !== "waiting");
  const got = impact(before, child.id);
  assert.equal(got.action, "promote");
  assert.equal(got.parked, true);
  assert.ok(got.resumeVia, "the ancestor holding it parked is named");
  const ancestor = before.stitches.find((s) => s.id === got.resumeVia);
  assert.equal(ancestor.state, "waiting");
});

// A thread root is not a piece of work; its children are. Resolving it is
// resolving the subtree, which is not one step.
test("a stitch with unresolved children reports them rather than a tie", () => {
  const got = impact(before, "queue-control");
  assert.equal(got.action, "decompose");
  assert.ok(got.openChildren.length > 0);
});

test("an unknown stitch is null rather than a guess", () => {
  assert.equal(impact(before, "no-such-stitch"), null);
});

test("the ranking is two groups, each sorted by what it releases", () => {
  const { looseEnds, parked } = ranking(before);
  for (const group of [looseEnds, parked]) {
    for (let i = 1; i < group.length; i++) {
      assert.ok(
        group[i - 1].releases.length >= group[i].releases.length,
        "each group descends by impact",
      );
    }
  }
  // The groups are disjoint and answer different questions.
  const ends = new Set(looseEnds.map((entry) => entry.id));
  for (const entry of parked) assert.ok(!ends.has(entry.id), `${entry.id} is in one group only`);

  assert.equal(looseEnds[0].id, "queue-mutations", "the biggest loose end leads");
});

test("nothing archived or terminal is ranked", () => {
  const { looseEnds, parked } = ranking(now);
  for (const entry of [...looseEnds, ...parked]) {
    const stitch = now.stitches.find((s) => s.id === entry.id);
    assert.equal(stitch.tray, "threads");
    assert.ok(!["tied", "dropped", "abandoned"].includes(stitch.state));
  }
});

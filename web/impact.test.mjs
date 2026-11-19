// Tests for the impact view, run with `node --test web/`.
//
// The view builds DOM nodes and never touches innerHTML, so the whole of what
// it does is `document.createElement` plus `append` — which is little enough
// to stand in for. That is not a browser and it does not pretend to be one: it
// checks that the view runs, that it says the right things, and that it puts
// them in the right group. What it looks like is a thing to look at.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

// The stand-in has to exist before the modules that close over `document` are
// loaded, which is why this import is dynamic and the ones above are not.
globalThis.document = {
  createElement(tag) {
    return {
      tag,
      className: "",
      textContent: "",
      href: "",
      type: "",
      children: [],
      dataset: {},
      attributes: {},
      handlers: {},
      append(...kids) {
        this.children.push(...kids);
      },
      setAttribute(name, value) {
        this.attributes[name] = value;
      },
      addEventListener(name, fn) {
        this.handlers[name] = fn;
      },
    };
  },
};

const { cost, renderImpact } = await import("./impact.js");

const here = dirname(fileURLToPath(import.meta.url));
const load = (name) => JSON.parse(readFileSync(join(here, "testdata", name), "utf8"));
const before = load("easyloom-bite7.json");

/** text flattens a rendered tree into its words. */
function text(node) {
  if (!node) return "";
  return [node.textContent ?? "", ...(node.children ?? []).map(text)].join(" ");
}

/** find walks a rendered tree for the first node matching a predicate. */
function find(node, predicate) {
  if (predicate(node)) return node;
  for (const child of node.children ?? []) {
    const found = find(child, predicate);
    if (found) return found;
  }
  return null;
}

/** all collects every node matching a predicate. */
function all(node, predicate, out = []) {
  if (predicate(node)) out.push(node);
  for (const child of node.children ?? []) all(child, predicate, out);
  return out;
}

const render = (snapshot, state = { zeroLooseEnds: false, zeroParked: false }, onChange = () => {}) =>
  renderImpact(snapshot, "easyloom", state, onChange);

test("the two groups are drawn separately and named", () => {
  const view = render(before);
  const headings = all(view, (node) => node.tag === "h2").map((node) => node.textContent);
  assert.equal(headings.length, 2);
  assert.match(headings[0], /Loose ends/);
  assert.match(headings[1], /Parked/);
});

test("a row names what it releases rather than only counting it", () => {
  const view = render(before);
  const row = find(view, (node) => node.dataset?.stitchId === "queue-mutations");
  assert.ok(row, "queue-mutations is ranked");
  const words = text(row);
  assert.match(words, /releases 2/);
  assert.match(words, /mutation-refresh/);
  assert.match(words, /queue-reorder-ui/);
});

test("the zero-impact tail folds away and says how many it folded", () => {
  const view = render(before);
  const fold = find(view, (node) => node.className === "fold");
  assert.ok(fold, "the tail folds rather than disappearing");
  assert.match(fold.textContent, /release nothing/);
  assert.equal(fold.attributes["aria-expanded"], "false");

  // Nothing releasing zero is drawn while the fold is shut.
  assert.equal(find(view, (node) => node.dataset?.stitchId === "loom-commands"), null);
});

test("opening the fold is a change handed back to the shell", () => {
  let got = null;
  const view = render(before, { zeroLooseEnds: false, zeroParked: false }, (change) => {
    got = change;
  });
  find(view, (node) => node.className === "fold").handlers.click();
  assert.deepEqual(got, { zeroLooseEnds: true });

  // And with it open, the quiet rows are there.
  const open = render(before, { zeroLooseEnds: true, zeroParked: false });
  assert.ok(find(open, (node) => node.dataset?.stitchId === "loom-commands"));
});

test("a parked stitch is in the parked group and never in the loose ends", () => {
  const view = render(before, { zeroLooseEnds: true, zeroParked: true });
  const rows = all(view, (node) => node.dataset?.stitchId === "feature-backlog");
  assert.equal(rows.length, 1, "one group only");
});

test("every row is selectable, so the shell handles it like any other row", () => {
  const view = render(before, { zeroLooseEnds: true, zeroParked: true });
  for (const row of all(view, (node) => node.tag === "li" && node.className?.startsWith("impact-row"))) {
    assert.ok(row.dataset.stitchId, "a row is a row about a stitch");
  }
});

// The cost is the thing the reader acts on, and it is different in each case.
test("the cost names the blockers", () => {
  const line = cost({
    action: "unblock",
    blockedBy: [
      { id: "snapshot-refresh", status: "blocked", reason: null },
      { id: "gone", status: "broken", reason: "missing" },
    ],
    openChildren: [],
    resumeVia: null,
  });
  assert.match(line, /blocked by 2/);
  assert.match(line, /snapshot-refresh/);
  assert.match(line, /gone \(missing\)/);
});

// resume lifts only a stitch's own suffix, so a stitch parked by inheritance
// has two possible acts and the reader has to pick one. Saying "waiting" and
// stopping there is what has bitten this repo before.
test("a stitch parked by inheritance is told both ways out, with the ancestor named", () => {
  const line = cost({
    action: "promote",
    blockedBy: [],
    openChildren: [],
    resumeVia: "feature-backlog",
  });
  assert.match(line, /promote/);
  assert.match(line, /resume feature-backlog/);
});

test("an empty group says so, and says it is not a broken view", () => {
  const empty = { ...before, stitches: [], cycles: [], frontier: [], dependency_edges: [] };
  const words = text(render(empty));
  assert.match(words, /Nothing is active/);
  assert.match(words, /Nothing is parked/);
});

// The ranking is only trustworthy if the simulation agrees with the loom about
// the present. When it does not, the view has to say so rather than rank.
test("a disagreement with the projection is drawn as an error", () => {
  const broken = structuredClone(before);
  broken.stitches.find((stitch) => stitch.id === "ci-workflow").ready = false;
  const words = text(render(broken));
  assert.match(words, /cannot be trusted/);
  assert.match(words, /ci-workflow/);
});

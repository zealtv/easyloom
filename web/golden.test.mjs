// The golden file must be what the JS actually produces.
//
// `internal/unblock` is checked against the golden file, not against this
// module — nothing in Go can run `unblock.js`. That makes the golden the JS's
// signature, and a stale signature is worse than none: the Go port would be
// held to an answer the browser stopped giving. So a change to `unblock.js`
// that moves any ranking fails here until `make golden` is run, and then the
// diff says which answers moved.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { goldenPath, rankings } from "./fixtures.mjs";

test("the golden rankings are current", () => {
  const golden = JSON.parse(readFileSync(goldenPath, "utf8"));
  assert.deepEqual(
    rankings(),
    golden,
    "web/testdata/rankings.golden.json is stale — run `make golden`",
  );
});

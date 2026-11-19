// Writes testdata/rankings.golden.json: the JS ranking over every fixture.
//
//     make golden
//
// The golden file is the whole reason the terminal is allowed to have a second
// implementation of readiness. `web/unblock.js` and `internal/unblock` both
// answer "what would resolving this release?", and two implementations of one
// idea disagree eventually. Testing each against itself would not catch it —
// the two frontends would simply tell the user different things about what to
// work on next, and nothing would go red.
//
// So this file is generated from the JS, asserted by the JS test (which fails
// if it is stale), and asserted by `go test ./internal/unblock` (which fails if
// the Go port drifts from it). Regenerating it is a deliberate act with a diff
// to read: if a change to either side moves a ranking, the change shows up here
// as the answers that moved.

import { writeFileSync } from "node:fs";

import { goldenPath, rankings } from "./fixtures.mjs";

writeFileSync(goldenPath, JSON.stringify(rankings(), null, 2) + "\n");
console.log(`wrote ${goldenPath}`);

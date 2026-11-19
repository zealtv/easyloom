// The fixture set both implementations of the ranking run over, and the shape
// of the golden file that holds them to each other.
//
// One list, used by the JS tests, by the golden generator, and — by the same
// rule, reimplemented in a few lines — by `internal/unblock`. A fixture added
// here is a fixture both sides are held to; that is the only thing stopping
// the two from agreeing about the present and diverging on the hypothetical.
//
// .mjs, like the tests, so `//go:embed *.js` never carries it into the binary.

import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { ranking } from "./unblock.js";

const here = dirname(fileURLToPath(import.meta.url));

/** dir is testdata/, holding real `map --json` output and one synthetic loom. */
export const dir = join(here, "testdata");

/** goldenPath is the cross-check file. It lives beside the fixtures it was
 * generated from, and is excluded from them by suffix. */
export const goldenPath = join(dir, "rankings.golden.json");

/** names lists the fixtures, sorted, so both sides iterate the same order. */
export function names() {
  return readdirSync(dir)
    .filter((name) => name.endsWith(".json") && !name.endsWith(".golden.json"))
    .sort();
}

/** load reads one fixture by file name. */
export function load(name) {
  return JSON.parse(readFileSync(join(dir, name), "utf8"));
}

/** rankings is the golden document: fixture file name -> the two groups.
 *
 * `ranking` also returns the derived ready set, which is not carried here: the
 * agreement-with-the-projection test already pins that on both sides, against
 * the fixture's own `ready` flags, which is the stronger check.
 */
export function rankings() {
  const out = {};
  for (const name of names()) {
    const { looseEnds, parked } = ranking(load(name));
    out[name] = { looseEnds, parked };
  }
  return out;
}

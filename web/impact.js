// The impact view: what each stitch would release, and what it costs to get
// there.
//
// The arithmetic is all in `unblock.js`; this file only draws it. That is the
// same split `graph.js` and `graphview.js` are on, and it is why the graph and
// the tree can grow an impact badge later without a second implementation of
// the one thing that must never disagree with itself.
//
// Two groups, never merged. "What should I work on now" and "what should I
// un-park" are different questions and their answers are not comparable: a
// parked stitch releasing six is not six times better than a ready one
// releasing one, it is a different decision. One leaderboard over both would
// invite exactly that comparison.

import { element, selectable, selectionPath } from "./stitches.js";
import { disagreements, ranking } from "./unblock.js";

// What each action costs the reader, in the loom's own words. The wording is
// the command they would run wherever there is one.
const actions = {
  tie: { label: "ready", detail: "claim it and tie it" },
  unblock: { label: "blocked", detail: "" },
  decompose: { label: "has open children", detail: "" },
  resume: { label: "waiting", detail: "loom resume" },
  promote: { label: "waiting above", detail: "" },
  cycle: { label: "in a cycle", detail: "never ready until an edge is removed" },
  "in-hand": { label: "in hand", detail: "already claimed or tended" },
};

/** cost is the second line of a row: what resolving this one would take.
 *
 * A stitch parked by inheritance is the case worth spelling out. `resume` lifts
 * only a stitch's own suffix, so `resume <child>` fails with "not directly
 * waiting" — the reader has to promote it to its own thread or resume the
 * ancestor, and which of those they want is a real decision.
 */
export function cost(entry) {
  switch (entry.action) {
    case "unblock":
      return `blocked by ${entry.blockedBy.length}: ${entry.blockedBy.map(named).join(", ")}`;
    case "decompose":
      return `${entry.openChildren.length} open child${entry.openChildren.length === 1 ? "" : "ren"}: ${entry.openChildren.join(", ")}`;
    case "promote":
      return entry.resumeVia
        ? `parked by ${entry.resumeVia}: promote it, or resume ${entry.resumeVia}`
        : "parked by inheritance: promote it to its own thread";
    default:
      return actions[entry.action]?.detail ?? "";
  }
}

function named(blocker) {
  return blocker.status === "broken"
    ? `${blocker.id} (${blocker.reason ?? "broken"})`
    : blocker.id;
}

/** row draws one ranked stitch. */
function row(entry, slug) {
  const item = element("li", `impact-row action-${entry.action}${entry.releases.length ? "" : " quiet"}`);
  selectable(item, entry.id);

  const head = element("div", "impact-head");
  const link = element("a", "id", entry.id);
  link.href = selectionPath(slug, "impact", entry.id);
  head.append(link);
  head.append(
    element(
      "span",
      `badge ${entry.action}`,
      actions[entry.action]?.label ?? entry.action,
    ),
  );
  // The number and the names, together. "3" is a leaderboard; "3 —
  // live-updates, mutation-refresh, queue-reorder-ui" is an answer, and the
  // reader can check it against the loom without leaving the row.
  head.append(element("span", "releases-count", `releases ${entry.releases.length}`));
  item.append(head);

  const detail = cost(entry);
  if (detail) item.append(element("p", "impact-cost", detail));

  if (entry.releases.length) {
    const list = element("ul", "releases");
    for (const id of entry.releases) {
      const released = element("li", null);
      const to = element("a", "id", id);
      to.href = selectionPath(slug, "impact", id);
      released.append(to);
      list.append(released);
    }
    item.append(list);
  }
  return item;
}

/** group draws one ranked list, with the zero-impact tail folded away.
 *
 * A list where most rows say "0" buries the three that matter, and on this
 * repo's own loom most rows do say 0. The tail is still reachable — it is the
 * answer to "is that really all of them" — so it folds rather than
 * disappearing, and whether it is open is the reader's, held in the shell.
 */
function group(title, entries, { slug, blurb, showZero, onToggle }) {
  const section = element("section", "impact-group");
  section.append(element("h2", null, title));

  if (entries.length === 0) {
    section.append(element("p", "muted", blurb.empty));
    return section;
  }

  const carries = entries.filter((entry) => entry.releases.length > 0);
  const zero = entries.filter((entry) => entry.releases.length === 0);

  if (carries.length === 0) {
    section.append(element("p", "muted", blurb.none));
  } else {
    const list = element("ul", "impact-rows");
    for (const entry of carries) list.append(row(entry, slug));
    section.append(list);
  }

  if (zero.length) {
    const toggle = element(
      "button",
      "fold",
      showZero ? `hide ${zero.length} that release nothing` : `${zero.length} release nothing`,
    );
    toggle.type = "button";
    toggle.setAttribute("aria-expanded", String(Boolean(showZero)));
    toggle.addEventListener("click", () => onToggle());
    section.append(toggle);

    if (showZero) {
      const list = element("ul", "impact-rows zero");
      for (const entry of zero) list.append(row(entry, slug));
      section.append(list);
    }
  }
  return section;
}

/** renderImpact returns the impact view.
 *
 * `state` is the reader's: which of the zero-impact tails are open. It lives in
 * the shell beside the tree's folds and the graph's toggles, and goes nowhere
 * near the server.
 */
export function renderImpact(snapshot, slug, state, onChange) {
  const view = element("section", "impact");
  const { looseEnds, parked } = ranking(snapshot);

  // The simulation re-derives readiness, and it must agree with the loom about
  // the present to be trusted about the future. Asserting that here costs one
  // pass over the projection and turns a silently wrong ranking into a visible
  // one.
  const wrong = disagreements(snapshot);
  if (wrong.length) {
    const warning = element(
      "p",
      "error",
      `This ranking cannot be trusted: the simulation disagrees with the loom about ${wrong.length} stitch${wrong.length === 1 ? "" : "es"} that is ready right now (${wrong.map((entry) => entry.id).join(", ")}). That is a bug in web/unblock.js, not in the loom.`,
    );
    view.append(warning);
  }

  view.append(
    group("Loose ends — what to work on now", looseEnds, {
      slug,
      showZero: state.zeroLooseEnds,
      onToggle: () => onChange({ zeroLooseEnds: !state.zeroLooseEnds }),
      blurb: {
        empty: "Nothing is active. Every thread is tied, dropped or parked.",
        none: "Nothing active would release anything — every dependency in this loom is already satisfied. That is a healthy loom, not an empty view.",
      },
    }),
  );

  view.append(
    group("Parked — what to un-park", parked, {
      slug,
      showZero: state.zeroParked,
      onToggle: () => onChange({ zeroParked: !state.zeroParked }),
      blurb: {
        empty: "Nothing is parked. There is no `.waiting` stitch in this loom.",
        none: "Nothing parked is holding anything back: resuming any of it would make only that stitch workable.",
      },
    }),
  );

  return view;
}

// The frontier view: the ready work, in effective preference order.
//
// The projection's `frontier` is already ordered — queued stitches first in
// queue order, then unqueued ready ones in bytewise path order — so it is
// rendered in the order it arrives. Re-sorting it here would quietly disagree
// with what `loom next` will actually take.

import { queueButton } from "./reorder.js";
import { element, hidden, selectable, selectionPath } from "./stitches.js";

/** counts summarises a projection's active work.
 *
 * The waiting count is the point: bopOS's loom has 35 active stitches, 15 of
 * them waiting, and exactly one ready. A frontier view is a single card there.
 * Shown beside the waiting count, that reads as "mostly parked" rather than
 * "nothing to do".
 */
export function counts(snapshot) {
  const active = snapshot.stitches.filter((stitch) => !hidden(stitch));
  return {
    ready: snapshot.frontier.length,
    waiting: active.filter((stitch) => stitch.state === "waiting" || stitch.waiting_inherited).length,
    claimed: active.filter((stitch) => stitch.state === "stitching").length,
    active: active.length,
  };
}

/** card renders one ready stitch.
 *
 * The queue is sparse and opt-in, so most rows have no position — and "queued"
 * and "merely ready" being told apart at a glance is the whole reason this list
 * carries a badge at all. The row says which it is twice over: the position
 * badge, and the toggle, which reads "queued" or "queue".
 */
function card(stitch, slug, onQueueToggle) {
  const queued = stitch.queue_position != null;
  const row = selectable(element("li", `card ${queued ? "queued" : "loose"}`), stitch.id);
  // Every row can be picked up. A drop resolves to a queued anchor even when
  // the row it landed next to has no position of its own.
  row.draggable = true;

  if (queued) {
    row.append(element("span", "badge queue", `queue ${stitch.queue_position}`));
  }

  const link = element("a", "id", stitch.id);
  link.href = selectionPath(slug, "frontier", stitch.id);
  row.append(link);

  if (stitch.root_id && stitch.root_id !== stitch.id) {
    row.append(element("span", "thread", stitch.root_id));
  }
  if (stitch.state !== "plain") {
    row.append(element("span", "badge state", stitch.state));
  }
  if (onQueueToggle) row.append(queueButton(stitch, onQueueToggle));
  return row;
}

/** renderFrontier returns the frontier view for a projection. */
export function renderFrontier(snapshot, slug, onQueueToggle) {
  const view = element("section", "frontier");
  const summary = counts(snapshot);

  const header = element("header", "counts");
  header.append(element("span", "count ready", `${summary.ready} ready`));
  header.append(element("span", "count waiting", `${summary.waiting} waiting`));
  if (summary.claimed > 0) {
    header.append(element("span", "count", `${summary.claimed} claimed`));
  }
  header.append(element("span", "count muted", `${summary.active} active`));
  view.append(header);

  if (summary.ready === 0) {
    const empty = element(
      "p",
      "muted",
      summary.waiting > 0
        ? `Nothing is ready. ${summary.waiting} of ${summary.active} active stitches are waiting.`
        : "Nothing is ready.",
    );
    view.append(empty);
    return view;
  }

  const index = new Map(snapshot.stitches.map((stitch) => [stitch.id, stitch]));
  const list = element("ol", "cards");
  for (const id of snapshot.frontier) {
    const stitch = index.get(id);
    // A frontier naming a stitch the projection does not carry is a projection
    // problem, not a reason to drop the row silently.
    list.append(
      stitch
        ? card(stitch, slug, onQueueToggle)
        : element("li", "card missing", `${id} (not in this projection)`),
    );
  }
  view.append(list);

  return view;
}

/** renderDiagnostics lists what the loom reported while projecting itself.
 *
 * They ride along in the projection untouched, and a frontier with broken
 * dependencies behind it should say so on the landing page.
 */
export function renderDiagnostics(snapshot) {
  if (!snapshot.diagnostics?.length) return null;
  const section = element("section", "diagnostics");
  section.append(element("h2", null, `${snapshot.diagnostics.length} problem(s)`));
  const list = element("ul", "problems");
  for (const diagnostic of snapshot.diagnostics) {
    const item = element("li", `problem ${diagnostic.severity}`);
    item.append(element("span", "badge", diagnostic.severity));
    item.append(element("span", "code", diagnostic.code));
    item.append(element("span", "message", diagnostic.message));
    list.append(item);
  }
  section.append(list);
  return section;
}

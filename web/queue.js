// The queue view: `.loom/queue` in order, one row per record, with what the
// loom will really do with each one.
//
// Order comes from queue_position in the projection and is never recomputed.
// Readiness comes from ready. This view decides nothing about the queue; it
// says what the projection already says, in the order the loom will read it.
//
// It exists because a badge on the frontier was not enough. Queued and
// unqueued rows sit mixed together there, and a queue whose head is skipped
// does not say what it looks like it says.

import { queueButton } from "./reorder.js";
import { element, hidden, selectable, selectionPath, terminal } from "./stitches.js";

// queueError is the diagnostic the loom raises for a record it cannot resolve.
// The code set is open, so an unrecognised code is ignored rather than treated
// as a failure.
const QUEUE_ERROR = "queue_error";

// A queue_error's structured fields are null, so the record's ID and position
// have to come out of the message. One that does not match is still shown, at
// the end, rather than dropped.
const RECORD = /'([^']*)'.*\bposition (\d+)/;

/** rows is the queue as the loom will read it. */
export function rows(snapshot) {
  const index = new Map(snapshot.stitches.map((stitch) => [stitch.id, stitch]));
  const positions = new Map();
  for (const stitch of snapshot.stitches) {
    if (hidden(stitch) || stitch.queue_position == null) continue;
    positions.set(stitch.id, stitch.queue_position);
  }

  const out = [];
  for (const [id, position] of positions) {
    out.push({ position, id, stitch: index.get(id) ?? null, next: false, skipped: false, problem: "" });
  }
  appendProblems(out, snapshot);
  sort(out);
  return mark(out, positions);
}

/** appendProblems folds the loom's queue diagnostics into the list: onto the
 * row they concern when the projection has one, and as a row of their own when
 * it does not.
 *
 * These matter more than they look. An unrelated invalid record makes an
 * unrelated queue mutation fail *without changing the file*, which is
 * otherwise an inexplicable refusal. */
function appendProblems(out, snapshot) {
  for (const diagnostic of snapshot.diagnostics ?? []) {
    if (diagnostic.code !== QUEUE_ERROR) continue;
    const match = RECORD.exec(diagnostic.message ?? "");
    const id = match?.[1] ?? "";
    const position = match ? Number(match[2]) : 0;

    const found = id ? out.find((row) => row.id === id) : undefined;
    if (found) {
      found.problem = diagnostic.message;
      continue;
    }
    // The record names no stitch the projection has, so nothing else in this
    // view would mention it at all.
    out.push({ position, id, stitch: null, next: false, skipped: false, problem: diagnostic.message });
  }
}

/** sort puts the rows in queue order. A record whose position could not be
 * read draws at the end, where it is visible without displacing anything that
 * has one. */
function sort(out) {
  out.sort((left, right) => {
    if ((left.position === 0) !== (right.position === 0)) return left.position === 0 ? 1 : -1;
    if (left.position !== right.position) return left.position - right.position;
    return left.id < right.id ? -1 : left.id > right.id ? 1 : 0;
  });
}

/** mark works out what the loom will do with each record: which one it would
 * take next, which it will skip, which can never become ready, and which sits
 * above something it needs. */
function mark(out, positions) {
  let taken = false;
  let skipped = 0;

  for (const row of out) {
    const stitch = row.stitch;
    if (stitch && !terminal(stitch) && stitch.ready) {
      // `next` scans queued IDs in order and takes the first ready one. It is
      // not always row 1.
      if (!taken) {
        row.next = true;
        taken = true;
      }
    } else {
      row.skipped = true;
      skipped += 1;
    }
    if (stitch) {
      row.impossible = impossible(stitch);
      row.inverted = row.impossible ? "" : inversion(stitch, positions, row.position);
    }
  }
  return { rows: out, counts: { queued: out.length, skipped } };
}

/** impossible names a row that cannot become ready until somebody repairs the
 * loom. A queue made of these is a plan that cannot run. */
export function impossible(stitch) {
  if (stitch.cycle?.length) return "never: in a dependency cycle";
  for (const dependency of stitch.dependencies ?? []) {
    if (dependency.status !== "broken") continue;
    return `never: ${dependency.to} is ${dependency.reason ?? "broken"}`;
  }
  return "";
}

/** inversion reports a dependency of this row queued below it, or not queued
 * at all. That is not impossible — `next` skips the row and takes the
 * dependency anyway — but then the queue is no longer the order the work will
 * happen in, which is the only thing the queue is for. */
export function inversion(stitch, positions, position) {
  const below = [];
  for (const dependency of stitch.dependencies ?? []) {
    if (dependency.status !== "blocked") continue;
    const at = positions.get(dependency.to);
    if (at != null && at < position) continue;
    below.push(at != null ? `${dependency.to} is queued below` : `${dependency.to} is not queued`);
  }
  if (below.length === 0) return "";
  return `out of order: ${below.join(", ")}`;
}

/** why says, in the loom's terms, what will happen to a skipped row. */
function why(row) {
  if (!row.stitch) return "no such stitch";
  if (terminal(row.stitch)) return row.stitch.state;
  if (row.stitch.state === "stitching") return "claimed";
  if (row.stitch.state === "waiting") return "waiting";
  if (row.stitch.waiting_inherited) return "waiting above";
  return "blocked";
}

function record(row, slug, onQueueToggle) {
  const item = element("li", `record${row.next ? " next" : ""}${row.skipped ? " skipped" : ""}`);
  // A record naming a stitch the projection does not have is still drawn, but
  // it is not selectable: there is nothing for the detail pane to fetch, and
  // there is nothing to drag it relative to either.
  if (row.stitch) {
    selectable(item, row.id);
    item.draggable = true;
  }
  item.append(element("span", "position", row.position > 0 ? String(row.position) : "?"));

  if (row.stitch) {
    const link = element("a", "id", row.id);
    link.href = selectionPath(slug, "queue", row.id);
    item.append(link);
  } else {
    // A record the projection has no stitch for still names one, and that is
    // what makes it repairable.
    item.append(element("span", "id missing", row.id || "(unreadable record)"));
  }

  if (row.next) item.append(element("span", "badge next", "next"));
  if (row.skipped) item.append(element("span", "badge skip", `skipped · ${why(row)}`));
  if (row.impossible) item.append(element("span", "note never", row.impossible));
  if (row.inverted) item.append(element("span", "note inverted", row.inverted));
  if (row.problem) item.append(element("span", "note problem", row.problem));
  // A stale record is the one row where the button is the only thing that can
  // help: unqueue is what removes it, and nothing else in this view can.
  if (onQueueToggle) {
    item.append(
      queueButton(row.stitch ?? { id: row.id, queue_position: row.position || 1 }, onQueueToggle),
    );
  }
  return item;
}

/** renderQueue returns the queue view. */
export function renderQueue(snapshot, slug, onQueueToggle) {
  const view = element("section", "queue");
  const { rows: list, counts } = rows(snapshot);

  const header = element("header", "counts");
  header.append(element("span", "count ready", `${counts.queued} queued`));
  // The skipped count sits beside the length permanently: a queue whose head
  // is skipped is a queue that is not saying what it looks like it is saying.
  header.append(element("span", "count waiting", `${counts.skipped} skipped`));
  view.append(header);

  if (list.length === 0) {
    view.append(
      element(
        "p",
        "muted",
        "The queue is empty. It is sparse and opt-in — the frontier is still ordered without it.",
      ),
    );
    return view;
  }

  const ordered = element("ol", "records");
  for (const row of list) ordered.append(record(row, slug, onQueueToggle));
  view.append(ordered);

  if (!list.some((row) => row.next)) {
    view.append(element("p", "muted", "Nothing on the queue is ready; `next` will take unqueued work."));
  }
  return view;
}

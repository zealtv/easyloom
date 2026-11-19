// Changing the queue from the browser, and staying honest about it.
//
// A mutation changes the loom, so the projection on screen is stale the moment
// it lands — and a fresh one costs seconds on a large loom. Waiting for it
// would make every drag feel broken. So the change is applied here first, the
// server re-snapshots in the background, and the new generation arrives over
// the event stream.
//
// Which makes the discipline the whole point:
//
//   * the overlay is display state with a lifetime of one snapshot generation
//   * when a newer projection lands it is discarded **whole**, whether or not
//     it agrees — the projection is authoritative and we are a viewer
//   * a mutation the loom refuses is rolled back and said out loud, because the
//     one thing worse than a slow UI is a UI asserting something that did not
//     happen
//
// This is `internal/tui/mutate.go`'s pattern, in the other frontend. The
// terminal proved it; the reasoning is the same and so is the vocabulary.
//
// Nothing here recomputes protocol state. The overlay carries queue positions
// and nothing else, because queue order is the only thing this file can predict
// — what a move does to readiness, to the frontier, or to a dependency is the
// loom's answer and arrives with the next projection.

import { hidden } from "./stitches.js";

/** queueOrder is the queue as the loom will read it: IDs by position.
 *
 * Position comes from the projection and is never recomputed. Archived and
 * legacy records are left out for the same reason every view leaves them out.
 */
export function queueOrder(snapshot) {
  return (snapshot?.stitches ?? [])
    .filter((stitch) => !hidden(stitch) && stitch.queue_position != null)
    .sort((left, right) => left.queue_position - right.queue_position)
    .map((stitch) => stitch.id);
}

/** reorder is what one command would do to the queue's order.
 *
 * The commands are the protocol's and they are relative — first, before, after
 * — so this is a prediction of one command's effect, never a new queue file to
 * send. Returns null for a move that cannot mean anything.
 */
export function reorder(order, command, id, anchor) {
  const rest = order.filter((entry) => entry !== id);
  switch (command) {
    case "unqueue":
      return rest;
    case "append":
      return [...rest, id];
    case "first":
      return [id, ...rest];
    case "before":
    case "after": {
      if (!anchor || anchor === id) return null;
      const at = rest.indexOf(anchor);
      if (at < 0) return null;
      const cut = command === "before" ? at : at + 1;
      return [...rest.slice(0, cut), id, ...rest.slice(cut)];
    }
    default:
      return null;
  }
}

/** plan is the optimistic overlay for one command: the whole queue afterwards,
 * with null for a stitch that would leave it.
 *
 * A move renumbers everything after it, which is exactly why the row cannot
 * simply be spliced in the view: the positions the reader is looking at are
 * part of what the queue says.
 *
 * It is deliberately absolute rather than a diff against the snapshot it was
 * planned from. Two commands can land before a single refresh does, and an
 * overlay that only recorded what changed would have to be composed against a
 * base that has itself already been overlaid — where "unchanged" means
 * unchanged from the *prediction*, and the first command's effect quietly
 * disappears under the second.
 */
export function plan(snapshot, command, id, anchor) {
  const before = queueOrder(snapshot);
  const after = reorder(before, command, id, anchor);
  if (!after) return null;

  const overlay = new Map();
  after.forEach((entry, index) => overlay.set(entry, index + 1));
  for (const entry of before) {
    if (!overlay.has(entry)) overlay.set(entry, null);
  }
  return overlay;
}

/** applyOverlay is the projection as the pending mutation would leave it.
 *
 * The snapshot is copied rather than written to. It is the server's answer,
 * held for the length of a generation and re-read on every draw, so a view that
 * edited it in place would make the optimistic change permanent — surviving
 * exactly the reconciliation it is supposed to lose to.
 */
export function applyOverlay(snapshot, overlay) {
  if (!snapshot || !overlay || overlay.size === 0) return snapshot;
  return {
    ...snapshot,
    stitches: snapshot.stitches.map((stitch) =>
      overlay.has(stitch.id) ? { ...stitch, queue_position: overlay.get(stitch.id) } : stitch,
    ),
  };
}

/** neighbourMove turns "move this one place up or down" into one command.
 *
 * The queue is sparse, so a stitch that is not on it is queued rather than
 * refused — a move on an unqueued row is a reader saying they want it in the
 * order, which is the same call the terminal makes.
 *
 * Nothing can go before the first record, so the top of the queue is `first`
 * rather than `before`.
 */
export function neighbourMove(snapshot, id, delta) {
  const order = queueOrder(snapshot);
  const here = order.indexOf(id);
  if (here < 0) return { command: "append", id };

  const destination = here + delta;
  if (destination < 0) return { refusal: `${id} is already at the top of the queue` };
  if (destination >= order.length) return { refusal: `${id} is already at the end of the queue` };

  const anchor = order[destination];
  if (delta < 0 && destination === 0) return { command: "first", id };
  return { command: delta < 0 ? "before" : "after", id, anchor };
}

/** toggle is Q: on the queue, or off it. */
export function toggle(snapshot, id) {
  return queueOrder(snapshot).includes(id) ? { command: "unqueue", id } : { command: "append", id };
}

/** said is what to announce once a command has been accepted.
 *
 * Every queue move says what it did. A stray keystroke that reorders the queue
 * and says nothing is how this repo lost track of its own plan once already,
 * and it was found in `git diff` rather than on screen.
 */
export function said({ command, id, anchor }) {
  switch (command) {
    case "unqueue":
      return `${id} is off the queue`;
    case "append":
      return `${id} is on the queue, at the end`;
    case "first":
      return `${id} is at the top of the queue`;
    case "before":
      return `${id} moved above ${anchor}`;
    case "after":
      return `${id} moved below ${anchor}`;
    default:
      return "";
  }
}

/** post sends one queue command and returns the loom's own result.
 *
 * A refusal becomes an Error carrying the loom's words verbatim. An unrelated
 * stale queue record makes an unrelated mutation fail *without changing the
 * file*, and only the loom's own output explains that — so its stderr is kept
 * on the error rather than paraphrased away.
 */
export async function post(slug, { command, id, anchor }) {
  const response = await fetch(
    `/api/l/${encodeURIComponent(slug)}/queue/${encodeURIComponent(command)}`,
    {
      method: "POST",
      headers: { "content-type": "application/json", accept: "application/json" },
      body: JSON.stringify(anchor ? { id, anchor } : { id }),
    },
  );
  const body = await response.json().catch(() => null);
  if (!response.ok) {
    const failure = body?.error ?? {};
    const error = new Error(failure.message || `the loom refused ${command} (${response.status})`);
    error.code = failure.code ?? "";
    error.loomCode = failure.loom_code ?? "";
    error.stderr = failure.stderr ?? "";
    throw error;
  }
  return body;
}

/** Mutations owns the one mutation in flight and the overlay it is asserting.
 *
 * One at a time, like the terminal: queue commands contend for the loom's queue
 * lock, and a burst of keystrokes should not become a burst of commands racing
 * each other into it.
 */
export class Mutations {
  /**
   * @param {object} options
   * @param {() => string} options.slug        which loom to mutate
   * @param {() => object} options.snapshot    the projection as drawn, overlay included
   * @param {() => number} options.generation  the projection's generation
   * @param {() => void} options.onChange      redraw; the overlay has moved
   * @param {(text: string) => void} options.onSay     announce an accepted move
   * @param {(err: Error) => void} options.onFail      report a refusal
   * @param {typeof post} [options.send]       injected for tests
   */
  constructor({ slug, snapshot, generation, onChange, onSay, onFail, send = post }) {
    this.slug = slug;
    this.snapshot = snapshot;
    this.generation = generation;
    this.onChange = onChange;
    this.onSay = onSay;
    this.onFail = onFail;
    this.send = send;

    /** overlay is the pending change, or null. */
    this.overlay = null;
    /** at is the generation the overlay was applied over. */
    this.at = 0;
    /** pending is the command in flight, or null. */
    this.pending = null;
  }

  /** busy reports a command already in flight. */
  get busy() {
    return this.pending !== null;
  }

  /** projection is the snapshot with the pending change laid over it. */
  project(snapshot) {
    return this.at === this.generation() ? applyOverlay(snapshot, this.overlay) : snapshot;
  }

  /** settle discards the overlay when a newer projection has landed.
   *
   * Silently, and whether or not it agrees. The projection is authoritative;
   * whatever the overlay believed about the queue is now the loom's answer.
   */
  settle() {
    if (this.overlay && this.at !== this.generation()) {
      this.overlay = null;
      this.at = 0;
      return true;
    }
    return false;
  }

  /** run applies a command optimistically and sends it.
   *
   * A refusal rolls the overlay back to exactly what was on screen before, so
   * the pane never keeps asserting something the loom rejected.
   */
  async run(command) {
    if (!command || command.refusal) {
      if (command?.refusal) this.onSay(command.refusal);
      return;
    }
    if (this.busy) {
      this.onSay("a queue command is already running");
      return;
    }

    const overlay = plan(this.snapshot(), command.command, command.id, command.anchor);
    if (!overlay) {
      this.onFail(new Error(`${command.command} ${command.id} cannot be applied to this queue`));
      return;
    }

    const wasOverlay = this.overlay;
    const wasAt = this.at;
    // Layered over whatever is already pending, so a stitch the previous
    // command took off the queue stays off it.
    this.overlay = new Map([...(this.overlay ?? []), ...overlay]);
    this.at = this.generation();
    this.pending = command;
    this.onChange();

    try {
      await this.send(this.slug(), command);
      // The overlay stays: it is what is on screen until the refresh the server
      // has already asked for arrives over the stream and replaces it.
      this.onSay(said(command));
    } catch (err) {
      this.overlay = wasOverlay;
      this.at = wasAt;
      this.onFail(err);
      this.onChange();
    } finally {
      this.pending = null;
    }
  }
}

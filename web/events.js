// The live-update stream.
//
// `GET /api/l/<slug>/events` pushes a small event whenever the server's
// published state changes — a new generation, a failed attempt, or a run
// starting or stopping. The event never carries the projection: it says
// something moved, and the shell asks `/snapshot` for what.
//
// EventSource reconnects on its own, but at a fixed interval it decides. A
// laptop waking up with the server long gone would then reconnect once a
// second forever, so this closes the stream on an error and reopens it with a
// backoff of its own.

// The reconnect schedule: quick enough that a server restart during
// development is invisible, slow enough that a closed server is not hammered.
const baseDelay = 500;
const maxDelay = 15000;

/** delayFor is the wait before reconnect attempt n, with jitter.
 *
 * The jitter is not about thundering herds — this is a loopback server with
 * one reader — it is so a tab that has been asleep does not land its retry on
 * exactly the same tick as every other tab that was asleep with it.
 */
export function delayFor(attempt) {
  const capped = Math.min(maxDelay, baseDelay * 2 ** Math.max(0, attempt));
  return Math.round(capped * (0.5 + Math.random() * 0.5));
}

/** watch opens the event stream for a loom and keeps it open.
 *
 * `onSnapshot` is called with each event's payload — `{generation,
 * captured_at, stale, error, refreshing}`. `onLive` is called with true or
 * false as the connection comes and goes, so the UI can say when what it is
 * showing has stopped being live rather than quietly going stale.
 *
 * The returned handle's `close` is final: nothing reconnects after it.
 */
export function watch(slug, { onSnapshot, onLive } = {}) {
  const url = `/api/l/${encodeURIComponent(slug)}/events`;
  let source = null;
  let timer = null;
  let attempt = 0;
  let closed = false;

  const live = (yes) => {
    if (onLive) onLive(yes);
  };

  const open = () => {
    timer = null;
    if (closed) return;
    source = new EventSource(url);

    source.addEventListener("open", () => {
      attempt = 0;
      live(true);
    });

    source.addEventListener("snapshot", (event) => {
      let payload;
      try {
        payload = JSON.parse(event.data);
      } catch {
        return; // A malformed event is not a reason to tear the stream down.
      }
      if (onSnapshot) onSnapshot(payload);
    });

    // An error on an EventSource is either "connecting failed" or "the
    // connection dropped", and it does not say which. Both want the same
    // thing: close this one and try again later.
    source.addEventListener("error", () => {
      source.close();
      source = null;
      live(false);
      if (closed) return;
      timer = setTimeout(open, delayFor(attempt));
      attempt += 1;
    });
  };

  open();

  return {
    close() {
      closed = true;
      if (timer !== null) clearTimeout(timer);
      if (source) source.close();
      source = null;
    },
  };
}

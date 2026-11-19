// What every view shares: DOM helpers, and the two predicates about a stitch
// that all three views have to agree on.

/** element builds a node. It exists so a view can be written as data rather
 * than as a string of HTML — nothing here ever interpolates a loom's contents
 * into markup. */
export function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text != null) node.textContent = text;
  return node;
}

/** hidden is the filter every view shares: archived and legacy records are
 * counted, not drawn.
 *
 * bopOS's loom is 446 stitches, 393 of them legacy. Showing archives by
 * default makes a view 88% noise — so they are hidden, and the count of what
 * was hidden is shown instead. */
export function hidden(stitch) {
  return Boolean(stitch.archived || stitch.legacy);
}

/** terminal reports a stitch that is finished, whichever way it finished. */
export function terminal(stitch) {
  return stitch.state === "tied" || stitch.state === "dropped";
}

/** viewPath is where a view of a loom lives. */
export function viewPath(slug, view) {
  return `/l/${encodeURIComponent(slug)}/${view}`;
}

/** selectionPath is a view with one stitch selected in it — the shape every
 * row links to.
 *
 * The selection is a query parameter rather than a path segment for two
 * reasons: the server's routes match on the path alone, so no route has to
 * learn about selection; and a stitch ID may contain a slash, which a path
 * segment cannot carry without escaping the server would then have to undo. */
export function selectionPath(slug, view, id) {
  return `${viewPath(slug, view)}?stitch=${encodeURIComponent(id)}`;
}

/** selectable marks a row as the row for a stitch.
 *
 * Selection lives in the shell, not in the views: a view says which stitch a
 * row is about, and the shell decides which of them is selected. This is the
 * whole of the contract between them. */
export function selectable(row, id) {
  row.dataset.stitchId = id;
  return row;
}

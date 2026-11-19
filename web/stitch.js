// The stitch detail view: what a stitch entails.
//
// Two sources, kept apart deliberately. The protocol facts — state, tray,
// readiness, queue position, dependencies, cycle membership — come from the
// projection and are authoritative. The instructions and the artifact list are
// files, read from the stitch directory because the projection carries a
// stitch's path but not its contents. Reading artifacts is the point of the
// feature; nothing here ever infers state from what is on disk.

import { renderMarkdown } from "./markdown.js";
import { element, selectable } from "./stitches.js";

/** facts is the protocol's view of a stitch, in label/value pairs. */
export function facts(stitch) {
  const out = [
    { label: "state", value: stitch.state },
    { label: "ready", value: stitch.ready ? "yes" : "no" },
    { label: "tray", value: stitch.tray },
  ];
  if (stitch.waiting_inherited) {
    // Inherited waiting is a consequence of an ancestor, and `resume` will
    // refuse it — "not directly waiting" is otherwise a confusing refusal.
    out.push({ label: "waiting", value: "inherited from an ancestor" });
  }
  if (stitch.root_id && stitch.root_id !== stitch.id) {
    out.push({ label: "thread", value: stitch.root_id });
  }
  if (stitch.parent_id) out.push({ label: "parent", value: stitch.parent_id });
  out.push({ label: "queue", value: stitch.queue_position == null ? "not queued" : `position ${stitch.queue_position}` });
  if (stitch.state === "tied" || stitch.state === "dropped" || stitch.completed_at) {
    // The protocol forbids guessing completed_at from mtime: a legacy record
    // has none, and it renders as unknown rather than as a fabricated date.
    out.push({ label: "completed", value: stitch.completed_at ?? "unknown (legacy record)" });
  }
  if (stitch.legacy) out.push({ label: "legacy", value: "a v1 record, migrated" });
  out.push({ label: "path", value: stitch.path });
  return out;
}

function definitionList(pairs) {
  const list = element("dl", "facts");
  for (const { label, value } of pairs) {
    list.append(element("dt", null, label));
    list.append(element("dd", null, String(value)));
  }
  return list;
}

/** dependency describes one needs/ edge in the loom's own terms. */
function dependency(edge, slug, known) {
  const item = element("li", `dependency ${edge.status}`);
  item.append(element("span", `badge ${edge.status}`, edge.status));

  if (known.has(edge.to)) {
    const link = selectable(element("a", "id", edge.to), edge.to);
    link.href = `/l/${encodeURIComponent(slug)}/stitch/${encodeURIComponent(edge.to)}`;
    item.append(link);
  } else {
    item.append(element("span", "id missing", edge.to));
  }

  if (edge.status === "broken") {
    // missing | dropped | ambiguous | invalid — the reason is the whole of
    // what makes a broken edge repairable.
    item.append(element("span", "note never", edge.reason ?? "broken"));
  }
  return item;
}

function bytes(size) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${Math.round(size / 1024)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

/** renderStitch returns the detail view for one stitch.
 *
 * `detail` is the API's stitch response; `snapshot` is the projection it came
 * from, used only to resolve IDs to links.
 */
export function renderStitch(detail, snapshot, slug) {
  const view = element("section", "detail");
  const stitch = detail.stitch;
  const known = new Set((snapshot?.stitches ?? []).map((entry) => entry.id));
  const wikilink = (id) => (known.has(id) ? `/l/${encodeURIComponent(slug)}/stitch/${encodeURIComponent(id)}` : null);

  const header = element("header", "detail-head");
  header.append(element("h1", "id", stitch.id));
  if (stitch.ready) header.append(element("span", "badge ready", "ready"));
  if (stitch.state !== "plain") header.append(element("span", `badge state ${stitch.state}`, stitch.state));
  if (stitch.queue_position != null) {
    header.append(element("span", "badge queue", `queue ${stitch.queue_position}`));
  }
  view.append(header);

  view.append(definitionList(facts(stitch)));

  if (stitch.cycle?.length) {
    const cycle = element("section", "cycle");
    cycle.append(element("h2", null, "In a dependency cycle"));
    // Members of a cycle are never ready, and no amount of working the loom
    // will change that until an edge is removed.
    cycle.append(element("p", "note never", stitch.cycle.join(" → ")));
    view.append(cycle);
  }

  const edges = stitch.dependencies ?? [];
  const dependencies = element("section", "dependencies");
  dependencies.append(element("h2", null, `Needs (${edges.length})`));
  if (edges.length === 0) {
    dependencies.append(element("p", "muted", "Nothing. This stitch is sequenced only by its ancestry."));
  } else {
    const list = element("ul", "edges");
    for (const edge of edges) list.append(dependency(edge, slug, known));
    dependencies.append(list);
  }
  view.append(dependencies);

  const instructions = element("section", "instructions");
  instructions.append(element("h2", null, detail.instructions_path || "instructions.md"));
  if (detail.instructions) {
    const body = element("article", "markdown");
    body.append(renderMarkdown(detail.instructions, wikilink));
    instructions.append(body);
  } else {
    instructions.append(
      element("p", "muted", detail.instructions_error || "This stitch has no instructions."),
    );
  }
  view.append(instructions);

  const artifacts = detail.artifacts ?? [];
  if (artifacts.length > 0 || detail.artifacts_error) {
    const section = element("section", "artifacts");
    section.append(element("h2", null, `Artifacts (${artifacts.length})`));
    if (detail.artifacts_error) {
      section.append(element("p", "muted", detail.artifacts_error));
    }
    const list = element("ul", "files");
    for (const artifact of artifacts) {
      const item = element("li", "file");
      item.append(element("span", "name", artifact.name));
      item.append(element("span", "muted", artifact.dir ? "directory" : bytes(artifact.size)));
      list.append(item);
    }
    if (artifacts.length > 0) section.append(list);
    view.append(section);
  }

  return view;
}

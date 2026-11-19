# vendor

Third-party modules, as they were published. Nothing here is edited, so a copy
can be checked against its origin byte for byte.

| Module | Version | Licence | Origin |
| --- | --- | --- | --- |
| `dagre.esm.js` | `@dagrejs/dagre` 3.1.0 | MIT (`dagre.LICENSE`) | https://unpkg.com/@dagrejs/dagre@3.1.0/dist/dagre.esm.js |

The one edit made to a vendored file is stripping its `sourceMappingURL`
comment: the map is not vendored, and a browser asking this server for a file
that is not there logs a 404 on every load.

## Why dagre and not ELK

The graph needs a layered layout and the rule the loom set is to pick a library
rather than write a layout engine. The two candidates were dagre and elkjs.

ELK is the more capable engine, and it lays out compound nodes — decomposition
as real containment — which dagre only approximates with clusters. It lost on
two things that matter more here:

* **It is 1.6 MB against dagre's 48 KB.** These assets are embedded in the
  binary with `embed.FS`, and the binary is the whole distribution. A 34×
  larger layout engine than the entire rest of the frontend is not a trade this
  project makes for a viewer.
* **It has no ES module build.** `elk.bundled.js` is UMD, and there is no build
  step here by design — the sources are the artefact. Loading it would mean
  either a shim or a bundler, and the bundler is the thing `embedded-assets`
  settled against.

Licensing is the tiebreak rather than the reason: dagre is MIT, elkjs is
EPL-2.0 or GPL-3.0-or-later, and MIT is the easier thing to embed in a binary.

The cost is real and is written down in `graph.js`: decomposition is drawn as a
layered tree with a containment spine rather than as nesting. If nesting turns
out to be what makes the graph legible on a large loom, that is the reason to
revisit this, and ELK behind a lazy import is how.

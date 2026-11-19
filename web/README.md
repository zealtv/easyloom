# web

Frontend sources, embedded into the binary with `embed.FS` so the distribution
stays a single static file.

There is no build step: hand-written HTML, CSS and ES modules loaded natively
by the browser. The sources are the artefact, so `go build` works from a clean
checkout with no toolchain beyond Go. The reasoning, and what would justify
revisiting it, is in the loom stitch `embedded-assets`.

Third-party modules are vendored into `vendor/` with their licence, version and
origin URL.

## The shape

The page is two panes: a list on top — the frontier, the thread tree or the
queue — and what the selected stitch entails below. Selecting a row changes the
bottom pane and never navigates. It is the shape the TUI has, for the same
reason: reading a row and reading what it means is one act.

`app.js` is the shell and owns what the views do not — which loom and view the
URL names, which stitch each list has selected, and where the reader put the
split. A view renders into whatever element it is handed and marks each row
with the stitch it is about (`selectable` in `stitches.js`); it decides nothing
about selection or layout.

## The graph

`graph.js` and `graphview.js` are two halves of one view, kept apart on
purpose. `graph.js` is layout: it hands dagre the combined decomposition and
dependency graph and returns boxes and routed points, and it decides nothing
about how any of it reads. `graphview.js` is the drawing: it turns those
boxes into SVG, and it is the only place the loom's vocabulary becomes colour —
containment as a solid spine, a dependency as a dashed arrow coloured by its
status, a cycle boxed as the hard stall it is.

The colours themselves are in `app.css` as `--ready`, `--claimed`, `--blocked`
and the rest, and the lists use them too: a blocked edge in the graph and a
blocked badge in the detail pane are the same word and must not be two
different colours.

A graph node is a row about a stitch, exactly like a list row — it carries
`data-stitch-id` and nothing else, so the shell selects it the same way, and
clicking one draws it in the pane below with no machinery the lists do not
already have.

`graphinteraction.js` is the third piece: what the reader *does* to the graph.
Drag pans and the wheel zooms about the pointer, both by arithmetic on the
`viewBox` — the drawing fills its pane and scrolls nothing, so there is no
second, disagreeing way to move around it. Hovering a node lights the
dependency edges in and out of it and dims the rest. Three toggles say whether
archived, legacy and tied stitches are drawn, and focus mode reduces the graph
to one goal thread plus both ends of every dependency edge that touches it.

All of that is the reader's, not the loom's, so it lives in the shell's state
beside the tree's folds and survives a re-render — flipping a toggle must not
throw away where you were looking. None of it is ever sent anywhere.

## The impact simulation

`unblock.js` is a third piece of the same kind as `graph.js`: it only decides.
A projection goes in and sets of stitch IDs come out — no DOM, no fetch, no
module state — so it is testable as a fixture in and an answer out.

What it computes is readiness, re-derived from the projection rather than read
off it, and then re-derived again with one stitch assumed resolved. The
difference is what that stitch would release. The projection's own `ready` flag
stays authoritative: the only reason to re-derive it is to ask a hypothetical
the loom cannot be asked, and `disagreements()` exists so a test can assert
that with nothing simulated the two agree, stitch for stitch. A re-derivation
that disagrees with the loom about the present is wrong about the future too.

Impact is one step. What becomes ready *next* is checkable by looking at the
screen; a transitive count over a chain rewards the head of the longest thread
regardless of what that thread costs.

`impact.js` draws it, at `/l/<slug>/impact`, in the same two-pane frame as
every other view: a row is a row about a stitch, so the shell selects it with
no machinery the lists do not already have. Each row says what the stitch would
release — named, because the number alone is a leaderboard — and what resolving
it would cost, which is a different act in each case: tie it, unblock it,
decompose it, resume it, or promote it. The last one matters most. `resume`
lifts only a stitch's own suffix, so for a stitch parked by inheritance the row
names the ancestor actually holding it and offers both ways out.

The two groups are never merged. "What should I work on now" and "what should I
un-park" are not comparable, and one leaderboard over both would invite exactly
that comparison. Rows that release nothing fold away, because on this loom most
of them do and a list of zeroes buries the three that matter; the fold is the
reader's and lives in the shell.

An impact badge on a graph node and a tree row is not built. It would be the
same module driving it — that is the point of the split — but it is a change to
two more views for a number the impact view already gives, and the stitch says
not to let it hold the view up.

## Testing

```sh
make test-web       # or: node --test web/
make golden         # regenerate the cross-check against the Go port
```

`web/testdata/` holds the fixtures, described in `testdata/README.md`, and
`package.json` says `"type": "module"` so node reads these files as what the
browser already reads them as. That is the whole of it: no dependencies, no
config, nothing installed, and no build step — `go build` from a clean checkout
still needs nothing beyond Go, and a machine without node skips the web tests
rather than failing them.

`unblock.js` now has a second implementation, `internal/unblock`, because the
terminal needs the same ranking and cannot run this one. Two implementations of
one idea disagree eventually, so `make golden` writes this module's answers over
every fixture to `testdata/rankings.golden.json`; `golden.test.mjs` fails when
that file is stale, and `go test ./internal/unblock` fails when the port
disagrees with it. A change here that moves a ranking is a change that has to be
regenerated, and the diff says which answers moved.

The `.mjs` extension on the tests, the fixture list and the generator is load
bearing: `//go:embed *.js` would otherwise carry them into the binary.

## URLs

```text
/l/<slug>/<view>?stitch=<stitch-id>
```

The view is a path segment because it is a place you navigate to; the selection
is a query parameter because it moves under you as you arrow down a list.
Keeping it out of the path also means the server's routes never learn about
selection, and a stitch ID containing a slash needs no escaping the server
would then have to undo.

Selecting replaces the current history entry — arrowing through a list must not
build twenty entries of back-button history — and switching views pushes one.

A stitch link *inside* the detail pane — a `[[stitch-id]]` in the instructions,
or a dependency edge — selects its target rather than navigating. The target can
be anything the projection carries, including a stitch the current list is not
showing; the pane says which list would show it rather than switching views out
from under the reader.

`/l/<slug>/stitch/<id>` is the old per-page route. It still resolves: the shell
lands on the frontier if that stitch is ready work and on the thread tree
otherwise, with it selected. Links to it exist in people's notes.

## Changing the queue

The read half is `GET /api/l/<slug>/snapshot`. The write half is five POSTs,
one per protocol command:

```text
POST /api/l/<slug>/queue/append    {id}
POST /api/l/<slug>/queue/first     {id}
POST /api/l/<slug>/queue/before    {id, anchor}
POST /api/l/<slug>/queue/after     {id, anchor}
POST /api/l/<slug>/queue/unqueue   {id}
```

`before` and `after` take the moved ID first and the anchor second, the way the
loom does. Getting that backwards reorders the wrong stitch silently, so nothing
in this directory builds that argv — the endpoint names the two separately.

A response is the loom's own result: the new `queue_position`, and `changed`,
which is `false` for an accepted no-op such as a drop that landed where the
stitch already was. That is not a failure and a view that treats it as one will
flap on every repeated drag. A refusal carries the loom's error code and its
`stderr` verbatim; show it rather than paraphrasing it, because an unrelated
stale queue record makes an unrelated mutation fail without changing the file.

None of these return the new projection. A queue write renumbers every position
after the moved one, so the server re-snapshots and the new generation arrives
over the events stream.

### Optimistic, and honest about it

`mutate.js` is the layer between the keys and those endpoints, and it is
`internal/tui/mutate.go`'s pattern in this frontend. A fresh projection costs
seconds on a large loom, so the change is applied here first — and then three
rules keep that from becoming a lie:

* the overlay lives for one snapshot generation, and a newer projection
  discards it **whole**, silently, whether or not it agrees;
* a refusal rolls it back to exactly what was on screen, and says why in the
  loom's own words;
* it carries queue positions and nothing else. What a move does to readiness or
  to the frontier is the loom's answer, and it arrives with the projection.

The shell lays the overlay on the snapshot it hands the views, so the queue, the
frontier and the impact ranking all respond on the keystroke without any of them
knowing a mutation exists. The overlay is the whole queue afterwards rather than
a diff, because two commands can land before one refresh does and a diff would
have to be composed against a base that has itself been predicted. One command
is in flight at a time: they contend for the loom's queue lock.

The keys are the terminal's — `Q` on or off the queue, `J`/`K` one place along —
and every move says what it did in the footer.

### Dragging a row

`reorder.js` turns a drop into one command, and it is kept away from the DOM so
it can be tested: `dropCommand` takes the IDs as drawn, which of them are
queued, and where the row landed.

The frontier is a *mixed* list — queued stitches first in queue order, then the
unqueued ready ones, which have no position at all — so the row a stitch is
dropped next to is very often not something the loom can anchor against. The
drop resolves to one that is: the nearest queued row above it (`after`), or
`first` when there is none above and something queued below, or `append` when
nothing is queued at all.

There is no `before`. `before X` and `after` the row above X are the same drop
whenever X has a queued row above it, and when it has not the answer is `first`
— one rule fewer, and no rule that never runs. A drop that would change nothing
sends nothing.

Only the frontier and the queue can be dragged. The tree, the graph and the
impact ranking have an order that is the decomposition's or the arithmetic's,
not a preference anyone can express.

## Running it

```sh
make web        # build and open the browser on this repo's own loom
make web-dev    # the same, serving this directory from disk
```

Opening `index.html` from the file system does not work and is not meant to.
The asset and API paths are absolute (`/app.js`, `/api/l/<slug>/snapshot`), so
under `file://` they resolve to the root of your disk — and there is no API
behind them without the binary running. `easyloom web` serves this directory,
embedded, from the same origin as the API.
